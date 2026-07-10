package mcpserver

import (
	"log"
	"os"

	"desktop/internal/hubclient"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// MCPServerApp wraps storage and the MCP server.
type MCPServerApp struct {
	storage *Storage
	server  *server.MCPServer
	logger  *log.Logger
}

const (
	roomArgDescription           = "Room name (empty = default room)"
	agentNameDescription         = "Your agent name"
	optionalAgentNameDescription = "Your agent name (optional, for updating last_seen)"
)

func roomArg() mcp.ToolOption {
	return mcp.WithString("room",
		mcp.Description(roomArgDescription),
	)
}

func requiredAgentNameArg() mcp.ToolOption {
	return mcp.WithString("agent_name",
		mcp.Required(),
		mcp.Description(agentNameDescription),
	)
}

func optionalAgentNameArg() mcp.ToolOption {
	return mcp.WithString("agent_name",
		mcp.Description(optionalAgentNameDescription),
	)
}

func nonDestructiveToolOptions(description string, opts ...mcp.ToolOption) []mcp.ToolOption {
	return annotatedToolOptions(description, false, opts...)
}

func readOnlyToolOptions(description string, opts ...mcp.ToolOption) []mcp.ToolOption {
	return annotatedToolOptions(description, true, opts...)
}

func annotatedToolOptions(description string, readOnly bool, opts ...mcp.ToolOption) []mcp.ToolOption {
	options := []mcp.ToolOption{mcp.WithDescription(description)}
	if readOnly {
		options = append(options, mcp.WithReadOnlyHintAnnotation(true))
	}
	options = append(options, mcp.WithDestructiveHintAnnotation(false))
	return append(options, opts...)
}

// NewMCPServerApp creates a new MCP server application backed by a hub client.
func NewMCPServerApp(client *hubclient.HubClient, defaultRoom string, logger *log.Logger) *MCPServerApp {
	s := server.NewMCPServer(
		"agent-chat",
		"1.0.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
		server.WithLogging(),
	)

	app := &MCPServerApp{
		storage: NewStorage(client, defaultRoom),
		server:  s,
		logger:  logger,
	}
	app.registerTools()

	logger.Printf("MCP server initialized — defaultRoom=%s pid=%d", defaultRoom, os.Getpid())
	return app
}

// Serve starts the MCP server on stdio.
func (app *MCPServerApp) Serve() error {
	app.logger.Println("Serving on stdio...")
	err := server.ServeStdio(app.server)
	if err != nil {
		app.logger.Printf("Server exited with error: %v", err)
	} else {
		app.logger.Println("Server exited cleanly")
	}
	return err
}

func (app *MCPServerApp) registerTools() {
	h := newToolHandlers(app.storage, app.logger)

	// join_room
	app.server.AddTool(mcp.NewTool("join_room", nonDestructiveToolOptions(`Join the chat room with a unique name.

Args:
    agent_name: Unique name for this agent (e.g., "backend", "frontend", "mobile")
    role: Optional role description (e.g., "Backend API Developer")
    room: Room name (empty = default room from AGENT_CHAT_ROOM env or "default")

Returns:
    Confirmation message with list of other agents in the room

Notes:
    - Agent names must be unique per room; duplicate names are rejected
    - role="manager" claims manager lock for the room (only one active manager)`,
		mcp.WithString("agent_name",
			mcp.Required(),
			mcp.Description("Unique name for this agent (e.g., \"backend\", \"frontend\", \"mobile\")"),
		),
		mcp.WithString("role",
			mcp.Description("Optional role description (e.g., \"Backend API Developer\")"),
		),
		mcp.WithString("room",
			mcp.Description("Room name (empty = default room from AGENT_CHAT_ROOM env or \"default\")"),
		),
	)...), h.joinRoom)

	// send_message
	app.server.AddTool(mcp.NewTool("send_message", nonDestructiveToolOptions(`Send a message to other agents.

Args:
    from_agent: Your agent name
    content: Message content
    to_agent: Target agent name or "all" for broadcast (default: "all")
    expects_reply: Set False for acknowledgments/thanks to prevent infinite loops (default: True)
    priority: "urgent", "normal", or "low" (default: "normal")
    room: Room name (empty = default room)

Returns:
    Confirmation that message was sent

Notes:
    - from_agent must match the name you joined with via join_room
    - If a manager is active in the room, non-manager messages are first routed to manager`,
		mcp.WithString("from_agent",
			mcp.Required(),
			mcp.Description(agentNameDescription),
		),
		mcp.WithString("content",
			mcp.Required(),
			mcp.Description("Message content"),
		),
		mcp.WithString("to_agent",
			mcp.Description("Target agent name or \"all\" for broadcast (default: \"all\")"),
		),
		mcp.WithBoolean("expects_reply",
			mcp.Description("Set False for acknowledgments/thanks to prevent infinite loops (default: True)"),
		),
		mcp.WithString("priority",
			mcp.Description("\"urgent\", \"normal\", or \"low\" (default: \"normal\")"),
		),
		roomArg(),
	)...), h.sendMessage)

	// read_messages
	app.server.AddTool(mcp.NewTool("read_messages", readOnlyToolOptions(`Read messages from the chat room.

Args:
    agent_name: Your agent name (to filter relevant messages)
    since_id: Only get messages after this ID (default: 0 for all)
    unread_only: If True, only show messages not from yourself (default: True)
    limit: Maximum number of messages to return (default: 10, 0 for unlimited)
    room: Room name (empty = default room)

Returns:
    List of messages formatted for reading`,
		mcp.WithString("agent_name",
			mcp.Required(),
			mcp.Description("Your agent name (to filter relevant messages)"),
		),
		mcp.WithNumber("since_id",
			mcp.Description("Only get messages after this ID (default: 0 for all)"),
		),
		mcp.WithBoolean("unread_only",
			mcp.Description("If True, only show messages not from yourself (default: True)"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of messages to return (default: 10, 0 for unlimited)"),
		),
		roomArg(),
	)...), h.readMessages)

	// list_agents
	app.server.AddTool(mcp.NewTool("list_agents", readOnlyToolOptions(`List all agents currently in the chat room.

Args:
    agent_name: Your agent name (optional, for updating last_seen)
    room: Room name (empty = default room)

Returns:
    List of active agents with their roles`,
		optionalAgentNameArg(),
		roomArg(),
	)...), h.listAgents)

	// leave_room
	app.server.AddTool(mcp.NewTool("leave_room", nonDestructiveToolOptions(`Leave the chat room.

Args:
    agent_name: Your agent name
    room: Room name (empty = default room)

Returns:
    Confirmation message`,
		requiredAgentNameArg(),
		roomArg(),
	)...), h.leaveRoom)

	// clear_room
	app.server.AddTool(mcp.NewTool("clear_room",
		mcp.WithDescription(`Clear all messages and agents from the room. Use with caution!

Args:
    room: Room name (empty = default room)

Returns:
    Confirmation message`),
		roomArg(),
	), h.clearRoom)

	// read_all_messages
	app.server.AddTool(mcp.NewTool("read_all_messages", readOnlyToolOptions(`Read ALL messages in the chat room (for manager/admin use).

Args:
    since_id: Only get messages after this ID (default: 0 for all)
    limit: Maximum number of messages to return (default: 15, 0 for unlimited)
    room: Room name (empty = default room)

Returns:
    List of all messages formatted for reading`,
		mcp.WithNumber("since_id",
			mcp.Description("Only get messages after this ID (default: 0 for all)"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of messages to return (default: 15, 0 for unlimited)"),
		),
		roomArg(),
	)...), h.readAllMessages)

	// read_summary
	app.server.AddTool(mcp.NewTool("read_summary", readOnlyToolOptions(`Read the previous session's summary for this room, if one was saved.

Prefer this over read_all_messages on join: it gives the prior-session context
(goals, decisions, open items) in a few tokens instead of pulling the whole
history. Read recent messages only for detail beyond the summary.

Args:
    room: Room name (empty = default room)

Returns:
    The latest saved summary, or a notice if none exists yet`,
		roomArg(),
	)...), h.readSummary)

	// get_last_message_id
	app.server.AddTool(mcp.NewTool("get_last_message_id", readOnlyToolOptions(`Get the ID of the last message. Useful for polling new messages.

Args:
    agent_name: Your agent name (optional, for updating last_seen)
    room: Room name (empty = default room)

Returns:
    The ID of the last message, or 0 if no messages`,
		optionalAgentNameArg(),
		roomArg(),
	)...), h.getLastMessageID)

	// list_rooms
	app.server.AddTool(mcp.NewTool("list_rooms", readOnlyToolOptions(`List all available chat rooms.

Returns:
    List of rooms with agent counts`)...), h.listRooms)
}
