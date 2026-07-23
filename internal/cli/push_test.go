package cli

import (
	"context"
	"errors"
	"github.com/regent-vcs/regent/internal/config"
	"net/http/httptest"
	"testing"

	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/server"
	"github.com/regent-vcs/regent/internal/store"
)

// addSession records one step for session in st and advances its ref.
func addSession(t *testing.T, st *store.Store, session, content string) store.Hash {
	t.Helper()
	blob, err := st.WriteBlob([]byte(content))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	tree, err := st.WriteTree(&store.Tree{Entries: []store.TreeEntry{{Path: "a.txt", Blob: blob, Mode: 0o644}}})
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}
	step, err := st.WriteStep(&store.Step{
		Tree:           tree,
		SessionID:      session,
		Origin:         "claude_code",
		TimestampNanos: 1,
	})
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	if err := st.UpdateRef("sessions/"+session, "", step); err != nil {
		t.Fatalf("update ref: %v", err)
	}
	return step
}

// newPushTestStore creates a local store holding one session with one step.
func newPushTestStore(t *testing.T, session, content string) (*store.Store, store.Hash) {
	t.Helper()
	st, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return st, addSession(t, st, session, content)
}

func newPushTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, err := server.New(t.TempDir())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func TestSelectSessionRefs(t *testing.T) {
	st, _ := newPushTestStore(t, "claude_code--s1", "one")
	addSession(t, st, "codex_cli--s2", "two")

	tests := []struct {
		name     string
		sessions []string
		want     []string
		wantErr  bool
	}{
		{
			name:     "all sessions by default, sorted",
			sessions: nil,
			want:     []string{"sessions/claude_code--s1", "sessions/codex_cli--s2"},
		},
		{
			name:     "bare session id",
			sessions: []string{"claude_code--s1"},
			want:     []string{"sessions/claude_code--s1"},
		},
		{
			name:     "already prefixed",
			sessions: []string{"sessions/codex_cli--s2"},
			want:     []string{"sessions/codex_cli--s2"},
		},
		{"unknown session", []string{"nope"}, nil, true},
		{"empty session id", []string{"  "}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectSessionRefs(st, tt.sessions)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("selectSessionRefs(%v) = %v, want error", tt.sessions, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectSessionRefs(%v): %v", tt.sessions, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("refs = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("refs = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestSelectSessionRefsWithNoSessions(t *testing.T) {
	st, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	if _, err := selectSessionRefs(st, nil); err == nil {
		t.Fatal("expected an error when there is nothing to push")
	}
}

// TestRunPushKeepsTwoReposApart is the CLI-level acceptance check: two
// workspaces pushed to two repo ids on one server keep their own refs and
// objects, even though both use the same session id.
func TestRunPushKeepsTwoReposApart(t *testing.T) {
	ts := newPushTestServer(t)
	ctx := context.Background()

	alphaStore, alphaStep := newPushTestStore(t, "claude_code--s1", "alpha content")
	betaStore, betaStep := newPushTestStore(t, "claude_code--s1", "beta content")

	for _, tc := range []struct {
		repo  string
		st    *store.Store
		step  store.Hash
		other store.Hash
	}{
		{"alpha", alphaStore, alphaStep, betaStep},
		{"beta", betaStore, betaStep, alphaStep},
	} {
		stats, err := runPush(ctx, tc.st, pushParams{URL: ts.URL, RepoID: tc.repo})
		if err != nil {
			t.Fatalf("push %s: %v", tc.repo, err)
		}
		if stats.RefsUpdated != 1 {
			t.Fatalf("%s: RefsUpdated = %d, want 1", tc.repo, stats.RefsUpdated)
		}

		client, err := remote.NewClient(ts.URL, tc.repo)
		if err != nil {
			t.Fatalf("client: %v", err)
		}
		got, err := client.GetRef(ctx, "sessions/claude_code--s1")
		if err != nil {
			t.Fatalf("%s GetRef: %v", tc.repo, err)
		}
		if got != tc.step {
			t.Fatalf("%s ref = %s, want %s", tc.repo, got, tc.step)
		}
		if has, err := client.HasObject(ctx, tc.other); err != nil || has {
			t.Fatalf("%s can see the other repo's step (has=%v, err=%v)", tc.repo, has, err)
		}
	}
}

func TestRunPushRejectsBadTarget(t *testing.T) {
	st, _ := newPushTestStore(t, "claude_code--s1", "one")

	tests := []struct {
		name string
		p    pushParams
	}{
		{"missing url", pushParams{RepoID: "alpha"}},
		{"bad url scheme", pushParams{URL: "ftp://example.com", RepoID: "alpha"}},
		{"missing repo", pushParams{URL: "http://127.0.0.1:1"}},
		{"invalid repo id", pushParams{URL: "http://127.0.0.1:1", RepoID: "Alpha"}},
		{"traversal repo id", pushParams{URL: "http://127.0.0.1:1", RepoID: "../etc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := runPush(context.Background(), st, tt.p); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// TestResolveTargetRemembersRepoIdentity is the end-to-end identity check: the
// first push binds the working copy to one repo on one server, and every later
// push resolves that identity with no flags.
func TestResolveTargetRemembersRepoIdentity(t *testing.T) {
	ts := newPushTestServer(t)
	st, _ := newPushTestStore(t, "claude_code--s1", "alpha content")

	// Nothing recorded yet: a push with no flags has no repo to address.
	if _, err := resolveTarget(st, pushParams{}); err == nil {
		t.Fatal("expected an error when no repo identity is recorded")
	}

	first, err := resolveTarget(st, pushParams{URL: ts.URL, RepoID: "alpha"})
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if _, err := runPush(context.Background(), st, first); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if err := rememberTarget(st, first); err != nil {
		t.Fatalf("rememberTarget: %v", err)
	}

	got, err := resolveTarget(st, pushParams{})
	if err != nil {
		t.Fatalf("resolveTarget after first push: %v", err)
	}
	if got.URL != ts.URL || got.RepoID != "alpha" {
		t.Fatalf("recorded target = %s/%s, want %s/alpha", got.URL, got.RepoID, ts.URL)
	}
}

// TestRememberTargetDoesNotRebindAnAlreadyBoundRepo pins the safety rule that
// keeps two repos apart: a one-off push to another repo id must not move where
// this working copy pushes by default.
func TestRememberTargetDoesNotRebindAnAlreadyBoundRepo(t *testing.T) {
	st, _ := newPushTestStore(t, "claude_code--s1", "alpha content")

	bound := pushParams{URL: "http://127.0.0.1:7654", RepoID: "alpha"}
	if err := rememberTarget(st, bound); err != nil {
		t.Fatalf("rememberTarget: %v", err)
	}
	if err := rememberTarget(st, pushParams{URL: "http://127.0.0.1:7655", RepoID: "beta"}); err != nil {
		t.Fatalf("rememberTarget (second repo): %v", err)
	}

	got, err := resolveTarget(st, pushParams{})
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if got.URL != bound.URL || got.RepoID != bound.RepoID {
		t.Fatalf("recorded target = %s/%s, want %s/%s", got.URL, got.RepoID, bound.URL, bound.RepoID)
	}

	// An explicit flag still wins for the invocation that passes it.
	override, err := resolveTarget(st, pushParams{RepoID: "beta"})
	if err != nil {
		t.Fatalf("resolveTarget with override: %v", err)
	}
	if override.RepoID != "beta" || override.URL != bound.URL {
		t.Fatalf("override target = %s/%s, want %s/beta", override.URL, override.RepoID, bound.URL)
	}
}

// TestRecordedIdentitiesStayPerWorkingCopy: two working copies on one machine
// each remember their own repo, so neither can drift into the other's history.
func TestRecordedIdentitiesStayPerWorkingCopy(t *testing.T) {
	ts := newPushTestServer(t)
	ctx := context.Background()

	alphaStore, alphaStep := newPushTestStore(t, "claude_code--s1", "alpha content")
	betaStore, betaStep := newPushTestStore(t, "claude_code--s1", "beta content")

	for _, tc := range []struct {
		repo string
		st   *store.Store
	}{{"alpha", alphaStore}, {"beta", betaStore}} {
		target, err := resolveTarget(tc.st, pushParams{URL: ts.URL, RepoID: tc.repo})
		if err != nil {
			t.Fatalf("%s resolveTarget: %v", tc.repo, err)
		}
		if _, err := runPush(ctx, tc.st, target); err != nil {
			t.Fatalf("%s push: %v", tc.repo, err)
		}
		if err := rememberTarget(tc.st, target); err != nil {
			t.Fatalf("%s rememberTarget: %v", tc.repo, err)
		}
	}

	// Both working copies now push with no flags; the same session id must
	// still land on two different tips.
	for _, tc := range []struct {
		repo string
		st   *store.Store
		want store.Hash
	}{{"alpha", alphaStore, alphaStep}, {"beta", betaStore, betaStep}} {
		target, err := resolveTarget(tc.st, pushParams{})
		if err != nil {
			t.Fatalf("%s resolveTarget: %v", tc.repo, err)
		}
		if target.RepoID != tc.repo {
			t.Fatalf("%s resolved repo %q", tc.repo, target.RepoID)
		}
		if _, err := runPush(ctx, tc.st, target); err != nil {
			t.Fatalf("%s flagless push: %v", tc.repo, err)
		}

		client, err := remote.NewClient(ts.URL, tc.repo)
		if err != nil {
			t.Fatalf("client: %v", err)
		}
		got, err := client.GetRef(ctx, "sessions/claude_code--s1")
		if err != nil {
			t.Fatalf("%s GetRef: %v", tc.repo, err)
		}
		if got != tc.want {
			t.Fatalf("%s ref = %s, want %s", tc.repo, got, tc.want)
		}
	}
}

func TestPushCmdRejectsUnknownFlags(t *testing.T) {
	cmd := PushCmd()
	cmd.SetArgs([]string{"--nope"})
	cmd.SetOut(nopWriter{})
	cmd.SetErr(nopWriter{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("rgt push should reject unknown flags")
	}
}

func TestPushCmdRejectsPositionalArgs(t *testing.T) {
	cmd := PushCmd()
	cmd.SetArgs([]string{"--url", "http://127.0.0.1:1", "--repo", "alpha", "unexpected"})
	cmd.SetOut(nopWriter{})
	cmd.SetErr(nopWriter{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("rgt push should reject positional arguments")
	}
}

// nopWriter silences cobra output during tests.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- Auth gate (RE-9): push must fail closed when unauthenticated. ---
// These exercise requirePushAuth, the gate PushCmd.RunE applies before any
// network operation. Behaviour matches RE-9's original acceptance criteria.

// TestPushRejectsUnauthenticated verifies the gate fails closed when no token
// is configured. This is the primary acceptance gate for RE-9.
func TestPushRejectsUnauthenticated(t *testing.T) {
	err := requirePushAuth(&config.UserConfig{})
	if err == nil {
		t.Fatal("expected error for unauthenticated push, got nil")
	}
	if !errors.Is(err, config.ErrNotSignedIn) {
		t.Errorf("expected ErrNotSignedIn, got %v", err)
	}
}

// TestPushRejectsNilConfig ensures a nil config also fails closed.
func TestPushRejectsNilConfig(t *testing.T) {
	err := requirePushAuth(nil)
	if err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
	if !errors.Is(err, config.ErrNotSignedIn) {
		t.Errorf("expected ErrNotSignedIn, got %v", err)
	}
}

// TestPushRejectsInvalidToken ensures a token that is present but too short
// also fails closed (ErrTokenInvalid, which is distinct from ErrNotSignedIn).
func TestPushRejectsInvalidToken(t *testing.T) {
	cfg := &config.UserConfig{Auth: config.Auth{Token: "short"}}
	err := requirePushAuth(cfg)
	if err == nil {
		t.Fatal("expected error for invalid token, got nil")
	}
	if !errors.Is(err, config.ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

// TestPushAllowsValidToken verifies that a properly formed token passes the
// auth gate.
func TestPushAllowsValidToken(t *testing.T) {
	cfg := &config.UserConfig{Auth: config.Auth{
		ServerURL: "https://regent.example.com",
		Token:     "a-valid-token-of-sufficient-length",
	}}
	if err := requirePushAuth(cfg); err != nil {
		t.Errorf("unexpected error with valid token: %v", err)
	}
}

// TestPushAuthDegradationIsNonPanicking verifies that even pathological input
// does not cause a panic — auth failures must never crash an agent turn.
func TestPushAuthDegradationIsNonPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("requirePushAuth panicked: %v", r)
		}
	}()
	// Nil config simulates an auth subsystem failure without a config file.
	_ = requirePushAuth(nil)
}
