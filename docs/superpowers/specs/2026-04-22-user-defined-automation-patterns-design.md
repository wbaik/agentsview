# User-Defined Automation Patterns — Design

**Status:** Approved **Date:** 2026-04-22 **Tracking issue:** #370

## Goal

Let users append their own prefix patterns to the automated-session classifier
via `~/.agentsview/config.toml`, so personal tooling that issues recognizable
single-turn prompts is treated as automated without forking the binary.

## Background

The classifier in `internal/db/automated.go` currently ships with three
hardcoded slices: `automatedPrefixes`, `automatedSubstrings`, and
`automatedExactMatches`. `IsAutomatedSession(firstMessage)` returns true on any
prefix match, substring match, or exact match (after trim). The `is_automated`
flag is gated on `user_message_count <= 1` at write time and recomputed during a
one-shot backfill on `db.Open`, controlled by a manually-bumped marker
(currently `is_automated_backfill_v3`).

User patterns to support (motivating examples):

- `"You are analyzing an essay"`
- `"You are grading quotes"`
- `"You are analyzing a blog post"`
- `"Grade these Benn Stancil quotes"`

All four match as prefixes against the `first_message` column.

## Out of scope

- Substring and exact-match user patterns (YAGNI; revisit if a real use case
  appears).
- Per-project pattern overrides (samples are personal-tooling patterns that fire
  across repos).
- Hot-reload on config changes (restart is acceptable).
- Regex patterns (literal strings only).
- Removal or override of built-in patterns (additive only).

## Architecture

Seven units, each with a clear single responsibility:

1. **Config schema** — TOML parsing, normalization, validation.
1. **Classifier singleton** — package-level state in `internal/db` that merges
   built-ins with the configured user prefixes.
1. **Classifier hash** — stable hash over (algorithm version + all built-in
   slices + user prefixes) used as the backfill trigger.
1. **Backfill driver (SQLite)** — replaces the version-keyed marker with a hash
   check against `stats`.
1. **Backfill driver (PostgreSQL)** — same hash mechanism against the PG
   `sync_metadata` table; `pushSession` switches to using `sess.IsAutomated`
   instead of recomputing.
1. **Wiring** — central `applyClassifierConfig` helper installed in every
   command that opens a store, plus a static guardrail test that prevents future
   commands from regressing.
1. **`agentsview classifier rebuild`** — CLI command that clears the stored hash
   to force a backfill on next open. Documented recovery path for
   downgrade-then-upgrade and live-config debugging.

## Component details

### 1. Config schema

In `internal/config/config.go`:

```go
type AutomatedConfig struct {
    Prefixes []string `toml:"prefixes" json:"prefixes,omitempty"`
}

// On Config:
Automated AutomatedConfig `toml:"automated" json:"automated,omitempty"`
```

TOML usage:

```toml
[automated]
prefixes = [
  "You are analyzing an essay",
  "You are grading quotes",
]
```

Normalization at load (in `config.Load` after TOML unmarshalling):

- `strings.TrimSpace` each entry.
- Drop entries that become empty after trimming.
- Drop pattern entries longer than 1024 characters; log at warning level.
- Drop within-list duplicates (preserving first occurrence).
- Drop entries that exactly equal a built-in prefix; log at info level. This
  keeps the merged set tight and signals to the user that the pattern is already
  covered.

If `[automated]` is absent or has no entries, `Automated.Prefixes` is nil and
the classifier is unchanged from current behavior.

### 2. Classifier singleton

In `internal/db/automated.go`:

```go
var (
    userPrefixesMu sync.RWMutex
    userPrefixes   []string
)

// SetUserAutomationPrefixes replaces the user-pattern slice.
// The caller may pass nil to clear. Each entry is assumed to be
// pre-normalized by the caller (config layer enforces this).
func SetUserAutomationPrefixes(prefixes []string) {
    userPrefixesMu.Lock()
    defer userPrefixesMu.Unlock()
    userPrefixes = append([]string(nil), prefixes...)
}

// UserAutomationPrefixes returns a copy of the current slice.
// Used by ClassifierHash and tests.
func UserAutomationPrefixes() []string {
    userPrefixesMu.RLock()
    defer userPrefixesMu.RUnlock()
    return append([]string(nil), userPrefixes...)
}
```

`IsAutomatedSession` gains a third loop after the built-in prefix loop:

```go
func IsAutomatedSession(firstMessage string) bool {
    for _, prefix := range automatedPrefixes {
        if strings.HasPrefix(firstMessage, prefix) {
            return true
        }
    }
    userPrefixesMu.RLock()
    for _, prefix := range userPrefixes {
        if strings.HasPrefix(firstMessage, prefix) {
            userPrefixesMu.RUnlock()
            return true
        }
    }
    userPrefixesMu.RUnlock()
    // Existing substring + exact-match arms unchanged.
}
```

The `RWMutex` keeps the read path lock-free under contention; writes only happen
once at process start. Defensive copies on Set and Get prevent the caller from
mutating the singleton's backing array.

### 3. Classifier hash

New file `internal/db/classifier_hash.go`:

```go
const classifierAlgorithmVersion = 1

// ClassifierHash returns a stable hex-encoded SHA-256 over the
// algorithm version, all built-in pattern slices, and the
// currently configured user prefixes. Inputs are sorted before
// hashing so config order doesn't affect the result.
func ClassifierHash() string {
    h := sha256.New()
    fmt.Fprintf(h, "v%d\n", classifierAlgorithmVersion)
    writeSorted(h, "P", automatedPrefixes)
    writeSorted(h, "S", automatedSubstrings)
    writeSorted(h, "E", automatedExactMatches)
    writeSorted(h, "U", UserAutomationPrefixes())
    return hex.EncodeToString(h.Sum(nil))
}

func writeSorted(h hash.Hash, tag string, items []string) {
    sorted := append([]string(nil), items...)
    sort.Strings(sorted)
    for _, s := range sorted {
        fmt.Fprintf(h, "%s\t%d\t%s\n", tag, len(s), s)
    }
}
```

The tag prefix (`P`/`S`/`E`/`U`) and length-prefixed encoding prevent two
different inputs from producing the same hash by splicing across slice
boundaries.

`classifierAlgorithmVersion` is bumped manually when the matching *logic*
changes (e.g. a future case-insensitivity flag). It is the only remaining
manual-bump residue and lives next to the function that consumes it.

### 4. Backfill driver (SQLite)

`internal/db/db.go` changes:

- Remove the exported `IsAutomatedBackfillMarker` constant (was
  `"is_automated_backfill_v3"`). Internal-only; no external consumers.
- Replace `backfillIsAutomatedLocked` marker check with hash check:

```go
const classifierHashStatsKey = "is_automated_classifier_hash"

func (db *DB) backfillIsAutomatedLocked(w *sql.DB) error {
    current := ClassifierHash()
    var stored string
    if err := w.QueryRow(
        "SELECT value FROM stats WHERE key = ?",
        classifierHashStatsKey,
    ).Scan(&stored); err != nil && err != sql.ErrNoRows {
        return fmt.Errorf("probing classifier hash: %w", err)
    }
    if stored == current {
        return nil
    }
    // The existing set/clear loop in this function stays:
    // SELECT id, first_message, user_message_count, is_automated
    // → compute want = umc <= 1 && IsAutomatedSession(fm)
    // → batchUpdateAutomated for additions and clears
    // (already bumps local_modified_at for pg push pickup).
    // After that loop returns, write the hash:
    _, err := w.Exec(
        `INSERT INTO stats (key, value) VALUES (?, ?)
         ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
        classifierHashStatsKey, current,
    )
    return err
}
```

The legacy `is_automated_backfill_v2` and `_v3` keys are left in place as dead
data so a `DELETE FROM stats WHERE key LIKE 'is_automated_backfill_%'` migration
isn't required. New code never reads them. Old code (downgrade) still finds
`_v3=1` and skips its own backfill, but that does not protect derived flags from
old code's write-path overwrites — see the Backward/forward compatibility
section.

(Wiring is described in its own section below, since user prefixes must reach
the singleton in every process that opens SQLite or PostgreSQL — not just
`serve`.)

### 5. Backfill driver (PostgreSQL)

`internal/postgres/schema.go` changes:

- Replace `isAutomatedBackfillMetadataKey = "is_automated_backfill_v3"` with
  `classifierHashMetadataKey = "is_automated_classifier_hash"`.
- `backfillIsAutomatedPG` follows the same hash-compare pattern, reading and
  writing against PG's `sync_metadata` table instead of SQLite's `stats` table.
- Hash input is the same `db.ClassifierHash()` — both stores see the same
  in-process classifier state because both run inside the same agentsview
  process.

PG-side write path change: today `pushSession` in `internal/postgres/push.go`
(line 717) recomputes `is_automated` locally via
`db.IsAutomatedSession(*sess.FirstMessage)` rather than copying
`sess.IsAutomated`. This design changes that to use `sess.IsAutomated` as the
single source of truth — the SQLite row already carries the correct value
(written by `UpsertSession` and `UpdateSessionIncremental` under the same
singleton), so PG push should trust it. This eliminates a hidden classifier
coupling on the PG side and removes one place where missing wiring would
silently produce wrong values.

PG's own backfill (`backfillIsAutomatedPG`) still runs when the PG hash key
differs from the current process's hash, so a DB rehosted from a machine with a
different classifier set still gets reclassified.

## Wiring

User prefixes must reach the singleton in every process that opens SQLite or
PostgreSQL, because all classifier consumers (`UpsertSession`,
`UpdateSessionIncremental`, `backfillIsAutomatedLocked`,
`backfillIsAutomatedPG`) read it. A command that loads config but forgets to
call the setter would silently classify with built-ins only.

### Central helper

Add `cmd/agentsview/classifier_wiring.go`:

```go
// applyClassifierConfig installs user-defined classifier
// prefixes into the db package singleton. Every command that
// loads config and may open SQLite or PostgreSQL must call
// this before db.Open / postgres.Open.
func applyClassifierConfig(cfg config.Config) {
    db.SetUserAutomationPrefixes(cfg.Automated.Prefixes)
}
```

The helper is intentionally trivial today; making it a named function ensures
one place updates if the wiring grows (e.g. future substring/exact-match user
lists, or per-store filtering).

### Entry points (must call helper before opening a store)

| File / command                                         | Open path            |
| ------------------------------------------------------ | -------------------- |
| `cmd/agentsview/main.go` — root `serve`                | SQLite via transport |
| `cmd/agentsview/transport.go` — direct-mode services   | SQLite               |
| `cmd/agentsview/sync.go` — `agentsview sync`           | SQLite               |
| `cmd/agentsview/import.go` — `agentsview import`       | SQLite               |
| `cmd/agentsview/health.go` — `agentsview health`       | SQLite               |
| `cmd/agentsview/prune.go` — `agentsview prune`         | SQLite               |
| `cmd/agentsview/stats.go` — `agentsview stats`         | SQLite               |
| `cmd/agentsview/usage.go` — `agentsview usage`         | SQLite               |
| `cmd/agentsview/token_use.go` — `agentsview token-use` | SQLite               |
| `cmd/agentsview/session*.go` — session subcommands     | SQLite               |
| `cmd/agentsview/pg.go` — `pg push`, `pg serve`         | SQLite + PostgreSQL  |
| `cmd/agentsview/classifier.go` (new, see below)        | SQLite + PostgreSQL  |

Commands that never open a store (e.g. `--help`, `update`, top-level group help)
need no change.

### Guardrail test

Add `cmd/agentsview/classifier_wiring_test.go` that statically scans the
`cmd/agentsview` package: parse every `.go` file (excluding `_test.go`) with
`go/parser`; for each function that contains a call to `db.Open` (or
`postgres.Open`), require an earlier call to `applyClassifierConfig` in the same
function body. Fail with the offending function name and file:line on miss.

The test is reflection-free static analysis, so it doesn't actually execute any
command. It runs in unit-test time and prevents new commands from regressing
into "loaded config but never wired the singleton."

## Data flow

```
config.Load → AutomatedConfig.Prefixes (normalized slice)
            ↓
applyClassifierConfig(cfg)   [in every command that opens a store]
            ↓
db.SetUserAutomationPrefixes(prefixes)
            ↓
db.Open
  └─ backfillIsAutomatedLocked
       ├─ ClassifierHash()  ← reads built-ins + user singleton
       ├─ compare to stats['is_automated_classifier_hash']
       └─ if differs: scan sessions, recompute is_automated, save hash

UpsertSession (per parsed session)
  └─ IsAutomatedSession(first_message) ← checks built-ins + user singleton

UpdateSessionIncremental (per file growth)
  └─ IsAutomatedSession(first_message) ← already added in PR #369

postgres.pushSession (per pushed row)
  └─ uses sess.IsAutomated directly (no recompute) ← changed by this design
```

## Backward and forward compatibility

**Upgrade (existing DB → new code).** Stored: legacy `_v3` marker present, no
`is_automated_classifier_hash`. New code computes the current hash, sees no
stored value, runs backfill, stores hash. One extra backfill pass on first open
after upgrade — same cost as a manual `_vN` bump would have been, no user action
needed.

**Downgrade (new code → old code → new code).** This is *not* a supported
workflow, but the actual behavior is documented here so users hitting it can
recover:

1. New code wrote `is_automated=1` for user-pattern matches and stored the
   classifier hash.
1. User downgrades to old code. Old code's `UpsertSession` and
   `UpdateSessionIncremental` actively *recompute* `is_automated` on every parse
   using the old classifier (which has no user prefixes). Rows that matched only
   user patterns get `is_automated=0` written back to SQLite by the very next
   file growth or re-parse.
1. User upgrades again. The stored hash matches the current hash → backfill
   skipped → those rows stay `is_automated=0` despite matching the current
   classifier.

The hash detects "classifier set changed since the last full pass," which is not
the same as "stored derived flags reflect the current classifier." Old code's
writes can drift the flags without changing the hash.

**Recovery:** run `agentsview classifier rebuild` (defined below) to clear the
hash and force a backfill on the next open. This is the documented recovery path
for any "stored flags drifted from current classifier" situation, including
downgrade.

**Built-in pattern changes going forward.** No more manual marker bumps. Any
change to `automatedPrefixes`/`automatedSubstrings`/`automatedExactMatches`
changes the hash, which triggers backfill on next open. Removes the "did I
remember to bump the marker?" footgun from PR #369.

**Logic changes going forward.** Any change to `IsAutomatedSession` matching
semantics requires bumping `classifierAlgorithmVersion` so the hash changes. The
constant lives in the same file as `ClassifierHash` so the bump is visible at
code-review time.

## CLI: `agentsview classifier rebuild`

A new subcommand that forces the next backfill on open by deleting the stored
classifier hash from `stats` (and, if PG is configured, from PG's
`sync_metadata`). Used after downgrade-then-upgrade, or as a debugging tool when
iterating on `[automated]` config locally.

### Behavior

1. Loads config (so `Automated.Prefixes` is parsed and normalized; surfaces
   config errors before touching the DB).
1. Refuses to run if a daemon is already serving from the same DB. Detection
   reuses `detectTransport(cfg.DataDir, 0)` from `cmd/agentsview/transport.go`.
   If `tr.Mode == transportHTTP`, print an error directing the user to stop the
   daemon and exit non-zero.
1. Opens the SQLite DB directly (writable mode), deletes the
   `is_automated_classifier_hash` row from `stats`, closes.
1. If `[pg]` is configured, attempts to delete the equivalent row from PG's
   `sync_metadata`. PG step is best-effort: log a warning and continue if PG is
   unreachable (the next `pg push` from a running daemon will trigger PG's own
   hash check anyway).
1. The next `db.Open` (e.g. when the user starts the daemon again) sees the
   missing hash, runs the full backfill, and stores the new hash.

### Hot-reload boundary

Hot reload of running processes is out of scope. After editing `config.toml` and
running `classifier rebuild`, the user must restart any running
`agentsview serve` for new writes (`UpsertSession`, `UpdateSessionIncremental`)
to use the updated prefixes. The daemon-running guard above enforces this:
rebuilding while the daemon is running would clear the hash but leave the
running process's singleton stale, producing the confusing state where future
writes still classify with the old prefixes until restart. Forcing a stop
sidesteps that confusion entirely. The CLI prints a one-line reminder on
success:
`restart any running 'agentsview serve' so write paths use the updated prefixes`.

## Validation behavior

| Input                         | Behavior                                  |
| ----------------------------- | ----------------------------------------- |
| Missing `[automated]` section | Empty user prefix list (current behavior) |
| Empty `prefixes = []`         | Empty user prefix list                    |
| Whitespace-only entry         | Trimmed; if empty, dropped silently       |
| Duplicate within user list    | First occurrence kept, rest dropped       |
| Exact duplicate of built-in   | Dropped; logged at info level             |
| Pattern length > 1024 chars   | Dropped; logged at warning level          |
| Non-string entry (TOML error) | TOML decoder reports parse error          |

## Testing

| File                                               | Coverage                                                                                                                                                       |
| -------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/config/config_test.go`                   | TOML round-trip; normalization (trim/dedupe/empty/length-cap/built-in-overlap)                                                                                 |
| `internal/db/automated_test.go`                    | New table-driven cases that set user prefixes, classify, then reset                                                                                            |
| `internal/db/classifier_hash_test.go` (new)        | Hash stable across runs; differs when user list changes; differs across algo bumps                                                                             |
| `internal/db/automated_backfill_test.go`           | Backfill no-ops when hash matches; runs and updates hash when it differs                                                                                       |
| `internal/postgres/automated_pgtest_test.go` (new) | PG backfill parity + `pushSession` honors `sess.IsAutomated` (no recompute), under `pgtest` build tag                                                          |
| `cmd/agentsview/classifier_wiring_test.go` (new)   | Static guardrail: every function in `cmd/agentsview` that calls `db.Open` (or `postgres.Open`) also calls `applyClassifierConfig` earlier in the same function |
| `cmd/agentsview/classifier_test.go` (new)          | `classifier rebuild` clears SQLite hash, refuses when daemon detected, best-effort on PG                                                                       |

All new tests follow the existing table-driven Go convention in this repo.
SQLite tests use `testDB(t)` from `internal/db/db_test.go`.

## Files touched

| File                                                                                                                           | Change type                                                       |
| ------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------- |
| `internal/config/config.go`                                                                                                    | Add struct, parsing, validation                                   |
| `internal/config/config_test.go`                                                                                               | Tests                                                             |
| `internal/db/automated.go`                                                                                                     | Singleton + IsAutomatedSession update                             |
| `internal/db/automated_test.go`                                                                                                | Tests                                                             |
| `internal/db/classifier_hash.go` (new)                                                                                         | Hash function                                                     |
| `internal/db/classifier_hash_test.go` (new)                                                                                    | Tests                                                             |
| `internal/db/db.go`                                                                                                            | Backfill marker → hash                                            |
| `internal/db/automated_backfill_test.go`                                                                                       | Tests                                                             |
| `internal/postgres/schema.go`                                                                                                  | PG marker → hash                                                  |
| `internal/postgres/push.go`                                                                                                    | `pushSession` uses `sess.IsAutomated` instead of recomputing      |
| `internal/postgres/automated_pgtest_test.go` (new)                                                                             | Tests                                                             |
| `cmd/agentsview/classifier_wiring.go` (new)                                                                                    | `applyClassifierConfig` central helper                            |
| `cmd/agentsview/classifier_wiring_test.go` (new)                                                                               | Static guardrail test                                             |
| `cmd/agentsview/classifier.go` (new)                                                                                           | `classifier rebuild` cobra command and group root                 |
| `cmd/agentsview/classifier_test.go` (new)                                                                                      | Rebuild command tests                                             |
| `cmd/agentsview/main.go`                                                                                                       | Register `classifier` group; ensure `serve` uses helper           |
| `cmd/agentsview/transport.go`                                                                                                  | Helper invocation in `direct(...)` path                           |
| `cmd/agentsview/sync.go`, `import.go`, `health.go`, `prune.go`, `stats.go`, `usage.go`, `token_use.go`, `pg.go`, `session*.go` | Add helper call between `config.Load*` and `db.Open` (or PG open) |

## Risks and mitigations

| Risk                                                                                       | Mitigation                                                                                                                                                                    |
| ------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Singleton state leaks across `go test` runs                                                | Reset helper in test; tests using user prefixes use it                                                                                                                        |
| User accidentally adds a pattern that matches a common user prompt and clobbers their feed | Built-in patterns aren't override-able; user prefixes are additive only. Worst case: user-defined false positive, fixable by editing config + `classifier rebuild` + restart. |
| Hash collision (two different pattern sets → same hash)                                    | SHA-256 + length-prefixed encoding makes this cryptographically negligible                                                                                                    |
| New command added that opens a store without calling the helper                            | Static guardrail test in `cmd/agentsview/classifier_wiring_test.go` fails the build                                                                                           |
| Downgrade-then-upgrade leaves stale `is_automated=0` for user-pattern matches              | Documented; recovery via `agentsview classifier rebuild`                                                                                                                      |
| User runs `classifier rebuild` while daemon is serving                                     | Rebuild refuses with a clear error directing the user to stop the daemon first                                                                                                |
| Forgetting to bump `classifierAlgorithmVersion` on a logic change                          | Constant lives next to `ClassifierHash`; reviewers see both together                                                                                                          |
