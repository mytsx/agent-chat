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
CHAT_DIR = Path("/tmp/agent-chat-room")
MESSAGES_FILE = CHAT_DIR / "messages.json"
AGENTS_FILE = CHAT_DIR / "agents.json"

# Ensure directory exists
CHAT_DIR.mkdir(parents=True, exist_ok=True)

mcp = FastMCP("agent-chat")

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

def _get_agents() -> dict:
    """Get active agents."""
    return _read_json(AGENTS_FILE, {})

def _get_messages() -> list:
    """Get all messages."""
    return _read_json(MESSAGES_FILE, [])

def _cleanup_stale_agents(agents: dict, timeout: int = 300) -> dict:
    """Remove agents that haven't been active for `timeout` seconds."""
    now = time.time()
    return {name: info for name, info in agents.items()
            if now - info.get("last_seen", 0) < timeout}

@mcp.tool()
def join_room(agent_name: str, role: str = "") -> str:
    """
    Join the chat room with a unique name.

    Args:
        agent_name: Unique name for this agent (e.g., "backend", "frontend", "mobile")
        role: Optional role description (e.g., "Backend API Developer")

    Returns:
        Confirmation message with list of other agents in the room
    """
    agents = _get_agents()
    agents = _cleanup_stale_agents(agents)

    agents[agent_name] = {
        "role": role,
        "joined_at": datetime.now().isoformat(),
        "last_seen": time.time()
    }
    _write_json(AGENTS_FILE, agents)

    other_agents = [name for name in agents.keys() if name != agent_name]

    # Add system message about joining
    messages = _get_messages()
    messages.append({
        "id": len(messages) + 1,
        "from": "SYSTEM",
        "to": "all",
        "content": f"🟢 {agent_name} odaya katıldı" + (f" (Rol: {role})" if role else ""),
        "timestamp": datetime.now().isoformat(),
        "type": "system"
    })
    _write_json(MESSAGES_FILE, messages)

    if other_agents:
        return f"✅ '{agent_name}' olarak odaya katıldın. Odadaki diğer agent'lar: {', '.join(other_agents)}"
    return f"✅ '{agent_name}' olarak odaya katıldın. Şu an odada başka agent yok."

@mcp.tool()
def send_message(
    from_agent: str,
    content: str,
    to_agent: str = "all",
    expects_reply: bool = True,
    priority: str = "normal"
) -> str:
    """
    Send a message to other agents.

    Args:
        from_agent: Your agent name
        content: Message content
        to_agent: Target agent name or "all" for broadcast (default: "all")
        expects_reply: Set False for acknowledgments/thanks to prevent infinite loops (default: True)
        priority: "urgent", "normal", or "low" (default: "normal")

    Returns:
        Confirmation that message was sent
    """
    # Update last_seen
    agents = _get_agents()
    if from_agent in agents:
        agents[from_agent]["last_seen"] = time.time()
        _write_json(AGENTS_FILE, agents)

    messages = _get_messages()
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
    _write_json(MESSAGES_FILE, messages)

    if to_agent == "all":
        return f"📤 Mesaj tüm agent'lara gönderildi (ID: {message['id']})"
    return f"📤 Mesaj '{to_agent}' agent'ına gönderildi (ID: {message['id']})"

@mcp.tool()
def read_messages(agent_name: str, since_id: int = 0, unread_only: bool = True, limit: int = 10) -> str:
    """
    Read messages from the chat room.

    Args:
        agent_name: Your agent name (to filter relevant messages)
        since_id: Only get messages after this ID (default: 0 for all)
        unread_only: If True, only show messages not from yourself (default: True)
        limit: Maximum number of messages to return (default: 10, 0 for unlimited)

    Returns:
        List of messages formatted for reading
    """
    # Update last_seen
    agents = _get_agents()
    if agent_name in agents:
        agents[agent_name]["last_seen"] = time.time()
        _write_json(AGENTS_FILE, agents)

    messages = _get_messages()

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
        return "📭 Yeni mesaj yok."

    total_count = len(filtered)

    # Apply limit (get last N messages)
    if limit > 0 and len(filtered) > limit:
        filtered = filtered[-limit:]
        result = f"📬 Son {limit} mesaj (toplam {total_count}):\n\n"
    else:
        result = f"📬 {len(filtered)} mesaj:\n\n"

    for msg in filtered:
        timestamp = msg["timestamp"].split("T")[1].split(".")[0]  # HH:MM:SS
        if msg["type"] == "system":
            result += f"[{timestamp}] {msg['content']}\n"
        elif msg["to"] == "all":
            result += f"[{timestamp}] {msg['from']} → HERKESE: {msg['content']}\n"
        else:
            result += f"[{timestamp}] {msg['from']} → {msg['to']}: {msg['content']}\n"
        result += f"  (ID: {msg['id']})\n\n"

    return result

@mcp.tool()
def list_agents(agent_name: str = "") -> str:
    """
    List all agents currently in the chat room.

    Args:
        agent_name: Your agent name (optional, for updating last_seen)

    Returns:
        List of active agents with their roles
    """
    agents = _get_agents()
    agents = _cleanup_stale_agents(agents)
    _write_json(AGENTS_FILE, agents)

    if agent_name and agent_name in agents:
        agents[agent_name]["last_seen"] = time.time()
        _write_json(AGENTS_FILE, agents)

    if not agents:
        return "👥 Odada kimse yok."

    result = f"👥 Odadaki agent'lar ({len(agents)}):\n\n"
    for name, info in agents.items():
        role = info.get("role", "")
        joined = info.get("joined_at", "").split("T")[0]
        marker = " (sen)" if name == agent_name else ""
        result += f"  • {name}{marker}"
        if role:
            result += f" - {role}"
        result += f"\n    Katılım: {joined}\n"

    return result

@mcp.tool()
def leave_room(agent_name: str) -> str:
    """
    Leave the chat room.

    Args:
        agent_name: Your agent name

    Returns:
        Confirmation message
    """
    agents = _get_agents()

    if agent_name not in agents:
        return f"⚠️ '{agent_name}' zaten odada değil."

    del agents[agent_name]
    _write_json(AGENTS_FILE, agents)

    # Add system message about leaving
    messages = _get_messages()
    messages.append({
        "id": len(messages) + 1,
        "from": "SYSTEM",
        "to": "all",
        "content": f"🔴 {agent_name} odadan ayrıldı",
        "timestamp": datetime.now().isoformat(),
        "type": "system"
    })
    _write_json(MESSAGES_FILE, messages)

    return f"👋 '{agent_name}' odadan ayrıldı."

@mcp.tool()
def clear_room() -> str:
    """
    Clear all messages and agents from the room. Use with caution!

    Returns:
        Confirmation message
    """
    _write_json(MESSAGES_FILE, [])
    _write_json(AGENTS_FILE, {})
    return "🧹 Oda temizlendi. Tüm mesajlar ve agent kayıtları silindi."

@mcp.tool()
def read_all_messages(since_id: int = 0, limit: int = 15) -> str:
    """
    Read ALL messages in the chat room (for manager/admin use).

    Args:
        since_id: Only get messages after this ID (default: 0 for all)
        limit: Maximum number of messages to return (default: 15, 0 for unlimited)

    Returns:
        List of all messages formatted for reading
    """
    messages = _get_messages()

    filtered = [m for m in messages if m["id"] > since_id]

    if not filtered:
        return "📭 Yeni mesaj yok."

    total_count = len(filtered)

    # Apply limit (get last N messages)
    if limit > 0 and len(filtered) > limit:
        filtered = filtered[-limit:]
        result = f"📬 Son {limit} mesaj (toplam {total_count}):\n\n"
    else:
        result = f"📬 {len(filtered)} mesaj (tümü):\n\n"

    for msg in filtered:
        timestamp = msg["timestamp"].split("T")[1].split(".")[0]
        msg_type = msg.get("type", "direct")

        if msg_type == "system":
            result += f"[{timestamp}] SYSTEM: {msg['content']}\n"
        else:
            result += f"[{timestamp}] #{msg['id']} {msg['from']} → {msg['to']}: {msg['content'][:100]}\n"
        result += "\n"

    return result

@mcp.tool()
def get_last_message_id(agent_name: str = "") -> int:
    """
    Get the ID of the last message. Useful for polling new messages.

    Args:
        agent_name: Your agent name (optional, for updating last_seen)

    Returns:
        The ID of the last message, or 0 if no messages
    """
    if agent_name:
        agents = _get_agents()
        if agent_name in agents:
            agents[agent_name]["last_seen"] = time.time()
            _write_json(AGENTS_FILE, agents)

    messages = _get_messages()
    return messages[-1]["id"] if messages else 0

if __name__ == "__main__":
    mcp.run()
