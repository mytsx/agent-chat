package hub

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	case "delete_room":
		h.handleDeleteRoom(c, req)
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
	if !c.decodeRequestData(req, &data, "invalid identify payload") {
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

	c.sendOK(req.ID, req.Type)
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

func (c *Client) requireDesktopAuthorized(req types.Request, message string) bool {
	if c.isDesktopAuthorized() {
		return true
	}
	c.sendError(req.ID, req.Type, message)
	return false
}

func (c *Client) requireValidName(req types.Request, name string) bool {
	if err := validation.ValidateName(name); err != nil {
		c.sendError(req.ID, req.Type, err.Error())
		return false
	}
	return true
}

func (c *Client) requireValidNonEmptyNames(req types.Request, names []string) bool {
	for _, name := range names {
		if n := strings.TrimSpace(name); n != "" {
			if !c.requireValidName(req, n) {
				return false
			}
		}
	}
	return true
}

func (c *Client) requireValidRoomNames(req types.Request, rooms []string) bool {
	for _, room := range rooms {
		if err := validation.ValidateName(room); err != nil {
			c.sendError(req.ID, req.Type, fmt.Sprintf("geçersiz oda adı %q: %v", room, err))
			return false
		}
	}
	return true
}

func (c *Client) decodeRequestData(req types.Request, dest any, invalidPayloadMessage string) bool {
	if err := json.Unmarshal(req.Data, dest); err != nil {
		c.sendError(req.ID, req.Type, invalidPayloadMessage)
		return false
	}
	return true
}

func (c *Client) requireJoinedRoom(req types.Request, room, notJoinedMessage, wrongRoomFormat string) bool {
	if c.agentName == "" || c.joinedRoom == "" {
		c.sendError(req.ID, req.Type, notJoinedMessage)
		return false
	}
	if c.joinedRoom != room {
		c.sendError(req.ID, req.Type, fmt.Sprintf(wrongRoomFormat, c.joinedRoom))
		return false
	}
	return true
}

func (c *Client) requireJoinedRoomOrDesktop(req types.Request, room, notAuthorizedMessage, wrongRoomFormat string) bool {
	if c.agentName == "" {
		if !c.isDesktopAuthorized() {
			c.sendError(req.ID, req.Type, notAuthorizedMessage)
			return false
		}
		return true
	}
	if c.joinedRoom != room {
		c.sendError(req.ID, req.Type, fmt.Sprintf(wrongRoomFormat, c.joinedRoom))
		return false
	}
	return true
}

func (h *Hub) handleSetManager(c *Client, req types.Request) {
	if !c.requireDesktopAuthorized(req, "yalnızca yetkili desktop istemcisi manager atayabilir") {
		return
	}

	var data struct {
		ManagerAgent string `json:"manager_agent"`
	}
	if !c.decodeRequestData(req, &data, "invalid set_manager payload") {
		return
	}
	managerAgent := strings.TrimSpace(data.ManagerAgent)
	if managerAgent != "" {
		if !c.requireValidName(req, managerAgent) {
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
	c.sendText(req.ID, req.Type, text)
}

// handleSetObservers replaces the desktop-authorized observer set for a room (#17).
// Only the authorized desktop may call it (mirrors handleSetManager) — it is the
// authority that decides who is a read-only observer, so a CLI agent can't grant
// itself observer (and thus read-all) access.
func (h *Hub) handleSetObservers(c *Client, req types.Request) {
	if !c.requireDesktopAuthorized(req, "yalnızca yetkili desktop istemcisi observer atayabilir") {
		return
	}

	var data struct {
		Observers []string `json:"observers"`
	}
	if !c.decodeRequestData(req, &data, "invalid set_observers payload") {
		return
	}
	if !c.requireValidNonEmptyNames(req, data.Observers) {
		return
	}

	room := h.resolveRoom(req.Room)
	h.setConfiguredObservers(room, data.Observers)

	text := fmt.Sprintf("'%s' odası için %d observer atandı.", room, len(data.Observers))
	c.sendText(req.ID, req.Type, text)
}

func (h *Hub) handleSubscribe(c *Client, req types.Request) {
	var data struct {
		Rooms []string `json:"rooms"`
	}
	if !c.decodeRequestData(req, &data, "invalid subscribe payload") {
		return
	}

	// Reject invalid room names before creating any subscription (same filename
	// safety as join_room — see handleJoinRoom).
	if !c.requireValidRoomNames(req, data.Rooms) {
		return
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

	c.sendOK(req.ID, req.Type)
}

func (h *Hub) handleJoinRoom(c *Client, req types.Request) {
	var data struct {
		AgentName string `json:"agent_name"`
		Role      string `json:"role"`
	}
	if !c.decodeRequestData(req, &data, "invalid join_room payload") {
		return
	}

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
	if !c.requireValidName(req, data.AgentName) {
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

	c.sendSuccess(req.ID, req.Type, map[string]any{"text": text, "agents": agents})

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
	if !c.decodeRequestData(req, &data, "invalid send_message payload") {
		return
	}

	room := h.resolveRoom(req.Room)

	if !c.requireJoinedRoom(req, room, "önce join_room çağırmalısınız", "yalnızca katıldığınız odada mesaj gönderebilirsiniz: %s") {
		return
	}

	if !c.requireValidName(req, data.From) {
		return
	}
	if data.From != c.agentName {
		c.sendError(req.ID, req.Type, "from_agent yalnızca kendi adınız olabilir")
		return
	}
	if data.To != "all" {
		if !c.requireValidName(req, data.To) {
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
	// Nobody may address a DIRECT message to an observer (#17): it is a read-only
	// outside eye that talks only to the user, never a routing target. Reject before
	// any routing/recording. Two signals so a revoked-but-still-joined observer is
	// still protected: the desktop allow-list (configured) OR the live roster role
	// (joined as observer, even if just de-configured). Broadcasts (to="all") are
	// fine — the observer just watches them — so only a direct recipient is checked.
	if data.To != "all" && (h.isConfiguredObserver(room, data.To) || roomState.IsObserver(data.To)) {
		c.sendError(req.ID, req.Type, "observer'a doğrudan mesaj gönderilemez; observer yalnızca odayı izler ve kullanıcıyla konuşur")
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

	c.sendSuccess(req.ID, req.Type, map[string]any{"text": text, "message_id": msg.ID})

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

	if !c.requireJoinedRoom(req, room, "önce join_room çağırmalısınız", "yalnızca katıldığınız odadan mesaj okuyabilirsiniz: %s") {
		return
	}
	if !c.requireValidName(req, data.AgentName) {
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
		c.sendText(req.ID, req.Type, "\U0001f4ed Yeni mesaj yok.")
		return
	}

	c.sendText(req.ID, req.Type, formatAgentMessages(filtered, totalCount, data.Limit))
}

func formatAgentMessages(messages []types.Message, totalCount, limit int) string {
	var sb strings.Builder
	writeMessagesHeader(&sb, len(messages), totalCount, limit, "")
	for _, msg := range messages {
		writeAgentMessageLine(&sb, msg)
		fmt.Fprintf(&sb, "  (ID: %d)\n\n", msg.ID)
	}
	return sb.String()
}

func (h *Hub) handleGetAllMessages(c *Client, req types.Request) {
	var data struct {
		SinceID int `json:"since_id"`
		Limit   int `json:"limit"`
	}
	data.Limit = 15
	if !c.decodeRequestData(req, &data, "invalid get_all_messages payload") {
		return
	}

	room := h.resolveRoom(req.Room)
	roomState := h.getRoom(room)

	if !h.authorizeReadAllMessages(c, req, room, roomState) {
		return
	}
	var filtered []types.Message
	totalCount := 0
	if roomState != nil {
		filtered, totalCount = roomState.ReadAllMessages(data.SinceID, data.Limit)
	}

	if len(filtered) == 0 {
		c.sendText(req.ID, req.Type, "\U0001f4ed Yeni mesaj yok.")
		return
	}

	c.sendText(req.ID, req.Type, formatAllMessages(filtered, totalCount, data.Limit))
}

func formatAllMessages(messages []types.Message, totalCount, limit int) string {
	var sb strings.Builder
	writeMessagesHeader(&sb, len(messages), totalCount, limit, " (tümü)")
	for _, msg := range messages {
		writeAllMessagesLine(&sb, msg)
		sb.WriteString("\n")
	}
	return sb.String()
}

func writeMessagesHeader(sb *strings.Builder, messageCount, totalCount, limit int, suffix string) {
	if limit > 0 && totalCount > limit {
		fmt.Fprintf(sb, "\U0001f4ec Son %d mesaj (toplam %d):\n\n", limit, totalCount)
		return
	}
	fmt.Fprintf(sb, "\U0001f4ec %d mesaj%s:\n\n", messageCount, suffix)
}

func (h *Hub) authorizeReadAllMessages(c *Client, req types.Request, room string, roomState *RoomState) bool {
	// Only the active manager or authorized desktop app can read all messages.
	if c.agentName == "" {
		if !c.isDesktopAuthorized() {
			c.sendError(req.ID, req.Type, "önce yetkili desktop identify veya join_room çağırmalısınız")
			return false
		}
		return true
	}

	if c.joinedRoom != room {
		c.sendError(req.ID, req.Type, fmt.Sprintf("yalnızca katıldığınız odadan mesaj okuyabilirsiniz: %s", c.joinedRoom))
		return false
	}

	activeManager := ""
	if roomState != nil {
		activeManager = roomState.GetActiveManager()
	}
	isManager := activeManager != "" && sameAgentName(c.agentName, activeManager)
	// Observer agents (#17) get read-only access to the full transcript. Authorize
	// from the DESKTOP allow-list (isConfiguredObserver), not the room roster: this
	// is revocable — removing an agent from the observer set via set_observers
	// immediately drops its read-all access even while it stays connected.
	isObserver := h.isConfiguredObserver(room, c.agentName)
	if !isManager && !isObserver {
		c.sendError(req.ID, req.Type, "yalnızca aktif manager veya observer tüm mesajları okuyabilir")
		return false
	}
	if isManager {
		roomState.TouchManagerHeartbeat(c.agentName)
	} else if roomState != nil {
		// Refresh the observer's last_seen so a read_all-only poller is not
		// stale-evicted (the read path itself doesn't touch last_seen). Without
		// this the observer would lose read_all access after staleTimeout.
		roomState.TouchAgentLastSeen(c.agentName)
	}
	return true
}

func writeAgentMessageLine(sb *strings.Builder, msg types.Message) {
	ts := parseTimestamp(msg.Timestamp)
	if msg.Type == "system" {
		fmt.Fprintf(sb, "[%s] %s\n", ts, sanitize(msg.Content))
		return
	}
	if msg.To == "all" {
		fmt.Fprintf(sb, "[%s] %s \u2192 HERKESE: %s\n", ts, sanitize(msg.From), sanitize(msg.Content))
		return
	}
	if msg.OriginalTo != "" && msg.OriginalTo != msg.To {
		fmt.Fprintf(sb, "[%s] %s \u2192 %s (orijinal: %s): %s\n",
			ts, sanitize(msg.From), sanitize(msg.To), sanitize(msg.OriginalTo), sanitize(msg.Content))
		return
	}
	fmt.Fprintf(sb, "[%s] %s \u2192 %s: %s\n", ts, sanitize(msg.From), sanitize(msg.To), sanitize(msg.Content))
}

func writeAllMessagesLine(sb *strings.Builder, msg types.Message) {
	ts := parseTimestamp(msg.Timestamp)
	if msg.Type == "system" {
		fmt.Fprintf(sb, "[%s] SYSTEM: %s\n", ts, sanitize(msg.Content))
		return
	}
	contentPreview := msg.Content
	if len(contentPreview) > 100 {
		contentPreview = contentPreview[:100]
	}
	if msg.OriginalTo != "" && msg.OriginalTo != msg.To {
		fmt.Fprintf(sb, "[%s] #%d %s \u2192 %s (orijinal: %s): %s\n",
			ts, msg.ID, sanitize(msg.From), sanitize(msg.To), sanitize(msg.OriginalTo), sanitize(contentPreview))
		return
	}
	fmt.Fprintf(sb, "[%s] #%d %s \u2192 %s: %s\n", ts, msg.ID, sanitize(msg.From), sanitize(msg.To), sanitize(contentPreview))
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
		c.sendText(req.ID, req.Type, "\U0001f465 Odada kimse yok.")
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

	c.sendText(req.ID, req.Type, sb.String())
}

func (h *Hub) handleLeaveRoom(c *Client, req types.Request) {
	var data struct {
		AgentName string `json:"agent_name"`
	}
	json.Unmarshal(req.Data, &data)

	room := h.resolveRoom(req.Room)

	if !c.requireValidName(req, data.AgentName) {
		return
	}
	if !c.requireJoinedRoom(req, room, "önce join_room çağırmalısınız", "yalnızca katıldığınız odadan ayrılabilirsiniz: %s") {
		return
	}
	if data.AgentName != c.agentName {
		c.sendError(req.ID, req.Type, "yalnızca kendi adınızla leave_room çağırabilirsiniz")
		return
	}

	roomState := h.getOrCreateRoom(room)
	sysMsg, found := roomState.Leave(data.AgentName)

	if !found {
		c.sendText(req.ID, req.Type, fmt.Sprintf("\u26a0\ufe0f '%s' zaten odada değil.", data.AgentName))
		return
	}

	c.sendText(req.ID, req.Type, fmt.Sprintf("\U0001f44b '%s' odadan ayrıldı.", data.AgentName))
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
		if !c.requireJoinedRoom(req, room, "önce join_room çağırmalısınız", "yalnızca katıldığınız odayı temizleyebilirsiniz: %s") {
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
	if err := validation.ValidateName(room); err != nil {
		c.sendError(req.ID, req.Type, fmt.Sprintf("geçersiz oda adı: %v", err))
		return
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
	c.sendText(req.ID, req.Type, text)

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

	if !c.requireDesktopAuthorized(req, "yalnızca yetkili desktop odayı arşivleyebilir") {
		return
	}
	if err := validation.ValidateName(room); err != nil {
		c.sendError(req.ID, req.Type, fmt.Sprintf("geçersiz oda adı: %v", err))
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
	c.sendSuccess(req.ID, req.Type, map[string]any{"text": text, "archived": len(msgs)})
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

	if !c.requireDesktopAuthorized(req, "yalnızca yetkili desktop session kaydedebilir") {
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
	c.sendSuccess(req.ID, req.Type, map[string]any{"text": text, "saved": saved, "count": count})
}

// handleLogMessage records an out-of-band human→agent prompt in the room
// transcript as a "user_prompt" message (#29). Desktop-authorized only: the
// "from" identity is server-forced to the user sentinel so a logged prompt can
// never be confused with — or spoof — real agent traffic. The message goes
// through the normal append/broadcast path so it is snapshotted, archived, and
// shown live, but the orchestrator must skip user_prompt so it is never injected
// back into agent terminals (it was already delivered to the target PTY).
func (h *Hub) handleLogMessage(c *Client, req types.Request) {
	if !c.requireDesktopAuthorized(req, "yalnızca yetkili desktop prompt loglayabilir") {
		return
	}

	var data struct {
		To        string `json:"to"`
		Content   string `json:"content"`
		Timestamp string `json:"timestamp"`
	}
	data.To = "all"
	if !c.decodeRequestData(req, &data, "invalid log_message payload") {
		return
	}

	room := h.resolveRoom(req.Room)
	// room feeds getOrCreateRoom → persistRoom (hub-state/{room}.json), so reject a
	// traversal name here the same way clear_room/archive_room/delete_room do, rather
	// than materializing an unvalidated room in memory that would be written to disk.
	if !c.requireValidName(req, room) {
		return
	}

	if data.To != "all" {
		if !c.requireValidName(req, data.To) {
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
	msg := roomState.LogUserPrompt(types.UserPromptFrom, data.To, content, data.Timestamp)

	c.sendSuccess(req.ID, req.Type, map[string]any{"ok": true, "message_id": msg.ID})

	h.broadcastEvent(room, "message_new", map[string]any{"message": msg})
}

// handleReadSummary returns the newest saved per-session summary for a room
// (#29). Readable by any agent joined to the room (so a continuing agent can pull
// prior context cheaply instead of the whole history) or by the authorized
// desktop.
func (h *Hub) handleReadSummary(c *Client, req types.Request) {
	room := h.resolveRoom(req.Room)

	if !c.requireJoinedRoomOrDesktop(req, room, "önce join_room veya yetkili desktop identify çağırmalısınız", "yalnızca katıldığınız odanın özetini okuyabilirsiniz: %s") {
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
		c.sendText(req.ID, req.Type, "\U0001f4ed Bu oda için henüz özet yok.")
		return
	}

	text := fmt.Sprintf("\U0001f4dd Önceki session özeti (%s):\n\n%s", doc.CreatedAt, doc.Text)
	c.sendText(req.ID, req.Type, text)
}

func (h *Hub) handleGetLastMessageID(c *Client, req types.Request) {
	var data struct {
		AgentName string `json:"agent_name"`
	}
	json.Unmarshal(req.Data, &data)

	room := h.resolveRoom(req.Room)
	// room feeds getOrCreateRoom → persistRoom (hub-state/{room}.json); validate it as a
	// filename so a desktop-authorized caller can't materialize a traversal-named room.
	if !c.requireValidName(req, room) {
		return
	}

	if !c.requireJoinedRoomOrDesktop(req, room, "önce yetkili desktop identify veya join_room çağırmalısınız", "yalnızca katıldığınız odadan sorgulama yapabilirsiniz: %s") {
		return
	}
	if c.agentName != "" {
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

	c.sendSuccess(req.ID, req.Type, map[string]any{"last_id": lastID})
}

// handleGetAgents returns raw agent data for a room (used by desktop app).
func (h *Hub) handleGetAgents(c *Client, req types.Request) {
	if !c.requireDesktopAuthorized(req, "yalnızca yetkili desktop istemcisi agent listesini ham biçimde okuyabilir") {
		return
	}

	room := h.resolveRoom(req.Room)
	c.sendSuccess(req.ID, req.Type, map[string]any{"agents": h.roomAgentsSnapshot(room)})
}

// handleGetMessagesRaw returns raw message data for a room (used by desktop app).
func (h *Hub) handleGetMessagesRaw(c *Client, req types.Request) {
	if !c.requireDesktopAuthorized(req, "yalnızca yetkili desktop istemcisi mesajları ham biçimde okuyabilir") {
		return
	}

	room := h.resolveRoom(req.Room)
	c.sendSuccess(req.ID, req.Type, map[string]any{"messages": h.roomMessagesSnapshot(room)})
}

func (h *Hub) roomAgentsSnapshot(room string) map[string]types.Agent {
	if roomState := h.getRoom(room); roomState != nil {
		return roomState.GetAgents()
	}
	return map[string]types.Agent{}
}

func (h *Hub) roomMessagesSnapshot(room string) []types.Message {
	if roomState := h.getRoom(room); roomState != nil {
		return roomState.GetMessages()
	}
	return []types.Message{}
}

func (h *Hub) handleListRooms(c *Client, req types.Request) {
	h.mu.RLock()
	infos := ListRoomInfos(h.rooms)
	defaultRoom := h.defaultRoom
	h.mu.RUnlock()

	if len(infos) == 0 {
		c.sendText(req.ID, req.Type, "\U0001f4ad Henüz hiç oda yok.")
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

	c.sendText(req.ID, req.Type, sb.String())
}

// handleListRoomsDetailed returns structured room summaries for the desktop room
// browser (orphan rooms included). Desktop-authorized only \u2014 agents must not see
// other teams' history.
func (h *Hub) handleListRoomsDetailed(c *Client, req types.Request) {
	if !c.requireDesktopAuthorized(req, "yaln\u0131zca yetkili desktop istemcisi oda listesini ayr\u0131nt\u0131l\u0131 okuyabilir") {
		return
	}

	h.mu.RLock()
	summaries := ListRoomSummaries(h.rooms, h.defaultRoom)
	h.mu.RUnlock()

	c.sendSuccess(req.ID, req.Type, map[string]any{"rooms": summaries})
}

// handleDeleteRoom removes an orphan room's live state + persisted state file. Only the
// authorized desktop may call it; the default room and any subscribed/live room are
// refused (the authoritative orphan check — "no team owns this name" — lives in app.go,
// where teams are known). The append-only archive (hub-state/archive/{room}.jsonl) and
// session snapshots (hub-state/sessions/{room}/) are PRESERVED — only the live snapshot
// goes. A tombstone keeps the persist loop from resurrecting the file.
func (h *Hub) handleDeleteRoom(c *Client, req types.Request) {
	if !c.requireDesktopAuthorized(req, "yalnızca yetkili desktop istemcisi oda silebilir") {
		return
	}
	room := h.resolveRoom(req.Room)
	// room feeds os.Remove below; validate it as a filename the same way every other
	// file-touching handler does (archive.go, session.go, transcript.go) — rejects
	// path traversal ("..", "/") so a crafted name can't delete files outside hub-state.
	if err := validation.ValidateName(room); err != nil {
		c.sendError(req.ID, req.Type, fmt.Sprintf("geçersiz oda adı: %v", err))
		return
	}
	if room == h.defaultRoom {
		c.sendError(req.ID, req.Type, "varsayılan oda silinemez")
		return
	}

	h.mu.Lock()
	if len(h.subs[room]) > 0 {
		h.mu.Unlock()
		c.sendError(req.ID, req.Type, fmt.Sprintf("'%s' odası aktif (aboneleri var); silinemez", room))
		return
	}
	h.deletedRooms[room] = true
	delete(h.rooms, room)
	delete(h.subs, room)
	delete(h.roomManager, room)
	delete(h.roomObservers, room)
	h.mu.Unlock()

	// Forget the room's last-snapshot signature so a later room reusing this name isn't
	// wrongly skipped by the unchanged-check on its first session save (same reason
	// clear_room resets it). resetSessionTracking takes its own sessionMu.
	h.resetSessionTracking(room)

	// Remove ONLY the live state file (+ stray temp). Archive + session snapshots stay.
	// Confine the path to hub-state with a cleaned-prefix containment check — defense in
	// depth beyond the ValidateName above, and (matching sessionsDir) provably safe to
	// static path-injection analysis since room is a user-influenced path segment.
	stateDir := filepath.Join(h.dataDir, "hub-state")
	stateFile := filepath.Join(stateDir, room+".json")
	if !strings.HasPrefix(stateFile, stateDir+string(os.PathSeparator)) {
		c.sendError(req.ID, req.Type, fmt.Sprintf("geçersiz oda yolu: %q", room))
		return
	}
	if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
		h.logger.Printf("delete_room: state dosyası kaldırılamadı (%s): %v", room, err)
	}
	os.Remove(stateFile + ".tmp")

	text := fmt.Sprintf("\U0001f5d1️ '%s' odası silindi.", room)
	c.sendText(req.ID, req.Type, text)
}
