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
3. **MCP Server** (`cmd/mcp-server/main.go`): Dual-mode Go binary embedded via `//go:embed`, extracted to `~/.agent-chat/mcp-server-bin`. Flag `--hub` → WebSocket server; no flag → stdio MCP server + WebSocket client connecting to hub.

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
| `internal/hubclient/` | WebSocket client: request-response RPC (15s timeout), event handling, reconnection (exponential backoff) |
| `internal/mcpserver/` | MCP tool implementations (9 tools), thin RPC wrappers over hub client |
| `internal/pty/` | PTY management — spawns CLIs, handles UTF-8 buffering, idle detection |
| `internal/orchestrator/` | Message routing — analyzes content, manages cooldowns (3s), batches notifications |
| `internal/cli/` | CLI detection, MCP config management (~/.claude.json etc.), startup prompt composition |
| `internal/team/` | Team CRUD persistence (teams.json) |
| `internal/prompt/` | Prompt template storage with variable substitution |
| `internal/validation/` | Name validation (path traversal, forbidden chars, emoji) |

### Hub Internals

- Room messages capped at 500; truncated to 300 when limit exceeded
- Stale agents (5min idle) automatically removed on `list_agents`
- Each WebSocket message must be a single JSON frame (no batching/concatenation)
- Hub discovers port via `~/.agent-chat/hub.port` file or `AGENT_CHAT_HUB_PORT` env override
- Persistence: atomic write (temp file + rename) to `hub-state/{room}.json`

### Manager + Orchestrator Routing

Hub is the routing authority:
- **Manager gateway:** when an agent joins with `role="manager"`, non-manager `send_message` calls are intercepted to manager first
- **Single manager lock:** room allows only one active manager at a time
- **Identity enforcement:** `from_agent` must match the agent name bound by `join_room`
- **Heartbeat timeout:** manager routing lock auto-clears after ~30s inactivity

Orchestrator is PTY notification authority:
- **Skip:** acknowledgments (<80 chars + contains "teşekkür/thanks/ok/tamam")
- **Always notify:** questions (contains "?", "nasıl", "how"), `expects_reply=true`
- **Cooldown batching:** within 3s window, messages are queued and flushed as a single batch notification
- **Broadcast:** sender is excluded from notification targets

### MCP Config Management

The desktop app writes MCP server config to CLI config files at startup and per-terminal creation:
- `~/.claude.json` → `mcpServers["agent-chat"]`
- `~/.gemini/settings.json` → same structure
- `~/.copilot/mcp-config.json` → same structure

MCP config includes `AGENT_CHAT_DATA_DIR` env var pointing to `~/.agent-chat/` so MCP instances can discover the hub port. `AGENT_CHAT_ROOM` env var sets the default room name.

**Critical:** Claude Code has per-project MCP overrides in `~/.claude.json` under `projects[path].mcpServers`. The `cleanProjectMCPOverrides()` function removes stale per-project `agent-chat` entries that would shadow the global config.

### Data Directory (`~/.agent-chat/`)

- `mcp-server-bin` — extracted dual-mode Go binary
- `mcp-server.log` — shared log (hub and all MCP instances append here)
- `hub.port` — current hub WebSocket port (deleted before hub start to prevent stale reads)
- `hub-state/{room}.json` — persisted room state (messages + agents)
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
