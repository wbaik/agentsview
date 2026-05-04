# Issue 364 Watch Degradation Design

## Context

Issue 364 reports `agentsview serve` failing after initial sync with:

```text
fatal: server failed to start: listen tcp 127.0.0.1:8080: socket: too many open files
```

The current startup sequence runs initial sync, recursively registers file
watchers for supported agent roots, and only then starts the HTTP listener.
Large session archives can consume enough process file descriptors during
fsnotify setup that the later `net.Listen` call fails.

Issue 302 addressed the same pattern for OpenHands by adding shallow watch mode,
but other recursive agent roots can still create thousands of watches.

## Goals

- `agentsview serve` must not fail because file watching consumes too many file
  descriptors.
- Small and medium installations should keep near-real-time updates where
  practical.
- Large directory trees should degrade to periodic polling with visible startup
  messaging.
- The fix should protect future agent integrations without requiring every agent
  definition to choose a perfect watch mode up front.

## Non-Goals

- Remove fsnotify entirely.
- Change sync parser behavior or database schema.
- Add user-facing configuration before the internal policy has proven useful.

## Recommended Approach

Start the HTTP server before expensive watcher registration, then make recursive
watch setup best-effort with a process-wide internal watch budget.

The sequence becomes:

1. Load config, open the database, and run initial sync as today.
1. Prepare and start the HTTP server.
1. After the server is listening, start file watching.
1. Register recursive watches only while under an unexported process-wide
   budget, initially `8192` watched directories.
1. When the budget is exhausted, mark the current root and later recursive roots
   as unwatched.
1. When watch registration hits resource exhaustion, stop walking that root and
   mark it as unwatched.
1. Periodically poll unwatched roots using the existing `startUnwatchedPoll`
   path.

This preserves live updates where they are cheap and prevents watcher setup from
blocking the web UI from starting.

## Existing Behavior

The current code already has partial degradation, but it is too coarse for this
failure mode:

- `internal/sync/watcher.go` `WatchRecursive` returns
  `(watched, unwatched, err)`. It increments `unwatched` for every failed
  `fsnotify.Add` call and keeps walking.
- `cmd/agentsview/main.go` `startFileWatcher` currently calls
  `watched, uw, _ := watcher.WatchRecursive(r.root)`, discarding the returned
  error and losing the distinction between skipped inaccessible paths, resource
  exhaustion, and an intentional early stop.
- `startFileWatcher` logs per-root unwatched counts and already adds those roots
  to the polling set, but it has no summary that tells the user how many roots
  will be polled.

The change should extend this existing path rather than replacing it.

## Components

- `cmd/agentsview/main.go`

  - Reorder `runServe` so server startup happens before `startFileWatcher`.
  - Keep watcher cleanup tied to serve runtime shutdown.
  - Inspect the full recursive watch result instead of discarding the error.
  - Keep the existing per-root log warning, and add one stdout summary line
    after the watch loop when any roots will be polled.

- `internal/sync/watcher.go`

  - Add a budget-aware recursive watch method or extend `WatchRecursive` with an
    option/result that reports the watched count, unwatched count, and whether
    the walk stopped early because of budget exhaustion or resource exhaustion.
  - Use an unexported process-wide budget constant at the call site or watcher
    package boundary. The initial value should be `8192`; keep it private so it
    can be tuned without API churn.
  - Stop the directory walk on the first resource-exhaustion errno. Treat
    `syscall.EMFILE` as the macOS/kqueue and process descriptor case, and
    `syscall.ENOSPC` as the Linux inotify exhaustion case.

- Tests

  - Cover budget exhaustion without requiring real OS file descriptor
    exhaustion.
  - Cover watcher result accounting for watched and unwatched directories.
  - Cover the startup ordering regression by extracting the watcher-start step
    enough for a unit test to pass a blocking stub. The test should verify that
    a local dial to the HTTP listener succeeds before the watcher stub returns.

## Data Flow

`startFileWatcher` resolves each file-backed agent root. For each root, it asks
the watcher to register either a shallow watch or a budgeted recursive watch.
Successful watched directories stay on the fsnotify path and trigger debounced
`engine.SyncPaths(paths)`. Roots that cannot be fully watched are added to the
unwatched set. If the set is non-empty, the existing `startUnwatchedPoll`
periodically calls `engine.SyncAll`.

## Error Handling

Watcher creation failure remains non-fatal and falls back to polling all roots.
Recursive watch failures are root-scoped: a large or failing root is degraded to
polling while other roots can still use fsnotify until the shared budget is
exhausted. Once the process-wide budget is exhausted, remaining recursive roots
are marked for polling without walking them.

On `EMFILE` or `ENOSPC`, the watcher should stop walking the current root
immediately. Continuing would only produce repeated failed `Add` syscalls and
would not restore live watching. The server listener is already active before
this work starts, so watcher resource pressure cannot prevent the UI from
becoming available.

## Testing

Unit tests should pass a deliberately small synthesized watch budget through the
new budget-aware path. This exercises the new behavior through a real
`fsnotify.Watcher` without introducing a watcher interface only for tests.

Startup-ordering coverage should avoid platform file descriptor limits. Extract
the watcher-start step behind a small internal function/hook so a unit test can
block watcher setup, then verify the HTTP listener is already bound before that
stub returns.
