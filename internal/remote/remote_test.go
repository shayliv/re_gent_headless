package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regent-vcs/regent/internal/server"
	"github.com/regent-vcs/regent/internal/store"
)

// sessionRef is the ref both test repos use, on purpose: identical ref names in
// different repos must not collide.
const sessionRef = "sessions/claude_code--s1"

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// testRepo is a local re_gent repo whose history is built step by step, the way
// capture builds it: blobs -> tree -> transcript -> step -> ref CAS.
type testRepo struct {
	t          *testing.T
	st         *store.Store
	session    string
	tip        store.Hash
	transcript store.Hash
	steps      []store.Hash
	clock      int64
}

func newTestRepo(t *testing.T, session string) *testRepo {
	t.Helper()
	st, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return &testRepo{t: t, st: st, session: session}
}

// commit records one step containing the given files and conversation lines.
func (r *testRepo) commit(files map[string]string, messages ...string) store.Hash {
	r.t.Helper()

	tree := &store.Tree{}
	for path, content := range files {
		blob, err := r.st.WriteBlob([]byte(content))
		if err != nil {
			r.t.Fatalf("write blob: %v", err)
		}
		tree.Entries = append(tree.Entries, store.TreeEntry{Path: path, Blob: blob, Mode: 0o644})
	}
	treeHash, err := r.st.WriteTree(tree)
	if err != nil {
		r.t.Fatalf("write tree: %v", err)
	}

	msgHashes := make([]store.Hash, 0, len(messages))
	for _, m := range messages {
		h, err := r.st.WriteBlob([]byte(m))
		if err != nil {
			r.t.Fatalf("write message: %v", err)
		}
		msgHashes = append(msgHashes, h)
	}
	transcript, err := r.st.WriteTranscript(r.transcript, msgHashes)
	if err != nil {
		r.t.Fatalf("write transcript: %v", err)
	}
	r.transcript = transcript

	args, err := r.st.WriteBlob([]byte(fmt.Sprintf(`{"session":%q,"n":%d}`, r.session, len(r.steps))))
	if err != nil {
		r.t.Fatalf("write args: %v", err)
	}
	result, err := r.st.WriteBlob([]byte(fmt.Sprintf("ok %s %d", r.session, len(r.steps))))
	if err != nil {
		r.t.Fatalf("write result: %v", err)
	}

	r.clock++
	step := &store.Step{
		Parent:         r.tip,
		Tree:           treeHash,
		Transcript:     transcript,
		SessionID:      r.session,
		Origin:         "claude_code",
		TurnID:         fmt.Sprintf("turn-%d", r.clock),
		TimestampNanos: r.clock,
		Causes: []store.Cause{{
			ToolUseID:  fmt.Sprintf("tool-%d", r.clock),
			ToolName:   "Write",
			ArgsBlob:   args,
			ResultBlob: result,
		}},
	}
	stepHash, err := r.st.WriteStep(step)
	if err != nil {
		r.t.Fatalf("write step: %v", err)
	}
	if err := r.st.UpdateRef("sessions/"+r.session, r.tip, stepHash); err != nil {
		r.t.Fatalf("update ref: %v", err)
	}
	r.tip = stepHash
	r.steps = append(r.steps, stepHash)
	return stepHash
}

// newServer starts a multi-repo server and returns it with its URL.
func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, err := server.New(t.TempDir())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func newTestClient(t *testing.T, ts *httptest.Server, repoID string) *Client {
	t.Helper()
	c, err := NewClient(ts.URL, repoID)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// serverHistory walks the step chain the server holds for one repo, newest first.
func serverHistory(t *testing.T, c *Client, tip store.Hash) []store.Hash {
	t.Helper()
	ctx := context.Background()
	var chain []store.Hash
	for h := tip; h != ""; {
		data, err := c.GetObject(ctx, h)
		if err != nil {
			t.Fatalf("repo %s: server is missing step %s: %v", c.RepoID(), h, err)
		}
		chain = append(chain, h)
		var step store.Step
		if err := json.Unmarshal(data, &step); err != nil {
			t.Fatalf("decode step %s: %v", h, err)
		}
		h = step.Parent
	}
	return chain
}

// ---------------------------------------------------------------------------
// AC-1: two repos, interleaved pushes, independent histories
// ---------------------------------------------------------------------------

func TestInterleavedPushesKeepHistoriesIndependent(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()

	// Same session id in both repos, and one identical file in both, so any
	// leak between repos would show up.
	const sharedContent = "# shared license text\n"
	alphaRepo := newTestRepo(t, "claude_code--s1")
	betaRepo := newTestRepo(t, "claude_code--s1")

	alphaClient := newTestClient(t, ts, "alpha")
	betaClient := newTestClient(t, ts, "beta")

	for i := 0; i < 3; i++ {
		alphaRepo.commit(map[string]string{
			"LICENSE": sharedContent,
			"main.go": fmt.Sprintf("package main // alpha %d\n", i),
		}, fmt.Sprintf("alpha prompt %d", i))
		if _, err := Push(ctx, alphaClient, alphaRepo.st, []string{sessionRef}); err != nil {
			t.Fatalf("push alpha step %d: %v", i, err)
		}

		betaRepo.commit(map[string]string{
			"LICENSE": sharedContent,
			"app.py":  fmt.Sprintf("# beta %d\n", i),
		}, fmt.Sprintf("beta prompt %d", i))
		if _, err := Push(ctx, betaClient, betaRepo.st, []string{sessionRef}); err != nil {
			t.Fatalf("push beta step %d: %v", i, err)
		}
	}

	// Each repo's ref points at its own tip …
	for _, tc := range []struct {
		client *Client
		repo   *testRepo
	}{{alphaClient, alphaRepo}, {betaClient, betaRepo}} {
		got, err := tc.client.GetRef(ctx, sessionRef)
		if err != nil {
			t.Fatalf("%s GetRef: %v", tc.client.RepoID(), err)
		}
		if got != tc.repo.tip {
			t.Fatalf("%s ref = %s, want %s", tc.client.RepoID(), got, tc.repo.tip)
		}
	}

	// … and the history behind it is exactly that repo's steps, in order.
	assertChain := func(c *Client, repo *testRepo) {
		t.Helper()
		chain := serverHistory(t, c, repo.tip)
		if len(chain) != len(repo.steps) {
			t.Fatalf("%s: server history has %d steps, want %d", c.RepoID(), len(chain), len(repo.steps))
		}
		for i, h := range chain {
			want := repo.steps[len(repo.steps)-1-i] // chain walks newest first
			if h != want {
				t.Fatalf("%s: step %d = %s, want %s", c.RepoID(), i, h, want)
			}
		}
	}
	assertChain(alphaClient, alphaRepo)
	assertChain(betaClient, betaRepo)

	// No bleed: neither repo holds the other's steps.
	for _, h := range betaRepo.steps {
		if has, err := alphaClient.HasObject(ctx, h); err != nil || has {
			t.Fatalf("alpha holds beta's step %s (has=%v, err=%v)", h, has, err)
		}
	}
	for _, h := range alphaRepo.steps {
		if has, err := betaClient.HasObject(ctx, h); err != nil || has {
			t.Fatalf("beta holds alpha's step %s (has=%v, err=%v)", h, has, err)
		}
	}

	// Intended sharing: the identical file is content-addressed to the same
	// hash in both repos, and each repo holds its own copy of it.
	sharedHash, err := alphaRepo.st.WriteBlob([]byte(sharedContent))
	if err != nil {
		t.Fatalf("hash shared content: %v", err)
	}
	for _, c := range []*Client{alphaClient, betaClient} {
		data, err := c.GetObject(ctx, sharedHash)
		if err != nil {
			t.Fatalf("%s: shared blob missing: %v", c.RepoID(), err)
		}
		if string(data) != sharedContent {
			t.Fatalf("%s: shared blob = %q, want %q", c.RepoID(), data, sharedContent)
		}
	}
}

// ---------------------------------------------------------------------------
// AC-3: dedupe is per repo
// ---------------------------------------------------------------------------

func TestPushDedupesWithinRepoButNotAcrossRepos(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()
	const shared = "identical bytes in both repos\n"

	alphaRepo := newTestRepo(t, "claude_code--s1")
	alphaRepo.commit(map[string]string{"shared.txt": shared}, "prompt")
	alphaClient := newTestClient(t, ts, "alpha")

	first, err := Push(ctx, alphaClient, alphaRepo.st, []string{sessionRef})
	if err != nil {
		t.Fatalf("first push: %v", err)
	}
	if first.ObjectsSent == 0 {
		t.Fatal("first push sent no objects")
	}
	if first.ObjectsSkipped != 0 {
		t.Fatalf("first push skipped %d objects, want 0", first.ObjectsSkipped)
	}
	if first.RefsUpdated != 1 {
		t.Fatalf("first push updated %d refs, want 1", first.RefsUpdated)
	}

	// Re-pushing the same history is a no-op: everything is already there.
	second, err := Push(ctx, alphaClient, alphaRepo.st, []string{sessionRef})
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if second.ObjectsSent != 0 {
		t.Fatalf("second push sent %d objects, want 0 (deduped)", second.ObjectsSent)
	}
	if second.ObjectsSkipped != first.ObjectsSent {
		t.Fatalf("second push skipped %d, want %d", second.ObjectsSkipped, first.ObjectsSent)
	}
	if second.RefsUpdated != 0 {
		t.Fatalf("second push updated %d refs, want 0", second.RefsUpdated)
	}

	// A second repo containing the identical blob must upload its own copy:
	// dedupe never reaches across the repo boundary.
	betaRepo := newTestRepo(t, "claude_code--s1")
	betaRepo.commit(map[string]string{"shared.txt": shared}, "prompt")
	betaClient := newTestClient(t, ts, "beta")

	sharedHash, err := betaRepo.st.WriteBlob([]byte(shared))
	if err != nil {
		t.Fatalf("hash shared: %v", err)
	}
	if has, err := betaClient.HasObject(ctx, sharedHash); err != nil || has {
		t.Fatalf("beta already had alpha's blob before pushing (has=%v, err=%v)", has, err)
	}

	betaStats, err := Push(ctx, betaClient, betaRepo.st, []string{sessionRef})
	if err != nil {
		t.Fatalf("beta push: %v", err)
	}
	if betaStats.ObjectsSkipped != 0 {
		t.Fatalf("beta push skipped %d objects, want 0 (no cross-repo dedupe)", betaStats.ObjectsSkipped)
	}
	if has, err := betaClient.HasObject(ctx, sharedHash); err != nil || !has {
		t.Fatalf("beta is missing its own copy of the shared blob (has=%v, err=%v)", has, err)
	}
}

// ---------------------------------------------------------------------------
// push safety
// ---------------------------------------------------------------------------

func TestPushRefusesToClobberDivergedRemote(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()
	client := newTestClient(t, ts, "alpha")

	// One repo pushes a history …
	first := newTestRepo(t, "claude_code--s1")
	first.commit(map[string]string{"a.txt": "one"}, "first prompt")
	if _, err := Push(ctx, client, first.st, []string{sessionRef}); err != nil {
		t.Fatalf("first push: %v", err)
	}

	// … then an unrelated local history claims the same session ref.
	other := newTestRepo(t, "claude_code--s1")
	other.commit(map[string]string{"a.txt": "completely different"}, "other prompt")

	_, err := Push(ctx, client, other.st, []string{sessionRef})
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("push over diverged ref: err = %v, want ErrDiverged", err)
	}
	got, err := client.GetRef(ctx, sessionRef)
	if err != nil {
		t.Fatalf("GetRef: %v", err)
	}
	if got != first.tip {
		t.Fatalf("ref moved to %s despite refusal, want %s", got, first.tip)
	}
}

func TestPushFastForwardsAfterMoreLocalWork(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()
	client := newTestClient(t, ts, "alpha")

	repo := newTestRepo(t, "claude_code--s1")
	repo.commit(map[string]string{"a.txt": "one"}, "p1")
	if _, err := Push(ctx, client, repo.st, []string{sessionRef}); err != nil {
		t.Fatalf("push 1: %v", err)
	}
	repo.commit(map[string]string{"a.txt": "two"}, "p2")
	stats, err := Push(ctx, client, repo.st, []string{sessionRef})
	if err != nil {
		t.Fatalf("push 2: %v", err)
	}
	if stats.RefsUpdated != 1 {
		t.Fatalf("RefsUpdated = %d, want 1", stats.RefsUpdated)
	}
	if stats.ObjectsSkipped == 0 {
		t.Fatal("expected the first step's objects to be skipped as already present")
	}
	got, err := client.GetRef(ctx, sessionRef)
	if err != nil {
		t.Fatalf("GetRef: %v", err)
	}
	if got != repo.tip {
		t.Fatalf("ref = %s, want %s", got, repo.tip)
	}
}

func TestPushArgumentValidation(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()
	client := newTestClient(t, ts, "alpha")
	repo := newTestRepo(t, "claude_code--s1")
	repo.commit(map[string]string{"a.txt": "one"}, "p1")

	if _, err := Push(ctx, client, repo.st, nil); err == nil {
		t.Fatal("push with no refs: want error")
	}
	if _, err := Push(ctx, client, repo.st, []string{"sessions/does-not-exist"}); err == nil {
		t.Fatal("push of unknown ref: want error")
	}
}

// ---------------------------------------------------------------------------
// reachability
// ---------------------------------------------------------------------------

func TestReachableObjectsCoversTheWholeGraph(t *testing.T) {
	repo := newTestRepo(t, "claude_code--s1")
	repo.commit(map[string]string{"a.txt": "one", "b.txt": "two"}, "first prompt", "second line")
	repo.commit(map[string]string{"a.txt": "one changed", "b.txt": "two"}, "third prompt")

	objects, missing, err := ReachableObjects(repo.st, repo.tip)
	if err != nil {
		t.Fatalf("ReachableObjects: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}

	got := make(map[store.Hash]bool, len(objects))
	for _, h := range objects {
		if got[h] {
			t.Fatalf("object %s reported twice", h)
		}
		got[h] = true
	}

	// Everything in this local object store is reachable from the tip, so the
	// two sets must match exactly.
	var all []store.Hash
	if err := repo.st.WalkObjects(func(h store.Hash) error {
		all = append(all, h)
		return nil
	}); err != nil {
		t.Fatalf("WalkObjects: %v", err)
	}
	for _, h := range all {
		if !got[h] {
			t.Fatalf("object %s exists locally but was not reported reachable", h)
		}
	}
	if len(objects) != len(all) {
		t.Fatalf("reachable = %d objects, store holds %d", len(objects), len(all))
	}
}

func TestReachableObjectsReportsMissingWithoutFailing(t *testing.T) {
	repo := newTestRepo(t, "claude_code--s1")
	repo.commit(map[string]string{"a.txt": "one"}, "first prompt")
	repo.commit(map[string]string{"a.txt": "two"}, "second prompt")

	// Simulate history whose older payload was pruned.
	victim, err := repo.st.WriteBlob([]byte("first prompt"))
	if err != nil {
		t.Fatalf("hash message: %v", err)
	}
	path := filepath.Join(repo.st.Root, "objects", string(victim)[:2], string(victim))
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove object: %v", err)
	}

	objects, missing, err := ReachableObjects(repo.st, repo.tip)
	if err != nil {
		t.Fatalf("ReachableObjects: %v", err)
	}
	if len(missing) != 1 || missing[0] != victim {
		t.Fatalf("missing = %v, want [%s]", missing, victim)
	}
	for _, h := range objects {
		if h == victim {
			t.Fatal("a missing object was reported as pushable")
		}
	}
}

func TestReachableObjectsRejectsNonStepTip(t *testing.T) {
	repo := newTestRepo(t, "claude_code--s1")
	repo.commit(map[string]string{"a.txt": "one"}, "prompt")

	// A tree blob decodes into a Step with every field zero — unmarshalling
	// success is not type validation, so this must be rejected.
	step, err := repo.st.ReadStep(repo.tip)
	if err != nil {
		t.Fatalf("ReadStep: %v", err)
	}
	if _, _, err := ReachableObjects(repo.st, step.Tree); err == nil {
		t.Fatal("ReachableObjects accepted a tree hash as a step tip")
	}
	if _, _, err := ReachableObjects(repo.st, ""); err == nil {
		t.Fatal("ReachableObjects accepted an empty tip")
	}
	if _, _, err := ReachableObjects(repo.st, store.Hash(strings.Repeat("ab", 32))); err == nil {
		t.Fatal("ReachableObjects accepted an unknown hash")
	}
}

// ---------------------------------------------------------------------------
// client
// ---------------------------------------------------------------------------

func TestNewClientValidation(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		repo    string
		wantErr bool
	}{
		{"ok", "http://127.0.0.1:7654", "alpha", false},
		{"https ok", "https://regent.example.com", "alpha", false},
		{"trailing slash trimmed", "http://127.0.0.1:7654/", "alpha", false},
		{"missing scheme", "127.0.0.1:7654", "alpha", true},
		{"file scheme", "file:///etc/passwd", "alpha", true},
		{"missing host", "http://", "alpha", true},
		{"empty repo", "http://127.0.0.1:7654", "", true},
		{"uppercase repo", "http://127.0.0.1:7654", "Alpha", true},
		{"traversal repo", "http://127.0.0.1:7654", "../etc", true},
		{"reserved repo", "http://127.0.0.1:7654", "repos", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(tt.url, tt.repo)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NewClient(%q, %q) = nil error, want error", tt.url, tt.repo)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewClient(%q, %q): %v", tt.url, tt.repo, err)
			}
			if strings.HasSuffix(c.BaseURL(), "/") {
				t.Fatalf("BaseURL %q keeps a trailing slash", c.BaseURL())
			}
			if c.RepoID() != tt.repo {
				t.Fatalf("RepoID = %q, want %q", c.RepoID(), tt.repo)
			}
		})
	}
}

func TestClientNotFoundErrors(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()
	client := newTestClient(t, ts, "alpha")

	if _, err := client.GetRef(ctx, sessionRef); !errors.Is(err, ErrRepoNotFound) {
		t.Fatalf("GetRef on unknown repo: err = %v, want ErrRepoNotFound", err)
	}
	if _, err := client.EnsureRepo(ctx); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if _, err := client.GetRef(ctx, sessionRef); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("GetRef on unknown ref: err = %v, want ErrRefNotFound", err)
	}
	unknown := store.Hash(strings.Repeat("cd", 32))
	if _, err := client.GetObject(ctx, unknown); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("GetObject on unknown object: err = %v, want ErrObjectNotFound", err)
	}
	if has, err := client.HasObject(ctx, unknown); err != nil || has {
		t.Fatalf("HasObject = (%v, %v), want (false, nil)", has, err)
	}
}

func TestEnsureRepoIsIdempotent(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()
	client := newTestClient(t, ts, "alpha")

	created, err := client.EnsureRepo(ctx)
	if err != nil || !created {
		t.Fatalf("first EnsureRepo = (%v, %v), want (true, nil)", created, err)
	}
	created, err = client.EnsureRepo(ctx)
	if err != nil || created {
		t.Fatalf("second EnsureRepo = (%v, %v), want (false, nil)", created, err)
	}
}

func TestClientUpdateRefReportsConflict(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()
	client := newTestClient(t, ts, "alpha")

	repo := newTestRepo(t, "claude_code--s1")
	repo.commit(map[string]string{"a.txt": "one"}, "p1")
	if _, err := Push(ctx, client, repo.st, []string{sessionRef}); err != nil {
		t.Fatalf("push: %v", err)
	}
	repo.commit(map[string]string{"a.txt": "two"}, "p2")
	if err := client.PutObject(ctx, repo.tip, mustReadBlob(t, repo.st, repo.tip)); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	// Claiming the ref is still empty must not clobber the existing value.
	err := client.UpdateRef(ctx, sessionRef, "", repo.tip)
	if !errors.Is(err, store.ErrRefConflict) {
		t.Fatalf("UpdateRef with stale old: err = %v, want ErrRefConflict", err)
	}
}

func TestListRefsIsRepoScoped(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()

	alphaRepo := newTestRepo(t, "claude_code--s1")
	alphaRepo.commit(map[string]string{"a.txt": "alpha"}, "p")
	alphaClient := newTestClient(t, ts, "alpha")
	if _, err := Push(ctx, alphaClient, alphaRepo.st, []string{sessionRef}); err != nil {
		t.Fatalf("alpha push: %v", err)
	}

	betaClient := newTestClient(t, ts, "beta")
	if _, err := betaClient.EnsureRepo(ctx); err != nil {
		t.Fatalf("beta EnsureRepo: %v", err)
	}

	alphaRefs, err := alphaClient.ListRefs(ctx, "sessions")
	if err != nil {
		t.Fatalf("alpha ListRefs: %v", err)
	}
	if len(alphaRefs) != 1 || alphaRefs["claude_code--s1"] != alphaRepo.tip {
		t.Fatalf("alpha refs = %v, want one entry at %s", alphaRefs, alphaRepo.tip)
	}
	betaRefs, err := betaClient.ListRefs(ctx, "sessions")
	if err != nil {
		t.Fatalf("beta ListRefs: %v", err)
	}
	if len(betaRefs) != 0 {
		t.Fatalf("beta refs = %v, want none", betaRefs)
	}
}

func mustReadBlob(t *testing.T, st *store.Store, h store.Hash) []byte {
	t.Helper()
	data, err := st.ReadBlob(h)
	if err != nil {
		t.Fatalf("read blob %s: %v", h, err)
	}
	return data
}
