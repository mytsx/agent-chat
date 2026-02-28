# Repository Guidelines

## Project Structure & Module Organization
- `main.go` and `app.go` are the Wails app entry points and lifecycle glue.
- `internal/` holds backend packages (`hub`, `orchestrator`, `pty`, `mcpserver`, `cli`, `team`, `prompt`, `types`, `validation`).
- `cmd/mcp-server/` contains the dual-mode MCP/hub binary entrypoint.
- `frontend/src/` contains the React + TypeScript UI (components, Zustand stores, shared types, styles).
- `build/`, `dist/`, and `scripts/` are for packaging/release artifacts; `docs/` and `prompts/` contain design docs and embedded prompt templates.
- Tests live alongside implementation files as `*_test.go`.

## Build, Test, and Development Commands
- `make dev`: builds `build/mcp-server-bin` and starts Wails dev mode.
- `make build`: production desktop build (includes MCP binary step).
- `make mcp-server`: builds only the MCP server binary.
- `go test ./...`: run all Go tests.
- `go test ./internal/orchestrator/ -v`: run one package in verbose mode.
- `cd frontend && npm run dev`: run frontend-only Vite server.
- `cd frontend && npm run build`: type-check + production frontend bundle.
- `make clean`: remove local build artifacts.

## Coding Style & Naming Conventions
- Go: run `gofmt` on changed files; keep package names lowercase and exported identifiers in `PascalCase`.
- TypeScript/React: project runs with `strict` TS settings; match existing style (2-space indentation, functional components, Zustand hooks).
- Naming: components use `PascalCase` (`TerminalGrid.tsx`), hooks/stores use `useX` (`useMessages.ts`).
- There is no enforced ESLint/Prettier config; keep formatting consistent with nearby code.

## Testing Guidelines
- Prefer table-driven tests with `t.Run` for backend logic.
- Add or update tests whenever changing hub routing, orchestrator behavior, validation, or PTY lifecycle code.
- Before opening a PR, run `go test ./...` locally.
- Frontend currently has no dedicated test harness; include manual verification notes for UI changes.

## Commit & Pull Request Guidelines
- Follow Conventional Commits used in history: `feat:`, `fix:`, `docs:` (optional scope is fine).
- Keep commits focused and descriptive; avoid mixing unrelated changes.
- PRs should include: clear summary, linked issue (if any), test commands run, and screenshots/GIFs for UI updates.
- Call out any release/signing implications (for example `DEVELOPER_ID` or notarization secrets).

## Security & Configuration Tips
- Never commit credentials, Apple notarization secrets, or local runtime data from `~/.agent-chat/`.
- `app.go` embeds `build/mcp-server-bin`; ensure it exists via `make mcp-server`, `make dev`, or `make build` before compiling.
