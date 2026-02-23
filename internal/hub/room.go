package hub

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"desktop/internal/types"
)

const (
	maxMessagesInRoom  = 500
	truncateToMessages = 300
	maxFieldLength     = 32000
	staleTimeout       = 300 // seconds
	managerTimeoutSec  = 300
)

// RoomState holds in-memory state for a single chat room.
type RoomState struct {
	mu              sync.RWMutex
	messages        []types.Message
	agents          map[string]types.Agent
	dirty           bool
	managerAgent    string
	managerLastSeen float64
}

// NewRoomState creates an empty room.
func NewRoomState() *RoomState {
	return &RoomState{
		messages: []types.Message{},
		agents:   make(map[string]types.Agent),
	}
}

// PersistedRoom is the JSON-serializable form of a room.
type PersistedRoom struct {
	Messages []types.Message        `json:"messages"`
	Agents   map[string]types.Agent `json:"agents"`
}

// SendOptions carries optional routing metadata.
type SendOptions struct {
	OriginalTo      string
	RoutedByManager bool
}

// nextID returns the next message ID.
func (r *RoomState) nextID() int {
	if len(r.messages) == 0 {
		return 1
	}
	return r.messages[len(r.messages)-1].ID + 1
}

// Join adds an agent to the room, returning the system message and current agents.
func (r *RoomState) Join(agentName, role string) (types.Message, map[string]types.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cleanupStaleLocked()

	if _, exists := r.agents[agentName]; exists {
		return types.Message{}, nil, fmt.Errorf("agent adı '%s' bu odada zaten kullanımda", agentName)
	}

	if strings.EqualFold(strings.TrimSpace(role), "manager") {
		if active := r.getActiveManagerLocked(); active != "" && active != agentName {
			return types.Message{}, nil, fmt.Errorf("bu odada aktif manager var: %s", active)
		}
		r.managerAgent = agentName
		r.managerLastSeen = types.Now()
	}

	r.agents[agentName] = types.Agent{
		Role:     role,
		JoinedAt: types.Timestamp(),
		LastSeen: types.Now(),
	}

	content := fmt.Sprintf("\U0001f7e2 %s odaya katıldı", agentName)
	if role != "" {
		content += fmt.Sprintf(" (Rol: %s)", role)
	}

	sysMsg := types.Message{
		ID:        r.nextID(),
		From:      "SYSTEM",
		To:        "all",
		Content:   content,
		Timestamp: types.Timestamp(),
		Type:      "system",
	}
	r.messages = append(r.messages, sysMsg)
	r.dirty = true

	agentsCopy := r.copyAgentsLocked()
	return sysMsg, agentsCopy, nil
}

// SendMessage adds a message to the room.
func (r *RoomState) SendMessage(from, to, content string, expectsReply bool, priority string, opts SendOptions) (types.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Update sender's last_seen
	if agent, ok := r.agents[from]; ok {
		agent.LastSeen = types.Now()
		r.agents[from] = agent
	}

	msgType := "broadcast"
	if to != "all" {
		msgType = "direct"
	}

	msg := types.Message{
		ID:              r.nextID(),
		From:            from,
		To:              to,
		OriginalTo:      opts.OriginalTo,
		Content:         content,
		Timestamp:       types.Timestamp(),
		Type:            msgType,
		RoutedByManager: opts.RoutedByManager,
		ExpectsReply:    expectsReply,
		Priority:        priority,
	}
	r.messages = append(r.messages, msg)

	// Truncate if needed
	if len(r.messages) > maxMessagesInRoom {
		r.messages = r.messages[len(r.messages)-truncateToMessages:]
	}

	r.dirty = true
	return msg, nil
}

// ReadMessages returns filtered messages for an agent.
func (r *RoomState) ReadMessages(agentName string, sinceID, limit int, unreadOnly bool) ([]types.Message, int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Update last_seen
	if agent, ok := r.agents[agentName]; ok {
		agent.LastSeen = types.Now()
		r.agents[agentName] = agent
		r.dirty = true
	}

	var filtered []types.Message
	for _, msg := range r.messages {
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

	totalCount := len(filtered)
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}

	return filtered, totalCount
}

// ReadAllMessages returns all messages after sinceID, optionally limited.
func (r *RoomState) ReadAllMessages(sinceID, limit int) ([]types.Message, int) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var filtered []types.Message
	for _, m := range r.messages {
		if m.ID > sinceID {
			filtered = append(filtered, m)
		}
	}

	totalCount := len(filtered)
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}

	return filtered, totalCount
}

// ListAgents returns active agents, cleaning up stale ones.
func (r *RoomState) ListAgents(agentName string) map[string]types.Agent {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cleanupStaleLocked()

	if agentName != "" {
		if agent, ok := r.agents[agentName]; ok {
			agent.LastSeen = types.Now()
			r.agents[agentName] = agent
			r.dirty = true
		}
	}

	return r.copyAgentsLocked()
}

// Leave removes an agent from the room, returning a system message.
func (r *RoomState) Leave(agentName string) (types.Message, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.agents[agentName]; !ok {
		return types.Message{}, false
	}

	delete(r.agents, agentName)
	if r.managerAgent == agentName {
		r.managerAgent = ""
		r.managerLastSeen = 0
	}

	sysMsg := types.Message{
		ID:        r.nextID(),
		From:      "SYSTEM",
		To:        "all",
		Content:   fmt.Sprintf("\U0001f534 %s odadan ayrıldı", agentName),
		Timestamp: types.Timestamp(),
		Type:      "system",
	}
	r.messages = append(r.messages, sysMsg)
	r.dirty = true

	return sysMsg, true
}

// GetActiveManager returns the active manager agent name, or empty if none.
func (r *RoomState) GetActiveManager() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getActiveManagerLocked()
}

// GetActiveManagerAndTouch atomically checks if the given agent is the active
// manager and refreshes the heartbeat if so. Returns the active manager name.
func (r *RoomState) GetActiveManagerAndTouch(agentName string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	active := r.getActiveManagerLocked()
	if active != "" && active == agentName {
		r.managerLastSeen = types.Now()
	}
	return active
}

// TouchManagerHeartbeat updates manager heartbeat if this agent is active manager.
func (r *RoomState) TouchManagerHeartbeat(agentName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getActiveManagerLocked() == agentName {
		r.managerLastSeen = types.Now()
		return true
	}
	return false
}

// Clear removes all messages and agents.
func (r *RoomState) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = []types.Message{}
	r.agents = make(map[string]types.Agent)
	r.managerAgent = ""
	r.managerLastSeen = 0
	r.dirty = true
}

// GetLastMessageID returns the highest message ID.
func (r *RoomState) GetLastMessageID(agentName string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agentName != "" {
		if agent, ok := r.agents[agentName]; ok {
			agent.LastSeen = types.Now()
			r.agents[agentName] = agent
			r.dirty = true
		}
	}

	if len(r.messages) == 0 {
		return 0
	}
	return r.messages[len(r.messages)-1].ID
}

// GetAgents returns a snapshot of current agents (no cleanup).
func (r *RoomState) GetAgents() map[string]types.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.copyAgentsLocked()
}

// GetMessages returns a snapshot of all messages.
func (r *RoomState) GetMessages() []types.Message {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]types.Message, len(r.messages))
	copy(out, r.messages)
	return out
}

// Snapshot returns the current room state for persistence.
func (r *RoomState) Snapshot() PersistedRoom {
	r.mu.RLock()
	defer r.mu.RUnlock()
	msgs := make([]types.Message, len(r.messages))
	copy(msgs, r.messages)
	return PersistedRoom{
		Messages: msgs,
		Agents:   r.copyAgentsLocked(),
	}
}

// IsDirty returns whether the room has unsaved changes.
func (r *RoomState) IsDirty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dirty
}

// MarkClean clears the dirty flag.
func (r *RoomState) MarkClean() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirty = false
}

// Info returns agent count and message count for listing.
func (r *RoomState) Info() (agentCount, messageCount int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents), len(r.messages)
}

// RoomInfo is used by ListRooms.
type RoomInfo struct {
	Name     string
	Agents   int
	Messages int
}

// ListRoomInfos returns sorted info about all rooms.
func ListRoomInfos(rooms map[string]*RoomState) []RoomInfo {
	var infos []RoomInfo
	for name, room := range rooms {
		ac, mc := room.Info()
		infos = append(infos, RoomInfo{Name: name, Agents: ac, Messages: mc})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
	return infos
}

// -- internal helpers --

func (r *RoomState) cleanupStaleLocked() {
	now := float64(time.Now().UnixNano()) / 1e9
	for name, info := range r.agents {
		if now-info.LastSeen >= float64(staleTimeout) {
			delete(r.agents, name)
			r.dirty = true
		}
	}
	// Clear manager lock if timed out or agent was removed
	r.clearManagerIfStale()
}

// clearManagerIfStale resets manager lock if the manager agent no longer exists
// in the room or if the manager heartbeat has timed out. Must be called with mu held.
func (r *RoomState) clearManagerIfStale() {
	if r.managerAgent == "" {
		return
	}
	if _, ok := r.agents[r.managerAgent]; !ok {
		r.managerAgent = ""
		r.managerLastSeen = 0
		return
	}
	if types.Now()-r.managerLastSeen > float64(managerTimeoutSec) {
		r.managerAgent = ""
		r.managerLastSeen = 0
	}
}

func (r *RoomState) copyAgentsLocked() map[string]types.Agent {
	cp := make(map[string]types.Agent, len(r.agents))
	for k, v := range r.agents {
		cp[k] = v
	}
	return cp
}

func (r *RoomState) getActiveManagerLocked() string {
	r.clearManagerIfStale()
	return r.managerAgent
}

// sanitize strips ANSI escape sequences and control characters.
func sanitize(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	i := 0
	for i < len(s) {
		b := s[i]
		if b == 0x1b && i+1 < len(s) {
			next := s[i+1]
			if next == '[' {
				i += 2
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7E) {
					i++
				}
				if i < len(s) {
					i++
				}
				continue
			}
			if next == ']' {
				i += 2
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				continue
			}
			i += 2
			continue
		}
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			i++
			continue
		}
		if b == 0x7F {
			i++
			continue
		}
		sb.WriteByte(b)
		i++
	}
	return sb.String()
}

// parseTimestamp extracts HH:MM:SS from an ISO timestamp string.
func parseTimestamp(ts string) string {
	t, err := time.Parse("2006-01-02T15:04:05.000000", ts)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05", ts)
		if err != nil {
			return ts
		}
	}
	return t.Format("15:04:05")
}
