# PR Review: #1 — feat(rewind): restore workspace to a previous step

**Reviewed**: 2026-07-17
**Author**: arad1410
**Branch**: feat/rewind → main
**Decision**: REQUEST CHANGES

## Summary
The rewind feature is well-structured and mirrors the existing `Snapshot`/ignore
conventions cleanly, with good test coverage on `snapshot.Restore`. One confirmed
data-loss footgun blocks approval: `rgt rewind --tree <hash>` does not validate
that the blob is actually a tree, so passing a **step** hash (the identifier users
see everywhere) silently resolves to an empty tree and **deletes the entire
workspace**. Recoverable via the safety snapshot, but silent and easily triggered.

## Findings

### CRITICAL
None.

### HIGH
- **`--tree` type confusion wipes the workspace** — `internal/cli/rewind.go`, `resolveRewindTarget` (tree mode). `s.ReadTree(arg)` unmarshals any blob into `Tree{Entries}`. A Step blob has no `entries` key, so it yields an *empty* tree with no error; `snapshot.Restore` then treats every workspace file as "absent from target" and deletes it. **Reproduced:** `rgt rewind --tree <step-hash>` deleted `keep.py`, leaving the workspace empty ("deleted: 1", no files remain). Step-mode guards this exact case (`if step.Tree == "" → error`), but tree-mode does not.
  - *Fix:* validate the target is genuinely a tree (reject a blob that parses as a Step with a non-empty `tree`, or require the tree JSON to contain `entries`); and/or refuse to restore an **empty** target tree over a **non-empty** workspace unless an explicit `--allow-empty`/confirmation is given.

### MEDIUM
- **Non-atomic restore** — `internal/snapshot/restore.go`, `Restore`. A blob-read or write error mid-loop leaves the workspace half-restored (some files deleted/written, others not). Recoverable via the safety tree, but there is no rollback. Acceptable for v0; consider staging then swapping, or at least document the partial-state possibility.
- **No tests for the CLI command / `--tree` path** — `internal/cli/rewind.go` has no test. `resolveRewindTarget`, the step-vs-tree distinction, and the empty-tree behavior are untested; a single test would have caught the HIGH finding above. `snapshot.Restore` itself is well tested.

### LOW
- **Safety snapshot is unreferenced** — `internal/cli/rewind.go`, `printRewindResult`. The pre-rewind tree is written to the object store but pointed to by no ref; recovery depends on the user keeping the printed hash, and it becomes GC-eligible once GC lands. Consider writing a `refs/rewind-backups/<ts>` ref.
- **Symlink handling** — `internal/snapshot/restore.go`, `scanDeletable` treats symlinks as regular files (may delete them), and `Snapshot` follows symlinks, so symlink-ness is not round-tripped. Edge case; fine for v0.

## Validation Results

| Check | Result |
|---|---|
| Type check (`go vet ./...`) | Pass |
| Lint | Skipped (vet used) |
| Tests (`go test ./internal/...`) | Pass |
| Build (`go build ./...`) | Pass |

## Files Reviewed
- `internal/snapshot/restore.go` — Added
- `internal/snapshot/restore_test.go` — Added
- `internal/cli/rewind.go` — Added
- `cmd/rgt/main.go` — Modified (register `RewindCmd`)
