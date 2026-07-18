package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/regent-vcs/regent/internal/ignore"
	"github.com/regent-vcs/regent/internal/snapshot"
	"github.com/regent-vcs/regent/internal/store"
)

// newTreeHash snapshots a one-file workspace and returns the store and tree hash.
func newTreeHash(t *testing.T) (*store.Store, store.Hash) {
	t.Helper()
	ws := t.TempDir()
	s, err := store.Init(ws)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tree, err := snapshot.Snapshot(s, ws, ignore.Default(ws))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return s, tree
}

// TestResolveRewindTargetRejectsStepInTreeMode is the regression test for the
// HIGH finding: a step hash passed to --tree must be rejected, not silently
// treated as an empty tree (which would delete the whole workspace).
func TestResolveRewindTargetRejectsStepInTreeMode(t *testing.T) {
	s, _ := newTreeHash(t)
	stepHash, err := s.WriteStep(&store.Step{Tree: "deadbeef", SessionID: "sess", Origin: "claude_code"})
	if err != nil {
		t.Fatalf("WriteStep: %v", err)
	}

	if _, _, err := resolveRewindTarget(s, nil, string(stepHash), true); err == nil {
		t.Fatal("expected --tree with a step hash to be rejected, got nil error")
	}
}

func TestResolveRewindTargetAcceptsTreeInTreeMode(t *testing.T) {
	s, tree := newTreeHash(t)

	got, label, err := resolveRewindTarget(s, nil, string(tree), true)
	if err != nil {
		t.Fatalf("resolveRewindTarget: %v", err)
	}
	if got != tree {
		t.Errorf("target = %s, want %s", got, tree)
	}
	if label == "" {
		t.Error("expected a non-empty label")
	}
}

func TestGuardEmptyRestore(t *testing.T) {
	s, safety := newTreeHash(t) // non-empty workspace tree
	empty, err := s.WriteTree(&store.Tree{})
	if err != nil {
		t.Fatalf("WriteTree: %v", err)
	}

	if err := guardEmptyRestore(s, empty, safety, "tree x", false); err == nil {
		t.Fatal("expected guard to block empty restore over a non-empty workspace")
	}
	if err := guardEmptyRestore(s, empty, safety, "tree x", true); err != nil {
		t.Fatalf("--allow-empty should bypass the guard: %v", err)
	}
	// A non-empty target must always pass.
	if err := guardEmptyRestore(s, safety, safety, "tree y", false); err != nil {
		t.Fatalf("non-empty target should pass: %v", err)
	}
}

func TestIsStepBlobAndIsTreeBlob(t *testing.T) {
	stepRaw, _ := json.Marshal(store.Step{Tree: "t", SessionID: "s"})
	treeRaw, _ := json.Marshal(store.Tree{Entries: []store.TreeEntry{{Path: "a", Blob: "b"}}})
	emptyTreeRaw, _ := json.Marshal(store.Tree{})

	if !isStepBlob(stepRaw) {
		t.Error("step blob not detected as step")
	}
	if isStepBlob(treeRaw) {
		t.Error("tree blob misdetected as step")
	}
	if !isTreeBlob(treeRaw) {
		t.Error("tree blob not detected as tree")
	}
	if !isTreeBlob(emptyTreeRaw) {
		t.Error("empty tree blob (has entries key) should be a tree")
	}
	if isTreeBlob(stepRaw) {
		t.Error("step blob misdetected as tree")
	}
	if isTreeBlob([]byte("not json")) {
		t.Error("non-json misdetected as tree")
	}
}
