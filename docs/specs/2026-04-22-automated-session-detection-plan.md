# Automated Session Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend agentsview's automated-session classifier to recognize Claude
Code internal sessions (title generator, warmup) and a new roborev
review-combiner pattern; filter automated sessions out of insights generation;
ensure both SQLite and PostgreSQL stores converge on the new classifications.

**Architecture:** Add three new patterns to `IsAutomatedSession`, hoist the
SQLite backfill marker to an exported constant, bump both SQLite and PostgreSQL
backfill markers to `_v3` so existing databases re-classify on next open, dirty
`local_modified_at` during SQLite backfill so incremental `pg push` re-emits the
changed rows, set `ExcludeAutomated: true` in `insight.BuildPrompt`, relabel the
sidebar toggle.

**Tech Stack:** Go 1.x, SQLite (CGO + fts5 build tag), PostgreSQL, Svelte 5.

**Spec:** `docs/specs/2026-04-22-automated-session-detection-design.md`.

**Branch:** `feat/detect-claude-internals-automation` (already created; spec
already committed).

______________________________________________________________________

## File Map

| File                                                     | Change                                                                                                                                   |
| -------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/db/automated.go`                               | Add exported `IsAutomatedBackfillMarker` constant; add 3 patterns; add exact-match arm.                                                  |
| `internal/db/automated_test.go`                          | Add positive + negative test cases for the 3 new patterns.                                                                               |
| `internal/db/db.go`                                      | Replace inline `marker` literal with the new constant; bump `_v2` → `_v3` value; add `local_modified_at` bump in `batchUpdateAutomated`. |
| `internal/db/automated_backfill_test.go`                 | Replace `_v2` string literals with the constant; add `local_modified_at`-bump assertion.                                                 |
| `internal/postgres/schema.go`                            | Bump `isAutomatedBackfillMetadataKey` `_v2` → `_v3`.                                                                                     |
| `internal/insight/prompt.go`                             | Set `ExcludeAutomated: true` on `db.SessionFilter`.                                                                                      |
| `internal/insight/prompt_test.go`                        | Add a case asserting an automated session is excluded from the prompt.                                                                   |
| `frontend/src/lib/components/sidebar/SessionList.svelte` | Rename toggle label `Include automated reviews` → `Include automated sessions`.                                                          |

No new files are created. No schema migrations are required (`is_automated`
column already exists on both SQLite and PostgreSQL).

______________________________________________________________________

## Task 1: Hoist SQLite backfill marker to an exported constant

**Files:**

- Modify: `internal/db/automated.go`
- Modify: `internal/db/db.go:575`
- Modify: `internal/db/automated_backfill_test.go:40`, `:80`
- Test: `internal/db/automated_backfill_test.go` (existing tests cover this; no
  new test in this task)

This task is a pure refactor: introduce an exported constant so the backfill
marker has a single source of truth, then update every existing reference. Tests
must continue to pass without modification of behavior.

- [ ] **Step 1: Add the constant to `internal/db/automated.go`**

Append to `internal/db/automated.go` (above or below the existing pattern
slices, doesn't matter):

```go
// IsAutomatedBackfillMarker is the stats/sync_metadata key that
// gates the one-time is_automated re-classification. Bump the
// suffix whenever the classifier patterns change so existing
// databases re-run the backfill on next open.
const IsAutomatedBackfillMarker = "is_automated_backfill_v2"
```

- [ ] **Step 2: Replace the inline `marker` literal in `db.go`**

In `internal/db/db.go`, find
`func (db *DB) backfillIsAutomatedLocked(w *sql.DB) error` (around line 574).
Replace:

```go
const marker = "is_automated_backfill_v2"
```

with:

```go
const marker = IsAutomatedBackfillMarker
```

(The `const marker = ...` shadow stays so the rest of the function is
unchanged.)

- [ ] **Step 3: Replace `_v2` literals in the existing backfill test**

In `internal/db/automated_backfill_test.go`, two lines reference the literal
`'is_automated_backfill_v2'` inside `DELETE FROM stats WHERE key = '...'` SQL
strings (lines 40 and 80). Replace each with a parameterized query:

At line 38-42:

```go
// Clear the marker so the backfill will run.
_, err = d.getWriter().Exec(
    "DELETE FROM stats WHERE key = ?",
    IsAutomatedBackfillMarker,
)
requireNoError(t, err, "clear marker")
```

And the same replacement at lines 78-82.

- [ ] **Step 4: Run the full automated test suite to confirm refactor is
  behavior-preserving**

Run: `CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run "Automated" -v`

Expected: all existing tests pass. No test count change.

- [ ] **Step 5: Commit**

```bash
git add internal/db/automated.go internal/db/db.go internal/db/automated_backfill_test.go
git commit -m "refactor(db): hoist is_automated backfill marker to an exported constant"
```

______________________________________________________________________

## Task 2: Add new automation patterns and the exact-match category

**Files:**

- Modify: `internal/db/automated.go`
- Modify: `internal/db/automated_test.go`

TDD: write failing cases for each new pattern, then add patterns and the new
exact-match arm.

- [ ] **Step 1: Add failing positive cases to `automated_test.go`**

In `internal/db/automated_test.go`, append these table entries inside the
existing `tests` slice in `TestIsAutomatedSession`, before the negative cases
section:

```go
// Roborev review combiner
{
    "RoborevCombiner",
    "You are combining multiple code review outputs into a single GitHub PR comment.\nRules:\n- Deduplicate findings reported by multiple agents",
    true,
},

// Claude Code title generator (note leading "-\n" wrapper)
{
    "ClaudeCodeTitleGenerator",
    "-\nYou are a conversation title generator. Given the conversation below, create a short title (3-5 words) that describes the session's main topic.",
    true,
},

// Claude Code warmup (exact match)
{
    "ClaudeCodeWarmup",
    "Warmup",
    true,
},
{
    "ClaudeCodeWarmupTrailingNewline",
    "Warmup\n",
    true,
},
```

And append these negative cases:

```go
// Negative: "Warmup" must not match as substring or prefix
{
    "WarmupAsPrefix",
    "Warmup fans for the show",
    false,
},
// Negative: title-generator phrase appearing in normal user prose
{
    "TitleGeneratorPhraseInProse",
    "I need to generate a conversation about titles for my book.",
    false,
},
```

- [ ] **Step 2: Run tests to verify failures**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestIsAutomatedSession -v`

Expected: the 4 new positive cases (`RoborevCombiner`,
`ClaudeCodeTitleGenerator`, `ClaudeCodeWarmup`,
`ClaudeCodeWarmupTrailingNewline`) FAIL. Negative cases pass (current classifier
doesn't match these strings).

- [ ] **Step 3: Add new patterns and the exact-match arm to `automated.go`**

Edit `internal/db/automated.go`. Append to `automatedPrefixes`:

```go
"You are combining multiple code review outputs into a single GitHub PR comment.",
```

Append to `automatedSubstrings`:

```go
"You are a conversation title generator",
```

Add a new package-level slice:

```go
// automatedExactMatches are first messages that, after trimming
// surrounding whitespace, exactly equal one of these strings.
// Used for prompts too generic for prefix or substring matching
// (e.g., a single-word warmup ping).
var automatedExactMatches = []string{
    "Warmup",
}
```

Add the exact-match arm to `IsAutomatedSession`, after the substring loop and
before `return false`:

```go
trimmed := strings.TrimSpace(firstMessage)
for _, exact := range automatedExactMatches {
    if trimmed == exact {
        return true
    }
}
```

- [ ] **Step 4: Run tests to verify all cases pass**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestIsAutomatedSession -v`

Expected: all cases (existing + new positive + new negative) PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/automated.go internal/db/automated_test.go
git commit -m "feat(db): detect Claude Code title generator + warmup + roborev combiner"
```

______________________________________________________________________

## Task 3: Bump `local_modified_at` during backfill UPDATEs

**Files:**

- Modify: `internal/db/db.go:666` (the `batchUpdateAutomated` function)
- Modify: `internal/db/automated_backfill_test.go` (add new test)

TDD: write a failing test that asserts `local_modified_at` is bumped after
backfill, then update the SQL.

- [ ] **Step 1: Add a failing test asserting `local_modified_at` is bumped**

Append to `internal/db/automated_backfill_test.go`:

```go
func TestBackfillIsAutomatedBumpsLocalModifiedAt(t *testing.T) {
    d := testDB(t)
    ctx := context.Background()

    // Seed a single-turn roborev session that the new classifier
    // will flip to is_automated = 1.
    insertSession(t, d, "to-flip", "proj", func(s *Session) {
        fm := "You are a code reviewer. Review the code."
        s.FirstMessage = &fm
        s.MessageCount = 3
        s.UserMessageCount = 1
    })
    // Force is_automated = 0 so the backfill has work to do.
    _, err := d.getWriter().Exec(
        "UPDATE sessions SET is_automated = 0 WHERE id = 'to-flip'",
    )
    requireNoError(t, err, "force to-flip to 0")

    // Snapshot local_modified_at before the backfill.
    before, err := d.GetSessionFull(ctx, "to-flip")
    requireNoError(t, err, "get to-flip before")
    var beforeLM string
    if before.LocalModifiedAt != nil {
        beforeLM = *before.LocalModifiedAt
    }

    // SQLite's strftime('now') ticks at millisecond precision.
    // Sleep a few ms so a re-set produces a strictly later value.
    // (Mirrors internal/db/signals_test.go:164.)
    time.Sleep(5 * time.Millisecond)

    // Clear the marker so the backfill runs.
    _, err = d.getWriter().Exec(
        "DELETE FROM stats WHERE key = ?",
        IsAutomatedBackfillMarker,
    )
    requireNoError(t, err, "clear marker")

    d.mu.Lock()
    err = d.backfillIsAutomatedLocked(d.getWriter())
    d.mu.Unlock()
    requireNoError(t, err, "backfill run")

    after, err := d.GetSessionFull(ctx, "to-flip")
    requireNoError(t, err, "get to-flip after")
    if !after.IsAutomated {
        t.Fatal("to-flip should be automated after backfill")
    }
    if after.LocalModifiedAt == nil || *after.LocalModifiedAt == "" {
        t.Fatal("local_modified_at not set after backfill")
    }
    if *after.LocalModifiedAt <= beforeLM {
        t.Errorf(
            "local_modified_at not bumped: before=%q after=%q",
            beforeLM, *after.LocalModifiedAt,
        )
    }
}
```

Add the `time` import at the top of the file:

```go
import (
    "context"
    "testing"
    "time"
)
```

- [ ] **Step 2: Run test to verify failure**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestBackfillIsAutomatedBumpsLocalModifiedAt -v`

Expected: FAIL — `local_modified_at` is unchanged because `batchUpdateAutomated`
only sets `is_automated`.

- [ ] **Step 3: Update `batchUpdateAutomated` SQL to bump `local_modified_at`**

In `internal/db/db.go`, find `func batchUpdateAutomated` (around line 652).
Replace the `Exec` call:

```go
_, err := w.Exec(
    "UPDATE sessions SET is_automated = ?"+
        " WHERE id IN ("+
        strings.Join(phs, ",")+
        ")",
    args...,
)
```

with:

```go
_, err := w.Exec(
    "UPDATE sessions"+
        " SET is_automated = ?,"+
        "     local_modified_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')"+
        " WHERE id IN ("+
        strings.Join(phs, ",")+
        ")",
    args...,
)
```

(`%Y-%m-%dT%H:%M:%fZ` matches the convention at
`sessions.go:947, 1489, 1504, 1523`.)

- [ ] **Step 4: Run new test and the full backfill suite to verify pass + no
  regression**

Run: `CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run "Backfill" -v`

Expected: `TestBackfillIsAutomatedBumpsLocalModifiedAt` PASS.
`TestBackfillIsAutomatedBidirectional` and
`TestBackfillIsAutomatedMarkerIdempotent` still PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/db.go internal/db/automated_backfill_test.go
git commit -m "fix(db): bump local_modified_at during is_automated backfill so pg push picks up changes"
```

______________________________________________________________________

## Task 4: Bump SQLite backfill marker to `_v3`

**Files:**

- Modify: `internal/db/automated.go`

This is the trigger: bumping the marker causes the backfill to run on next open
of any existing database. By this task the patterns + the `local_modified_at`
bump are already in place, so the backfill produces correct results.

- [ ] **Step 1: Update the constant value**

In `internal/db/automated.go`, change:

```go
const IsAutomatedBackfillMarker = "is_automated_backfill_v2"
```

to:

```go
const IsAutomatedBackfillMarker = "is_automated_backfill_v3"
```

- [ ] **Step 2: Run the full backfill test suite to confirm marker bump doesn't
  break anything**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run "Backfill|Automated" -v`

Expected: all PASS. (The tests reference the constant, not the literal, so they
automatically use `_v3`.)

- [ ] **Step 3: Commit**

```bash
git add internal/db/automated.go
git commit -m "feat(db): bump is_automated backfill marker to v3 to re-classify with new patterns"
```

______________________________________________________________________

## Task 5: Bump PostgreSQL backfill marker to `_v3`

**Files:**

- Modify: `internal/postgres/schema.go:561`

The PG backfill marker is independent of the SQLite one. Both must be bumped so
`pg serve` deployments converge.

- [ ] **Step 1: Update the constant**

In `internal/postgres/schema.go`, find:

```go
const isAutomatedBackfillMetadataKey = "is_automated_backfill_v2"
```

(around line 561) and change the suffix to `_v3`:

```go
const isAutomatedBackfillMetadataKey = "is_automated_backfill_v3"
```

- [ ] **Step 2: Run the PostgreSQL integration test suite (if available)**

Run: `make test-postgres`

Expected: PASS. (If the PG container is unavailable in the dev environment, skip
this step and rely on CI; flag the skip in the commit message? — no, just rely
on CI.)

If `make test-postgres` is not available in this environment, run the standard
suite to confirm no compile error:

Run: `CGO_ENABLED=1 go build -tags fts5 ./...`

Expected: builds cleanly.

- [ ] **Step 3: Commit**

```bash
git add internal/postgres/schema.go
git commit -m "feat(postgres): bump is_automated backfill marker to v3"
```

______________________________________________________________________

## Task 6: Exclude automated sessions from insights

**Files:**

- Modify: `internal/insight/prompt.go:29`
- Modify: `internal/insight/prompt_test.go`

TDD: write a failing test that asserts an automated session does not appear in
the prompt, then set the filter.

- [ ] **Step 1: Add a failing test case to `TestBuildPrompt`**

In `internal/insight/prompt_test.go`, append a new case to the `tests` slice in
`TestBuildPrompt`:

```go
{
    name: "excludes automated sessions",
    req: GenerateRequest{
        Type:     "daily_activity",
        DateFrom: "2025-01-15",
        DateTo:   "2025-01-15",
    },
    seed: func(t *testing.T, d *db.DB) {
        // A normal user session.
        dbtest.SeedSession(t, d, "user-session", "my-app", func(s *db.Session) {
            s.MessageCount = 5
            s.UserMessageCount = 2
            s.StartedAt = dbtest.Ptr("2025-01-15T10:00:00Z")
            s.EndedAt = dbtest.Ptr("2025-01-15T11:00:00Z")
            s.FirstMessage = dbtest.Ptr("Fix the login bug")
        })
        // An automated session: roborev review, single-turn,
        // is_automated must be true.
        dbtest.SeedSession(t, d, "auto-session", "my-app", func(s *db.Session) {
            s.MessageCount = 2
            s.UserMessageCount = 1
            s.StartedAt = dbtest.Ptr("2025-01-15T12:00:00Z")
            s.EndedAt = dbtest.Ptr("2025-01-15T12:05:00Z")
            s.FirstMessage = dbtest.Ptr(
                "You are a code reviewer. Review the diff.",
            )
            s.IsAutomated = true
        })
    },
    wantContains: []string{"user-session", "Fix the login bug"},
    wantNot:      []string{"auto-session", "code reviewer"},
},
```

`dbtest.SeedSession` calls `db.UpsertSession`, which persists the `IsAutomated`
field (see `internal/db/sessions.go:695, 717`). Setting `s.IsAutomated = true`
together with `s.UserMessageCount = 1` is sufficient.

- [ ] **Step 2: Run the test to verify failure**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./internal/insight/ -run TestBuildPrompt/excludes_automated_sessions -v`

Expected: FAIL — `wantNot` substrings appear in the prompt because `BuildPrompt`
does not filter automated sessions.

- [ ] **Step 3: Set `ExcludeAutomated: true` in `BuildPrompt`**

In `internal/insight/prompt.go`, at the filter construction (around line 29):

```go
filter := db.SessionFilter{
    DateFrom: req.DateFrom,
    DateTo:   req.DateTo,
    Limit:    maxSessions + 1,
}
```

Add the field:

```go
filter := db.SessionFilter{
    DateFrom:         req.DateFrom,
    DateTo:           req.DateTo,
    Limit:            maxSessions + 1,
    ExcludeAutomated: true,
}
```

- [ ] **Step 4: Run the test to verify pass + no regression in other cases**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./internal/insight/ -run TestBuildPrompt -v`

Expected: all cases including the new one PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/insight/prompt.go internal/insight/prompt_test.go
git commit -m "feat(insight): exclude automated sessions from generated prompts"
```

______________________________________________________________________

## Task 7: Rename sidebar toggle label

**Files:**

- Modify: `frontend/src/lib/components/sidebar/SessionList.svelte:485`

The only user-facing copy of the string is at line 485. URL parameter, store
names, API field, and DB columns are unchanged.

- [ ] **Step 1: Update the label text**

In `frontend/src/lib/components/sidebar/SessionList.svelte`, change line 485
from:

```svelte
            Include automated reviews
```

to:

```svelte
            Include automated sessions
```

- [ ] **Step 2: Confirm no other frontend file or test references the old
  label**

Run: `rg "Include automated reviews" frontend/`

Expected: no matches.

- [ ] **Step 3: Run the frontend test suite to confirm no test was asserting on
  the old label**

Run: `cd frontend && npm test -- --run`

Expected: PASS (no test count change).

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/components/sidebar/SessionList.svelte
git commit -m "feat(frontend): rename sidebar toggle to 'Include automated sessions'"
```

______________________________________________________________________

## Task 8: Final verification across the repo

**Files:** none modified.

Cross-cutting verification before opening the PR. Run all standard checks the
project requires.

- [ ] **Step 1: `go fmt` and `go vet`**

Run: `go fmt ./... && go vet ./...`

Expected: no output (all formatted, no warnings).

- [ ] **Step 2: Full Go test suite**

Run: `make test`

Expected: all PASS.

- [ ] **Step 3: Lint**

Run: `make lint`

Expected: no findings.

- [ ] **Step 4: Frontend tests**

Run: `cd frontend && npm test -- --run`

Expected: PASS.

- [ ] **Step 5: Build the binary to confirm the embedded frontend still
  compiles**

Run: `make build`

Expected: builds cleanly; `agentsview` binary present.

- [ ] **Step 6: Manual smoke check (optional but recommended)**

Run: `make dev` in one terminal, `make frontend-dev` in another. Open the UI,
confirm the sidebar toggle reads "Include automated sessions" and that toggling
it changes which sessions appear.

If any title-generator or warmup sessions exist in the local data dir, they
should be hidden by default and visible when the toggle is enabled.

- [ ] **Step 7: Push branch and open PR**

Push the branch (only if the user has explicitly said to push):

```bash
git push -u origin feat/detect-claude-internals-automation
```

Open PR via:

```bash
gh pr create --title "feat: detect Claude Code internal automation + roborev combiner" --body "$(cat <<'EOF'
## Summary

Extends the automated-session classifier to recognize Claude Code's internal title generator and warmup pings, plus a missing roborev review-combiner pattern. Filters automated sessions out of insights generation. Bumps both SQLite and PostgreSQL backfill markers to v3 so existing databases re-classify on next open. Dirties `local_modified_at` during SQLite backfill so incremental `pg push` picks up the re-classified rows. Renames the sidebar toggle to "Include automated sessions" to reflect the broader category.

Spec: `docs/specs/2026-04-22-automated-session-detection-design.md`.

EOF
)"
```

Do not push or open the PR without explicit user approval.
