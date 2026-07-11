package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"desktop/internal/types"
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

func extractLastMessageID(data json.RawMessage) int {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return 0
	}
	id, ok := m["last_id"].(float64)
	if !ok {
		return 0
	}
	return int(id)
}

func toolResultError(err error) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(err.Error()), nil
}

func toolResultErrorf(format string, args ...any) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(fmt.Sprintf(format, args...)), nil
}

func invalidNameResult(name string) *mcp.CallToolResult {
	if err := validation.ValidateName(name); err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	return nil
}

func invalidNamesResult(names ...string) *mcp.CallToolResult {
	for _, name := range names {
		if result := invalidNameResult(name); result != nil {
			return result
		}
	}
	return nil
}

func (h *toolHandlers) responseFromHub(tool string, call func() (*types.Response, error)) (*types.Response, *mcp.CallToolResult, error) {
	resp, err := call()
	if err != nil {
		h.logger.Printf("%s: hub error: %v", tool, err)
		result, resultErr := toolResultError(err)
		return nil, result, resultErr
	}
	if !resp.Success {
		return nil, mcp.NewToolResultError(resp.Error), nil
	}
	return resp, nil, nil
}

func (h *toolHandlers) resultFromHub(tool string, call func() (*types.Response, error)) (*mcp.CallToolResult, error) {
	resp, result, err := h.responseFromHub(tool, call)
	if result != nil || err != nil {
		return result, err
	}
	return mcp.NewToolResultText(extractText(resp.Data)), nil
}

func (h *toolHandlers) joinRoom(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentName, err := request.RequireString("agent_name")
	if err != nil {
		return toolResultError(err)
	}
	role := request.GetString("role", "")
	room := request.GetString("room", "")

	if result := invalidNamesResult(agentName, room); result != nil {
		return result, nil
	}
	if len(role) > maxFieldLength {
		return toolResultErrorf("role too long: %d chars, max %d", len(role), maxFieldLength)
	}

	h.logger.Printf("join_room: agent=%q role=%q room=%q", agentName, role, room)

	return h.resultFromHub("join_room", func() (*types.Response, error) {
		return h.storage.JoinRoom(room, agentName, role)
	})
}

func (h *toolHandlers) sendMessage(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	fromAgent, err := request.RequireString("from_agent")
	if err != nil {
		return toolResultError(err)
	}
	content, err := request.RequireString("content")
	if err != nil {
		return toolResultError(err)
	}
	toAgent := request.GetString("to_agent", "all")
	expectsReply := request.GetBool("expects_reply", true)
	priority := request.GetString("priority", "normal")
	room := request.GetString("room", "")

	if result := invalidNameResult(fromAgent); result != nil {
		return result, nil
	}
	if toAgent != "all" {
		if result := invalidNameResult(toAgent); result != nil {
			return result, nil
		}
	}
	if result := invalidNameResult(room); result != nil {
		return result, nil
	}
	if len(content) > maxFieldLength {
		return toolResultErrorf("content too long: %d chars, max %d", len(content), maxFieldLength)
	}

	h.logger.Printf("send_message: from=%q to=%q room=%q priority=%s expects_reply=%v contentLen=%d",
		fromAgent, toAgent, room, priority, expectsReply, len(content))

	return h.resultFromHub("send_message", func() (*types.Response, error) {
		return h.storage.SendMessage(room, fromAgent, toAgent, content, expectsReply, priority)
	})
}

func (h *toolHandlers) readMessages(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentName, err := request.RequireString("agent_name")
	if err != nil {
		return toolResultError(err)
	}
	sinceID := request.GetInt("since_id", 0)
	unreadOnly := request.GetBool("unread_only", true)
	limit := request.GetInt("limit", 10)
	room := request.GetString("room", "")

	if result := invalidNamesResult(agentName, room); result != nil {
		return result, nil
	}

	h.logger.Printf("read_messages: agent=%q since_id=%d unread_only=%v limit=%d room=%q",
		agentName, sinceID, unreadOnly, limit, room)

	return h.resultFromHub("read_messages", func() (*types.Response, error) {
		return h.storage.GetMessages(room, agentName, sinceID, limit, unreadOnly)
	})
}

func (h *toolHandlers) listAgents(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentName := request.GetString("agent_name", "")
	room := request.GetString("room", "")

	if result := invalidNamesResult(agentName, room); result != nil {
		return result, nil
	}

	h.logger.Printf("list_agents: agent=%q room=%q", agentName, room)

	return h.resultFromHub("list_agents", func() (*types.Response, error) {
		return h.storage.ListAgents(room, agentName)
	})
}

func (h *toolHandlers) leaveRoom(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentName, err := request.RequireString("agent_name")
	if err != nil {
		return toolResultError(err)
	}
	room := request.GetString("room", "")

	if result := invalidNamesResult(agentName, room); result != nil {
		return result, nil
	}

	h.logger.Printf("leave_room: agent=%q room=%q", agentName, room)

	return h.resultFromHub("leave_room", func() (*types.Response, error) {
		return h.storage.LeaveRoom(room, agentName)
	})
}

func (h *toolHandlers) clearRoom(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	room := request.GetString("room", "")

	if result := invalidNameResult(room); result != nil {
		return result, nil
	}

	h.logger.Printf("clear_room: room=%q", room)

	return h.resultFromHub("clear_room", func() (*types.Response, error) {
		return h.storage.ClearRoom(room)
	})
}

func (h *toolHandlers) readAllMessages(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sinceID := request.GetInt("since_id", 0)
	limit := request.GetInt("limit", 15)
	room := request.GetString("room", "")

	if result := invalidNameResult(room); result != nil {
		return result, nil
	}

	h.logger.Printf("read_all_messages: since_id=%d limit=%d room=%q", sinceID, limit, room)

	return h.resultFromHub("read_all_messages", func() (*types.Response, error) {
		return h.storage.GetAllMessages(room, sinceID, limit)
	})
}

func (h *toolHandlers) readSummary(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	room := request.GetString("room", "")

	if result := invalidNameResult(room); result != nil {
		return result, nil
	}

	h.logger.Printf("read_summary: room=%q", room)

	return h.resultFromHub("read_summary", func() (*types.Response, error) {
		return h.storage.GetSummary(room)
	})
}

func (h *toolHandlers) getLastMessageID(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	agentName := request.GetString("agent_name", "")
	room := request.GetString("room", "")

	if result := invalidNamesResult(agentName, room); result != nil {
		return result, nil
	}

	resp, result, err := h.responseFromHub("get_last_message_id", func() (*types.Response, error) {
		return h.storage.GetLastMessageID(room, agentName)
	})
	if result != nil || err != nil {
		return result, err
	}

	lastID := extractLastMessageID(resp.Data)

	h.logger.Printf("get_last_message_id: room=%q lastID=%d", room, lastID)

	return mcp.NewToolResultText(fmt.Sprintf("%d", lastID)), nil
}

func (h *toolHandlers) listRooms(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Printf("list_rooms")

	return h.resultFromHub("list_rooms", h.storage.ListRooms)
}
