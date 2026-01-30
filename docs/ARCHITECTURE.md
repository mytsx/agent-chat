# Agent Chat Room - Architecture

## Overview

This system enables multiple Claude Code agents to communicate with each other. An optional **Manager Claude** can coordinate all communication.

## Panel Layout

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              tmux session: agents                            │
├────────────────┬────────────────┬────────────────┬────────────────────────────┤
│    Pane 0      │    Pane 1      │    Pane 2      │         Pane 3            │
│                │                │                │                           │
│   Python       │   Manager      │   Backend      │   Frontend/Mobile         │
│  Orchestrator  │    Claude      │    Claude      │      Claude               │
│                │                │                │                           │
│  - Watches     │  - Analyzes    │  - Works on    │   - Works on              │
│    messages    │  - Decides     │    backend     │     frontend/mobile       │
│  - Notifies    │  - Routes      │    project     │     project               │
│    manager     │                │                │                           │
│                │                │                │                           │
└────────────────┴────────────────┴────────────────┴────────────────────────────┘
```

## Components

### 1. MCP Server (`server.py`)

Provides tools for agent messaging:
- `join_room(agent_name, role)` - Join the room
- `send_message(from, content, to)` - Send a message
- `read_messages(agent_name)` - Read messages
- `list_agents()` - List agents

### 2. Python Orchestrator (`orchestrator.py`)

Runs in the background:
- Watches `/tmp/agent-chat-room/messages.json`
- Notifies agents when new messages arrive
- Sends commands to panes via `tmux send-keys`

### 3. Manager Claude (Pane 1) - Optional

The intelligent coordinator agent:
- Analyzes all messages
- Decides who should respond
- Prevents infinite loops (thanks → you're welcome → ...)
- Summarizes when needed

### 4. Worker Agents (Pane 2, 3, ...)

The agents that do the actual work:
- Work on their respective projects
- Receive instructions from Manager
- Communicate with each other

## Message Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                         MESSAGE FLOW                             │
└─────────────────────────────────────────────────────────────────┘

  Backend          Orchestrator        Manager           Frontend
    │                   │                 │                   │
    │ 1. send_message   │                 │                   │
    │ "Question?"       │                 │                   │
    │──────────────────►│                 │                   │
    │                   │                 │                   │
    │                   │ 2. New message! │                   │
    │                   │ "Check it"      │                   │
    │                   │────────────────►│                   │
    │                   │                 │                   │
    │                   │           3. Reads message          │
    │                   │              analyzes it            │
    │                   │                 │                   │
    │                   │           4. Decides:               │
    │                   │           "Frontend should          │
    │                   │            respond"                 │
    │                   │                 │                   │
    │                   │                 │ 5. Instruction    │
    │                   │                 │ "Respond"         │
    │                   │                 │──────────────────►│
    │                   │                 │                   │
    │                   │                 │    6. Response    │
    │                   │                 │◄──────────────────│
    │                   │                 │                   │
    │                   │           7. Analyzes               │
    │                   │                 │                   │
    │  8. "Response"    │                 │                   │
    │◄──────────────────│◄────────────────│                   │
    │                   │                 │                   │
```

## Manager Decision Logic

```
┌─────────────────────────────────────────────────────────────────┐
│                    MANAGER DECISION TREE                         │
└─────────────────────────────────────────────────────────────────┘

                         New Message
                             │
                             ▼
                    ┌─────────────────┐
                    │ Analyze message │
                    │    content      │
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
              ▼              ▼              ▼
         ┌────────┐    ┌──────────┐   ┌──────────┐
         │Question│    │   Info?  │   │  Thanks/ │
         │   ?    │    │          │   │  Bye?    │
         └───┬────┘    └────┬─────┘   └────┬─────┘
             │              │              │
             ▼              ▼              ▼
      ┌────────────┐ ┌────────────┐ ┌────────────┐
      │ Route to   │ │ Inform but │ │   SKIP     │
      │ relevant   │ │ response   │ │ Don't      │
      │ agent:     │ │ optional   │ │ notify     │
      │ "respond"  │ │            │ │            │
      └────────────┘ └────────────┘ └────────────┘
```

## Data Files

```
/tmp/agent-chat-room/
├── messages.json       # All messages
├── agents.json         # Active agents
├── agent_panes.json    # Agent → Pane mapping
└── orchestrator_state.json  # Last processed message ID
```

## Setup & Launch

```bash
# 1. Start tmux session (dynamic)
./setup.py 3 --manager --names backend,mobile,web
tmux attach -t agents

# 2. Pane 0: Orchestrator
./orchestrator.py --watch --manager

# 3. Pane 1: Manager Claude
claude
# Then: Paste manager prompt (docs/MANAGER_PROMPT.md)

# 4. Pane 2: Backend Claude
cd /backend/project
claude
# Then: "Join agent-chat as 'backend'"

# 5. Pane 3: Frontend Claude
cd /frontend/project
claude
# Then: "Join agent-chat as 'frontend'"
```

## Operating Modes

### Direct Mode (No Manager)

Agents communicate directly with each other:
```
Backend ←──→ Frontend ←──→ Mobile
```

### Manager Mode

All messages are routed through the manager:
```
Backend ──→ Manager ──→ Frontend
    ↑          │           │
    └──────────┴───────────┘
```
