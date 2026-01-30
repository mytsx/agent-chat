# Agent Chat Room

**Multi-agent communication system for Claude Code instances via MCP (Model Context Protocol)**

[![Python 3.10+](https://img.shields.io/badge/python-3.10+-blue.svg)](https://www.python.org/downloads/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![MCP](https://img.shields.io/badge/MCP-Compatible-green.svg)](https://modelcontextprotocol.io/)

---

## What is this?

Agent Chat Room enables multiple Claude Code agents to communicate with each other in real-time. Think of it as a **chat room for AI agents** - they can collaborate on complex tasks, share information, and coordinate their work.

```
┌─────────────────────────────────────────────────────────────┐
│                    AGENT CHAT ROOM                          │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│   Backend Agent  ←──────→  Frontend Agent                   │
│        ↑                         ↑                          │
│        │                         │                          │
│        └────────→ Mobile ←───────┘                          │
│                   Agent                                     │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## Features

- **Multi-Agent Messaging** - Agents can send direct or broadcast messages
- **Dynamic Agent Count** - Support 1-8 concurrent agents
- **Optional Manager Mode** - Centralized coordination with a manager agent
- **Infinite Loop Prevention** - Smart detection of acknowledgment messages
- **tmux Integration** - Visual multi-pane workspace
- **Real-time Orchestration** - Automatic message routing and notifications

## Use Cases

- **Parallel Development** - Backend, frontend, and mobile agents working together
- **Code Review** - One agent writes code, another reviews it
- **Complex Refactoring** - Multiple agents handling different parts of a codebase
- **Research & Implementation** - One agent researches, another implements

## Tech Stack

| Component | Technology |
|-----------|------------|
| Protocol | [MCP (Model Context Protocol)](https://modelcontextprotocol.io/) |
| Runtime | Python 3.10+ |
| MCP Framework | [FastMCP](https://github.com/modelcontextprotocol/python-sdk) (via mcp package) |
| Terminal Multiplexer | tmux |
| Data Storage | JSON files (ephemeral, in `/tmp`) |
| IPC | File-based with `fcntl` locking |

## Prerequisites

- **Python 3.10+**
- **tmux** - Terminal multiplexer
- **Claude Code CLI** - Anthropic's CLI tool

```bash
# Install tmux (macOS)
brew install tmux

# Install tmux (Ubuntu/Debian)
sudo apt install tmux
```

## Installation

### 1. Clone the repository

```bash
git clone https://github.com/mytsx/agent-chat.git
cd agent-chat
```

### 2. Set up Python environment

```bash
python3 -m venv venv
source venv/bin/activate
pip install -r requirements.txt
```

### 3. Add MCP server to Claude Code

```bash
claude mcp add agent-chat -- "$(pwd)/venv/bin/python" "$(pwd)/server.py"
```

## Quick Start

### Two Agents (Direct Mode)

```bash
# Terminal 1: Setup and start orchestrator
./setup.py 2 --names backend,frontend
tmux attach -t agents

# In Pane 0: Start orchestrator
./orchestrator.py --watch

# In Pane 1: Start first agent
claude
# → "Join agent-chat as 'backend', you're a Backend Developer"

# In Pane 2: Start second agent
claude
# → "Join agent-chat as 'frontend', you're a Frontend Developer"
```

### Three Agents with Manager

```bash
# Setup with manager mode
./setup.py 3 --manager --names backend,mobile,web

# In Pane 0: Orchestrator with manager flag
./orchestrator.py --watch --manager

# In Pane 1: Manager agent (see docs/MANAGER_PROMPT.md)
# In Pane 2-4: Worker agents
```

## MCP Tools Reference

| Tool | Description |
|------|-------------|
| `join_room(agent_name, role)` | Join the chat room |
| `send_message(from_agent, content, to_agent, expects_reply, priority)` | Send a message |
| `read_messages(agent_name, since_id, unread_only, limit)` | Read messages for you |
| `read_all_messages(since_id, limit)` | Read ALL messages (manager use, limit default: 15) |
| `list_agents(agent_name)` | List active agents |
| `leave_room(agent_name)` | Leave the chat room |
| `clear_room()` | Clear all messages and agents |
| `get_last_message_id(agent_name)` | Get last message ID |

### send_message Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `from_agent` | required | Sender agent name |
| `content` | required | Message content |
| `to_agent` | `"all"` | Target agent or "all" for broadcast |
| `expects_reply` | `True` | Set `False` for acks to prevent loops |
| `priority` | `"normal"` | `"urgent"`, `"normal"`, or `"low"` |

## CLI Commands

### setup.py

```bash
./setup.py <num_agents> [--manager] [--names name1,name2,...]

# Examples
./setup.py 2                              # 2 agents with default names
./setup.py 3 --names backend,mobile,web   # 3 agents with custom names
./setup.py 3 --manager                    # 3 agents + manager
```

### orchestrator.py

```bash
./orchestrator.py --watch              # Direct mode (no manager)
./orchestrator.py --watch --manager    # Manager mode
./orchestrator.py --clear              # Clear all state
./orchestrator.py --status             # Show current status
./orchestrator.py --assign <agent> <pane>  # Manual pane assignment
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      tmux session                           │
├────────────────┬────────────────┬────────────────┬──────────┤
│    Pane 0      │    Pane 1      │    Pane 2      │  Pane 3  │
│  Orchestrator  │    Agent 1     │    Agent 2     │ Agent 3  │
│   (Python)     │  (Claude Code) │  (Claude Code) │  (...)   │
└───────┬────────┴───────┬────────┴───────┬────────┴──────────┘
        │                │                │
        │    ┌───────────┴────────────────┴───────────┐
        │    │         MCP Server (server.py)         │
        │    │  ┌─────────────────────────────────┐   │
        │    │  │  /tmp/agent-chat-room/          │   │
        │    │  │  ├── messages.json              │   │
        │    │  │  ├── agents.json                │   │
        │    │  │  └── agent_panes.json           │   │
        │    │  └─────────────────────────────────┘   │
        │    └────────────────────────────────────────┘
        │                        │
        └────────────────────────┘
              watches & notifies
```

## Project Structure

```
agent-chat/
├── server.py           # MCP server with chat tools
├── orchestrator.py     # Message routing & tmux integration
├── setup.py            # Dynamic tmux session setup
├── start.sh            # Legacy 4-pane starter script
├── requirements.txt    # Python dependencies
├── config/
│   └── base_prompt.txt # Base prompt for agents
├── docs/
│   ├── ARCHITECTURE.md # Detailed architecture docs
│   └── MANAGER_PROMPT.md # Manager agent prompt
└── README.md
```

## Known Limitations

### Token Usage Warning

This system can consume significant tokens because:
- Each agent reads messages frequently
- Message history accumulates over time
- Broadcast messages are sent to all agents

**Recommendations:**
- Use `expects_reply=False` for acknowledgments
- Clear the room periodically with `clear_room()`
- Use direct messages instead of broadcasts when possible
- Keep conversations focused and concise

### Other Limitations

- Messages are stored in `/tmp` (cleared on reboot)
- Requires tmux for multi-pane orchestration
- macOS/Linux only (no Windows support)

## Infinite Loop Prevention

The orchestrator automatically skips notification for:
- Thank you messages: "thanks", "thank you", "got it"
- Acknowledgments: "ok", "okay", "understood"
- Short positive responses: "great", "perfect", "nice"

Agents can also use `expects_reply=False`:
```python
send_message("backend", "Thanks!", "frontend", expects_reply=False)
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

- [Anthropic](https://anthropic.com/) for Claude and Claude Code
- [Model Context Protocol](https://modelcontextprotocol.io/) for the MCP specification
- [FastMCP](https://github.com/jlowin/fastmcp) for the Python MCP framework

---

**Made with Claude Code**
