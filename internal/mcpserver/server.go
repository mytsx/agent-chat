package mcpserver

import (
	"log"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// MCPServerApp wraps storage and the MCP server.
type MCPServerApp struct {
	storage *Storage
	server  *server.MCPServer
	logger  *log.Logger
}

// NewMCPServerApp creates a new MCP server application.
func NewMCPServerApp(chatDir, defaultRoom string) *MCPServerApp {
	logger := setupLogger(chatDir)

	s := server.NewMCPServer(
		"agent-chat",
		"1.0.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
		server.WithLogging(),
	)

	app := &MCPServerApp{
		storage: NewStorage(chatDir, defaultRoom),
		server:  s,
		logger:  logger,
	}
	app.registerTools()

	logger.Printf("MCP server initialized — chatDir=%s defaultRoom=%s pid=%d", chatDir, defaultRoom, os.Getpid())
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

// setupLogger creates a file logger at chatDir/../mcp-server.log
// (e.g. ~/.agent-chat/mcp-server.log). Falls back to stderr if file open fails.
func setupLogger(chatDir string) *log.Logger {
	// chatDir is typically ~/.agent-chat/rooms — log file goes one level up
	logDir := filepath.Dir(chatDir)
	logPath := filepath.Join(logDir, "mcp-server.log")

	os.MkdirAll(logDir, 0700)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return log.New(os.Stderr, "[MCP] ", log.LstdFlags|log.Lshortfile)
	}

	return log.New(f, "[MCP] ", log.LstdFlags|log.Lshortfile)
}

func (app *MCPServerApp) registerTools() {
	h := newToolHandlers(app.storage, app.logger)

	// join_room
	app.server.AddTool(mcp.NewTool("join_room",
		mcp.WithDescription(`Join the chat room with a unique name.

Args:
    agent_name: Unique name for this agent (e.g., "backend", "frontend", "mobile")
    role: Optional role description (e.g., "Backend API Developer")
    room: Room name (empty = default room from AGENT_CHAT_ROOM env or "default")

Returns:
    Confirmation message with list of other agents in the room`),
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
	), h.joinRoom)

	// send_message
	app.server.AddTool(mcp.NewTool("send_message",
		mcp.WithDescription(`Send a message to other agents.

Args:
    from_agent: Your agent name
    content: Message content
    to_agent: Target agent name or "all" for broadcast (default: "all")
    expects_reply: Set False for acknowledgments/thanks to prevent infinite loops (default: True)
    priority: "urgent", "normal", or "low" (default: "normal")
    room: Room name (empty = default room)

Returns:
    Confirmation that message was sent`),
		mcp.WithString("from_agent",
			mcp.Required(),
			mcp.Description("Your agent name"),
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
		mcp.WithString("room",
			mcp.Description("Room name (empty = default room)"),
		),
	), h.sendMessage)

	// read_messages
	app.server.AddTool(mcp.NewTool("read_messages",
		mcp.WithDescription(`Read messages from the chat room.

Args:
    agent_name: Your agent name (to filter relevant messages)
    since_id: Only get messages after this ID (default: 0 for all)
    unread_only: If True, only show messages not from yourself (default: True)
    limit: Maximum number of messages to return (default: 10, 0 for unlimited)
    room: Room name (empty = default room)

Returns:
    List of messages formatted for reading`),
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
		mcp.WithString("room",
			mcp.Description("Room name (empty = default room)"),
		),
	), h.readMessages)

	// list_agents
	app.server.AddTool(mcp.NewTool("list_agents",
		mcp.WithDescription(`List all agents currently in the chat room.

Args:
    agent_name: Your agent name (optional, for updating last_seen)
    room: Room name (empty = default room)

Returns:
    List of active agents with their roles`),
		mcp.WithString("agent_name",
			mcp.Description("Your agent name (optional, for updating last_seen)"),
		),
		mcp.WithString("room",
			mcp.Description("Room name (empty = default room)"),
		),
	), h.listAgents)

	// leave_room
	app.server.AddTool(mcp.NewTool("leave_room",
		mcp.WithDescription(`Leave the chat room.

Args:
    agent_name: Your agent name
    room: Room name (empty = default room)

Returns:
    Confirmation message`),
		mcp.WithString("agent_name",
			mcp.Required(),
			mcp.Description("Your agent name"),
		),
		mcp.WithString("room",
			mcp.Description("Room name (empty = default room)"),
		),
	), h.leaveRoom)

	// clear_room
	app.server.AddTool(mcp.NewTool("clear_room",
		mcp.WithDescription(`Clear all messages and agents from the room. Use with caution!

Args:
    room: Room name (empty = default room)

Returns:
    Confirmation message`),
		mcp.WithString("room",
			mcp.Description("Room name (empty = default room)"),
		),
	), h.clearRoom)

	// read_all_messages
	app.server.AddTool(mcp.NewTool("read_all_messages",
		mcp.WithDescription(`Read ALL messages in the chat room (for manager/admin use).

Args:
    since_id: Only get messages after this ID (default: 0 for all)
    limit: Maximum number of messages to return (default: 15, 0 for unlimited)
    room: Room name (empty = default room)

Returns:
    List of all messages formatted for reading`),
		mcp.WithNumber("since_id",
			mcp.Description("Only get messages after this ID (default: 0 for all)"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of messages to return (default: 15, 0 for unlimited)"),
		),
		mcp.WithString("room",
			mcp.Description("Room name (empty = default room)"),
		),
	), h.readAllMessages)

	// get_last_message_id
	app.server.AddTool(mcp.NewTool("get_last_message_id",
		mcp.WithDescription(`Get the ID of the last message. Useful for polling new messages.

Args:
    agent_name: Your agent name (optional, for updating last_seen)
    room: Room name (empty = default room)

Returns:
    The ID of the last message, or 0 if no messages`),
		mcp.WithString("agent_name",
			mcp.Description("Your agent name (optional, for updating last_seen)"),
		),
		mcp.WithString("room",
			mcp.Description("Room name (empty = default room)"),
		),
	), h.getLastMessageID)

	// list_rooms
	app.server.AddTool(mcp.NewTool("list_rooms",
		mcp.WithDescription(`List all available chat rooms.

Returns:
    List of rooms with agent counts`),
	), h.listRooms)
}
