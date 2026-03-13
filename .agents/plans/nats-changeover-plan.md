# Nexis Echo Changeover Plan

## Purpose

Build `Nexis Echo` as a clean NATS-native terminal-output notification app.

This is a rewrite, not a transport swap. The previous tmux-based app had valid logic for notification UX, summarization, TTS, and history, but its runtime model was built around panes, shell transitions, local state files, and a FIFO queue. This new app should not inherit those constraints.

## Product Boundary

### What the app is

- A Go-first app that consumes terminal output from NATS
- A summarizer that turns completed terminal output into short spoken notifications
- A TTS worker that speaks notifications locally
- A small HTTP/UI surface for history, live events, health, and settings

### What the app is not

- A tmux watcher
- A pane manager
- A shell hook system
- A clone of Nexis semantics like `role` or `session_id`

## Core Principles

- NATS is the source of truth for incoming output events
- The core domain model is `source` plus output blocks, not panes or windows
- Terminal-output boundaries should be explicit whenever possible
- The runtime should be simple enough to ship as a portable Go binary
- Keep product features that matter: summaries, speech, history, DND, sounds, and a live UI
- Delete compatibility logic instead of preserving tmux-era abstractions

## Recommended Runtime Shape

### Core services

1. NATS consumer
   - Subscribes to terminal-output subjects
   - Buffers output by `source`
   - Detects when a block is ready for summarization

2. Summarizer
   - Produces short, spoken summaries from buffered terminal output
   - Supports pluggable backends and local caching

3. Notification pipeline
   - Persists event history
   - Applies DND, severity, webhook, and sound policy
   - Publishes “ready” and “spoken” lifecycle events

4. TTS worker
   - Speaks notifications sequentially
   - Plays optional success/failure sounds
   - Exposes a test-speak path

5. HTTP/UI server
   - Serves health, config, events, live stream, and raw-output lookup
   - Serves the web UI

## Recommended NATS Model

### Subjects

- `npng.output.raw.<source>`
- `npng.output.closed.<source>`
- `npng.notification.ready`
- `npng.notification.spoken`
- `npng.control.test`

### Minimal payloads

#### Raw output

```json
{
  "source": "default",
  "ts": "2026-03-12T21:10:00Z",
  "chunk": "running build...\n"
}
```

#### Output closed

```json
{
  "source": "default",
  "ts": "2026-03-12T21:12:10Z",
  "status": "success",
  "duration_ms": 130000,
  "final": true
}
```

#### Notification ready

```json
{
  "source": "default",
  "ts": "2026-03-12T21:12:11Z",
  "summary": "Build completed successfully.",
  "severity": "success",
  "excerpt": "Finished in 2 minutes with no errors."
}
```

### Important decision

The preferred design is raw output plus an explicit close/failure marker.

If the app only receives raw text with no boundary event, completion and failure detection become heuristic and much less reliable.

## Storage and State

### In-memory state

- Active output buffers keyed by `source`
- Last-seen status per `source`
- Health state for NATS, summarizer, and TTS

### Persistent state

- Notification history
- Optional raw-output artifact storage for inspection
- Config and policy state

### Recommended first cut

- Persist notifications locally in a simple embedded store or append-only log
- Store raw output blocks by event ID when they produced a notification
- Keep the live working set in memory

JetStream can be part of the system, but do not force the UI to query NATS directly if a simpler local read model is cleaner.

## HTTP/API Shape

### Keep

- `GET /api/health`
- `GET /api/config`
- `POST /api/config`
- `POST /api/validate`
- `POST /api/dnd`
- `POST /api/tts/test`
- `GET /api/events`
- `GET /api/events/stream`
- `GET /api/output/:id`

### Optional

- `GET /api/sources`
- `POST /api/sources/:id/mute`
- `POST /api/sources/:id/policy`

### Do not recreate

- Pane toggles
- Window toggles
- Pane labels
- tmux previews
- tmux layout persistence

## UI Direction

### Keep conceptually

- Notification log
- Live event streaming
- Settings and validation UX
- DND and test-speak controls
- Success/failure visual states

### Replace

- “Panes” view becomes either:
  - a “Sources” view, or
  - no second operational view at all

### Remove

- Any pane/window identity model
- Any UI assumption that tmux is installed or visible

## Migration Strategy

### Phase 1: Freeze the domain

- Confirm the app consumes terminal output, not tmux data
- Confirm `source` is the stable identity
- Confirm whether boundary events are required

### Phase 2: Scaffold the Go app

- Initialize Go module
- Add app entrypoint
- Add config loading
- Add NATS connection management
- Add minimal HTTP server and `/api/health`

### Phase 3: Build the event pipeline

- Subscribe to raw output subjects
- Buffer by `source`
- Handle close/failure events
- Emit internal notification-ready events

### Phase 4: Port summarization

- Implement the summary prompt and provider logic in Go
- Port cache and rate-limiting behavior
- Summarize buffered output blocks instead of terminal snapshots

### Phase 5: Build speech delivery

- Implement serialized speech queueing in-process
- Keep sound effects and DND
- Use the fastest reliable Windows-compatible speech path first

### Phase 6: Build history and live stream

- Persist notifications
- Expose paginated history
- Expose SSE or websocket stream for live updates
- Support raw-output lookup by event ID

### Phase 7: Build the UI

- Start with notifications and settings
- Add health indicators
- Add optional sources view only if it earns its complexity

### Phase 8: Harden and simplify

- Add dedupe and idempotency rules
- Add reconnection and replay behavior
- Package as a portable binary

## Legacy Guidance

### Worth carrying forward

- Short spoken-summary behavior
- Multi-backend TTS support
- Sound effects and DND
- Notification history and raw-output inspection
- Simple local control surface

### Explicitly do not carry forward

- tmux pane discovery
- shell-prompt transition detection
- `capture-pane`
- shell hooks for exit codes
- tmux user options
- FIFO-based speech handoff

## Key Open Questions

1. Will upstream producers publish explicit close/failure markers?
2. Is single-source support enough for v1, with multi-source reserved for later?
3. Should summaries be generated inside Nexis Echo or upstream?
4. Should history live in JetStream, a local embedded store, or both?
5. Should the final deliverable be one embedded Go binary or a Go backend plus separate frontend build?

## Recommended Immediate Next Step

Write a technical spec for v1 with:

- exact NATS subjects
- exact JSON payloads
- config schema
- notification lifecycle
- health model
- first HTTP routes

That spec should be treated as the build contract for the new codebase.
