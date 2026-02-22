package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"desktop/internal/validation"

	"github.com/mark3labs/mcp-go/mcp"
)

const maxFieldLength = 32000

// toolHandlers holds all MCP tool handler functions.
type toolHandlers struct {
	storage *Storage
	logger  *log.Logger
}

func newToolHandlers(storage *Storage, logger *log.Logger) *toolHandlers {
	return &toolHandlers{storage: storage, logger: logger}
}

// extractText extracts the "text" field from a response.
func extractText(data json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return string(data)
	}
	if text, ok := m["text"].(string); ok {
		return text
	}
	return string(data)
}

func (h *toolHandlers) joinRoom(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentName, err := request.RequireString("agent_name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	role := request.GetString("role", "")
	room := request.GetString("room", "")

	if err := validation.ValidateName(agentName); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := validation.ValidateName(room); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if len(role) > maxFieldLength {
		return mcp.NewToolResultError(fmt.Sprintf("role too long: %d chars, max %d", len(role), maxFieldLength)), nil
	}

	h.logger.Printf("join_room: agent=%q role=%q room=%q", agentName, role, room)

	resp, err := h.storage.JoinRoom(room, agentName, role)
	if err != nil {
		h.logger.Printf("join_room: hub error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !resp.Success {
		return mcp.NewToolResultError(resp.Error), nil
	}

	return mcp.NewToolResultText(extractText(resp.Data)), nil
}

func (h *toolHandlers) sendMessage(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	fromAgent, err := request.RequireString("from_agent")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	content, err := request.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	toAgent := request.GetString("to_agent", "all")
	expectsReply := request.GetBool("expects_reply", true)
	priority := request.GetString("priority", "normal")
	room := request.GetString("room", "")

	if err := validation.ValidateName(fromAgent); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if toAgent != "all" {
		if err := validation.ValidateName(toAgent); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}
	if err := validation.ValidateName(room); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if len(content) > maxFieldLength {
		return mcp.NewToolResultError(fmt.Sprintf("content too long: %d chars, max %d", len(content), maxFieldLength)), nil
	}

	h.logger.Printf("send_message: from=%q to=%q room=%q priority=%s expects_reply=%v contentLen=%d",
		fromAgent, toAgent, room, priority, expectsReply, len(content))

	resp, err := h.storage.SendMessage(room, fromAgent, toAgent, content, expectsReply, priority)
	if err != nil {
		h.logger.Printf("send_message: hub error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !resp.Success {
		return mcp.NewToolResultError(resp.Error), nil
	}

	return mcp.NewToolResultText(extractText(resp.Data)), nil
}

func (h *toolHandlers) readMessages(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentName, err := request.RequireString("agent_name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	sinceID := request.GetInt("since_id", 0)
	unreadOnly := request.GetBool("unread_only", true)
	limit := request.GetInt("limit", 10)
	room := request.GetString("room", "")

	if err := validation.ValidateName(agentName); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := validation.ValidateName(room); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	h.logger.Printf("read_messages: agent=%q since_id=%d unread_only=%v limit=%d room=%q",
		agentName, sinceID, unreadOnly, limit, room)

	resp, err := h.storage.GetMessages(room, agentName, sinceID, limit, unreadOnly)
	if err != nil {
		h.logger.Printf("read_messages: hub error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !resp.Success {
		return mcp.NewToolResultError(resp.Error), nil
	}

	return mcp.NewToolResultText(extractText(resp.Data)), nil
}

func (h *toolHandlers) listAgents(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentName := request.GetString("agent_name", "")
	room := request.GetString("room", "")

	if err := validation.ValidateName(agentName); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := validation.ValidateName(room); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	h.logger.Printf("list_agents: agent=%q room=%q", agentName, room)

	resp, err := h.storage.ListAgents(room, agentName)
	if err != nil {
		h.logger.Printf("list_agents: hub error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !resp.Success {
		return mcp.NewToolResultError(resp.Error), nil
	}

	return mcp.NewToolResultText(extractText(resp.Data)), nil
}

func (h *toolHandlers) leaveRoom(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentName, err := request.RequireString("agent_name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	room := request.GetString("room", "")

	if err := validation.ValidateName(agentName); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := validation.ValidateName(room); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	h.logger.Printf("leave_room: agent=%q room=%q", agentName, room)

	resp, err := h.storage.LeaveRoom(room, agentName)
	if err != nil {
		h.logger.Printf("leave_room: hub error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !resp.Success {
		return mcp.NewToolResultError(resp.Error), nil
	}

	return mcp.NewToolResultText(extractText(resp.Data)), nil
}

func (h *toolHandlers) clearRoom(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	room := request.GetString("room", "")

	if err := validation.ValidateName(room); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	h.logger.Printf("clear_room: room=%q", room)

	resp, err := h.storage.ClearRoom(room)
	if err != nil {
		h.logger.Printf("clear_room: hub error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !resp.Success {
		return mcp.NewToolResultError(resp.Error), nil
	}

	return mcp.NewToolResultText(extractText(resp.Data)), nil
}

func (h *toolHandlers) readAllMessages(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sinceID := request.GetInt("since_id", 0)
	limit := request.GetInt("limit", 15)
	room := request.GetString("room", "")

	if err := validation.ValidateName(room); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	h.logger.Printf("read_all_messages: since_id=%d limit=%d room=%q", sinceID, limit, room)

	resp, err := h.storage.GetAllMessages(room, sinceID, limit)
	if err != nil {
		h.logger.Printf("read_all_messages: hub error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !resp.Success {
		return mcp.NewToolResultError(resp.Error), nil
	}

	return mcp.NewToolResultText(extractText(resp.Data)), nil
}

func (h *toolHandlers) getLastMessageID(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentName := request.GetString("agent_name", "")
	room := request.GetString("room", "")

	if err := validation.ValidateName(agentName); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := validation.ValidateName(room); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	resp, err := h.storage.GetLastMessageID(room, agentName)
	if err != nil {
		h.logger.Printf("get_last_message_id: hub error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !resp.Success {
		return mcp.NewToolResultError(resp.Error), nil
	}

	// Extract last_id from response data
	var data map[string]any
	json.Unmarshal(resp.Data, &data)
	lastID := 0
	if id, ok := data["last_id"].(float64); ok {
		lastID = int(id)
	}

	h.logger.Printf("get_last_message_id: room=%q lastID=%d", room, lastID)

	return mcp.NewToolResultText(fmt.Sprintf("%d", lastID)), nil
}

func (h *toolHandlers) listRooms(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Printf("list_rooms")

	resp, err := h.storage.ListRooms()
	if err != nil {
		h.logger.Printf("list_rooms: hub error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !resp.Success {
		return mcp.NewToolResultError(resp.Error), nil
	}

	return mcp.NewToolResultText(extractText(resp.Data)), nil
}
