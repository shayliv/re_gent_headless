package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/regent-vcs/regent/internal/ignore"
	"github.com/regent-vcs/regent/internal/index"
	"github.com/regent-vcs/regent/internal/snapshot"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

// RewindCmd restores the workspace to the version recorded at a previous step.
func RewindCmd() *cobra.Command {
	var dryRun bool
	var treeMode bool
	var allowEmpty bool

	cmd := &cobra.Command{
		Use:   "rewind <step>",
		Short: "Restore the workspace to a previous step",
		Long: `Restore the working tree so its tracked files match the snapshot recorded at <step>.

Before changing anything, rewind snapshots the current workspace into the object
store and prints that tree hash, so the operation is always recoverable with
'rgt rewind --tree <hash>'. Ignored paths (.regent/, .git/, node_modules/, ...)
are never touched.

Use --dry-run to preview the changes without modifying any files. Restoring an
empty tree (which would delete every tracked file) requires --allow-empty.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}

			idx, err := index.Open(s)
			if err != nil {
				return fmt.Errorf("open index: %w", err)
			}
			defer func() { _ = idx.Close() }()

			targetTree, label, err := resolveRewindTarget(s, idx, args[0], treeMode)
			if err != nil {
				return err
			}

			root := filepath.Dir(s.Root)
			ig := ignore.Default(root)

			// Safety snapshot of the current workspace so the pre-rewind state is
			// always recoverable from the object store.
			safety, err := snapshot.Snapshot(s, root, ig)
			if err != nil {
				return fmt.Errorf("snapshot current workspace: %w", err)
			}

			// Guard: never silently wipe a non-empty workspace by restoring an
			// empty tree (e.g. a mistyped/step hash, or a genuinely empty tree).
			if err := guardEmptyRestore(s, targetTree, safety, label, allowEmpty); err != nil {
				return err
			}

			result, err := snapshot.Restore(s, root, targetTree, ig, dryRun)
			if err != nil {
				return err
			}

			printRewindResult(cmd, label, safety, result, dryRun)
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without modifying any files")
	cmd.Flags().BoolVar(&treeMode, "tree", false, "Treat the argument as a tree hash instead of a step hash")
	cmd.Flags().BoolVar(&allowEmpty, "allow-empty", false, "Allow restoring an empty tree that would delete all tracked files")
	return cmd
}

// resolveRewindTarget resolves the user-supplied argument to the tree that should
// be restored, along with a short label for display. When treeMode is set the
// argument must be a tree hash; otherwise it is a step hash whose recorded tree
// is used.
func resolveRewindTarget(s *store.Store, idx *index.DB, arg string, treeMode bool) (store.Hash, string, error) {
	if treeMode {
		// Read the raw object so we can verify it is actually a tree. Both steps
		// and trees are JSON blobs, and a Step unmarshals into an (empty) Tree
		// without error — which would make Restore delete every file. Reject
		// anything that is not genuinely a tree.
		raw, err := s.ReadBlob(store.Hash(arg))
		if err != nil {
			return "", "", fmt.Errorf("read object %s: %w", shortHashLabel(arg), err)
		}
		if isStepBlob(raw) {
			return "", "", fmt.Errorf("%s is a step, not a tree; run 'rgt rewind %s' without --tree", shortHashLabel(arg), shortHashLabel(arg))
		}
		if !isTreeBlob(raw) {
			return "", "", fmt.Errorf("%s is not a tree object", shortHashLabel(arg))
		}
		return store.Hash(arg), "tree " + shortHashLabel(arg), nil
	}

	stepHash, err := idx.NormalizeStepHash(arg)
	if err != nil {
		return "", "", err
	}
	step, err := s.ReadStep(stepHash)
	if err != nil {
		return "", "", fmt.Errorf("read step %s: %w", stepHash, err)
	}
	if step.Tree == "" {
		return "", "", fmt.Errorf("step %s has no recorded tree", shortHashLabel(string(stepHash)))
	}
	return step.Tree, "step " + shortHashLabel(string(stepHash)), nil
}

// guardEmptyRestore blocks restoring an empty target tree over a non-empty
// workspace unless allowEmpty is set. This is the second line of defense against
// accidentally deleting every tracked file (the first is tree-hash validation).
func guardEmptyRestore(s *store.Store, targetTree, safetyTree store.Hash, label string, allowEmpty bool) error {
	if allowEmpty {
		return nil
	}
	target, err := s.ReadTree(targetTree)
	if err != nil {
		return fmt.Errorf("read target tree: %w", err)
	}
	if len(target.Entries) > 0 {
		return nil
	}
	safety, err := s.ReadTree(safetyTree)
	if err != nil {
		return fmt.Errorf("read safety tree: %w", err)
	}
	if len(safety.Entries) == 0 {
		return nil // nothing to lose
	}
	return fmt.Errorf("%s is empty; restoring it would delete all %d tracked file(s) in the workspace. Re-run with --allow-empty to confirm", label, len(safety.Entries))
}

// isStepBlob reports whether raw is a serialized step (as opposed to a tree).
// A tree blob has none of these fields set, so it returns false for trees.
func isStepBlob(raw []byte) bool {
	var st store.Step
	if err := json.Unmarshal(raw, &st); err != nil {
		return false
	}
	return st.Tree != "" || st.SessionID != "" || len(st.Causes) != 0 || st.Cause.ToolName != ""
}

// isTreeBlob reports whether raw is a serialized tree, i.e. a JSON object that
// carries an "entries" key (even if its value is null or empty). Non-JSON or
// unrelated blobs (e.g. steps, which have no "entries" key) return false.
func isTreeBlob(raw []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m["entries"]
	return ok
}

func printRewindResult(cmd *cobra.Command, label string, safety store.Hash, result snapshot.RestoreResult, dryRun bool) {
	out := cmd.OutOrStdout()

	verb := "Rewound to"
	if dryRun {
		verb = "Would rewind to"
	}
	fmt.Fprintf(out, "%s %s\n\n", verb, label)
	fmt.Fprintf(out, "  %s created:   %d\n", style.Success(""), len(result.Created))
	fmt.Fprintf(out, "  %s modified:  %d\n", style.Success(""), len(result.Modified))
	fmt.Fprintf(out, "  %s deleted:   %d\n", style.Success(""), len(result.Deleted))
	fmt.Fprintf(out, "  %s unchanged: %d\n", style.DimText("-"), result.Unchanged)

	if dryRun {
		fmt.Fprintf(out, "\n%s No files were modified (--dry-run)\n", style.DimText("-"))
		return
	}

	fmt.Fprintf(out, "\n%s Pre-rewind workspace saved as tree %s\n", style.Label("Recoverable:"), safety)
	fmt.Fprintf(out, "  Undo with: rgt rewind --tree %s\n", safety)
}

// shortHashLabel abbreviates a hash for display, mirroring git-style short hashes.
func shortHashLabel(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
