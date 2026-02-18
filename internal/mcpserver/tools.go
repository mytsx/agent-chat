package mcpserver

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"desktop/internal/validation"

	"github.com/mark3labs/mcp-go/mcp"
)

// toolHandlers holds all MCP tool handler functions.
type toolHandlers struct {
	storage *Storage
	logger  *log.Logger
}

func newToolHandlers(storage *Storage, logger *log.Logger) *toolHandlers {
	return &toolHandlers{storage: storage, logger: logger}
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

	h.logger.Printf("join_room: agent=%q role=%q room=%q", agentName, role, room)

	agents, err := h.storage.GetAgents(room)
	if err != nil {
		h.logger.Printf("join_room: GetAgents error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}
	agents = h.storage.CleanupStaleAgents(agents, 300)

	agents[agentName] = Agent{
		Role:     role,
		JoinedAt: Timestamp(),
		LastSeen: Now(),
	}
	if err := h.storage.SaveAgents(agents, room); err != nil {
		h.logger.Printf("join_room: SaveAgents error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}

	var otherAgents []string
	for name := range agents {
		if name != agentName {
			otherAgents = append(otherAgents, name)
		}
	}

	// Add system message
	messages, _ := h.storage.GetMessages(room)
	content := fmt.Sprintf("\U0001f7e2 %s odaya katıldı", agentName)
	if role != "" {
		content += fmt.Sprintf(" (Rol: %s)", role)
	}
	messages = append(messages, Message{
		ID:        len(messages) + 1,
		From:      "SYSTEM",
		To:        "all",
		Content:   content,
		Timestamp: Timestamp(),
		Type:      "system",
	})
	h.storage.SaveMessages(messages, room)

	roomLabel := room
	if roomLabel == "" {
		roomLabel = h.storage.defaultRoom
	}

	h.logger.Printf("join_room: agent=%q joined room=%q, others=%v, roomDir=%s",
		agentName, roomLabel, otherAgents, h.storage.getRoomDir(room))

	if len(otherAgents) > 0 {
		return mcp.NewToolResultText(fmt.Sprintf("\u2705 '%s' olarak '%s' odasına katıldın. Odadaki diğer agent'lar: %s", agentName, roomLabel, strings.Join(otherAgents, ", "))), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("\u2705 '%s' olarak '%s' odasına katıldın. Şu an odada başka agent yok.", agentName, roomLabel)), nil
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

	h.logger.Printf("send_message: from=%q to=%q room=%q priority=%s expects_reply=%v contentLen=%d",
		fromAgent, toAgent, room, priority, expectsReply, len(content))

	// Update last_seen
	agents, _ := h.storage.GetAgents(room)
	if agent, ok := agents[fromAgent]; ok {
		agent.LastSeen = Now()
		agents[fromAgent] = agent
		h.storage.SaveAgents(agents, room)
	}

	messages, _ := h.storage.GetMessages(room)
	msgType := "broadcast"
	if toAgent != "all" {
		msgType = "direct"
	}
	msg := Message{
		ID:           len(messages) + 1,
		From:         fromAgent,
		To:           toAgent,
		Content:      content,
		Timestamp:    Timestamp(),
		Type:         msgType,
		ExpectsReply: expectsReply,
		Priority:     priority,
	}
	messages = append(messages, msg)

	// Truncate if messages exceed 500, keep last 300
	if len(messages) > 500 {
		originalLen := len(messages)
		messages = messages[len(messages)-300:]
		h.logger.Printf("send_message: truncated messages from %d to %d", originalLen, len(messages))
	}

	if err := h.storage.SaveMessages(messages, room); err != nil {
		h.logger.Printf("send_message: SaveMessages error: %v", err)
		return mcp.NewToolResultError(err.Error()), nil
	}

	h.logger.Printf("send_message: id=%d saved to %s", msg.ID, h.storage.getRoomDir(room))

	if toAgent == "all" {
		return mcp.NewToolResultText(fmt.Sprintf("\U0001f4e4 Mesaj tüm agent'lara gönderildi (ID: %d)", msg.ID)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("\U0001f4e4 Mesaj '%s' agent'ına gönderildi (ID: %d)", toAgent, msg.ID)), nil
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

	// Update last_seen
	agents, _ := h.storage.GetAgents(room)
	if agent, ok := agents[agentName]; ok {
		agent.LastSeen = Now()
		agents[agentName] = agent
		h.storage.SaveAgents(agents, room)
	}

	messages, _ := h.storage.GetMessages(room)

	var filtered []Message
	for _, msg := range messages {
		if msg.ID <= sinceID {
			continue
		}
		if unreadOnly && msg.From == agentName {
			continue
		}
		if msg.To == "all" || msg.To == agentName || msg.Type == "system" {
			filtered = append(filtered, msg)
		}
	}

	h.logger.Printf("read_messages: total=%d filtered=%d", len(messages), len(filtered))

	if len(filtered) == 0 {
		return mcp.NewToolResultText("\U0001f4ed Yeni mesaj yok."), nil
	}

	totalCount := len(filtered)
	var result string

	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
		result = fmt.Sprintf("\U0001f4ec Son %d mesaj (toplam %d):\n\n", limit, totalCount)
	} else {
		result = fmt.Sprintf("\U0001f4ec %d mesaj:\n\n", len(filtered))
	}

	for _, msg := range filtered {
		ts := parseTimestamp(msg.Timestamp)
		if msg.Type == "system" {
			result += fmt.Sprintf("[%s] %s\n", ts, msg.Content)
		} else if msg.To == "all" {
			result += fmt.Sprintf("[%s] %s \u2192 HERKESE: %s\n", ts, msg.From, msg.Content)
		} else {
			result += fmt.Sprintf("[%s] %s \u2192 %s: %s\n", ts, msg.From, msg.To, msg.Content)
		}
		result += fmt.Sprintf("  (ID: %d)\n\n", msg.ID)
	}

	return mcp.NewToolResultText(result), nil
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

	agents, _ := h.storage.GetAgents(room)
	agents = h.storage.CleanupStaleAgents(agents, 300)
	h.storage.SaveAgents(agents, room)

	if agentName != "" {
		if agent, ok := agents[agentName]; ok {
			agent.LastSeen = Now()
			agents[agentName] = agent
			h.storage.SaveAgents(agents, room)
		}
	}

	if len(agents) == 0 {
		return mcp.NewToolResultText("\U0001f465 Odada kimse yok."), nil
	}

	roomLabel := room
	if roomLabel == "" {
		roomLabel = h.storage.defaultRoom
	}

	h.logger.Printf("list_agents: room=%q count=%d", roomLabel, len(agents))

	result := fmt.Sprintf("\U0001f465 '%s' odasındaki agent'lar (%d):\n\n", roomLabel, len(agents))
	for name, info := range agents {
		marker := ""
		if name == agentName {
			marker = " (sen)"
		}
		result += fmt.Sprintf("  \u2022 %s%s", name, marker)
		if info.Role != "" {
			result += fmt.Sprintf(" - %s", info.Role)
		}
		joined := strings.Split(info.JoinedAt, "T")[0]
		result += fmt.Sprintf("\n    Katılım: %s\n", joined)
	}

	return mcp.NewToolResultText(result), nil
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

	agents, _ := h.storage.GetAgents(room)
	if _, ok := agents[agentName]; !ok {
		h.logger.Printf("leave_room: agent=%q not in room", agentName)
		return mcp.NewToolResultText(fmt.Sprintf("\u26a0\ufe0f '%s' zaten odada değil.", agentName)), nil
	}

	delete(agents, agentName)
	h.storage.SaveAgents(agents, room)

	// Add system message
	messages, _ := h.storage.GetMessages(room)
	messages = append(messages, Message{
		ID:        len(messages) + 1,
		From:      "SYSTEM",
		To:        "all",
		Content:   fmt.Sprintf("\U0001f534 %s odadan ayrıldı", agentName),
		Timestamp: Timestamp(),
		Type:      "system",
	})
	h.storage.SaveMessages(messages, room)

	return mcp.NewToolResultText(fmt.Sprintf("\U0001f44b '%s' odadan ayrıldı.", agentName)), nil
}

func (h *toolHandlers) clearRoom(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	room := request.GetString("room", "")

	if err := validation.ValidateName(room); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	h.logger.Printf("clear_room: room=%q", room)

	h.storage.SaveMessages([]Message{}, room)
	h.storage.SaveAgents(map[string]Agent{}, room)

	roomLabel := room
	if roomLabel == "" {
		roomLabel = h.storage.defaultRoom
	}
	return mcp.NewToolResultText(fmt.Sprintf("\U0001f9f9 '%s' odası temizlendi. Tüm mesajlar ve agent kayıtları silindi.", roomLabel)), nil
}

func (h *toolHandlers) readAllMessages(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sinceID := request.GetInt("since_id", 0)
	limit := request.GetInt("limit", 15)
	room := request.GetString("room", "")

	if err := validation.ValidateName(room); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	h.logger.Printf("read_all_messages: since_id=%d limit=%d room=%q", sinceID, limit, room)

	messages, _ := h.storage.GetMessages(room)

	var filtered []Message
	for _, m := range messages {
		if m.ID > sinceID {
			filtered = append(filtered, m)
		}
	}

	if len(filtered) == 0 {
		return mcp.NewToolResultText("\U0001f4ed Yeni mesaj yok."), nil
	}

	totalCount := len(filtered)
	var result string

	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
		result = fmt.Sprintf("\U0001f4ec Son %d mesaj (toplam %d):\n\n", limit, totalCount)
	} else {
		result = fmt.Sprintf("\U0001f4ec %d mesaj (tümü):\n\n", len(filtered))
	}

	for _, msg := range filtered {
		ts := parseTimestamp(msg.Timestamp)
		if msg.Type == "system" {
			result += fmt.Sprintf("[%s] SYSTEM: %s\n", ts, msg.Content)
		} else {
			contentPreview := msg.Content
			if len(contentPreview) > 100 {
				contentPreview = contentPreview[:100]
			}
			result += fmt.Sprintf("[%s] #%d %s \u2192 %s: %s\n", ts, msg.ID, msg.From, msg.To, contentPreview)
		}
		result += "\n"
	}

	return mcp.NewToolResultText(result), nil
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

	if agentName != "" {
		agents, _ := h.storage.GetAgents(room)
		if agent, ok := agents[agentName]; ok {
			agent.LastSeen = Now()
			agents[agentName] = agent
			h.storage.SaveAgents(agents, room)
		}
	}

	messages, _ := h.storage.GetMessages(room)
	lastID := 0
	if len(messages) > 0 {
		lastID = messages[len(messages)-1].ID
	}

	h.logger.Printf("get_last_message_id: room=%q lastID=%d", room, lastID)

	return mcp.NewToolResultText(fmt.Sprintf("%d", lastID)), nil
}

func (h *toolHandlers) listRooms(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chatDir := h.storage.chatDir

	h.logger.Printf("list_rooms: chatDir=%s", chatDir)

	entries, err := os.ReadDir(chatDir)
	if err != nil {
		return mcp.NewToolResultText("\U0001f4ad Henüz hiç oda yok."), nil
	}

	type roomInfo struct {
		Name     string
		Agents   int
		Messages int
	}

	var rooms []roomInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		roomDir := filepath.Join(chatDir, entry.Name())

		var agents map[string]Agent
		agentsPath := filepath.Join(roomDir, "agents.json")
		h.storage.readJSON(agentsPath, &agents)
		if agents != nil {
			agents = h.storage.CleanupStaleAgents(agents, 300)
		}

		var messages []Message
		messagesPath := filepath.Join(roomDir, "messages.json")
		h.storage.readJSON(messagesPath, &messages)

		agentCount := 0
		if agents != nil {
			agentCount = len(agents)
		}

		rooms = append(rooms, roomInfo{
			Name:     entry.Name(),
			Agents:   agentCount,
			Messages: len(messages),
		})
	}

	if len(rooms) == 0 {
		return mcp.NewToolResultText("\U0001f4ad Henüz hiç oda yok."), nil
	}

	sort.Slice(rooms, func(i, j int) bool {
		return rooms[i].Name < rooms[j].Name
	})

	result := fmt.Sprintf("\U0001f3e0 Mevcut odalar (%d):\n\n", len(rooms))
	for _, r := range rooms {
		defaultMarker := ""
		if r.Name == h.storage.defaultRoom {
			defaultMarker = " (varsayılan)"
		}
		result += fmt.Sprintf("  \u2022 %s%s - %d agent, %d mesaj\n", r.Name, defaultMarker, r.Agents, r.Messages)
	}

	return mcp.NewToolResultText(result), nil
}

// parseTimestamp extracts HH:MM:SS from an ISO timestamp string.
func parseTimestamp(ts string) string {
	t, err := time.Parse("2006-01-02T15:04:05.000000", ts)
	if err != nil {
		// Try without microseconds
		t, err = time.Parse("2006-01-02T15:04:05", ts)
		if err != nil {
			return ts
		}
	}
	return t.Format("15:04:05")
}
