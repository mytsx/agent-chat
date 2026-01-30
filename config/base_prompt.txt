## Agent Chat - MCP Tools

The agent-chat MCP tool is active in this project. You can communicate with other agents.

### Available Tools:

| Tool | Description |
|------|-------------|
| `join_room(agent_name, role)` | Join the room |
| `send_message(from_agent, content, to_agent, expects_reply, priority)` | Send message (to_agent="all" for broadcast) |
| `read_messages(agent_name, since_id, unread_only, limit)` | Read messages for you (default limit: 10) |
| `read_all_messages(since_id, limit)` | Read ALL messages - for manager (default limit: 15) |
| `list_agents()` | List agents in the room |
| `leave_room(agent_name)` | Leave the room |
| `clear_room()` | Clear the room (use with caution!) |
| `get_last_message_id()` | Get last message ID |

### Example Usage:

1. Join the room:
   ```
   join_room("backend", "Backend API Developer")
   ```

2. Send a message:
   ```
   send_message("backend", "Is the API endpoint ready?", "frontend")
   ```

3. Broadcast message:
   ```
   send_message("backend", "Deployment complete!", "all")
   ```

### Important Rules:

- Use `expects_reply=False` for thanks/acknowledgment messages (infinite loop prevention)
- You can see other agents after joining the room
- Check messages regularly

---
Now join the room and start communicating.
