# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Development

```bash
# Build MCP server binary first, then run Wails dev server
make dev

# Production build (MCP binary + Wails app)
make build

# Build only the MCP server binary
make mcp-server

# Run Go tests
go test ./...

# Run a specific test
go test ./internal/orchestrator/ -run TestAnalyzeMessage

# Frontend dev (standalone, without Wails)
cd frontend && npm run dev

# Build frontend only
cd frontend && npm run build

# macOS release (requires code signing identity)
make release VERSION=x.y.z
```

**Embed constraint:** `app.go` uses `//go:embed build/mcp-server-bin` and `//go:embed prompts/*.md`. The MCP binary must exist before `go build`. Always run `make mcp-server` first, or use `make build`/`make dev` which handle this automatically.

## Architecture

Wails v2 desktop app (Go backend + React frontend) that orchestrates multiple AI CLI agents (Claude Code, Gemini CLI, GitHub Copilot) communicating via a local WebSocket hub.

### Three Processes

1. **Desktop App** (`main.go` / `app.go`): Wails application — UI, PTY terminals, hub lifecycle, orchestrator
2. **Hub Server** (`internal/hub/`): WebSocket server for in-memory room state, spawned as child process (`mcp-server-bin --hub`)
3. **MCP Server** (`cmd/mcp-server/main.go`, `mark3labs/mcp-go` **v1.0.0**): Tri-mode Go binary embedded via `//go:embed`, extracted to `~/.agent-chat/mcp-server-bin`. Flag `--hub` → WebSocket server; `--analyze` → print event-log reports and exit; no flag → stdio MCP server + WebSocket client connecting to hub.

### Communication Flow

```
Desktop App (Wails)                    Hub Process (mcp-server-bin --hub)
  ├─ startup: spawn hub               ├─ WebSocket server localhost:{port}
  ├─ hubClient (WS client) ──────────→├─ In-memory room state
  ├─ Event handler:                    ├─ Periodic persist (5s)
  │   message_new → orchestrator       ├─ Event broadcast → subscribers
  │   agent_joined → frontend          └─ Port → ~/.agent-chat/hub.port
  ├─ Orchestrator
  └─ PTY Manager

CLI Agent (Claude/Gemini/Copilot)
  └─ stdio JSON-RPC
      └─ MCP Server (mcp-server-bin)
           └─ hubClient (WS client) ──→ Hub
```

### Key Packages

| Package | Purpose |
|---------|---------|
| `internal/types/` | Shared types: `Message`, `Agent`, `Request`, `Response`, `Event` |
| `internal/hub/` | WebSocket hub server: room state, client management, persistence, request dispatch |
| `internal/hubclient/` | WebSocket client: request-response RPC (15s timeout), event handling, **supervised reconnect** (jittered backoff, address re-resolution, session replay) |
| `internal/mcpserver/` | MCP tool implementations (9 tools), thin RPC wrappers over hub client |
| `internal/pty/` | PTY management — spawns CLIs, handles UTF-8 buffering, idle detection |
| `internal/orchestrator/` | Message routing — analyzes content, manages cooldowns (3s), batches notifications |
| `internal/cli/` | CLI detection, MCP config management (~/.claude.json etc.), startup prompt composition |
| `internal/team/` | Team CRUD persistence (teams.json) |
| `internal/prompt/` | Prompt template storage with variable substitution |
| `internal/validation/` | Name validation (path traversal, forbidden chars, emoji) |
| `internal/eventlog/` | Structured JSONL event stream + `--analyze` reports (OTel semconv field names) |

### Agent Liveness (#98)

Three distinct mechanisms used to look like one bug ("agent odadan düştü"):

1. **Never reached the hub.** `DiscoverHubAddr` failing used to `os.Exit(1)`, so
   an MCP process that started before the desktop wrote `hub.port` simply died
   (12.765 such lines in the shipped log). Startup no longer blocks or exits on
   the hub: stdio is served immediately and `StartBackgroundConnect` dials in the
   background. An MCP server that stalls or exits during startup is marked failed
   by its host and never retried, so this is not optional.
2. **Connected, dropped, never came back.** There was no reconnect at all —
   `ConnectWithRetry` ran once at startup, and after any read error `conn` went
   nil and every `Send` failed for the life of the process. `readLoop` now hands
   off to a supervisor: jittered exponential backoff (500ms → 30s), the address
   **re-resolved on every dial** (the hub uses `Run(0)`, so a restart moves it to
   a new port), and the session — identify, join, subscriptions — replayed on
   reconnect, because the hub drops an agent from the roster the moment its
   socket dies. A deliberate `leave_room` cancels the replay.
3. **Connected but evicted.** `staleTimeout` only saw `LastSeen`, which only RPC
   calls refreshed, so an agent working quietly for five minutes was removed.
   Liveness now comes from the connection (`Hub.connectedAgents`, a **count** so a
   reconnect handover never flickers to "gone"), guarded by its own `connMu` —
   reusing `h.mu` would invert `persistRoom`'s `h.mu → room lock` order.

Departures are deferred by `graceWindow` (5s): the room's system messages are
read by the other agents, so a blip must not tell the team that someone left and
a stranger arrived. Because the roster entry is retained, a reconnecting client's
replayed `join_room` finds its own name still present — that is a **takeover**
(`RoomState.Takeover`, `agent_chat.agent.rejoined`), not a name collision, and it
announces nothing. A clash with a still-CONNECTED agent is still rejected.

Liveness claims are per-connection (`Client.livenessKey`), not per join:
`clear_room` empties the roster without touching sockets, so a second join on the
same socket must not add a claim nothing will ever release.

Client-side read deadline is 90s with a ping handler, so a half-open socket is
detected instead of hanging `readLoop` forever. A **write** can notice the same
half-open socket sooner, so a write failure tears the connection down and wakes
the supervisor (`dropConnIf`) rather than leaving a dead socket installed.

A reconnecting socket is **gated** while its session replays: only
identity/join/subscribe pass (`HubClient.restoring`, and the `Bootstrap` view
handed to `SetBootstrap`), so an ordinary tool call cannot land on a connection
that has not identified yet and collect a protocol rejection. The gate is armed
**before the dial**, not after it, and the replay repeats while `sess.rev`
changed underneath it — a gated join still records its intent, and a pass that
had already snapshotted would otherwise leave that membership unmade until the
next disconnect.

`Takeover` **rejects** a manager role whose seat is held by a different live
manager instead of writing the role and silently skipping the lock — that is how
a room ended up with a connected manager and no routing gateway. The refused
**configuration is the claim**: `set_manager`'s `HandoffManager` seats whoever
the desktop names, if that agent is in the room and connected, in one locked step
that also clears the stale lock (roster key resolved case-insensitively, like the
rest of the manager path). Waiting for a refused join to leave a claim behind
cannot work — the client treats a protocol rejection as final, an agent not yet
in the roster leaves nothing to act on, and after a hub restart neither lock
field is persisted, so a replayed worker role lands before the desktop
re-configures the room. For the same reason `Takeover` neither downgrades an
agent the desktop still names as manager (`RoomState.configuredManager`) nor
leaves a free seat unclaimed by one. The
connection-bound observer flag is likewise authoritative in both directions, so a
revoked observer is not stuck read-only for the life of its socket.

`AGENT_CHAT_HUB_PORT` is fixed for the process lifetime and outranks `hub.port`,
so a malformed value is fatal at startup (`ErrInvalidHubPortConfig`). A missing
or torn `hub.port` stays transient and is waited out in the background.

### Hub Internals

- Room messages capped at 500; truncated to 300 when limit exceeded
- Stale agents (5min idle) removed on `list_agents` — **only if disconnected** (see Agent Liveness)
- Each WebSocket message must be a single JSON frame (no batching/concatenation)
- Hub discovers port via `~/.agent-chat/hub.port` file or `AGENT_CHAT_HUB_PORT` env override
- Persistence: atomic write (temp file + rename) to `hub-state/{room}.json`

### Addressing (#99)

- **Recipient must be present.** `send_message` to an agent that is not in the
  room is rejected, and the error lists who is. Not enforced while a manager is
  active: there every message goes to the manager first, so `to` is only a hint
  and rejecting it would break the manager's routing job.
- **Reads page forward.** `ReadMessages` returns the OLDEST matching messages
  from `since_id`, not the newest tail. Returning the tail lost messages
  silently: the agent advanced its cursor to the highest ID it saw, so anything
  between the cursor and that tail could never be asked for again.
- `unread_only` is a misleading name kept for compatibility — it means "hide my
  own messages", not read-state tracking. `since_id` is the real cursor.

### Manager + Orchestrator Routing

Hub is the routing authority:
- **Manager gateway:** when an agent joins with `role="manager"`, non-manager `send_message` calls are intercepted to manager first
- **Single manager lock:** room allows only one active manager at a time
- **Identity enforcement:** `from_agent` must match the agent name bound by `join_room`
- **Heartbeat timeout:** manager routing lock auto-clears after ~300s (5min) inactivity, matching stale agent cleanup

Orchestrator is PTY notification authority:
- **Skip:** acknowledgments (<80 chars + contains "teşekkür/thanks/ok/tamam")
- **Always notify:** questions (contains "?", "nasıl", "how"), `expects_reply=true`
- **Cooldown batching:** within 3s window, messages are queued and flushed as a single batch notification
- **Broadcast:** sender is excluded from notification targets

### Observer Role (#17)

A read-only "outside eye" agent (`role="observer"`), distinct from manager — it stays **outside** routing:
- **Send blocked:** the hub rejects an observer's `send_message` (`RoomState.IsObserver`), keyed on the join-bound `c.agentName`, *before* the manager gateway — so it never reroutes and never refreshes the manager heartbeat.
- **Read-only:** `read_all_messages` is allowed for observers; that branch calls `TouchAgentLastSeen` so a read_all-only poller isn't stale-evicted (the read path itself doesn't touch `last_seen`).
- **No lock, plural:** observers take no manager lock and can coexist with a manager and with each other.
- **Notification-isolated:** observers are NOT registered with the orchestrator and are skipped in `broadcastToSessions` (via `broadcastRoleLookup` → `AgentConfig.Role`), so no automatic PTY notifications reach them (user-driven).
- **Mode plumbing:** a single `agentMode` string ("manager"/"observer"/"") flows `resolveAgentMode → composeAgentPrompt → ComposeStartupPrompt`. Role is persisted as `AgentConfig.Role="observer"` (`SetTeamObserver`), mutually exclusive with `Team.ManagerAgent`.

### Structured Event Log (#101)

The hub — and only the hub — writes `~/.agent-chat/events.jsonl`, a JSON-lines
stream of room events (`agent_chat.hub.started`, `.agent.joined/left/evicted`,
`.message.sent/rerouted`, `.messages.read`, `.client.connected/disconnected`).
It is the measurement layer for the drop (#98) and addressing (#99) bugs; the
plain-text `mcp-server.log` stays alongside it as a fallback.

- **Field names are OpenTelemetry semantic conventions**, not invented ones:
  `gen_ai.conversation.id` (room), `gen_ai.agent.name`, `gen_ai.input.messages`
  (content), `mcp.method.name`, `jsonrpc.request.id`, `network.transport`,
  `error.type`. Only attributes OTel has no name for take the `agent_chat.`
  prefix. `trace_id` / `span_id` are reserved for #100. See `internal/eventlog/semconv.go`.
- **Logging never blocks the hub.** `Log` is a non-blocking channel send; a full
  buffer drops the event and increments a counter reported on `hub.stopped`.
  This is why `RoomState.evictFn` may be called under the room lock, unlike
  `archiveFn`.
- **`eventlog.New` never fails fatally** — it returns a working `NopLogger`
  alongside its error, and a hub without a data dir gets one by default.
- **Message content is captured by default** but gated by an explicit env var:
  `AGENT_CHAT_CAPTURE_MESSAGE_CONTENT=false` (OTel's
  `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` is honoured too).
  Truncated at 8 KB on a rune boundary. Deliberate deviation from OTel's
  default-off stance — documented in the design spec.
- **Read progress is a set, not a watermark.** `agent_chat.read.id_ranges`
  carries exactly what a read returned, as contiguous `[start,end]` pairs,
  because `RoomState.ReadMessages` returns only the newest matching tail once
  its limit bites. Ranges mean no cap is needed — which matters because manager
  and observer joins bypass the room's truncation, so a room can exceed 500 and
  `read_all_messages(limit=1000)` can exceed any fixed one. Both `get_messages`
  and `get_all_messages` (which managers poll) record it.
- **Generations are stamped, not inferred.** `agent_chat.room.generation` is
  captured under the room lock on send and read; `clear_room` increments it from
  inside `ClearArchived`, still holding the lock, so a boundary can never land
  on the wrong side of a racing send. `delete_room` emits its boundary while
  `h.mu` still excludes recreation. An unclean restart — or a clean one whose
  `agent_chat.persist.ok` is not true — starts a new epoch, because a rolled-back
  snapshot can reuse message IDs.
- **`agent_chat.delivery.target`** is who a message was actually stored for; the
  manager gateway makes it differ from `recipient.name`. Report 2 (#99) uses the
  addressee, report 3 uses the delivery target, so neither corrupts the other.
- **Reports:** `mcp-server-bin --analyze [--room X] [--since 24h] [--legacy-log] [--json]`
  answers: drops by mechanism, messages to absent recipients, messages never
  read, and hub outage windows. `--legacy-log` additionally scans the old
  plain-text log for "could not reach the hub at all" lines, which by definition
  cannot appear in the structured stream.

Design: `docs/superpowers/specs/2026-09-08-structured-agent-log-design.md`

### MCP Config Management

The desktop app writes MCP server config to CLI config files at startup and per-terminal creation:
- `~/.claude.json` → `mcpServers["agent-chat"]`
- `~/.gemini/settings.json` → same structure
- `~/.copilot/mcp-config.json` → same structure

MCP config includes `AGENT_CHAT_DATA_DIR` env var pointing to `~/.agent-chat/` so MCP instances can discover the hub port. `AGENT_CHAT_ROOM` names the terminal's room.

**No invented default room (#99).** An unset `AGENT_CHAT_ROOM` is left empty rather than becoming `"default"`, and the hub resolves an omitted room against the room that *connection joined* (`Hub.resolveRoomFor`), falling back to its own default only for unjoined clients like the desktop. The old behaviour answered from a room the agent's team was not in — silently, 1.479 times in the shipped log.

**Critical:** Claude Code has per-project MCP overrides in `~/.claude.json` under `projects[path].mcpServers`. The `cleanProjectMCPOverrides()` function removes stale per-project `agent-chat` entries that would shadow the global config.

### Data Directory (`~/.agent-chat/`)

- `mcp-server-bin` — extracted tri-mode Go binary
- `mcp-server.log` — shared log (hub and all MCP instances append here)
- `hub.port` — current hub WebSocket port (deleted before hub start to prevent stale reads)
- `hub-state/{room}.json` — persisted room state (messages + agents)
- `events.jsonl` — structured event stream (rotated via lumberjack; `events-*.jsonl[.gz]` backups)
- `teams.json`, `prompts.json`, `global_prompt.md` — app config

### Frontend

React 18 + TypeScript + Vite + Zustand. No ESLint/Prettier config.

- **Stores** (`frontend/src/store/`): `useTeams`, `useTerminals`, `useMessages`, `usePrompts` — Zustand stores
- **Terminal:** xterm.js v6 with fit addon, react-resizable-panels for collapsible sidebar
- **Events:** Wails `EventsEmit` pushes `messages:new`, `agents:updated`, `pty:output:{sessionID}` to frontend
- **Types:** `frontend/src/lib/types.ts` mirrors Go types — `CLIType`, `Team`, `TerminalSession`, `Message`, `Agent`

### App Lifecycle (`app.go`)

**startup():** create data dir → init PTY manager → init orchestrator → seed prompts → extract MCP binary → write MCP configs → start hub process → connect hub client → subscribe to team rooms → monitor hub health

**shutdown():** close hub client → SIGTERM hub (3s grace, then SIGKILL) → close all PTYs

**Hub crash recovery:** monitorHub() goroutine restarts hub process, reconnects client, re-subscribes all rooms.

## Code Conventions

- Agent-facing messages (MCP tool responses, system messages) use Turkish text with emoji
- `last_seen` fields use `float64` Unix timestamps (Python `time.time()` compatible)
- MCP server logs to file (not stdout/stderr) since stdio is used for JSON-RPC
- PTY environment strips `VSCODE_*`, `ELECTRON_*`, `NODE_OPTIONS` vars to prevent focus issues
- Startup prompts use ANSI bracketed paste mode (`ESC[200~...ESC[201~`) to prevent premature submission
- Go module name is `desktop` (not a URL-based module path)

## Testing Patterns

Tests use table-driven subtests with `t.Run()`. The orchestrator test suite (`internal/orchestrator/orchestrator_test.go`) includes:
- `newTestOrchestrator()` helper with fake state tracking and injectable `SendFunc`
- Thread safety tests spawning 50+ goroutines with `sync.WaitGroup`
- Integration flow tests covering cooldown, batching, and broadcast routing
