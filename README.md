# Agent Chat Room

**Multi-agent communication system for Claude Code instances via MCP (Model Context Protocol)**

A desktop application built with Go/Wails that enables multiple Claude Code agents to communicate with each other in real-time.

## Components

This project has two independent components:

| Component | Location | Description |
|-----------|----------|-------------|
| **Desktop App** | This repo | Go/Wails desktop application |
| **MCP Server** | `~/Developer/mcp-servers/agent-chat/` | Python MCP server for agent messaging |

Both components share only the `/tmp/agent-chat-room/` data directory and the `AGENT_CHAT_DIR` environment variable.

## Development

```bash
# Live development
wails dev

# Production build
wails build
```

## MCP Server Setup

The MCP server is located at `~/Developer/mcp-servers/agent-chat/`.

```bash
cd ~/Developer/mcp-servers/agent-chat
python3 -m venv venv
source venv/bin/activate
pip install -r requirements.txt
claude mcp add agent-chat -- "$(pwd)/venv/bin/python" "$(pwd)/server.py"
```

## License

MIT
