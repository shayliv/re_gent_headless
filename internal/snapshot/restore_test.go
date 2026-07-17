package snapshot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/regent-vcs/regent/internal/ignore"
	"github.com/regent-vcs/regent/internal/store"
)

// snapshotState writes the given files into a fresh workspace, snapshots it, and
// returns the store, workspace root, and recorded tree hash.
func snapshotState(t *testing.T, files map[string]string) (*store.Store, string, store.Hash) {
	t.Helper()
	workspace := t.TempDir()
	s, err := store.Init(workspace)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	writeFiles(t, workspace, files)

	ig := ignore.Default(workspace)
	tree, err := Snapshot(s, workspace, ig)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	return s, workspace, tree
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func TestRestoreCreatesModifiesDeletes(t *testing.T) {
	s, workspace, tree := snapshotState(t, map[string]string{
		"keep.txt":         "v1",
		"gone.txt":         "temporary",
		"nested/child.txt": "orig",
	})

	// Mutate the workspace away from the snapshot: change keep, delete a tracked
	// file, and add a new untracked file.
	writeFiles(t, workspace, map[string]string{
		"keep.txt":         "v2-modified",
		"nested/child.txt": "orig", // unchanged
		"added.txt":        "new file not in snapshot",
	})
	if err := os.Remove(filepath.Join(workspace, "gone.txt")); err != nil {
		t.Fatalf("remove gone.txt: %v", err)
	}

	ig := ignore.Default(workspace)
	result, err := Restore(s, workspace, tree, ig, false)
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if got := readFile(t, workspace, "keep.txt"); got != "v1" {
		t.Errorf("keep.txt = %q, want restored %q", got, "v1")
	}
	if got := readFile(t, workspace, "gone.txt"); got != "temporary" {
		t.Errorf("gone.txt = %q, want re-created %q", got, "temporary")
	}
	if pathExistsTest(filepath.Join(workspace, "added.txt")) {
		t.Errorf("added.txt should have been deleted (absent from target tree)")
	}

	assertContains(t, "Modified", result.Modified, "keep.txt")
	assertContains(t, "Created", result.Created, "gone.txt")
	assertContains(t, "Deleted", result.Deleted, "added.txt")
	if result.Unchanged != 1 {
		t.Errorf("Unchanged = %d, want 1 (nested/child.txt)", result.Unchanged)
	}
}

func TestRestoreDryRunMakesNoChanges(t *testing.T) {
	s, workspace, tree := snapshotState(t, map[string]string{"a.txt": "original"})

	writeFiles(t, workspace, map[string]string{
		"a.txt":     "changed",
		"extra.txt": "should survive dry-run",
	})

	ig := ignore.Default(workspace)
	result, err := Restore(s, workspace, tree, ig, true)
	if err != nil {
		t.Fatalf("Restore dry-run failed: %v", err)
	}

	if got := readFile(t, workspace, "a.txt"); got != "changed" {
		t.Errorf("dry-run modified a.txt: got %q, want %q", got, "changed")
	}
	if !pathExistsTest(filepath.Join(workspace, "extra.txt")) {
		t.Errorf("dry-run deleted extra.txt")
	}
	assertContains(t, "Modified", result.Modified, "a.txt")
	assertContains(t, "Deleted", result.Deleted, "extra.txt")
}

func TestRestoreNeverTouchesIgnoredPaths(t *testing.T) {
	s, workspace, tree := snapshotState(t, map[string]string{"src.txt": "code"})

	// Create an ignored file after the snapshot; restore must not delete it.
	nodeModules := filepath.Join(workspace, "node_modules")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nodeModules, "dep.js"), []byte("dep"), 0o644); err != nil {
		t.Fatalf("write dep.js: %v", err)
	}

	ig := ignore.Default(workspace)
	if _, err := Restore(s, workspace, tree, ig, false); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if !pathExistsTest(filepath.Join(nodeModules, "dep.js")) {
		t.Errorf("restore deleted an ignored file (node_modules/dep.js)")
	}
	if !pathExistsTest(filepath.Join(workspace, ".regent")) {
		t.Errorf("restore deleted the .regent store directory")
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func pathExistsTest(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func assertContains(t *testing.T, label string, got []string, want string) {
	t.Helper()
	for _, v := range got {
		if v == want {
			return
		}
	}
	t.Errorf("%s = %v, want it to contain %q", label, got, want)
}
