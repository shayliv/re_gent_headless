package push

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/server"
	"github.com/regent-vcs/regent/internal/store"
)

// makeStore creates a temp store and returns it.
func makeStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Init(dir)
	if err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return s
}

// buildStep writes a blob + tree + step and advances the session ref.
// Returns the step hash.
func buildStep(t *testing.T, s *store.Store, parent store.Hash, sessionID string, fileContent string) store.Hash {
	t.Helper()

	blobHash, err := s.WriteBlob([]byte(fileContent))
	if err != nil {
		t.Fatalf("WriteBlob: %v", err)
	}

	treeHash, err := s.WriteTree(&store.Tree{
		Entries: []store.TreeEntry{
			{Path: "file.txt", Blob: blobHash, Mode: 0o644},
		},
	})
	if err != nil {
		t.Fatalf("WriteTree: %v", err)
	}

	step := &store.Step{
		Parent:         parent,
		Tree:           treeHash,
		SessionID:      sessionID,
		TimestampNanos: time.Now().UnixNano(),
		Causes:         []store.Cause{{ToolUseID: "tool-1", ToolName: "Write"}},
	}
	stepHash, err := s.WriteStep(step)
	if err != nil {
		t.Fatalf("WriteStep: %v", err)
	}

	old, _ := s.ReadRef("sessions/" + sessionID)
	if err := s.UpdateRef("sessions/"+sessionID, old, stepHash); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}
	return stepHash
}

// TestPushLocalRemote verifies a basic push via in-process LocalRemote.
func TestPushLocalRemote(t *testing.T) {
	local := makeStore(t)
	rem := makeStore(t)

	buildStep(t, local, "", "sess-1", "hello")

	result, err := Push(local, LocalRemote(rem))
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if result.Uploaded == 0 {
		t.Error("expected at least one object uploaded")
	}
	if len(result.RefsUpdated) != 1 {
		t.Errorf("expected 1 ref updated, got %d", len(result.RefsUpdated))
	}

	localTip, _ := local.ReadRef("sessions/sess-1")
	remoteTip, _ := rem.ReadRef("sessions/sess-1")
	if localTip != remoteTip {
		t.Errorf("remote tip %s != local tip %s", remoteTip, localTip)
	}
}

// TestPushIdempotent verifies that a second push sends 0 objects and updates 0 refs.
func TestPushIdempotent(t *testing.T) {
	local := makeStore(t)
	rem := makeStore(t)

	buildStep(t, local, "", "sess-1", "hello")
	r := LocalRemote(rem)

	res1, err := Push(local, r)
	if err != nil {
		t.Fatalf("first Push: %v", err)
	}
	if res1.Uploaded == 0 {
		t.Error("first push should upload objects")
	}

	res2, err := Push(local, r)
	if err != nil {
		t.Fatalf("second Push: %v", err)
	}
	if res2.Uploaded != 0 {
		t.Errorf("idempotent push: expected 0 uploads, got %d", res2.Uploaded)
	}
	if len(res2.RefsUpdated) != 0 {
		t.Errorf("idempotent push: expected 0 refs updated, got %v", res2.RefsUpdated)
	}
}

// TestPushRefOnlyAfterObjects proves the ordering invariant: every object
// referenced by the remote ref tip must already be on the remote.
func TestPushRefOnlyAfterObjects(t *testing.T) {
	local := makeStore(t)
	rem := makeStore(t)
	stepHash := buildStep(t, local, "", "sess-1", "content")

	_, err := Push(local, LocalRemote(rem))
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if !rem.ObjectExists(stepHash) {
		t.Error("remote missing step blob after push")
	}

	remStep, err := rem.ReadStep(stepHash)
	if err != nil {
		t.Fatalf("ReadStep on remote: %v", err)
	}
	if !rem.ObjectExists(remStep.Tree) {
		t.Error("remote missing tree blob after push")
	}
}

// TestPushIncrementalTwoSteps verifies that adding a second step locally
// only uploads the delta on the second push.
func TestPushIncrementalTwoSteps(t *testing.T) {
	local := makeStore(t)
	rem := makeStore(t)
	r := LocalRemote(rem)

	step1 := buildStep(t, local, "", "sess-1", "v1")

	res1, err := Push(local, r)
	if err != nil {
		t.Fatalf("Push 1: %v", err)
	}
	_ = res1

	buildStep(t, local, step1, "sess-1", "v2")

	res2, err := Push(local, r)
	if err != nil {
		t.Fatalf("Push 2: %v", err)
	}
	if res2.Uploaded == 0 {
		t.Error("incremental push should upload new objects")
	}

	localTip, _ := local.ReadRef("sessions/sess-1")
	remoteTip, _ := rem.ReadRef("sessions/sess-1")
	if localTip != remoteTip {
		t.Errorf("after push 2: remote tip %s != local tip %s", remoteTip, localTip)
	}
}

// TestPushHTTPRemote verifies push against the HTTP server handler.
func TestPushHTTPRemote(t *testing.T) {
	local := makeStore(t)
	remStore := makeStore(t)

	buildStep(t, local, "", "sess-http", "hello over http")

	srv := httptest.NewServer(server.Handler(remStore))
	defer srv.Close()

	r := remote.NewHTTP(srv.URL, nil)

	res, err := Push(local, r)
	if err != nil {
		t.Fatalf("Push over HTTP: %v", err)
	}
	if res.Uploaded == 0 {
		t.Error("expected objects to be uploaded")
	}
	if len(res.RefsUpdated) != 1 {
		t.Errorf("expected 1 ref updated, got %d", len(res.RefsUpdated))
	}

	localTip, _ := local.ReadRef("sessions/sess-http")
	remoteTip, _ := remStore.ReadRef("sessions/sess-http")
	if localTip != remoteTip {
		t.Errorf("HTTP push: remote tip %s != local tip %s", remoteTip, localTip)
	}
}

// TestPushHTTPIdempotent verifies idempotency through the HTTP server.
func TestPushHTTPIdempotent(t *testing.T) {
	local := makeStore(t)
	remStore := makeStore(t)
	buildStep(t, local, "", "sess-idem", "data")

	srv := httptest.NewServer(server.Handler(remStore))
	defer srv.Close()
	r := remote.NewHTTP(srv.URL, nil)

	if _, err := Push(local, r); err != nil {
		t.Fatalf("first push: %v", err)
	}

	res2, err := Push(local, r)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if res2.Uploaded != 0 {
		t.Errorf("idempotent HTTP push uploaded %d objects, want 0", res2.Uploaded)
	}
	if len(res2.RefsUpdated) != 0 {
		t.Errorf("idempotent HTTP push updated %d refs, want 0", len(res2.RefsUpdated))
	}
}

// TestPushInterruptConsistency simulates a push interrupted between object
// upload and ref update.  After the interruption, objects must be on the
// remote but the ref must NOT point to the step (it was never set).
func TestPushInterruptConsistency(t *testing.T) {
	local := makeStore(t)
	remStore := makeStore(t)
	stepHash := buildStep(t, local, "", "sess-int", "interrupt test")

	interrupted := &interruptingRemote{Remote: LocalRemote(remStore)}

	_, err := Push(local, interrupted)
	if err == nil {
		t.Fatal("expected error from interrupted push")
	}

	// Objects must be present (uploaded before interrupt).
	if !remStore.ObjectExists(stepHash) {
		t.Error("after interrupted push: remote missing step object")
	}

	// The ref must NOT be set.
	remoteTip, _ := remStore.ReadRef("sessions/sess-int")
	if remoteTip != "" {
		t.Errorf("after interrupted push: remote ref is %s, want empty", remoteTip)
	}
}

// interruptingRemote wraps a Remote and fails every UpdateRef call.
type interruptingRemote struct {
	remote.Remote
}

func (i *interruptingRemote) UpdateRef(name string, old, new store.Hash) error {
	return fmt.Errorf("simulated network failure during ref update for %s", name)
}
