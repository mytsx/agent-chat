#!/usr/bin/env python3
"""
Agent Chat Room MCP Server
Allows multiple Claude Code agents to communicate with each other.
"""

import json
import os
import time
import fcntl
from datetime import datetime
from pathlib import Path
from mcp.server.fastmcp import FastMCP

# Chat data directory - shared between all instances
CHAT_DIR = Path(os.environ.get("AGENT_CHAT_DIR", "/tmp/agent-chat-room"))
DEFAULT_ROOM = os.environ.get("AGENT_CHAT_ROOM", "default")

# Ensure base directory exists
CHAT_DIR.mkdir(parents=True, exist_ok=True)

mcp = FastMCP("agent-chat")

def _get_room_dir(room: str = "") -> Path:
    """Get room directory, creating it if needed."""
    room_name = room if room else DEFAULT_ROOM
    room_dir = CHAT_DIR / room_name
    room_dir.mkdir(parents=True, exist_ok=True)
    return room_dir

def _read_json(filepath: Path, default: dict | list) -> dict | list:
    """Thread-safe JSON file reading."""
    if not filepath.exists():
        return default
    try:
        with open(filepath, "r") as f:
            fcntl.flock(f.fileno(), fcntl.LOCK_SH)
            content = f.read()
            fcntl.flock(f.fileno(), fcntl.LOCK_UN)
            return json.loads(content) if content else default
    except (json.JSONDecodeError, IOError):
        return default

def _write_json(filepath: Path, data: dict | list) -> None:
    """Thread-safe JSON file writing."""
    with open(filepath, "w") as f:
        fcntl.flock(f.fileno(), fcntl.LOCK_EX)
        json.dump(data, f, indent=2, ensure_ascii=False)
        fcntl.flock(f.fileno(), fcntl.LOCK_UN)

def _get_agents(room: str = "") -> dict:
    """Get active agents for a room."""
    return _read_json(_get_room_dir(room) / "agents.json", {})

def _save_agents(agents: dict, room: str = "") -> None:
    """Save agents for a room."""
    _write_json(_get_room_dir(room) / "agents.json", agents)

def _get_messages(room: str = "") -> list:
    """Get all messages for a room."""
    return _read_json(_get_room_dir(room) / "messages.json", [])

def _save_messages(messages: list, room: str = "") -> None:
    """Save messages for a room."""
    _write_json(_get_room_dir(room) / "messages.json", messages)

def _cleanup_stale_agents(agents: dict, timeout: int = 300) -> dict:
    """Remove agents that haven't been active for `timeout` seconds."""
    now = time.time()
    return {name: info for name, info in agents.items()
            if now - info.get("last_seen", 0) < timeout}

@mcp.tool()
def join_room(agent_name: str, role: str = "", room: str = "") -> str:
    """
    Join the chat room with a unique name.

    Args:
        agent_name: Unique name for this agent (e.g., "backend", "frontend", "mobile")
        role: Optional role description (e.g., "Backend API Developer")
        room: Room name (empty = default room from AGENT_CHAT_ROOM env or "default")

    Returns:
        Confirmation message with list of other agents in the room
    """
    agents = _get_agents(room)
    agents = _cleanup_stale_agents(agents)

    agents[agent_name] = {
        "role": role,
        "joined_at": datetime.now().isoformat(),
        "last_seen": time.time()
    }
    _save_agents(agents, room)

    other_agents = [name for name in agents.keys() if name != agent_name]

    # Add system message about joining
    messages = _get_messages(room)
    messages.append({
        "id": len(messages) + 1,
        "from": "SYSTEM",
        "to": "all",
        "content": f"\U0001f7e2 {agent_name} odaya katıldı" + (f" (Rol: {role})" if role else ""),
        "timestamp": datetime.now().isoformat(),
        "type": "system"
    })
    _save_messages(messages, room)

    room_label = room if room else DEFAULT_ROOM
    if other_agents:
        return f"\u2705 '{agent_name}' olarak '{room_label}' odasına katıldın. Odadaki diğer agent'lar: {', '.join(other_agents)}"
    return f"\u2705 '{agent_name}' olarak '{room_label}' odasına katıldın. Şu an odada başka agent yok."

@mcp.tool()
def send_message(
    from_agent: str,
    content: str,
    to_agent: str = "all",
    expects_reply: bool = True,
    priority: str = "normal",
    room: str = ""
) -> str:
    """
    Send a message to other agents.

    Args:
        from_agent: Your agent name
        content: Message content
        to_agent: Target agent name or "all" for broadcast (default: "all")
        expects_reply: Set False for acknowledgments/thanks to prevent infinite loops (default: True)
        priority: "urgent", "normal", or "low" (default: "normal")
        room: Room name (empty = default room)

    Returns:
        Confirmation that message was sent
    """
    # Update last_seen
    agents = _get_agents(room)
    if from_agent in agents:
        agents[from_agent]["last_seen"] = time.time()
        _save_agents(agents, room)

    messages = _get_messages(room)
    message = {
        "id": len(messages) + 1,
        "from": from_agent,
        "to": to_agent,
        "content": content,
        "timestamp": datetime.now().isoformat(),
        "type": "direct" if to_agent != "all" else "broadcast",
        "expects_reply": expects_reply,
        "priority": priority
    }
    messages.append(message)
    _save_messages(messages, room)

    if to_agent == "all":
        return f"\U0001f4e4 Mesaj tüm agent'lara gönderildi (ID: {message['id']})"
    return f"\U0001f4e4 Mesaj '{to_agent}' agent'ına gönderildi (ID: {message['id']})"

@mcp.tool()
def read_messages(agent_name: str, since_id: int = 0, unread_only: bool = True, limit: int = 10, room: str = "") -> str:
    """
    Read messages from the chat room.

    Args:
        agent_name: Your agent name (to filter relevant messages)
        since_id: Only get messages after this ID (default: 0 for all)
        unread_only: If True, only show messages not from yourself (default: True)
        limit: Maximum number of messages to return (default: 10, 0 for unlimited)
        room: Room name (empty = default room)

    Returns:
        List of messages formatted for reading
    """
    # Update last_seen
    agents = _get_agents(room)
    if agent_name in agents:
        agents[agent_name]["last_seen"] = time.time()
        _save_agents(agents, room)

    messages = _get_messages(room)

    # Filter messages
    filtered = []
    for msg in messages:
        if msg["id"] <= since_id:
            continue
        if unread_only and msg["from"] == agent_name:
            continue
        # Include if broadcast, direct to this agent, or system message
        if msg["to"] == "all" or msg["to"] == agent_name or msg["type"] == "system":
            filtered.append(msg)

    if not filtered:
        return "\U0001f4ed Yeni mesaj yok."

    total_count = len(filtered)

    # Apply limit (get last N messages)
    if limit > 0 and len(filtered) > limit:
        filtered = filtered[-limit:]
        result = f"\U0001f4ec Son {limit} mesaj (toplam {total_count}):\n\n"
    else:
        result = f"\U0001f4ec {len(filtered)} mesaj:\n\n"

    for msg in filtered:
        timestamp = datetime.fromisoformat(msg["timestamp"]).strftime('%H:%M:%S')  # HH:MM:SS
        if msg["type"] == "system":
            result += f"[{timestamp}] {msg['content']}\n"
        elif msg["to"] == "all":
            result += f"[{timestamp}] {msg['from']} \u2192 HERKESE: {msg['content']}\n"
        else:
            result += f"[{timestamp}] {msg['from']} \u2192 {msg['to']}: {msg['content']}\n"
        result += f"  (ID: {msg['id']})\n\n"

    return result

@mcp.tool()
def list_agents(agent_name: str = "", room: str = "") -> str:
    """
    List all agents currently in the chat room.

    Args:
        agent_name: Your agent name (optional, for updating last_seen)
        room: Room name (empty = default room)

    Returns:
        List of active agents with their roles
    """
    agents = _get_agents(room)
    agents = _cleanup_stale_agents(agents)
    _save_agents(agents, room)

    if agent_name and agent_name in agents:
        agents[agent_name]["last_seen"] = time.time()
        _save_agents(agents, room)

    if not agents:
        return "\U0001f465 Odada kimse yok."

    room_label = room if room else DEFAULT_ROOM
    result = f"\U0001f465 '{room_label}' odasındaki agent'lar ({len(agents)}):\n\n"
    for name, info in agents.items():
        role = info.get("role", "")
        joined = info.get("joined_at", "").split("T")[0]
        marker = " (sen)" if name == agent_name else ""
        result += f"  \u2022 {name}{marker}"
        if role:
            result += f" - {role}"
        result += f"\n    Katılım: {joined}\n"

    return result

@mcp.tool()
def leave_room(agent_name: str, room: str = "") -> str:
    """
    Leave the chat room.

    Args:
        agent_name: Your agent name
        room: Room name (empty = default room)

    Returns:
        Confirmation message
    """
    agents = _get_agents(room)

    if agent_name not in agents:
        return f"\u26a0\ufe0f '{agent_name}' zaten odada değil."

    del agents[agent_name]
    _save_agents(agents, room)

    # Add system message about leaving
    messages = _get_messages(room)
    messages.append({
        "id": len(messages) + 1,
        "from": "SYSTEM",
        "to": "all",
        "content": f"\U0001f534 {agent_name} odadan ayrıldı",
        "timestamp": datetime.now().isoformat(),
        "type": "system"
    })
    _save_messages(messages, room)

    return f"\U0001f44b '{agent_name}' odadan ayrıldı."

@mcp.tool()
def clear_room(room: str = "") -> str:
    """
    Clear all messages and agents from the room. Use with caution!

    Args:
        room: Room name (empty = default room)

    Returns:
        Confirmation message
    """
    room_dir = _get_room_dir(room)
    _write_json(room_dir / "messages.json", [])
    _write_json(room_dir / "agents.json", {})
    room_label = room if room else DEFAULT_ROOM
    return f"\U0001f9f9 '{room_label}' odası temizlendi. Tüm mesajlar ve agent kayıtları silindi."

@mcp.tool()
def read_all_messages(since_id: int = 0, limit: int = 15, room: str = "") -> str:
    """
    Read ALL messages in the chat room (for manager/admin use).

    Args:
        since_id: Only get messages after this ID (default: 0 for all)
        limit: Maximum number of messages to return (default: 15, 0 for unlimited)
        room: Room name (empty = default room)

    Returns:
        List of all messages formatted for reading
    """
    messages = _get_messages(room)

    filtered = [m for m in messages if m["id"] > since_id]

    if not filtered:
        return "\U0001f4ed Yeni mesaj yok."

    total_count = len(filtered)

    # Apply limit (get last N messages)
    if limit > 0 and len(filtered) > limit:
        filtered = filtered[-limit:]
        result = f"\U0001f4ec Son {limit} mesaj (toplam {total_count}):\n\n"
    else:
        result = f"\U0001f4ec {len(filtered)} mesaj (tümü):\n\n"

    for msg in filtered:
        timestamp = datetime.fromisoformat(msg["timestamp"]).strftime('%H:%M:%S')
        msg_type = msg.get("type", "direct")

        if msg_type == "system":
            result += f"[{timestamp}] SYSTEM: {msg['content']}\n"
        else:
            result += f"[{timestamp}] #{msg['id']} {msg['from']} \u2192 {msg['to']}: {msg['content'][:100]}\n"
        result += "\n"

    return result

@mcp.tool()
def get_last_message_id(agent_name: str = "", room: str = "") -> int:
    """
    Get the ID of the last message. Useful for polling new messages.

    Args:
        agent_name: Your agent name (optional, for updating last_seen)
        room: Room name (empty = default room)

    Returns:
        The ID of the last message, or 0 if no messages
    """
    if agent_name:
        agents = _get_agents(room)
        if agent_name in agents:
            agents[agent_name]["last_seen"] = time.time()
            _save_agents(agents, room)

    messages = _get_messages(room)
    return messages[-1]["id"] if messages else 0

@mcp.tool()
def list_rooms() -> str:
    """
    List all available chat rooms.

    Returns:
        List of rooms with agent counts
    """
    if not CHAT_DIR.exists():
        return "\U0001f4ad Henüz hiç oda yok."

    rooms = []
    for item in sorted(CHAT_DIR.iterdir()):
        if item.is_dir():
            agents = _read_json(item / "agents.json", {})
            agents = _cleanup_stale_agents(agents)
            messages = _read_json(item / "messages.json", [])
            rooms.append({
                "name": item.name,
                "agents": len(agents),
                "messages": len(messages)
            })

    if not rooms:
        return "\U0001f4ad Henüz hiç oda yok."

    result = f"\U0001f3e0 Mevcut odalar ({len(rooms)}):\n\n"
    for r in rooms:
        default_marker = " (varsayılan)" if r["name"] == DEFAULT_ROOM else ""
        result += f"  \u2022 {r['name']}{default_marker} - {r['agents']} agent, {r['messages']} mesaj\n"

    return result

if __name__ == "__main__":
    mcp.run()
