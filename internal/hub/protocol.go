package hub

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"

	"desktop/internal/summary"
	"desktop/internal/types"
	"desktop/internal/validation"
)

// handleRequest dispatches a request to the appropriate room operation.
func (h *Hub) handleRequest(c *Client, req types.Request) {
	switch req.Type {
	case "identify":
		h.handleIdentify(c, req)
	case "set_manager":
		h.handleSetManager(c, req)
	case "set_observers":
		h.handleSetObservers(c, req)
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
	case "archive_room":
		h.handleArchiveRoom(c, req)
	case "save_session":
		h.handleSaveSession(c, req)
	case "log_message":
		h.handleLogMessage(c, req)
	case "read_summary":
		h.handleReadSummary(c, req)
	case "get_last_message_id":
		h.handleGetLastMessageID(c, req)
	case "list_rooms":
		h.handleListRooms(c, req)
	case "list_rooms_detailed":
		h.handleListRoomsDetailed(c, req)
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
		AuthToken  string `json:"auth_token"`
	}
	if err := json.Unmarshal(req.Data, &data); err != nil {
		c.sendError(req.ID, req.Type, "invalid identify payload")
		return
	}

	clientType := strings.ToLower(strings.TrimSpace(data.ClientType))
	switch clientType {
	case "", "mcp", "desktop":
	default:
		c.sendError(req.ID, req.Type, fmt.Sprintf("unsupported client_type: %s", data.ClientType))
		return
	}

	if c.clientType != "" && clientType != "" && c.clientType != clientType {
		c.sendError(req.ID, req.Type, fmt.Sprintf("client_type değiştirilemez (mevcut: %s)", c.clientType))
		return
	}
	if clientType == "desktop" {
		if !h.validateDesktopToken(data.AuthToken) {
			c.sendError(req.ID, req.Type, "desktop authentication failed")
			return
		}
		c.desktopAuthed = true
	}

	c.clientType = clientType
	if data.AgentName != "" {
		if c.joinedRoom != "" && c.agentName != data.AgentName {
			c.sendError(req.ID, req.Type, fmt.Sprintf("join_room sonrası agent adı değiştirilemez (mevcut: %s)", c.agentName))
			return
		}
		c.agentName = data.AgentName
	}
	if data.Room != "" {
		c.rooms[data.Room] = true
	}

	h.logger.Printf("Client identified: type=%s agent=%s", data.ClientType, data.AgentName)

	resp := types.Response{ID: req.ID, RequestType: req.Type, Success: true}
	resp.Data, _ = json.Marshal(map[string]bool{"ok": true})
	c.sendJSON(resp)
}

func (h *Hub) validateDesktopToken(token string) bool {
	hubToken := strings.TrimSpace(h.desktopAuthToken)
	if hubToken == "" {
		return false
	}
	t := strings.TrimSpace(token)
	if len(t) != len(hubToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(t), []byte(hubToken)) == 1
}

func (c *Client) isDesktopAuthorized() bool {
	return c.clientType == "desktop" && c.desktopAuthed
}

func (h *Hub) handleSetManager(c *Client, req types.Request) {
	if !c.isDesktopAuthorized() {
		c.sendError(req.ID, req.Type, "yalnızca yetkili desktop istemcisi manager atayabilir")
		return
	}

	var data struct {
		ManagerAgent string `json:"manager_agent"`
	}
	if err := json.Unmarshal(req.Data, &data); err != nil {
		c.sendError(req.ID, req.Type, "invalid set_manager payload")
		return
	}
	managerAgent := strings.TrimSpace(data.ManagerAgent)
	if managerAgent != "" {
		if err := validation.ValidateName(managerAgent); err != nil {
			c.sendError(req.ID, req.Type, err.Error())
			return
		}
	}

	room := h.resolveRoom(req.Room)
	h.setConfiguredManager(room, managerAgent)

	roomState := h.getOrCreateRoom(room)
	roomState.ResetManagerLockIfDifferent(managerAgent)

	var text string
	if managerAgent == "" {
		text = fmt.Sprintf("'%s' odası için manager ataması temizlendi.", room)
	} else {
		text = fmt.Sprintf("'%s' odası manager'ı '%s' olarak ayarlandı.", room, managerAgent)
	}
	respData, _ := json.Marshal(map[string]string{"text": text})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

// handleSetObservers replaces the desktop-authorized observer set for a room (#17).
// Only the authorized desktop may call it (mirrors handleSetManager) — it is the
// authority that decides who is a read-only observer, so a CLI agent can't grant
// itself observer (and thus read-all) access.
func (h *Hub) handleSetObservers(c *Client, req types.Request) {
	if !c.isDesktopAuthorized() {
		c.sendError(req.ID, req.Type, "yalnızca yetkili desktop istemcisi observer atayabilir")
		return
	}

	var data struct {
		Observers []string `json:"observers"`
	}
	if err := json.Unmarshal(req.Data, &data); err != nil {
		c.sendError(req.ID, req.Type, "invalid set_observers payload")
		return
	}
	for _, name := range data.Observers {
		if n := strings.TrimSpace(name); n != "" {
			if err := validation.ValidateName(n); err != nil {
				c.sendError(req.ID, req.Type, err.Error())
				return
			}
		}
	}

	room := h.resolveRoom(req.Room)
	h.setConfiguredObservers(room, data.Observers)

	text := fmt.Sprintf("'%s' odası için %d observer atandı.", room, len(data.Observers))
	respData, _ := json.Marshal(map[string]string{"text": text})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

func (h *Hub) handleSubscribe(c *Client, req types.Request) {
	var data struct {
		Rooms []string `json:"rooms"`
	}
	json.Unmarshal(req.Data, &data)

	// Reject invalid room names before creating any subscription (same filename
	// safety as join_room — see handleJoinRoom).
	for _, room := range data.Rooms {
		if err := validation.ValidateName(room); err != nil {
			c.sendError(req.ID, req.Type, fmt.Sprintf("geçersiz oda adı %q: %v", room, err))
			return
		}
	}

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

	// Reject room names that can't be safely used as a filename: the hub keys
	// both snapshot persistence (hub-state/{room}.json) and archives
	// (archive/{room}.jsonl) by room name, so an unvalidated name would silently
	// fail to persist/archive while the room still accepted (and then dropped)
	// messages.
	if err := validation.ValidateName(room); err != nil {
		c.sendError(req.ID, req.Type, fmt.Sprintf("geçersiz oda adı: %v", err))
		return
	}
	if err := validation.ValidateName(data.AgentName); err != nil {
		c.sendError(req.ID, req.Type, err.Error())
		return
	}
	if c.agentName != "" && c.agentName != data.AgentName {
		c.sendError(req.ID, req.Type, fmt.Sprintf("bu bağlantı '%s' olarak join oldu; farklı adla join olamaz", c.agentName))
		return
	}
	if c.joinedRoom != "" && c.joinedRoom != room {
		c.sendError(req.ID, req.Type, fmt.Sprintf("zaten '%s' odasına katıldınız; farklı odaya join olamaz", c.joinedRoom))
		return
	}
	if len(data.Role) > maxFieldLength {
		c.sendError(req.ID, req.Type, fmt.Sprintf("role too long: %d chars, max %d", len(data.Role), maxFieldLength))
		return
	}
	role := strings.ToLower(strings.TrimSpace(data.Role))
	if role == "manager" {
		configuredManager := h.getConfiguredManager(room)
		if configuredManager == "" {
			c.sendError(req.ID, req.Type, "manager rolü atanmadı; önce desktop üzerinden manager belirlenmeli")
			return
		}
		if !sameAgentName(data.AgentName, configuredManager) {
			c.sendError(req.ID, req.Type, fmt.Sprintf("manager rolü yalnızca '%s' agent'ına atanabilir", configuredManager))
			return
		}
	}
	// Observer is desktop-gated like manager (#17 P1): role "observer" grants read-all
	// transcript access, so a client must not be able to self-assert it. Only an agent
	// the desktop registered as an observer for this room may join with that role.
	if role == "observer" && !h.isConfiguredObserver(room, data.AgentName) {
		c.sendError(req.ID, req.Type, "observer rolü atanmadı; önce desktop üzerinden observer belirlenmeli")
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
	// Bind the observer role to the connection (#17): a gated observer join makes
	// this connection permanently read-only, independent of later allow-list/roster
	// changes.
	if role == roleObserver {
		c.isObserver = true
	}
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

	// Observer agents (#17) are read-only. Reject send_message BEFORE the manager
	// gateway below, so an observer's message is never even rerouted to the manager
	// — and because this returns before GetActiveManagerAndTouch, it cannot refresh
	// the active manager's heartbeat. Two independent signals block the send, so
	// neither a roster clear nor an allow-list change can re-enable it:
	//   - c.isObserver: connection-bound (set at the gated join) — a connection that
	//     joined as observer can NEVER send, even after the desktop de-configures it
	//     (it would have to reconnect as a non-observer);
	//   - isConfiguredObserver: fail-safe for a configured observer that joined under
	//     a different role.
	// Both key on join-bound identity (c.agentName, pinned by the from== check), so a
	// forged `from` can't bypass the gate.
	if c.isObserver || h.isConfiguredObserver(room, c.agentName) {
		c.sendError(req.ID, req.Type, "\U0001f441️ observer rolündeki agent mesaj gönderemez; yalnızca odayı izleyebilir")
		return
	}

	activeManager := roomState.GetActiveManagerAndTouch(data.From)

	to := data.To
	opts := SendOptions{}
	intercepted := false
	if activeManager != "" && !sameAgentName(data.From, activeManager) {
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

	if c.joinedRoom == "" || c.agentName == "" {
		c.sendError(req.ID, req.Type, "önce join_room çağırmalısınız")
		return
	}
	if c.joinedRoom != room {
		c.sendError(req.ID, req.Type, fmt.Sprintf("yalnızca katıldığınız odadan mesaj okuyabilirsiniz: %s", c.joinedRoom))
		return
	}
	if err := validation.ValidateName(data.AgentName); err != nil {
		c.sendError(req.ID, req.Type, err.Error())
		return
	}
	if data.AgentName != c.agentName {
		c.sendError(req.ID, req.Type, "yalnızca kendi adınızla mesaj okuyabilirsiniz")
		return
	}

	roomState := h.getOrCreateRoom(room)
	roomState.TouchManagerHeartbeat(c.agentName)
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

	// Only the active manager or authorized desktop app can read all messages.
	if c.agentName == "" {
		if !c.isDesktopAuthorized() {
			c.sendError(req.ID, req.Type, "önce yetkili desktop identify veya join_room çağırmalısınız")
			return
		}
	} else {
		if c.joinedRoom != room {
			c.sendError(req.ID, req.Type, fmt.Sprintf("yalnızca katıldığınız odadan mesaj okuyabilirsiniz: %s", c.joinedRoom))
			return
		}
		activeManager := roomState.GetActiveManager()
		isManager := activeManager != "" && sameAgentName(c.agentName, activeManager)
		// Observer agents (#17) get read-only access to the full transcript. Authorize
		// from the DESKTOP allow-list (isConfiguredObserver), not the room roster: this
		// is revocable — removing an agent from the observer set via set_observers
		// immediately drops its read-all access even while it stays connected.
		isObserver := h.isConfiguredObserver(room, c.agentName)
		if !isManager && !isObserver {
			c.sendError(req.ID, req.Type, "yalnızca aktif manager veya observer tüm mesajları okuyabilir")
			return
		}
		if isManager {
			roomState.TouchManagerHeartbeat(c.agentName)
		} else {
			// Refresh the observer's last_seen so a read_all-only poller is not
			// stale-evicted (the read path itself doesn't touch last_seen). Without
			// this the observer would lose read_all access after staleTimeout.
			roomState.TouchAgentLastSeen(c.agentName)
		}
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

	// Only authorized desktop app or active manager can clear a room.
	if !c.isDesktopAuthorized() {
		if c.agentName == "" || c.joinedRoom == "" {
			c.sendError(req.ID, req.Type, "önce join_room çağırmalısınız")
			return
		}
		if c.joinedRoom != room {
			c.sendError(req.ID, req.Type, fmt.Sprintf("yalnızca katıldığınız odayı temizleyebilirsiniz: %s", c.joinedRoom))
			return
		}

		roomState := h.getOrCreateRoom(room)
		activeManager := roomState.GetActiveManager()
		if activeManager == "" || !sameAgentName(c.agentName, activeManager) {
			c.sendError(req.ID, req.Type, "yalnızca aktif manager veya yetkili desktop odayı temizleyebilir")
			return
		}
		roomState.TouchManagerHeartbeat(c.agentName)
	}

	roomState := h.getOrCreateRoom(room)

	// Durable destructive clear: archive the current history synchronously and
	// refuse to wipe if it cannot be preserved. Unlike the async truncate path,
	// clear_room is a deliberate, irreversible command, so it must not report
	// success while the only copy of the history fails to reach disk.
	msgs := roomState.GetMessages()
	if err := h.appendArchive(room, msgs); err != nil {
		h.logger.Printf("clear_room aborted; archive failed for %s: %v", room, err)
		c.sendError(req.ID, req.Type, fmt.Sprintf("oda temizlenmedi: geçmiş arşivlenemedi: %v", err))
		return
	}
	// Only wipe up to the archived snapshot's last ID, so a message sent while
	// the archive I/O ran (lock released) is kept rather than lost.
	maxID := 0
	if n := len(msgs); n > 0 {
		maxID = msgs[n-1].ID
	}
	roomState.ClearArchived(maxID)
	// The clear resets message IDs (next message restarts at 1), so forget the
	// last-snapshot ID — otherwise the next session's coincidental ID match could
	// wrongly skip its snapshot.
	h.resetSessionTracking(room)

	text := fmt.Sprintf("\U0001f9f9 '%s' odası temizlendi. Tüm mesajlar ve agent kayıtları silindi.", room)
	respData, _ := json.Marshal(map[string]string{"text": text})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})

	h.broadcastEvent(room, "room_cleared", map[string]any{})
}

// handleArchiveRoom flushes a room's current messages to its append-only
// archive. It is restricted to the authorized desktop app (the desktop is the
// only caller — e.g. DeleteTeam — that needs a synchronous flush). The write is
// synchronous, so the response confirms the hub has written the messages (the
// archive, like the rest of the hub's persistence, is buffered to the OS — not
// fsync'd). The archive is append-only, so repeated calls may duplicate current
// messages; that is acceptable and never loses history. A room that was never
// created archives nothing rather than materializing a phantom empty room.
func (h *Hub) handleArchiveRoom(c *Client, req types.Request) {
	room := h.resolveRoom(req.Room)

	if !c.isDesktopAuthorized() {
		c.sendError(req.ID, req.Type, "yalnızca yetkili desktop odayı arşivleyebilir")
		return
	}

	// Ordering caveat: if a truncation batch for this room is still queued on the
	// async writer when this synchronous flush runs, the retained tail can land
	// in the file before that older batch. No message is lost and every line
	// carries an ID and timestamp, so a reader can order them; a stricter
	// file-order guarantee is deferred along with archive read tooling.
	var msgs []types.Message
	if roomState := h.getRoom(room); roomState != nil {
		msgs = roomState.GetMessages()
	}
	if err := h.appendArchive(room, msgs); err != nil {
		h.logger.Printf("archive_room failed for %s: %v", room, err)
		c.sendError(req.ID, req.Type, fmt.Sprintf("oda arşivlenemedi: %v", err))
		return
	}

	text := fmt.Sprintf("\U0001f4e6 '%s' odası arşivlendi (%d mesaj).", room, len(msgs))
	respData, _ := json.Marshal(map[string]any{"text": text, "archived": len(msgs)})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

// handleSaveSession writes an immutable per-session snapshot of the room's full
// current state (messages + agent roster) to hub-state/sessions/{room}/{epoch}.json.
// It is restricted to the authorized desktop app (the only caller — DeleteTeam,
// shutdown, manual save). Unlike archive_room's rolling append-only stream, each
// call produces a distinct file that is never overwritten or pruned. An empty or
// unchanged room is skipped (saved=false) rather than written. A room that was
// never created saves nothing rather than materializing a phantom empty room.
func (h *Hub) handleSaveSession(c *Client, req types.Request) {
	room := h.resolveRoom(req.Room)

	if !c.isDesktopAuthorized() {
		c.sendError(req.ID, req.Type, "yalnızca yetkili desktop session kaydedebilir")
		return
	}

	_, count, skipped, err := h.saveSession(room)
	if err != nil {
		h.logger.Printf("save_session failed for %s: %v", room, err)
		c.sendError(req.ID, req.Type, fmt.Sprintf("session kaydedilemedi: %v", err))
		return
	}

	saved := !skipped
	text := fmt.Sprintf("ℹ️ '%s' odasında kaydedilecek yeni içerik yok.", room)
	if saved {
		text = fmt.Sprintf("\U0001f4be '%s' odası session olarak kaydedildi (%d mesaj).", room, count)
	}
	respData, _ := json.Marshal(map[string]any{"text": text, "saved": saved, "count": count})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

// handleLogMessage records an out-of-band human→agent prompt in the room
// transcript as a "user_prompt" message (#29). Desktop-authorized only: the
// "from" identity is server-forced to the user sentinel so a logged prompt can
// never be confused with — or spoof — real agent traffic. The message goes
// through the normal append/broadcast path so it is snapshotted, archived, and
// shown live, but the orchestrator must skip user_prompt so it is never injected
// back into agent terminals (it was already delivered to the target PTY).
func (h *Hub) handleLogMessage(c *Client, req types.Request) {
	if !c.isDesktopAuthorized() {
		c.sendError(req.ID, req.Type, "yalnızca yetkili desktop prompt loglayabilir")
		return
	}

	var data struct {
		To      string `json:"to"`
		Content string `json:"content"`
	}
	data.To = "all"
	if err := json.Unmarshal(req.Data, &data); err != nil {
		c.sendError(req.ID, req.Type, "invalid log_message payload")
		return
	}

	room := h.resolveRoom(req.Room)

	if data.To != "all" {
		if err := validation.ValidateName(data.To); err != nil {
			c.sendError(req.ID, req.Type, err.Error())
			return
		}
	}
	content := strings.TrimSpace(data.Content)
	if content == "" {
		c.sendError(req.ID, req.Type, "boş prompt loglanmaz")
		return
	}
	if len(content) > maxFieldLength {
		// The prompt was already delivered to the agent's PTY; logging is best-effort
		// and fire-and-forget, so TRUNCATE the transcript record (at a rune boundary)
		// rather than rejecting it — dropping it would silently omit a delivered
		// instruction from the summary.
		content = strings.ToValidUTF8(content[:maxFieldLength], "")
	}

	roomState := h.getOrCreateRoom(room)
	msg := roomState.LogUserPrompt(types.UserPromptFrom, data.To, content)

	respData, _ := json.Marshal(map[string]any{"ok": true, "message_id": msg.ID})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})

	h.broadcastEvent(room, "message_new", map[string]any{"message": msg})
}

// handleReadSummary returns the newest saved per-session summary for a room
// (#29). Readable by any agent joined to the room (so a continuing agent can pull
// prior context cheaply instead of the whole history) or by the authorized
// desktop.
func (h *Hub) handleReadSummary(c *Client, req types.Request) {
	room := h.resolveRoom(req.Room)

	if c.agentName == "" {
		if !c.isDesktopAuthorized() {
			c.sendError(req.ID, req.Type, "önce join_room veya yetkili desktop identify çağırmalısınız")
			return
		}
	} else if c.joinedRoom != room {
		c.sendError(req.ID, req.Type, fmt.Sprintf("yalnızca katıldığınız odanın özetini okuyabilirsiniz: %s", c.joinedRoom))
		return
	}

	// A continued agent is steered here instead of read_all_messages/read_messages,
	// so refresh BOTH the manager lock heartbeat AND the roster LastSeen like the
	// other read/poll handlers — otherwise an actively-polling manager goes stale
	// (managerTimeoutSec → routing bypassed) and any polling agent gets evicted by
	// roster stale cleanup (Agent.LastSeen) after the timeout.
	if c.agentName != "" {
		if rs := h.getRoom(room); rs != nil {
			rs.TouchManagerHeartbeat(c.agentName)
			rs.TouchAgentLastSeen(c.agentName)
		}
	}

	doc, ok, err := summary.Latest(h.dataDir, room)
	if err != nil {
		h.logger.Printf("read_summary failed for %s: %v", room, err)
		c.sendError(req.ID, req.Type, fmt.Sprintf("özet okunamadı: %v", err))
		return
	}
	if !ok {
		respData, _ := json.Marshal(map[string]string{"text": "\U0001f4ed Bu oda için henüz özet yok."})
		c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
		return
	}

	text := fmt.Sprintf("\U0001f4dd Önceki session özeti (%s):\n\n%s", doc.CreatedAt, doc.Text)
	respData, _ := json.Marshal(map[string]string{"text": text})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

func (h *Hub) handleGetLastMessageID(c *Client, req types.Request) {
	var data struct {
		AgentName string `json:"agent_name"`
	}
	json.Unmarshal(req.Data, &data)

	room := h.resolveRoom(req.Room)

	if c.agentName == "" {
		if !c.isDesktopAuthorized() {
			c.sendError(req.ID, req.Type, "önce yetkili desktop identify veya join_room çağırmalısınız")
			return
		}
	} else {
		if c.joinedRoom != room {
			c.sendError(req.ID, req.Type, fmt.Sprintf("yalnızca katıldığınız odadan sorgulama yapabilirsiniz: %s", c.joinedRoom))
			return
		}
		if data.AgentName != "" && data.AgentName != c.agentName {
			c.sendError(req.ID, req.Type, "yalnızca kendi adınızla sorgulama yapabilirsiniz")
			return
		}
	}

	roomState := h.getOrCreateRoom(room)
	if c.agentName != "" {
		roomState.TouchManagerHeartbeat(c.agentName)
	}
	agentForQuery := data.AgentName
	if agentForQuery == "" && c.agentName != "" {
		agentForQuery = c.agentName
	}
	lastID := roomState.GetLastMessageID(agentForQuery)

	respData, _ := json.Marshal(map[string]any{"last_id": lastID})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

// handleGetAgents returns raw agent data for a room (used by desktop app).
func (h *Hub) handleGetAgents(c *Client, req types.Request) {
	if !c.isDesktopAuthorized() {
		c.sendError(req.ID, req.Type, "yalnızca yetkili desktop istemcisi agent listesini ham biçimde okuyabilir")
		return
	}

	room := h.resolveRoom(req.Room)
	roomState := h.getOrCreateRoom(room)
	agents := roomState.GetAgents()

	respData, _ := json.Marshal(map[string]any{"agents": agents})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}

// handleGetMessagesRaw returns raw message data for a room (used by desktop app).
func (h *Hub) handleGetMessagesRaw(c *Client, req types.Request) {
	if !c.isDesktopAuthorized() {
		c.sendError(req.ID, req.Type, "yalnızca yetkili desktop istemcisi mesajları ham biçimde okuyabilir")
		return
	}

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

// handleListRoomsDetailed returns structured room summaries for the desktop room
// browser (orphan rooms included). Desktop-authorized only \u2014 agents must not see
// other teams' history.
func (h *Hub) handleListRoomsDetailed(c *Client, req types.Request) {
	if !c.isDesktopAuthorized() {
		c.sendError(req.ID, req.Type, "yaln\u0131zca yetkili desktop istemcisi oda listesini ayr\u0131nt\u0131l\u0131 okuyabilir")
		return
	}

	h.mu.RLock()
	summaries := ListRoomSummaries(h.rooms, h.defaultRoom)
	h.mu.RUnlock()

	respData, _ := json.Marshal(map[string]any{"rooms": summaries})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}
