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
```

**Important:** The MCP server binary (`build/mcp-server-bin`) must exist before `go build` because `app.go` uses `//go:embed build/mcp-server-bin`. Always run `make mcp-server` before `go build ./...` or use `make build`/`make dev`.

## Architecture

This is a Wails v2 desktop app (Go backend + React frontend) that orchestrates multiple AI CLI agents (Claude Code, Gemini CLI, GitHub Copilot) communicating via a WebSocket hub.

### Three Components

1. **Desktop App** (`main.go` / `app.go`): Wails application managing UI, terminals, and hub communication
2. **Hub Server** (`internal/hub/`): WebSocket server managing in-memory room state, spawned as a child process (`mcp-server-bin --hub`)
3. **MCP Server** (`cmd/mcp-server/`): Standalone Go binary embedded via `//go:embed`, extracted to `~/.agent-chat/mcp-server-bin`. Dual-mode: `--hub` runs WebSocket server, default runs stdio MCP server + WebSocket client.

### Communication Flow

```
Desktop App (Wails)                    Hub Process (mcp-server-bin --hub)
  ├─ startup: spawn hub               ├─ WebSocket server localhost:{port}
  ├─ hubClient (WS client) ──────────→├─ In-memory room state
  ├─ Event handler:                    ├─ Periodic persist (5s)
  │   message_new → orchestrator       ├─ Event broadcast → subscribers
  │   agent_joined → frontend          └─ Port → ~/.agent-chat/hub.port
  ├─ Orchestrator (unchanged)
  └─ PTY Manager (unchanged)

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
| `internal/hubclient/` | WebSocket client: request-response RPC, event handling, reconnection |
| `internal/mcpserver/` | MCP tool implementations (9 tools), thin RPC wrappers over hub client |
| `internal/pty/` | Pseudo-terminal management — spawns CLIs, handles UTF-8 buffering, idle detection |
| `internal/orchestrator/` | Message routing — analyzes content, manages cooldowns, batches notifications |
| `internal/cli/` | CLI detection, MCP config management (~/.claude.json etc.), startup prompt composition |
| `internal/team/` | Team configuration persistence (teams.json) |
| `internal/prompt/` | Prompt template storage with variable substitution |

### MCP Config Management

The desktop app writes MCP server config to CLI config files at startup and per-terminal creation:
- `~/.claude.json` → `mcpServers["agent-chat"]`
- `~/.gemini/settings.json` → same structure
- `~/.copilot/mcp-config.json` → same structure

MCP config includes `AGENT_CHAT_DATA_DIR` env var pointing to `~/.agent-chat/` so MCP instances can discover the hub port.

**Critical:** Claude Code has per-project MCP overrides in `~/.claude.json` under `projects[path].mcpServers`. The `cleanProjectMCPOverrides()` function removes stale per-project `agent-chat` entries that would shadow the global config.

### Data Directory

All runtime data lives in `~/.agent-chat/`:
- `mcp-server-bin` — extracted Go binary (dual-mode: MCP server or hub)
- `mcp-server.log` — debug log (hub and MCP instances append here)
- `hub.port` — current hub WebSocket port (written at startup, removed on shutdown)
- `hub-state/{room}.json` — persisted room state (messages + agents)
- `teams.json`, `prompts.json`, `global_prompt.md` — app config

### Frontend

React 18 + TypeScript + Vite + Zustand. Stores in `frontend/src/store/` manage teams, terminals, messages, and prompts. Terminal rendering uses xterm.js. Real-time updates come from Wails event emission (`messages:new`, `agents:updated`, `pty:output:{sessionID}`).

## Code Conventions

- Agent-facing messages (MCP tool responses, system messages) use Turkish text with emoji
- JSON files use `indent=2`, `ensure_ascii=false` equivalent, and `float64` unix timestamps for `last_seen` (Python `time.time()` compatible)
- MCP server logs to file (not stdout/stderr) since stdio is used for JSON-RPC
- PTY environment strips `VSCODE_*`, `ELECTRON_*`, `NODE_OPTIONS` vars to prevent focus issues
- Startup prompts use ANSI bracketed paste mode (`ESC[200~...ESC[201~`) to prevent premature submission
- Hub persistence uses atomic write (temp file + rename) to prevent corruption
