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
	// archiveFn, if set, receives messages that are about to leave the room
	// (dropped by truncation or wiped by Clear) so they can be archived before
	// they are lost. It is always invoked OUTSIDE the room lock. The wired hub
	// callback normally hands off to an async writer, but may write synchronously
	// when the backlog is saturated or during shutdown, so it can block briefly
	// on disk I/O — never call it while holding the room lock. nil means
	// archiving is disabled (backward compatible).
	archiveFn func([]types.Message)
}

// SetArchiveFn installs the callback that receives messages leaving the room.
// Passing nil disables archiving. Safe to call concurrently.
func (r *RoomState) SetArchiveFn(fn func([]types.Message)) {
	r.mu.Lock()
	r.archiveFn = fn
	r.mu.Unlock()
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

	r.cleanupStaleLocked()

	if _, exists := r.agents[agentName]; exists {
		r.mu.Unlock()
		return types.Message{}, nil, fmt.Errorf("agent adı '%s' bu odada zaten kullanımda", agentName)
	}

	isManager := strings.EqualFold(strings.TrimSpace(role), "manager")
	if isManager {
		if active := r.getActiveManagerLocked(); active != "" && active != agentName {
			r.mu.Unlock()
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
	// A MANAGER's own join must not truncate: it would drop history out from under
	// the manager's first read_all_messages. Every other system message (a
	// non-manager join, any leave) DOES go through the cap, so a flapping agent's
	// connect/disconnect churn can't grow the room unbounded.
	var dropped []types.Message
	if isManager {
		r.messages = append(r.messages, sysMsg)
	} else {
		dropped = r.appendMessageLocked(sysMsg)
	}
	r.dirty = true

	agentsCopy := r.copyAgentsLocked()
	fn := r.archiveFn
	r.mu.Unlock()

	if len(dropped) > 0 && fn != nil {
		fn(dropped)
	}
	return sysMsg, agentsCopy, nil
}

// SendMessage adds a message to the room.
func (r *RoomState) SendMessage(from, to, content string, expectsReply bool, priority string, opts SendOptions) (types.Message, error) {
	r.mu.Lock()

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
	dropped := r.appendMessageLocked(msg)

	r.dirty = true
	fn := r.archiveFn
	r.mu.Unlock()

	if len(dropped) > 0 && fn != nil {
		fn(dropped)
	}
	return msg, nil
}

// LogUserPrompt records an out-of-band human→agent prompt in the transcript as a
// "user_prompt" message (#29). Unlike SendMessage this is not agent traffic: no
// manager routing, no expects_reply semantics — it is purely a record so the
// prompts the user gave each agent become part of the summarized history. It goes
// through the normal cap/archive path so it is snapshotted and archived like any
// message. Callers MUST NOT re-inject it into agent terminals (it was already
// delivered to the target agent's PTY); the orchestrator skips this type.
func (r *RoomState) LogUserPrompt(from, to, content string) types.Message {
	r.mu.Lock()
	msg := types.Message{
		ID:        r.nextID(),
		From:      from,
		To:        to,
		Content:   content,
		Timestamp: types.Timestamp(),
		Type:      types.MsgTypeUserPrompt,
		Priority:  "normal",
	}
	dropped := r.appendMessageLocked(msg)
	r.dirty = true
	fn := r.archiveFn
	r.mu.Unlock()

	if len(dropped) > 0 && fn != nil {
		fn(dropped)
	}
	return msg
}

// appendMessageLocked appends msg and, if the room exceeds the cap, truncates to
// the retained tail — returning the dropped (oldest) messages as a cheap copy so
// the caller can archive them AFTER releasing the lock. The retained tail is
// moved into a fresh array so the old backing array (holding the dropped
// messages) becomes garbage immediately.
//
// Used by SendMessage, Leave, and non-manager Join — i.e. every append path
// EXCEPT a manager's own join, which must not truncate before the manager's
// first read_all_messages. Capping leave and non-manager-join system messages
// keeps connect/disconnect churn from growing the room (and its snapshot)
// without bound, while still preserving the manager-join read.
//
// Durability note: truncation archiving is asynchronous (the design's hot-path
// requirement), so a crash after the periodic snapshot persists the truncated
// room but before the async writer drains would lose the dropped batch. The
// window is microseconds (the writer is always ready) versus the 5s snapshot
// interval; a crash-recovery queue is possible future work, out of scope here.
// Must hold r.mu.
func (r *RoomState) appendMessageLocked(msg types.Message) (dropped []types.Message) {
	r.messages = append(r.messages, msg)
	if len(r.messages) > maxMessagesInRoom {
		cut := len(r.messages) - truncateToMessages
		dropped = make([]types.Message, cut)
		copy(dropped, r.messages[:cut])
		retained := make([]types.Message, truncateToMessages, maxMessagesInRoom+1)
		copy(retained, r.messages[cut:])
		r.messages = retained
	}
	return dropped
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
		// user_prompt is a transcript-only record of a prompt already delivered to
		// the target agent's PTY; surfacing it on read would make the agent
		// re-handle its own instruction (#29). It stays in the raw read + the
		// summary transcript, just not in agent-facing reads.
		if msg.Type == types.MsgTypeUserPrompt {
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
		if m.ID <= sinceID {
			continue
		}
		// Exclude transcript-only user_prompt records from the manager's read too,
		// so a polling manager doesn't re-route a prompt the user already gave an
		// agent directly (#29). Still present in raw read + summary transcript.
		if m.Type == types.MsgTypeUserPrompt {
			continue
		}
		filtered = append(filtered, m)
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

	if _, ok := r.agents[agentName]; !ok {
		r.mu.Unlock()
		return types.Message{}, false
	}

	delete(r.agents, agentName)
	if sameAgentName(r.managerAgent, agentName) {
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
	// Leave goes through the cap: nobody reads right after a leave, so truncating
	// here is safe and keeps connect/disconnect churn from growing the room.
	dropped := r.appendMessageLocked(sysMsg)
	r.dirty = true
	fn := r.archiveFn
	r.mu.Unlock()

	if len(dropped) > 0 && fn != nil {
		fn(dropped)
	}
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
	if sameAgentName(active, agentName) {
		r.managerLastSeen = types.Now()
	}
	return active
}

// TouchManagerHeartbeat updates manager heartbeat if this agent is active manager.
func (r *RoomState) TouchManagerHeartbeat(agentName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.isActiveManagerLocked(agentName) {
		r.managerLastSeen = types.Now()
		return true
	}
	return false
}

// ResetManagerLockIfDifferent clears active manager lock unless it matches managerAgent.
// The match is case-insensitive (sameAgentName): re-affirming the same manager via a
// different spelling — e.g. configured "pilot" while an agent is locked as "Pilot" —
// must NOT drop the lock. If managerAgent is empty, the lock is always cleared.
func (r *RoomState) ResetManagerLockIfDifferent(managerAgent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sameAgentName(r.managerAgent, managerAgent) {
		return
	}
	if r.managerAgent != "" || r.managerLastSeen != 0 {
		r.managerAgent = ""
		r.managerLastSeen = 0
		r.dirty = true
	}
}

// ClearArchived wipes all agents and every message with ID <= maxID, keeping any
// messages that arrived AFTER the snapshot was archived (ID > maxID). Archiving
// the history for this destructive path is the caller's responsibility
// (handleClearRoom archives synchronously and refuses to clear on failure); by
// only wiping up to the archived maxID, a message that races the clear (sent
// while the archive I/O ran with the lock released) is preserved rather than
// silently lost. An empty snapshot (maxID == 0) wipes nothing: any message
// present has ID > 0 and is kept (it raced in after the snapshot).
func (r *RoomState) ClearArchived(maxID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Keep only messages newer than the archived snapshot. messages are
	// append-ordered by ID, so binary-search the first kept one. This handles an
	// empty snapshot (maxID == 0) correctly: a message that raced in afterwards
	// has ID > 0 and is kept rather than wiped — no special "wipe all" branch,
	// which would have dropped exactly that racing message.
	idx := sort.Search(len(r.messages), func(i int) bool {
		return r.messages[i].ID > maxID
	})
	if idx > 0 {
		// Some messages dropped: move the kept tail into a fresh array so the old
		// backing array is freed. When idx == 0 nothing is dropped, so leave
		// r.messages as-is rather than reallocating.
		retained := make([]types.Message, len(r.messages)-idx)
		copy(retained, r.messages[idx:])
		r.messages = retained
	}

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

	// Return the last AGENT-VISIBLE message ID. user_prompt records are filtered
	// from read_messages/read_all_messages, so including them here would let an
	// agent seed its polling cursor past unread visible messages and skip them.
	for i := len(r.messages) - 1; i >= 0; i-- {
		if r.messages[i].Type != types.MsgTypeUserPrompt {
			return r.messages[i].ID
		}
	}
	return 0
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

// Summary returns structured metadata about the room for the desktop room browser.
// It does NOT run stale cleanup, so the persisted agent map is reported verbatim
// (rooms whose agents went idle still show the names they were created with).
func (r *RoomState) Summary(name string, isDefault bool) types.RoomSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	lastActivity := ""
	if len(r.messages) > 0 {
		lastActivity = r.messages[len(r.messages)-1].Timestamp
	}

	return types.RoomSummary{
		Name:         name,
		MessageCount: len(r.messages),
		Agents:       r.copyAgentsLocked(),
		LastActivity: lastActivity,
		IsDefault:    isDefault,
	}
}

// ListRoomSummaries returns structured summaries for all rooms, sorted by last
// activity descending (most recent first); rooms with no activity sort last.
func ListRoomSummaries(rooms map[string]*RoomState, defaultRoom string) []types.RoomSummary {
	summaries := make([]types.RoomSummary, 0, len(rooms))
	for name, room := range rooms {
		summaries = append(summaries, room.Summary(name, name == defaultRoom))
	}
	sort.Slice(summaries, func(i, j int) bool {
		a, b := summaries[i].LastActivity, summaries[j].LastActivity
		if a == b {
			return summaries[i].Name < summaries[j].Name
		}
		if a == "" {
			return false
		}
		if b == "" {
			return true
		}
		return a > b
	})
	return summaries
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

// isActiveManagerLocked reports whether name is the room's active manager,
// resolving a stale lock first and comparing case-insensitively. Must hold mu.
func (r *RoomState) isActiveManagerLocked(name string) bool {
	return sameAgentName(r.getActiveManagerLocked(), name)
}

// sameAgentName reports whether two agent names denote the same identity,
// comparing case-insensitively after trimming surrounding whitespace. Empty
// names never match — an absent manager lock is nobody's identity. Manager
// identity must not depend on casing: an agent configured as "pilot" and one
// that joins as "Pilot" are the same agent. This keeps the hub (the routing
// authority) consistent with team.Team.IsManagerAgent and app.go's
// resolveManagerIntent, which already normalize the same way.
func sameAgentName(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
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
