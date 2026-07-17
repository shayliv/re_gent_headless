package snapshot

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/regent-vcs/regent/internal/ignore"
	"github.com/regent-vcs/regent/internal/store"
)

// RestoreResult summarizes the filesystem changes a Restore performed, or would
// perform when dryRun is set.
type RestoreResult struct {
	Created   []string // files that did not exist and were written
	Modified  []string // files that existed with different content and were rewritten
	Deleted   []string // files present in the workspace but absent from the target tree
	Unchanged int      // target files already matching their recorded content
}

// Restore makes the workspace under root match the given tree: files recorded in
// the tree are written, and tracked files present in the workspace but absent
// from the tree are deleted. It is the inverse of Snapshot and shares its
// walk/ignore conventions, so paths matched by ig (.regent/, .git/,
// node_modules/, ...) are never read, written, or removed — the object store and
// unrelated VCS metadata are always preserved.
//
// When dryRun is true no files are written or removed; the returned result still
// describes the changes that would be made.
func Restore(s *store.Store, root string, treeHash store.Hash, ig *ignore.Matcher, dryRun bool) (RestoreResult, error) {
	var result RestoreResult

	tree, err := s.ReadTree(treeHash)
	if err != nil {
		return result, fmt.Errorf("read tree %s: %w", treeHash, err)
	}

	targetPaths := make(map[string]struct{}, len(tree.Entries))
	for _, entry := range tree.Entries {
		targetPaths[entry.Path] = struct{}{}
	}

	// Collect the current, non-ignored workspace files that are not in the
	// target tree. We gather them during the walk and delete afterwards so we
	// never mutate the directory tree while WalkDir is traversing it.
	toDelete, err := scanDeletable(root, targetPaths, ig)
	if err != nil {
		return result, err
	}

	sort.Strings(toDelete)
	for _, rel := range toDelete {
		result.Deleted = append(result.Deleted, rel)
		if dryRun {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return result, fmt.Errorf("delete %s: %w", rel, err)
		}
	}

	for _, entry := range tree.Entries {
		content, err := s.ReadBlob(entry.Blob)
		if err != nil {
			return result, fmt.Errorf("read blob for %s: %w", entry.Path, err)
		}

		abs := filepath.Join(root, filepath.FromSlash(entry.Path))
		mode := os.FileMode(entry.Mode)
		if mode == 0 {
			mode = 0o644
		}

		existing, readErr := os.ReadFile(abs)
		switch {
		case readErr == nil && bytes.Equal(existing, content):
			result.Unchanged++
			continue
		case readErr == nil:
			result.Modified = append(result.Modified, entry.Path)
		default:
			result.Created = append(result.Created, entry.Path)
		}

		if dryRun {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return result, fmt.Errorf("create dir for %s: %w", entry.Path, err)
		}
		if err := writeWorkspaceFile(abs, content, mode); err != nil {
			return result, fmt.Errorf("write %s: %w", entry.Path, err)
		}
	}

	if !dryRun && len(toDelete) > 0 {
		pruneEmptyDirs(root, toDelete)
	}

	return result, nil
}

// scanDeletable walks the workspace (honoring ig) and returns the slash-relative
// paths of files that exist on disk but are absent from targetPaths.
func scanDeletable(root string, targetPaths map[string]struct{}, ig *ignore.Matcher) ([]string, error) {
	var toDelete []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		isDir := d.IsDir()
		if ig.Match(filepath.ToSlash(rel), isDir) {
			if isDir {
				return fs.SkipDir
			}
			return nil
		}
		if isDir {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if _, keep := targetPaths[slashRel]; !keep {
			toDelete = append(toDelete, slashRel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan workspace: %w", err)
	}
	return toDelete, nil
}

// writeWorkspaceFile writes content to path with the recorded mode, clearing any
// read-only bit on an existing file first so the write is not blocked.
func writeWorkspaceFile(path string, content []byte, mode os.FileMode) error {
	if _, err := os.Stat(path); err == nil {
		_ = os.Chmod(path, 0o644)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return err
	}
	// Apply the recorded mode explicitly; WriteFile only sets it on create.
	return os.Chmod(path, mode)
}

// pruneEmptyDirs removes directories left empty by deletions, walking up from
// each deleted file toward root. Best-effort: any error stops that branch.
func pruneEmptyDirs(root string, deleted []string) {
	visited := make(map[string]struct{})
	for _, rel := range deleted {
		dir := filepath.Dir(filepath.FromSlash(rel))
		for dir != "." && dir != string(filepath.Separator) {
			abs := filepath.Join(root, dir)
			if _, seen := visited[abs]; seen {
				break
			}
			visited[abs] = struct{}{}

			entries, err := os.ReadDir(abs)
			if err != nil || len(entries) > 0 {
				break
			}
			if err := os.Remove(abs); err != nil {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
}
