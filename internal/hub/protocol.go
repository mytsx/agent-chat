package hub

import (
	"encoding/json"
	"fmt"
	"strings"

	"desktop/internal/types"
	"desktop/internal/validation"
)

// handleRequest dispatches a request to the appropriate room operation.
func (h *Hub) handleRequest(c *Client, req types.Request) {
	switch req.Type {
	case "identify":
		h.handleIdentify(c, req)
	case "subscribe":
		h.handleSubscribe(c, req)
	case "join_room":
		h.handleJoinRoom(c, req)
	case "send_message":
		h.handleSendMessage(c, req)
	case "get_messages":
		h.handleGetMessages(c, req)
	case "get_all_messages":
		h.handleGetAllMessages(c, req)
	case "list_agents":
		h.handleListAgents(c, req)
	case "leave_room":
		h.handleLeaveRoom(c, req)
	case "clear_room":
		h.handleClearRoom(c, req)
	case "get_last_message_id":
		h.handleGetLastMessageID(c, req)
	case "list_rooms":
		h.handleListRooms(c, req)
	case "get_agents":
		h.handleGetAgents(c, req)
	case "get_messages_raw":
		h.handleGetMessagesRaw(c, req)
	default:
		c.sendError(req.ID, req.Type, fmt.Sprintf("unknown request type: %s", req.Type))
	}
}

func (h *Hub) handleIdentify(c *Client, req types.Request) {
	var data struct {
		ClientType string `json:"client_type"`
		AgentName  string `json:"agent_name"`
		Room       string `json:"room"`
	}
	json.Unmarshal(req.Data, &data)

	c.clientType = data.ClientType
	c.agentName = data.AgentName
	if data.Room != "" {
		c.rooms[data.Room] = true
	}

	h.logger.Printf("Client identified: type=%s agent=%s", data.ClientType, data.AgentName)

	resp := types.Response{ID: req.ID, RequestType: req.Type, Success: true}
	resp.Data, _ = json.Marshal(map[string]bool{"ok": true})
	c.sendJSON(resp)
}

func (h *Hub) handleSubscribe(c *Client, req types.Request) {
	var data struct {
		Rooms []string `json:"rooms"`
	}
	json.Unmarshal(req.Data, &data)

	h.mu.Lock()
	for _, room := range data.Rooms {
		c.rooms[room] = true
		if h.subs[room] == nil {
			h.subs[room] = make(map[*Client]bool)
		}
		h.subs[room][c] = true
	}
	h.mu.Unlock()

	h.logger.Printf("Client subscribed to rooms: %v", data.Rooms)

	resp := types.Response{ID: req.ID, RequestType: req.Type, Success: true}
	resp.Data, _ = json.Marshal(map[string]bool{"ok": true})
	c.sendJSON(resp)
}

func (h *Hub) handleJoinRoom(c *Client, req types.Request) {
	var data struct {
		AgentName string `json:"agent_name"`
		Role      string `json:"role"`
	}
	json.Unmarshal(req.Data, &data)

	room := h.resolveRoom(req.Room)

	if err := validation.ValidateName(data.AgentName); err != nil {
		c.sendError(req.ID, req.Type, err.Error())
		return
	}
	if c.agentName != "" && c.agentName != data.AgentName {
		c.sendError(req.ID, req.Type, fmt.Sprintf("bu bağlantı '%s' olarak join oldu; farklı adla join olamaz", c.agentName))
		return
	}
	if len(data.Role) > maxFieldLength {
		c.sendError(req.ID, req.Type, fmt.Sprintf("role too long: %d chars, max %d", len(data.Role), maxFieldLength))
		return
	}

	h.logger.Printf("join_room: agent=%q role=%q room=%q", data.AgentName, data.Role, room)

	roomState := h.getOrCreateRoom(room)
	sysMsg, agents, err := roomState.Join(data.AgentName, data.Role)
	if err != nil {
		c.sendError(req.ID, req.Type, err.Error())
		return
	}

	// Also subscribe the client to this room
	h.mu.Lock()
	c.rooms[room] = true
	c.agentName = data.AgentName
	c.joinedRoom = room
	if h.subs[room] == nil {
		h.subs[room] = make(map[*Client]bool)
	}
	h.subs[room][c] = true
	h.mu.Unlock()

	// Build response text
	var otherAgents []string
	for name := range agents {
		if name != data.AgentName {
			otherAgents = append(otherAgents, name)
		}
	}

	var text string
	if len(otherAgents) > 0 {
		text = fmt.Sprintf("\u2705 '%s' olarak '%s' odasına katıldın. Odadaki diğer agent'lar: %s", data.AgentName, room, strings.Join(otherAgents, ", "))
	} else {
		text = fmt.Sprintf("\u2705 '%s' olarak '%s' odasına katıldın. Şu an odada başka agent yok.", data.AgentName, room)
	}

	respData, _ := json.Marshal(map[string]any{"text": text, "agents": agents})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})

	// Broadcast events
	h.broadcastEvent(room, "message_new", map[string]any{"message": sysMsg})
	h.broadcastEvent(room, "agent_joined", map[string]any{"agent_name": data.AgentName, "agents": agents})
}

func (h *Hub) handleSendMessage(c *Client, req types.Request) {
	var data struct {
		From         string `json:"from"`
		To           string `json:"to"`
		Content      string `json:"content"`
		ExpectsReply bool   `json:"expects_reply"`
		Priority     string `json:"priority"`
	}
	// Defaults
	data.To = "all"
	data.ExpectsReply = true
	data.Priority = "normal"
	json.Unmarshal(req.Data, &data)

	room := h.resolveRoom(req.Room)

	if c.joinedRoom == "" || c.agentName == "" {
		c.sendError(req.ID, req.Type, "önce join_room çağırmalısınız")
		return
	}
	if c.joinedRoom != room {
		c.sendError(req.ID, req.Type, fmt.Sprintf("yalnızca katıldığınız odada mesaj gönderebilirsiniz: %s", c.joinedRoom))
		return
	}

	if err := validation.ValidateName(data.From); err != nil {
		c.sendError(req.ID, req.Type, err.Error())
		return
	}
	if data.From != c.agentName {
		c.sendError(req.ID, req.Type, "from_agent yalnızca kendi adınız olabilir")
		return
	}
	if data.To != "all" {
		if err := validation.ValidateName(data.To); err != nil {
			c.sendError(req.ID, req.Type, err.Error())
			return
		}
	}
	if len(data.Content) > maxFieldLength {
		c.sendError(req.ID, req.Type, fmt.Sprintf("content too long: %d chars, max %d", len(data.Content), maxFieldLength))
		return
	}

	h.logger.Printf("send_message: from=%q to=%q room=%q priority=%s expects_reply=%v contentLen=%d",
		data.From, data.To, room, data.Priority, data.ExpectsReply, len(data.Content))

	roomState := h.getOrCreateRoom(room)

	activeManager := roomState.GetActiveManager()
	if activeManager != "" && data.From == activeManager {
		roomState.TouchManagerHeartbeat(data.From)
	}

	to := data.To
	opts := SendOptions{}
	intercepted := false
	if activeManager != "" && data.From != activeManager {
		intercepted = true
		opts.OriginalTo = data.To
		opts.RoutedByManager = true
		to = activeManager
	}

	msg, err := roomState.SendMessage(data.From, to, data.Content, data.ExpectsReply, data.Priority, opts)
	if err != nil {
		c.sendError(req.ID, req.Type, err.Error())
		return
	}

	h.logger.Printf("send_message: id=%d saved to room=%s", msg.ID, room)

	var text string
	if intercepted {
		text = fmt.Sprintf("\U0001f4e4 Mesaj manager '%s' agent'ına iletildi, onay bekliyor (ID: %d)", activeManager, msg.ID)
	} else if data.To == "all" {
		text = fmt.Sprintf("\U0001f4e4 Mesaj tüm agent'lara gönderildi (ID: %d)", msg.ID)
	} else {
		text = fmt.Sprintf("\U0001f4e4 Mesaj '%s' agent'ına gönderildi (ID: %d)", data.To, msg.ID)
	}

	respData, _ := json.Marshal(map[string]any{"text": text, "message_id": msg.ID})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})

	// Broadcast event
	h.broadcastEvent(room, "message_new", map[string]any{"message": msg})
}

func (h *Hub) handleGetMessages(c *Client, req types.Request) {
	var data struct {
		AgentName  string `json:"agent_name"`
		SinceID    int    `json:"since_id"`
		Limit      int    `json:"limit"`
		UnreadOnly bool   `json:"unread_only"`
	}
	data.Limit = 10
	data.UnreadOnly = true
	json.Unmarshal(req.Data, &data)

	room := h.resolveRoom(req.Room)

	if err := validation.ValidateName(data.AgentName); err != nil {
		c.sendError(req.ID, req.Type, err.Error())
		return
	}

	roomState := h.getOrCreateRoom(room)
	if c.agentName != "" {
		roomState.TouchManagerHeartbeat(c.agentName)
	}
	filtered, totalCount := roomState.ReadMessages(data.AgentName, data.SinceID, data.Limit, data.UnreadOnly)

	if len(filtered) == 0 {
		respData, _ := json.Marshal(map[string]string{"text": "\U0001f4ed Yeni mesaj yok."})
		c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
		return
	}

	var sb strings.Builder
	if data.Limit > 0 && totalCount > data.Limit {
		fmt.Fprintf(&sb, "\U0001f4ec Son %d mesaj (toplam %d):\n\n", data.Limit, totalCount)
	} else {
		fmt.Fprintf(&sb, "\U0001f4ec %d mesaj:\n\n", len(filtered))
	}

	for _, msg := range filtered {
		ts := parseTimestamp(msg.Timestamp)
		if msg.Type == "system" {
			fmt.Fprintf(&sb, "[%s] %s\n", ts, sanitize(msg.Content))
		} else if msg.To == "all" {
			fmt.Fprintf(&sb, "[%s] %s \u2192 HERKESE: %s\n", ts, sanitize(msg.From), sanitize(msg.Content))
		} else if msg.OriginalTo != "" && msg.OriginalTo != msg.To {
			fmt.Fprintf(&sb, "[%s] %s \u2192 %s (orijinal: %s): %s\n",
				ts, sanitize(msg.From), sanitize(msg.To), sanitize(msg.OriginalTo), sanitize(msg.Content))
		} else {
			fmt.Fprintf(&sb, "[%s] %s \u2192 %s: %s\n", ts, sanitize(msg.From), sanitize(msg.To), sanitize(msg.Content))
		}
		fmt.Fprintf(&sb, "  (ID: %d)\n\n", msg.ID)
	}

	respData, _ := json.Marshal(map[string]string{"text": sb.String()})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

func (h *Hub) handleGetAllMessages(c *Client, req types.Request) {
	var data struct {
		SinceID int `json:"since_id"`
		Limit   int `json:"limit"`
	}
	data.Limit = 15
	json.Unmarshal(req.Data, &data)

	room := h.resolveRoom(req.Room)
	roomState := h.getOrCreateRoom(room)
	if c.agentName != "" {
		roomState.TouchManagerHeartbeat(c.agentName)
	}
	filtered, totalCount := roomState.ReadAllMessages(data.SinceID, data.Limit)

	if len(filtered) == 0 {
		respData, _ := json.Marshal(map[string]string{"text": "\U0001f4ed Yeni mesaj yok."})
		c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
		return
	}

	var sb strings.Builder
	if data.Limit > 0 && totalCount > data.Limit {
		fmt.Fprintf(&sb, "\U0001f4ec Son %d mesaj (toplam %d):\n\n", data.Limit, totalCount)
	} else {
		fmt.Fprintf(&sb, "\U0001f4ec %d mesaj (tümü):\n\n", len(filtered))
	}

	for _, msg := range filtered {
		ts := parseTimestamp(msg.Timestamp)
		if msg.Type == "system" {
			fmt.Fprintf(&sb, "[%s] SYSTEM: %s\n", ts, sanitize(msg.Content))
		} else {
			contentPreview := msg.Content
			if len(contentPreview) > 100 {
				contentPreview = contentPreview[:100]
			}
			if msg.OriginalTo != "" && msg.OriginalTo != msg.To {
				fmt.Fprintf(&sb, "[%s] #%d %s \u2192 %s (orijinal: %s): %s\n",
					ts, msg.ID, sanitize(msg.From), sanitize(msg.To), sanitize(msg.OriginalTo), sanitize(contentPreview))
			} else {
				fmt.Fprintf(&sb, "[%s] #%d %s \u2192 %s: %s\n", ts, msg.ID, sanitize(msg.From), sanitize(msg.To), sanitize(contentPreview))
			}
		}
		sb.WriteString("\n")
	}

	respData, _ := json.Marshal(map[string]string{"text": sb.String()})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

func (h *Hub) handleListAgents(c *Client, req types.Request) {
	var data struct {
		AgentName string `json:"agent_name"`
	}
	json.Unmarshal(req.Data, &data)

	room := h.resolveRoom(req.Room)
	roomState := h.getOrCreateRoom(room)
	if c.agentName != "" {
		roomState.TouchManagerHeartbeat(c.agentName)
	}
	agents := roomState.ListAgents(data.AgentName)

	if len(agents) == 0 {
		respData, _ := json.Marshal(map[string]string{"text": "\U0001f465 Odada kimse yok."})
		c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "\U0001f465 '%s' odasındaki agent'lar (%d):\n\n", sanitize(room), len(agents))
	for name, info := range agents {
		marker := ""
		if name == data.AgentName {
			marker = " (sen)"
		}
		fmt.Fprintf(&sb, "  \u2022 %s%s", sanitize(name), marker)
		if info.Role != "" {
			fmt.Fprintf(&sb, " - %s", sanitize(info.Role))
		}
		joined := strings.Split(info.JoinedAt, "T")[0]
		fmt.Fprintf(&sb, "\n    Katılım: %s\n", joined)
	}

	respData, _ := json.Marshal(map[string]string{"text": sb.String()})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

func (h *Hub) handleLeaveRoom(c *Client, req types.Request) {
	var data struct {
		AgentName string `json:"agent_name"`
	}
	json.Unmarshal(req.Data, &data)

	room := h.resolveRoom(req.Room)

	if err := validation.ValidateName(data.AgentName); err != nil {
		c.sendError(req.ID, req.Type, err.Error())
		return
	}
	if c.agentName == "" || c.joinedRoom == "" {
		c.sendError(req.ID, req.Type, "önce join_room çağırmalısınız")
		return
	}
	if data.AgentName != c.agentName {
		c.sendError(req.ID, req.Type, "yalnızca kendi adınızla leave_room çağırabilirsiniz")
		return
	}
	if c.joinedRoom != room {
		c.sendError(req.ID, req.Type, fmt.Sprintf("yalnızca katıldığınız odadan ayrılabilirsiniz: %s", c.joinedRoom))
		return
	}

	roomState := h.getOrCreateRoom(room)
	sysMsg, found := roomState.Leave(data.AgentName)

	if !found {
		respData, _ := json.Marshal(map[string]string{"text": fmt.Sprintf("\u26a0\ufe0f '%s' zaten odada değil.", data.AgentName)})
		c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
		return
	}

	respData, _ := json.Marshal(map[string]string{"text": fmt.Sprintf("\U0001f44b '%s' odadan ayrıldı.", data.AgentName)})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
	c.agentName = ""
	c.joinedRoom = ""

	agents := roomState.GetAgents()
	h.broadcastEvent(room, "message_new", map[string]any{"message": sysMsg})
	h.broadcastEvent(room, "agent_left", map[string]any{"agent_name": data.AgentName, "agents": agents})
}

func (h *Hub) handleClearRoom(c *Client, req types.Request) {
	room := h.resolveRoom(req.Room)
	roomState := h.getOrCreateRoom(room)
	roomState.Clear()

	text := fmt.Sprintf("\U0001f9f9 '%s' odası temizlendi. Tüm mesajlar ve agent kayıtları silindi.", room)
	respData, _ := json.Marshal(map[string]string{"text": text})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})

	h.broadcastEvent(room, "room_cleared", map[string]any{})
}

func (h *Hub) handleGetLastMessageID(c *Client, req types.Request) {
	var data struct {
		AgentName string `json:"agent_name"`
	}
	json.Unmarshal(req.Data, &data)

	room := h.resolveRoom(req.Room)
	roomState := h.getOrCreateRoom(room)
	if c.agentName != "" {
		roomState.TouchManagerHeartbeat(c.agentName)
	}
	lastID := roomState.GetLastMessageID(data.AgentName)

	respData, _ := json.Marshal(map[string]any{"last_id": lastID})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

// handleGetAgents returns raw agent data for a room (used by desktop app).
func (h *Hub) handleGetAgents(c *Client, req types.Request) {
	room := h.resolveRoom(req.Room)
	roomState := h.getOrCreateRoom(room)
	agents := roomState.GetAgents()

	respData, _ := json.Marshal(map[string]any{"agents": agents})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

// handleGetMessagesRaw returns raw message data for a room (used by desktop app).
func (h *Hub) handleGetMessagesRaw(c *Client, req types.Request) {
	room := h.resolveRoom(req.Room)
	roomState := h.getOrCreateRoom(room)
	messages := roomState.GetMessages()

	respData, _ := json.Marshal(map[string]any{"messages": messages})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

func (h *Hub) handleListRooms(c *Client, req types.Request) {
	h.mu.RLock()
	infos := ListRoomInfos(h.rooms)
	defaultRoom := h.defaultRoom
	h.mu.RUnlock()

	if len(infos) == 0 {
		respData, _ := json.Marshal(map[string]string{"text": "\U0001f4ad Henüz hiç oda yok."})
		c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "\U0001f3e0 Mevcut odalar (%d):\n\n", len(infos))
	for _, r := range infos {
		defaultMarker := ""
		if r.Name == defaultRoom {
			defaultMarker = " (varsayılan)"
		}
		fmt.Fprintf(&sb, "  \u2022 %s%s - %d agent, %d mesaj\n", r.Name, defaultMarker, r.Agents, r.Messages)
	}

	respData, _ := json.Marshal(map[string]string{"text": sb.String()})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}
