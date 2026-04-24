# Stats pipeline: `is_automated` as the authority

**Status:** Draft **Date:** 2026-04-24 **Scope:** agentsview (primary),
tkmx-server (consumer update), tkmx-client (none)

## Problem

Two related issues surface on tkmx user profiles:

1. The **Multi-Agent Portfolio** section on `https://tkmx.odio.dev/user/<user>`
   is dominated by automated sessions. On one representative profile,
   `agent_portfolio.by_sessions` reports
   `{claude: 395, codex: 9094, gemini: 1806, ...}` — ~96% of the sessions
   counted toward the portfolio are automation (totals report
   `sessions_automation: 10,882` vs `sessions_human: 418`). The portfolio has no
   human-vs-automation scope at all; every session feeds the per-agent counters.
1. Elsewhere in the stats pipeline, "human" is derived from
   `userMessageCount >= 2` — a proxy that predates the dedicated `is_automated`
   flag. 0.24.0 broadened `is_automated` classification (PRs #369, #387), but
   those improvements never reach `stats --format json` because
   `internal/db/session_stats.go` doesn't read the flag.

The rest of the agentsview codebase (`analytics.go`, session-list queries in
`sessions.go`, `stats.go`) already respects `is_automated` via
`(user_message_count > 1 OR is_automated = 1)`. Only the stats-JSON pipeline is
out of step.

## Design principle

**`is_automated` is the single source of truth** for whether a session is
automated. The stats pipeline consumes the flag; it does not re-derive
automation status from `userMessageCount` or any other signal. Future
improvements to automation detection live in the flag-setter (`sessions.go:686`
and `IsAutomatedSession`); consumers get those improvements for free.

## Changes — agentsview

Code changes are isolated to `internal/db/session_stats.go`,
`internal/db/session_stats_types.go`, and their stats tests. No other analytics
code changes.

### 1. Project `is_automated` into `sessionStatsRow`

- Add `isAutomated bool` field to the struct (`session_stats.go:344`).
- Add `s.is_automated` to the SELECT in `loadSessionsInWindow`
  (`session_stats.go:434`).
- Scan it in the row loop (`session_stats.go:466`) via a `sql.NullBool`-or-int
  adapter consistent with how `hasTotalTokens` / `hasPeak` are scanned as `int`
  and compared.

### 2. `computeTotalsAndArchetypes` — flag-driven counts and buckets

Current behavior: both the `sessions_human`/`sessions_automation` tallies and
the archetype bucket assignment use `archetypeLabel(userMsgs)` with
`userMsgs <= 1 → "automation"`.

New behavior:

- `Totals.SessionsAutomation` increments when `r.isAutomated`.
- `Totals.SessionsHuman` increments otherwise.
- Archetype bucket assignment:
  - If `r.isAutomated`: `Archetypes.Automation++` (as today, but gated by the
    flag).
  - Otherwise: bucket by `userMessageCount` into quick/standard/deep/marathon.
- `Archetypes.Primary` / `Archetypes.PrimaryHuman` pickers unchanged (they
  consume the bucket counts).

Effect on representative data: 71 short non-automated sessions on one snapshot
shift from `Automation` bucket into `Quick`. `SessionsAutomation` drops by 71;
`SessionsHuman` rises by 71. Numerically small, semantically correct.

Rename `archetypeLabel(userMsgs int)` to `sessionShapeLabel` and make it a
shape-only helper. Callers assign `"automation"` directly when `isAutomated`,
then call `sessionShapeLabel` only for non-automated rows. Because short
non-automated sessions are valid, the renamed helper maps `userMsgs <= 5` to
`"quick"`; the old `userMsgs <= 1 -> "automation"` branch is deleted rather than
left unreachable.

### 3. `computeDistributions` — `scope_human` reads the flag

Replace `human := r.userMessageCount >= 2` (`session_stats.go:674`) with
`human := !r.isAutomated`. Update the doc comment at `session_stats.go:648` to
match.

### 4. `computeAgentPortfolio` — add human-scoped peer fields

Extend `StatsAgentPortfolio` (`session_stats_types.go:114`) with four peer
fields:

```go
type StatsAgentPortfolio struct {
    BySessions      map[string]int   `json:"by_sessions"`
    ByTokens        map[string]int64 `json:"by_tokens"`
    ByMessages      map[string]int   `json:"by_messages"`
    Primary         string           `json:"primary"`

    BySessionsHuman map[string]int   `json:"by_sessions_human"`
    ByTokensHuman   map[string]int64 `json:"by_tokens_human"`
    ByMessagesHuman map[string]int   `json:"by_messages_human"`
    PrimaryHuman    string           `json:"primary_human"`
}
```

`computeAgentPortfolio` populates both map families in a single pass, gating the
`*Human` maps on `!r.isAutomated`. `PrimaryHuman` is
`pickPrimaryAgent(bySessionsHuman)`. Maps stay non-nil (same invariant as
today's maps) so consumers can rely on stable `{}` values on empty windows.

### 5. `schema_version`

Stays at 1.

- New fields (`by_sessions_human` etc.) are additive — unknown-field tolerant
  consumers ignore them.
- Existing fields (`sessions_human`, `scope_human`, `archetypes.automation`)
  keep their shape; only their semantics tighten. The scope of that tightening
  is small (71 sessions on the snapshot above) and moves the pipeline toward the
  flag that's already authoritative elsewhere.
- Bumping would force `SUPPORTED_VERSIONS` updates across consumers for a
  non-breaking change.
- Update the existing schema-contract comments/docs that currently say any field
  semantic change requires a bump (`internal/db/session_stats_types.go` and
  tkmx-server's SessionStats OpenAPI text). The contract for v1 becomes:
  additive fields and bug-fix/tightening semantics are allowed when old
  consumers remain shape-compatible; incompatible shape or bucket-boundary
  changes still require a bump. Feature detection for this rollout uses
  `by_sessions_human` presence plus per-machine `agentsview_version`, not
  `schema_version`.

### 6. Tests

- `session_stats_test.go:563` uses `userMsgs <= 1` as its automation proxy in
  test fixtures. Switch fixtures to set `is_automated` explicitly so the test
  asserts the new contract. The current `sessionFixture.isAutomated` assignment
  is not enough by itself because `UpsertSession` recomputes the flag from
  `FirstMessage`; either set a matching first message for natural fixtures or
  patch `sessions.is_automated` with test-only SQL after `UpsertSession` for
  divergence fixtures.
- Add fixtures covering the divergence between `userMessageCount <= 1` and
  `is_automated`:
  - Short non-automated session (userMsgs=1, is_automated=0) — lands in `Quick`,
    counted in `SessionsHuman`, included in `scope_human` distributions,
    included in `BySessionsHuman`.
  - Multi-turn automated session (userMsgs=5, is_automated=1) — lands in
    `Automation`, counted in `SessionsAutomation`, excluded from `scope_human`,
    excluded from `BySessionsHuman`. (Currently unreachable via the
    flag-setter's `userMessageCount <= 1` guard — the fixture nonetheless
    exercises the consumer code in case the setter is later broadened.)
- Assert `by_sessions_human` and `by_sessions` differ when the fixture contains
  is_automated sessions.

## Changes — tkmx-server

### 1. `server/session_stats_aggregate.js:mergeAgentPortfolio`

Extend to aggregate the new peer maps:

```js
function mergeAgentPortfolio(blobs) {
  const by_sessions = {}, by_tokens = {}, by_messages = {};
  const by_sessions_human = {}, by_tokens_human = {}, by_messages_human = {};
  let portfolio_blob_count = 0;
  let human_reported_blobs = 0;
  for (const b of blobs) {
    const p = b.agent_portfolio || {};
    const has_portfolio = p.by_sessions !== undefined;
    if (has_portfolio) portfolio_blob_count++;
    for (const [k, v] of Object.entries(p.by_sessions || {})) by_sessions[k] = (by_sessions[k] || 0) + v;
    for (const [k, v] of Object.entries(p.by_tokens   || {})) by_tokens[k]   = (by_tokens[k]   || 0) + v;
    for (const [k, v] of Object.entries(p.by_messages || {})) by_messages[k] = (by_messages[k] || 0) + v;
    if (p.by_sessions_human !== undefined) {
      human_reported_blobs++;
      for (const [k, v] of Object.entries(p.by_sessions_human || {})) by_sessions_human[k] = (by_sessions_human[k] || 0) + v;
      for (const [k, v] of Object.entries(p.by_tokens_human   || {})) by_tokens_human[k]   = (by_tokens_human[k]   || 0) + v;
      for (const [k, v] of Object.entries(p.by_messages_human || {})) by_messages_human[k] = (by_messages_human[k] || 0) + v;
    }
  }
  const primary = pickPrimary(by_sessions);
  const any_human_reported = human_reported_blobs > 0;
  const all_human_reported = portfolio_blob_count > 0 &&
    human_reported_blobs === portfolio_blob_count;
  const primary_human = any_human_reported ? pickPrimary(by_sessions_human) : "";
  return {
    by_sessions, by_tokens, by_messages, primary,
    ...(any_human_reported && {
      by_sessions_human, by_tokens_human, by_messages_human,
      primary_human,
      any_human_reported: true,
      all_human_reported,
      human_reported_blobs,
      portfolio_blob_count,
    }),
  };
}
```

`any_human_reported` tells the renderer the difference between "no machines
reported human data" (legacy blobs only) and "human counts are zero" (new data
exists, but zero human sessions). `all_human_reported` / the count fields tell
the renderer whether the human maps are complete for every portfolio-bearing
blob. Minimal synthetic blobs that only carry legacy `outcome_stats` do not
count as portfolio-bearing.

Lift the existing ad-hoc "pick top by sessions" into a `pickPrimary` helper so
both `primary` and `primary_human` use the same logic. Match agentsview's
tie-break: highest session count wins; equal counts choose the lexicographically
smallest agent for deterministic output.

### 2. `server/public/profile.js:renderAgentPortfolio`

Decision rule:

- If `ap.all_human_reported` is true: render the human-scoped portfolio
  (`by_sessions_human`, `primary_human`) as the Multi-Agent Portfolio. Add a
  subtitle clarifying the scope: "Human sessions only — automated runs
  excluded."
- If `ap.any_human_reported` is true but `ap.all_human_reported` is false: keep
  rendering all-sessions (current behavior) and show a mixed-fleet note:
  "Includes automated sessions until all machines update agentsview." Include
  `human_reported_blobs / portfolio_blob_count` if present so the undercount
  risk is visible without switching the primary chart to partial data.
- Otherwise: render all-sessions (current behavior) with a small note: "Includes
  automated sessions. Update agentsview to v<N> to exclude them." Link to the
  agentsview install instructions.

When `all_human_reported` is true but every blob reported zero human sessions
across all agents (empty maps), render a "no human sessions in the window" empty
state rather than a blank chart. In mixed fleets, do not use the empty human map
as an empty state because legacy machines may still contain human sessions.

### 3. Version gate

Bump `MINIMUM_AGENTSVIEW_VERSION` in `server/versions.js` to the new agentsview
release (e.g., `0.25.0`) *after* the agentsview release is cut and tagged. The
existing freeze/nag mechanism then drives mixed fleets to upgrade. This is a
follow-up step, not part of the same PR as the aggregator/renderer update, so
the server still handles mixed fleets gracefully during the rollout window.

### 4. Tests

- `test/session_stats_aggregate.test.js`: extend existing `mergeAgentPortfolio`
  tests with fixtures that include `by_sessions_human`, fixtures that omit it
  (legacy), and mixed-fleet fixtures. Assert `any_human_reported`,
  `all_human_reported`, `human_reported_blobs`, and `portfolio_blob_count` flip
  accordingly.
- Snapshot or string-output test for `renderAgentPortfolio` HTML output under
  four fixture shapes: complete human-capable, legacy-only, mixed, and complete
  human-capable with zero human sessions.

## Changes — tkmx-client

None. The reporter is a passthrough for the agentsview stats JSON
(`reporter/session-stats.js` returns `JSON.parse(raw)` verbatim). New fields
flow through automatically.

## Backwards compatibility

| agentsview | tkmx-server | Result                                                                                     |
| ---------- | ----------- | ------------------------------------------------------------------------------------------ |
| old        | old         | Status quo.                                                                                |
| old        | new         | Renders all-sessions portfolio + update nag.                                               |
| new        | old         | New fields silently ignored by the old merger; no regression. Old portfolio still renders. |
| new        | new         | Human-scoped portfolio rendered once all portfolio blobs include human maps.               |

**Mixed fleets** (one machine old, one new, same user): the aggregator sums
what's present. All-sessions totals stay accurate. `any_human_reported` flips
true on the first new-agentsview blob, but `all_human_reported` stays false
until every portfolio-bearing blob has human maps. During that window, the
renderer keeps the all-sessions portfolio and shows an update note rather than
switching to partial human-scoped data. The existing
`MINIMUM_AGENTSVIEW_VERSION` freeze is the forcing function.

## Non-goals

- Broadening the `is_automated` setter (`sessions.go:686`) to catch multi-turn
  automated reviews. Worth doing separately; the new architecture means
  improvements there flow into stats automatically.
- Redefining archetype *band* boundaries (the userMessageCount thresholds for
  quick/standard/deep/marathon).
- Changing tkmx-client data collection, schema, or reporting cadence.
- Reworking other analytics surfaces (session list filters, non-stats analytics)
  — they already respect `is_automated`.

## Rollout

1. Land agentsview change on `stats/is-automated-authority`; merge and cut
   release.
1. Land tkmx-server aggregator + renderer update (with legacy fallback) on a
   feature branch; merge.
1. After both are deployed, bump `MINIMUM_AGENTSVIEW_VERSION` in tkmx-server to
   the new agentsview release in a follow-up PR.
1. tkmx-client release cycle unchanged; no code change required.

## Open questions

- Naming of the renamed archetype helper (`nonAutomationArchetypeLabel` vs
  `sessionShapeLabel` vs keeping `archetypeLabel` with a doc-comment change).
  Prefer the name that makes the "shape, not provenance" role obvious to the
  next reader.
- Whether the tkmx-server renderer's empty-state copy for "all-sessions only"
  should link directly to the agentsview update command or to the install docs.
  Both exist; the update path is one line but requires the user to already have
  agentsview.
