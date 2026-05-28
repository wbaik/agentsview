# Codex Agent Message Ingestion

AgentsView treats Codex `response_item` rows as the transcript source, with selected `event_msg` rows used as side-channel metadata. Codex `agent_message` events are not tool calls; they describe assistant message metadata that belongs on the normalized `messages` row.

## Wire Contract

Codex assistant prose appears twice in current JSONL:

- `event_msg.payload.type == "agent_message"` carries `message`, `phase`, and optional structured `memory_citation`.
- The following assistant `response_item.payload.type == "message"` carries display text in `content[].text` and also carries `phase`.

When memory citations exist, the response item appends a raw `<oai-mem-citation>...</oai-mem-citation>` suffix. That suffix is duplicate display/protocol text. The structured source of truth is `event_msg.payload.memory_citation`:

```json
{
  "entries": [
    {
      "path": "MEMORY.md",
      "lineStart": 10,
      "lineEnd": 12,
      "note": "used context"
    }
  ],
  "rolloutIds": ["019dd3e2-9e4d-7063-a240-779bc4efa78c"]
}
```

Codex reasoning is emitted as `response_item.payload.type == "reasoning"`.
The full reasoning payload is stored as `encrypted_content`, which AgentsView
cannot decode. Older and some current rows also include plaintext
`summary[].type == "summary_text"` entries; those are the only readable
reasoning text available to normalize.

## Ingestion Rules

- Persist assistant `response_item.payload.phase` into `messages.phase`.
- Capture readable Codex reasoning `summary_text` entries as pending thinking
  text and attach them to the next assistant item. If no assistant item follows,
  emit a thinking-only assistant message.
- Capture `event_msg.agent_message.memory_citation` into pending Codex assistant-message metadata.
- Attach pending memory citation metadata only to the next assistant response whose cleaned content matches the event message text.
- Strip only a trailing `<oai-mem-citation>...</oai-mem-citation>` block from stored content.
- Do not attempt to persist or decode `encrypted_content`; it is opaque
  ciphertext in the local session file.
- Do not route memory citations through `tool_calls` or `tool_result_events`.
- Leave non-Codex parsers unchanged; they write empty phase and citation metadata.

## Storage

The normalized SQLite/PostgreSQL `messages` contract includes:

- `thinking_text TEXT NOT NULL DEFAULT ''`
- `has_thinking INTEGER/BOOLEAN NOT NULL DEFAULT 0`
- `phase TEXT NOT NULL DEFAULT ''`
- `memory_citation_json TEXT NOT NULL DEFAULT ''`

The data version is bumped so existing Codex rows are re-parsed. Metadata fingerprints include phase and memory citation fields so downstream PostgreSQL push does not skip phase-only or citation-only updates. Thinking summaries also change message content length because the parser prepends a `[Thinking]...[/Thinking]` block for UI compatibility.

## Viewer Contract

Viewers should read `phase` and `memory_citation_json` from the message row. They may defensively strip legacy raw citation tags for old databases, but should not treat tag parsing as the canonical source.
