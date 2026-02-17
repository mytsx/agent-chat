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

This is a Wails v2 desktop app (Go backend + React frontend) that orchestrates multiple AI CLI agents (Claude Code, Gemini CLI, GitHub Copilot) communicating via shared JSON files.

### Two Independent Binaries

1. **Desktop App** (`main.go` / `app.go`): Wails application managing UI, terminals, and file watching
2. **MCP Server** (`cmd/mcp-server/`): Standalone Go binary embedded into the desktop app via `//go:embed`, extracted to `~/.agent-chat/mcp-server-bin` at runtime. Each CLI agent spawns its own MCP server instance via stdio.

### Communication Flow

```
Agent CLI (Claude/Gemini/Copilot)
  → MCP Server (per-agent stdio process)
    → JSON file write (~/.agent-chat/rooms/{team}/messages.json)
      → fsnotify watcher detects change
        → Orchestrator analyzes & routes notification
          → PTY write to target agent's terminal
```

All agent communication passes through JSON files with `syscall.Flock` locking. There is no database or network server.

### Key Packages

| Package | Purpose |
|---------|---------|
| `internal/mcpserver/` | MCP tool implementations (9 tools: join_room, send_message, read_messages, etc.) |
| `internal/pty/` | Pseudo-terminal management — spawns CLIs, handles UTF-8 buffering, idle detection |
| `internal/orchestrator/` | Message routing — analyzes content, manages cooldowns, batches notifications |
| `internal/watcher/` | fsnotify-based file watching for messages.json and agents.json changes |
| `internal/cli/` | CLI detection, MCP config management (~/.claude.json etc.), startup prompt composition |
| `internal/team/` | Team configuration persistence (teams.json) |
| `internal/prompt/` | Prompt template storage with variable substitution |

### MCP Config Management

The desktop app writes MCP server config to CLI config files at startup and per-terminal creation:
- `~/.claude.json` → `mcpServers["agent-chat"]`
- `~/.gemini/settings.json` → same structure
- `~/.copilot/mcp-config.json` → same structure

**Critical:** Claude Code has per-project MCP overrides in `~/.claude.json` under `projects[path].mcpServers`. The `cleanProjectMCPOverrides()` function removes stale per-project `agent-chat` entries that would shadow the global config.

### Data Directory

All runtime data lives in `~/.agent-chat/`:
- `mcp-server-bin` — extracted Go binary
- `mcp-server.log` — MCP server debug log (all instances append here)
- `rooms/{team_name}/messages.json` — message log per team
- `rooms/{team_name}/agents.json` — active agents per team
- `teams.json`, `prompts.json`, `global_prompt.md` — app config

### Frontend

React 18 + TypeScript + Vite + Zustand. Stores in `frontend/src/store/` manage teams, terminals, messages, and prompts. Terminal rendering uses xterm.js. Real-time updates come from Wails event emission (`messages:new`, `agents:updated`, `pty:output:{sessionID}`).

## Code Conventions

- Agent-facing messages (MCP tool responses, system messages) use Turkish text with emoji
- JSON files use `indent=2`, `ensure_ascii=false` equivalent, and `float64` unix timestamps for `last_seen` (Python `time.time()` compatible)
- MCP server logs to file (not stdout/stderr) since stdio is used for JSON-RPC
- PTY environment strips `VSCODE_*`, `ELECTRON_*`, `NODE_OPTIONS` vars to prevent focus issues
- Startup prompts use ANSI bracketed paste mode (`ESC[200~...ESC[201~`) to prevent premature submission
