# Codex Agent Message Ingestion Critique

This note critiques `80cfb30 codex: ingest agent message metadata` plus the
follow-up audit fixes on branch `codex-agent-message-ingestion`. It was written
after re-checking the current implementation and the companion viewer behavior.

## Scope Recheck

The core placement is correct: Codex JSONL ingestion belongs in AgentsView, not
in the Next.js viewer. The parser changes are scoped to `internal/parser/codex.go`;
non-Codex parsers only see the new generic `ParsedMessage` fields and continue
to leave them empty. The Claude test explicitly guards that boundary.

The schema additions are intentionally generic because normalized `messages`
rows need stable columns for all readers:

- `phase`
- `memory_citation_json`

That does not mean non-Codex agents are expected to populate them.

## Drift Check

The implementation is newer than the first design discussion in two important
ways:

- `phase` is read directly from assistant `response_item.payload.phase`, with
  `event_msg.agent_message.phase` used as a fallback for the matched pending
  memory citation.
- Memory citation arrays are now normalized to JSON arrays rather than allowing
  Go nil slices to serialize as `null`.

The committed producer doc is still broadly current. It should not imply that a
single-session sync always forces a rewrite on an already-current DB row. In
practice, current rows may be skipped unless their `data_version` is stale or a
full resync is run.

## Audit Findings

The first implementation treated Codex memory citations correctly during a full
parse, but it under-handled incremental parsing. If the sync boundary landed
between `event_msg.agent_message` and the matching assistant `response_item`,
the pending structured `memory_citation` could be lost. If the boundary landed
on the response item, the parser could strip the raw `<oai-mem-citation>` suffix
while still appending a message without `memory_citation_json`. That is the same
class of cross-line state problem already handled by the subagent incremental
fallbacks.

The parser now forces a full parse when incremental Codex input includes either
an `agent_message` with `memory_citation` or a response item carrying an
`<oai-mem-citation>` suffix. Memory citations are rare enough that the full
parse fallback is the safer behavior.

The first implementation also normalized empty citation slices by mutating the
parser-owned citation object during SQLite/importer conversion. That was not a
behavioral failure, but it was unnecessary shared-state drift. The conversion now
marshals a normalized copy through a single parser-owned helper.

The first implementation also left `messageInsertRowsPerStmt` exactly at
SQLite's historic 999-variable ceiling. The batch size is now derived from a
named parameter-count constant and stays strictly below the ceiling.

## Questionable Additions

The PostgreSQL parity work is broader than the immediate local viewer bug, but
it is defensible because AgentsView has a PG mirror path and message metadata
fingerprints. Leaving PG out would create a silent divergence between SQLite and
PG archives.

The importer preservation path is also slightly broader than the narrow Codex
case. It is still reasonable because archive import/export should preserve
message metadata once the normalized schema owns it.

The least satisfying choice is storing citations as JSON in `messages` instead
of a normalized `message_memory_citations` table. JSON kept the change small and
fits the current display-only requirement, but it limits future queryability by
memory path, line range, and rollout id.

## Remaining Risks

- Existing databases already stamped at the new data version can still contain
  stale message rows if they were processed by an earlier local build during
  development. A full resync or targeted stale-version rewrite is needed for
  those rows.
- Incremental Codex parsing around an `agent_message` / assistant response pair
  now falls back to a full parse when memory-citation metadata is involved.
  Other future cross-line Codex metadata would need the same treatment.
- `memory_citation_json` is trusted as parser-produced JSON. There is no
  database-level schema validation.
- Unknown future Codex `phase` strings are preserved as strings. That is safer
  than dropping them, but downstream viewers must not assume only
  `commentary` and `final_answer` forever.

## Follow-Up Bar

Before merging this producer branch, verify with:

```bash
go test ./internal/parser ./internal/sync ./internal/postgres
go test ./internal/db -run 'TestInsertAndGetMessage_CodexPhaseAndMemoryCitation|TestGetMessageByOrdinalTokenUsage|TestMessageContentFingerprint|TestSystemMessageFingerprint'
```

Then run a real sync/resync against a known Codex memory-citation session and
check:

```sql
SELECT phase, COUNT(*) FROM messages WHERE session_id = ? GROUP BY phase;
SELECT ordinal, memory_citation_json FROM messages
WHERE session_id = ? AND memory_citation_json != '';
SELECT COUNT(*) FROM messages
WHERE session_id = ? AND content LIKE '%<oai-mem-citation>%';
```
