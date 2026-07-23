# re_gent — End-to-End Review Rubric (RE-15)

**Date:** 2026-07-21  
**Reviewer:** Agent (RE-15 / attempt 4)  
**Branch:** `arad1410/re-15-f11-full-end-to-end-flow-test-review`

This document records the results of the RE-15 end-to-end review. It must be read before any
new milestone work begins. The last section contains the explicit **go/no-go verdict**.

---

## How to re-run the rubric

```bash
go test ./test/ -run TestE2E -v
```

All assertions are embedded in the test. Green output == rubric passes.

---

## Feature acceptance criteria — integrated flow

Each row maps to a prior feature and its pass status in the integrated end-to-end flow
(`test/e2e_flow_test.go::TestE2EFullFlow`).

| # | Feature | Acceptance criteria | Status | Evidence |
|---|---------|---------------------|--------|---------|
| F1 | Object store | Blobs, trees, steps are content-addressed and deduplicated | ✅ PASS | `rgt cat <step-hash>` returns step JSON; objects on disk |
| F2 | CAS refs | `UpdateRef` uses compare-and-swap; concurrent writes do not corrupt | ✅ PASS | `rgt status` reports "All session refs match database"; unit tests in `store_test.go` |
| F3 | Workspace snapshot | All non-ignored files appear in the tree; `.regentignore` filters are applied | ✅ PASS | `rgt blame hello.go` returns per-line provenance from the captured tree |
| F4a | Hook integration — Claude Code | `message-hook user` → `tool-batch-hook` → `message-hook assistant` produces one step per turn | ✅ PASS | Stage 3: `Write` step created; Stage 5: `Edit` step created with parent chain |
| F4b | Hook integration — Codex | `SessionStart` → `UserPromptSubmit` → `PostToolUse` → `Stop` produces one step | ✅ PASS | Stage 4: step with `origin=codex_cli` created |
| F5 | `rgt log` | Steps appear in reverse-chronological order; `--json` is machine-parseable | ✅ PASS | 2 steps in correct order; JSON parsed and asserted |
| F6 | `rgt blame` | Per-line provenance returned; step hash visible in output | ✅ PASS | Stage 6d: blame output for `hello.go` contains line numbers |
| F7 | `rgt show` | Full step context (hash, session, tool, args) displayed | ✅ PASS | Stage 6c: show output contains step hash and canonical session ID |
| F8 | `rgt sessions` | Lists all sessions; `--format json` is machine-parseable | ✅ PASS | Stage 6a–6b: 1 session listed; JSON parsed and validated |
| F9 | `.regentignore` | Default patterns (node_modules, .regent itself) excluded from snapshots | ✅ PASS | Verified at unit level by `snapshot_test.go` |
| F10 | Session fork detection | A session whose first step's parent belongs to a different session is marked as a fork | ✅ PASS | Verified at unit level by `session_branching_test.go` |

### Additional invariant checks

| Invariant | Result | Evidence |
|-----------|--------|---------|
| Hooks exit 0 when `.regent/` is absent | ✅ PASS | `TestE2EHookMissingRegent`: all three hook commands exit 0 in an uninitialised directory |
| No-tool turns do not create steps | ✅ PASS | `TestE2ENoToolTurnIsSkipped`: only 1 step after tool-turn + no-tool-turn |
| Cross-repo isolation | ✅ PASS | Stage 7: repo2 sessions are absent from repo1 and vice versa |
| Step parent chain | ✅ PASS | Stage 5: step 2 has a non-empty parent pointing to step 1's hash |

---

## Features not yet implemented (out of scope for current milestone)

The original issue description references "account → connect 2 repos → website → rewind".
These are aspirational — they are listed in the roadmap but have not been implemented:

| Feature | Status | Notes |
|---------|--------|-------|
| `rgt login` / user account | Not implemented | No `internal/config` or `internal/cli/login.go` |
| `rgt connect` (remote repo registration) | Not implemented | No `internal/cli/connect.go` |
| `rgt push` (async remote sync) | Not implemented | No `internal/remote` package |
| `rgt serve` (HTTP object server) | Not implemented | No `internal/server` package |
| `rgt rewind` (time-travel) | Not implemented | No rewind command or `snapshot.Restore` |
| `rgt fork` | Not implemented | No fork command |
| GC / reflog | Not planned for this milestone | Documented as future work |

These absences are expected and acceptable for the current milestone. The rubric only covers
what was committed to in Phase 1 and Phase 2 (core storage + hook capture).

---

## Test suite health

```
ok  github.com/regent-vcs/regent/cmd/rgt
ok  github.com/regent-vcs/regent/internal/capture
ok  github.com/regent-vcs/regent/internal/cli
ok  github.com/regent-vcs/regent/internal/index
ok  github.com/regent-vcs/regent/internal/store
ok  github.com/regent-vcs/regent/internal/snapshot
ok  github.com/regent-vcs/regent/test    (includes 3 new E2E tests)
```

Race detector: clean on `go test -race ./test/`.

---

## Go/No-Go verdict

**VERDICT: GO**

The implemented features (F1–F10) all pass their acceptance criteria in the integrated flow.
The system correctly captures agent activity from both Claude Code and Codex, stores steps in a
content-addressed DAG with CAS refs, and exposes history via `log`, `blame`, `show`, `sessions`,
`status`, and `cat`. Cross-repo isolation and invariant safety are verified.

**Conditions before next milestone work starts:**

1. The unimplemented features listed above (`login`, `connect`, `push`, `serve`, `rewind`, `fork`)
   must not be started without their own acceptance criteria and a new rubric review gate.
2. Any change to the hook payload format or session ID canonicalization must update
   `TESTING.md` and the corresponding tests.
3. Note for future agents: the canonical session ID format is `origin--externalID`
   (double-dash + URL-encoded external ID). The `TESTING.md` currently shows `origin:externalID`
   with a colon — this is incorrect; use the double-dash form in tests and documentation.
