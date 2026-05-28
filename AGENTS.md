# AGENTS.md

Instructions for autonomous coding agents working in this repository.

## Scope

- Applies to all agent-driven work in this repo.
- If multiple instruction files exist, follow the most specific one for the
  files you are editing.

## Required Git Rules

1. Commit every turn.
1. Do not amend commits.
1. Do not change branches without explicit user permission.

## Commit Expectations

- Keep commits focused and related to the requested task.
- Use clear commit messages.
- Do not push, pull, or rebase unless explicitly requested.

## Validation

- Run relevant tests before committing when practical.
- If tests cannot be run, state that clearly in the handoff.

## Safety

- Do not revert user-authored or unrelated local changes unless explicitly
  requested.
- Avoid destructive git commands unless explicitly requested.

## Data Safety

The SQLite database is a persistent archive. Never delete or recreate it to
handle data version changes. Schema changes use ALTER TABLE; parser changes
trigger a full resync (build fresh DB, sync files, copy orphaned sessions from
old DB, atomic swap). Existing session data must be preserved even when source
files no longer exist on disk.

## Cross-Repo: Transcript-Link Extraction

This repo's `~/.agentsview/sessions.db` is the **source** for a Convex-side
glue table that ties Claude sessions to published research syntheses.

`fds-sync-dbs` (sibling repo) runs an adapter against this database that
regex-scans every message body, thinking text, tool input, tool result, and
tool_result_event row for one of two markers:

```
FDS_TRANSCRIPT_SESSION id=<32-hex>  [workflow=<name>]   ← preferred
fds_transcript_session_id: "<32-hex>"                   ← JSON form
```

Distinct ids become rows in Convex `agentsviewTranscriptLinks`. The Convex
mutation only persists a link when a matching synthesis already exists in
`reasonerSyntheses` or `filerSyntheses`; orphan links are dropped.

Implications for changes inside this repo:

- Schema migrations on `sessions`, `messages`, `tool_calls`, or
  `tool_result_events` columns named in `EXPECTED_AGENTSVIEW_SCHEMA`
  (`fds-sync-dbs/src/adapters/agentsview/adapter.ts`) will fail the
  sync adapter's drift check. If a rename or type change is needed,
  coordinate with the sync adapter first.
- `sessions.deleted_at` is the **tombstone signal**. The adapter
  propagates it into Convex as `deletedAt` on the corresponding
  transcript-link row; do not change the tombstone semantics
  (soft-delete → still queryable, hard-delete → forbidden) without
  updating the adapter.
- The full session corpus is **not** automatically synced to Convex.
  Only the link projection is continuous; the session body and
  messages are pushed manually with `fds-sync-dbs push --source
  agentsview --session-id <id>`. Treat the corpus as local-first.
