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
	// roleObserver is the special, normalized role value (#17) for a read-only
	// "outside eye" agent: it watches the room but is blocked from send_message.
	// Like "manager" it is matched case-insensitively after trimming.
	roleObserver = "observer"
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
	// evictFn, if set, is called for each agent removed by the stale timeout,
	// with how many seconds it had been idle. Unlike archiveFn this runs WITH
	// the room lock held, which is safe only because the wired callback is a
	// non-blocking event-log append — never give this one disk I/O.
	evictFn func(agentName string, idleSeconds float64)
	// resetFn, if set, is called from inside ClearArchived while the room lock is
	// held, with the watermark that was wiped. Ordering it under the lock is the
	// point: once the lock is released the room already accepts new messages that
	// restart at ID 1, and a boundary logged after that would leave them on the
	// wrong side of it. Like evictFn this must not block — the wired callback is
	// a non-blocking event-log append.
	resetFn func(maxID, generation int)
	// connectedFn reports whether an agent still holds a live connection. Stale
	// cleanup consults it so a connected agent is never evicted for going quiet;
	// the timeout then only clears records whose client never came back. Must not
	// block and must not take the room lock — it is called with it held.
	connectedFn func(agentName string) bool
	// protectedFn reports whether an agent must survive stale cleanup even
	// though it looks idle: connected, or inside its grace window. Without the
	// second case a long-quiet agent whose socket blips is deleted by any
	// concurrent list_agents before its departure timer runs, so the reconnect
	// becomes a noisy fresh join — the grace window defeated precisely for the
	// agents it was written for.
	protectedFn func(agentName string) bool
	// generation counts how many times this room has been cleared. Stamped on
	// send and read events UNDER the room lock so the analyzer never has to
	// infer a message's generation from log ordering — a send that stores just
	// before a concurrent clear would otherwise be logged after the boundary and
	// attributed to the fresh room.
	generation int
}

// SetArchiveFn installs the callback that receives messages leaving the room.
// Passing nil disables archiving. Safe to call concurrently.
func (r *RoomState) SetArchiveFn(fn func([]types.Message)) {
	r.mu.Lock()
	r.archiveFn = fn
	r.mu.Unlock()
}

// SetEvictFn installs the callback invoked for each stale-timeout eviction.
// Passing nil disables it. Safe to call concurrently.
func (r *RoomState) SetEvictFn(fn func(agentName string, idleSeconds float64)) {
	r.mu.Lock()
	r.evictFn = fn
	r.mu.Unlock()
}

// SetResetFn installs the callback invoked from inside ClearArchived, under the
// room lock. Passing nil disables it. Safe to call concurrently.
func (r *RoomState) SetResetFn(fn func(maxID, generation int)) {
	r.mu.Lock()
	r.resetFn = fn
	r.mu.Unlock()
}

// SetConnectedFn installs the liveness predicate consulted by stale cleanup.
// Passing nil falls back to timestamp-only behaviour. Safe to call concurrently.
func (r *RoomState) SetConnectedFn(fn func(agentName string) bool) {
	r.mu.Lock()
	r.connectedFn = fn
	r.mu.Unlock()
}

// SetProtectedFn installs the predicate that shields an agent from stale
// cleanup. Falls back to connectedFn when nil.
func (r *RoomState) SetProtectedFn(fn func(agentName string) bool) {
	r.mu.Lock()
	r.protectedFn = fn
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
	// Generation must survive a restart: events stamped before it carry the
	// room's clear count, and reloading at zero would put a persisted message
	// and a later read of that same message in different generations — reporting
	// it unread forever. Omitted when zero so existing state files stay valid.
	Generation int `json:"generation,omitempty"`
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

func archiveDropped(dropped []types.Message, fn func([]types.Message)) {
	if len(dropped) > 0 && fn != nil {
		fn(dropped)
	}
}

// touchAgentLastSeenLocked refreshes an active roster entry. Must hold r.mu.
func (r *RoomState) touchAgentLastSeenLocked(agentName string) {
	if agent, ok := r.agents[agentName]; ok {
		agent.LastSeen = types.Now()
		r.agents[agentName] = agent
		r.dirty = true
	}
}

// touchAgentLastSeenByIdentityLocked refreshes the roster entry matching
// agentName with the same case-insensitive identity rules used for manager and
// observer roles. Must hold r.mu.
func (r *RoomState) touchAgentLastSeenByIdentityLocked(agentName string) {
	for name, agent := range r.agents {
		if sameAgentName(name, agentName) {
			agent.LastSeen = types.Now()
			r.agents[name] = agent
			r.dirty = true
			return
		}
	}
}

// Join adds an agent to the room, returning the system message and current agents.
func (r *RoomState) Join(agentName, role string) (types.Message, map[string]types.Agent, error) {
	return r.join(agentName, role, nil)
}

// JoinWithClaim is Join with the liveness registration folded into the same
// locked transaction.
//
// Claiming after the lock is released leaves a gap: two fresh sockets racing the
// same unused name can both end up live under one identity — the first adds the
// roster entry and pauses before claiming, the second sees nobody connected and
// takes the new entry over. Takeover rechecks atomically now, so this closes the
// other side of the same race.
func (r *RoomState) JoinWithClaim(agentName, role string, claim func()) (types.Message, map[string]types.Agent, error) {
	return r.join(agentName, role, claim)
}

// Takeover reclaims a roster entry the same agent already owns, for a
// reconnecting client whose previous socket is gone but whose entry is still
// held by the grace window. It refreshes liveness and returns the roster
// WITHOUT announcing an arrival: nobody new showed up.
//
// Returns false when there is no entry to reclaim, in which case the caller
// should perform a normal join.
// claim, if non-nil, runs while the room lock is still held, so registering the
// new connection cannot be interleaved by a grace timer that already decided the
// agent was gone.
//
// role matters: the manager routing lock is room state and is NOT persisted, so
// after a hub restart the reloaded roster has the manager present with no lock.
// A reconnecting manager takes this path rather than join, so re-applying the
// lock here is the only thing that brings the gateway back — and it cannot
// self-heal, because while the manager stays connected later joins are rejected
// as a duplicate name.
// heldByOther is evaluated HERE, under the lock, not at the call site: two
// replacement sockets replaying the same name can both see "free" outside it,
// and checking only for the entry's existence would let both succeed — two live
// clients under one identity. It must exclude the requesting connection, so a
// socket repeating a join it already owns still reclaims its own entry.
func (r *RoomState) Takeover(agentName, role string, heldByOther func() bool, claim func()) (map[string]types.Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.agents[agentName]; !exists {
		return nil, false
	}
	if heldByOther != nil && heldByOther() {
		return nil, false
	}
	r.touchAgentLastSeenLocked(agentName)

	// The requested role is authoritative — join_room with role X means "I am X"
	// — so BOTH directions are applied, and the roster entry and the manager
	// lock move together. Handling only the upgrade left a downgraded agent
	// still holding the lock, with the room routing through it while the client
	// had already recorded the lesser role for its next replay.
	agent := r.agents[agentName]
	agent.Role = role
	r.agents[agentName] = agent
	r.dirty = true

	if strings.EqualFold(strings.TrimSpace(role), "manager") {
		// Only when the seat is free or already ours: a live manager under a
		// different name must not be displaced by a reconnect.
		if active := r.getActiveManagerLocked(); active == "" || sameAgentName(active, agentName) {
			r.managerAgent = agentName
			r.managerLastSeen = types.Now()
		}
	} else if sameAgentName(r.managerAgent, agentName) {
		// Downgrade: give up the lock rather than keep routing through an agent
		// that no longer claims the role.
		r.managerAgent = ""
		r.managerLastSeen = 0
	}

	if claim != nil {
		claim()
	}
	return r.copyAgentsLocked(), true
}

// LeaveIfDisconnected removes an agent only if it still has no live connection,
// deciding and removing under ONE hold of the room lock.
//
// Checking connectivity and then leaving as two steps let a boundary-timed
// reconnect slip between them: the join reclaimed the entry and registered its
// connection, and the timer then removed an agent that was live — while the
// client had been told its join succeeded.
func (r *RoomState) LeaveIfDisconnected(agentName string) (types.Message, bool) {
	return r.leaveIf(agentName, func() bool {
		return r.connectedFn == nil || !r.connectedFn(agentName)
	})
}

func (r *RoomState) join(agentName, role string, claim func()) (types.Message, map[string]types.Agent, error) {
	r.mu.Lock()

	r.cleanupStaleLocked()

	if _, exists := r.agents[agentName]; exists {
		r.mu.Unlock()
		return types.Message{}, nil, fmt.Errorf("agent adı '%s' bu odada zaten kullanımda", agentName)
	}

	isManager := strings.EqualFold(strings.TrimSpace(role), "manager")
	isObserver := strings.EqualFold(strings.TrimSpace(role), roleObserver)
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
	// A MANAGER's or OBSERVER's own join must not truncate: it would drop history out
	// from under their first read_all_messages(limit=1000). Every other system message
	// (a normal-agent join, any leave) DOES go through the cap, so a flapping agent's
	// connect/disconnect churn can't grow the room unbounded. Managers are singular and
	// observers are few (desktop-spawned), so their bypass can't be abused for growth.
	var dropped []types.Message
	if isManager || isObserver {
		r.messages = append(r.messages, sysMsg)
	} else {
		dropped = r.appendMessageLocked(sysMsg)
	}
	r.dirty = true

	// Register the connection before releasing the lock, so no other socket can
	// see this brand-new entry as unclaimed and take it over.
	if claim != nil {
		claim()
	}

	agentsCopy := r.copyAgentsLocked()
	fn := r.archiveFn
	r.mu.Unlock()

	archiveDropped(dropped, fn)
	return sysMsg, agentsCopy, nil
}

// SendMessage adds a message to the room.
func (r *RoomState) SendMessage(from, to, content string, expectsReply bool, priority string, opts SendOptions) (types.Message, error) {
	msg, _, _, err := r.SendMessageWithPresence(from, to, content, expectsReply, priority, opts, "")
	return msg, err
}

// SendMessageWithPresence stores the message and, under the SAME room lock,
// reports whether presenceOf was in the roster at that instant.
//
// Reading the roster in a separate call after the send lets a concurrent join or
// leave change the answer, which would make the misaddressed-message report
// (#99) record the opposite of the roster state at the actual send point. An
// empty presenceOf reports false and is what the plain SendMessage passes.
// It also returns the room generation the message was stored in.
func (r *RoomState) SendMessageWithPresence(from, to, content string, expectsReply bool, priority string, opts SendOptions, presenceOf string) (types.Message, bool, int, error) {
	r.mu.Lock()

	// Update sender's last_seen
	r.touchAgentLastSeenLocked(from)

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

	present := false
	if presenceOf != "" {
		_, present = r.agents[presenceOf]
	}
	gen := r.generation

	r.dirty = true
	fn := r.archiveFn
	r.mu.Unlock()

	archiveDropped(dropped, fn)
	return msg, present, gen, nil
}

// LogUserPrompt records an out-of-band human→agent prompt in the transcript as a
// "user_prompt" message (#29). Unlike SendMessage this is not agent traffic: no
// manager routing, no expects_reply semantics — it is purely a record so the
// prompts the user gave each agent become part of the summarized history. It goes
// through the normal cap/archive path so it is snapshotted and archived like any
// message. Callers MUST NOT re-inject it into agent terminals (it was already
// delivered to the target agent's PTY); the orchestrator skips this type.
//
// ts is the delivery-moment timestamp produced app-side (when the prompt was
// written to the agent's PTY). Stamping at delivery rather than at this RPC's
// arrival keeps the prompt ordered before any agent reply in the
// timestamp-sorted transcript, even when the fire-and-forget log goroutine is
// delayed (#58). An empty ts falls back to the current time.
func (r *RoomState) LogUserPrompt(from, to, content, ts string) types.Message {
	if ts == "" {
		ts = types.Timestamp()
	}
	r.mu.Lock()
	msg := types.Message{
		ID:        r.nextID(),
		From:      from,
		To:        to,
		Content:   content,
		Timestamp: ts,
		Type:      types.MsgTypeUserPrompt,
		Priority:  "normal",
	}
	dropped := r.appendMessageLocked(msg)
	r.dirty = true
	fn := r.archiveFn
	r.mu.Unlock()

	archiveDropped(dropped, fn)
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

// ReadMessages returns filtered messages for an agent, the total match count,
// and the room generation the read observed.
func (r *RoomState) ReadMessages(agentName string, sinceID, limit int, unreadOnly bool) ([]types.Message, int, int) {
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

	return filtered, totalCount, r.generation
}

// ReadAllMessages returns all messages after sinceID, optionally limited.
// ReadAllMessages also returns the room generation the read observed.
func (r *RoomState) ReadAllMessages(sinceID, limit int) ([]types.Message, int, int) {
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

	return filtered, totalCount, r.generation
}

// ListAgents returns active agents, cleaning up stale ones.
func (r *RoomState) ListAgents(agentName string) map[string]types.Agent {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cleanupStaleLocked()

	if agentName != "" {
		r.touchAgentLastSeenLocked(agentName)
	}

	return r.copyAgentsLocked()
}

// Leave removes an agent from the room, returning a system message.
func (r *RoomState) Leave(agentName string) (types.Message, bool) {
	return r.leaveIf(agentName, nil)
}

// leaveIf removes an agent, optionally only when allow (evaluated UNDER the
// room lock) says so. Deciding and removing under one hold is what stops a
// boundary-timed reconnect from slipping between the two.
func (r *RoomState) leaveIf(agentName string, allow func() bool) (types.Message, bool) {
	r.mu.Lock()

	if _, ok := r.agents[agentName]; !ok {
		r.mu.Unlock()
		return types.Message{}, false
	}
	if allow != nil && !allow() {
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

	archiveDropped(dropped, fn)
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
		r.touchAgentLastSeenByIdentityLocked(active)
	}
	return active
}

// TouchManagerHeartbeat updates manager heartbeat if this agent is active manager.
// TouchAgentLastSeen refreshes a roster agent's LastSeen, marking it active so
// stale cleanup (which evicts by Agent.LastSeen) doesn't remove an agent that is
// actively polling a read RPC. No-op if the agent isn't in the roster.
func (r *RoomState) TouchAgentLastSeen(agentName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touchAgentLastSeenLocked(agentName)
}

// HasAgent reports whether an agent is currently in the room's roster.
func (r *RoomState) HasAgent(agentName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.agents[agentName]
	return ok
}

// IsObserver reports whether the named agent is currently in the room roster with
// the observer role (#17). Used to reject a DIRECT message addressed to a live
// observer even after the desktop revokes it from the allow-list — its roster entry
// still marks it an observer until it leaves. The roster is matched by sameAgentName
// (case-insensitive), consistent with isConfiguredObserver and the rest of observer
// identity, so a send to "watcher" can't slip past an observer that joined as "Watcher".
func (r *RoomState) IsObserver(agentName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, a := range r.agents {
		if sameAgentName(name, agentName) {
			return strings.EqualFold(strings.TrimSpace(a.Role), roleObserver)
		}
	}
	return false
}

func (r *RoomState) TouchManagerHeartbeat(agentName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.isActiveManagerLocked(agentName) {
		r.managerLastSeen = types.Now()
		r.touchAgentLastSeenByIdentityLocked(agentName)
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
	r.generation++
	r.dirty = true

	// Still holding the lock: the generation boundary is ordered before any
	// message the cleared room can accept, including the ID-1 message a client
	// that is still joined may send the instant the lock is released.
	if r.resetFn != nil {
		// The generation the clear ended: r.generation was just incremented, so
		// the boundary belongs to the one before it.
		r.resetFn(maxID, r.generation-1)
	}
}

// GetLastMessageID returns the highest message ID.
func (r *RoomState) GetLastMessageID(agentName string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agentName != "" {
		r.touchAgentLastSeenLocked(agentName)
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
		Messages:   msgs,
		Agents:     r.copyAgentsLocked(),
		Generation: r.generation,
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

const (
	joinMsgPrefix = "\U0001f7e2 "    // "🟢 " — join system message prefix
	joinMsgInfix  = " odaya katıldı" // text between the agent name and optional role suffix
)

// deriveHistoricalAgents extracts the distinct, sorted set of agent names that have
// joined the room at some point, parsed from join system-messages. Leave messages are
// ignored: the result answers "which agent names were ever created here", which is what
// archived (roster-empty) rooms need. The caller must hold r.mu (it reads r.messages).
func deriveHistoricalAgents(messages []types.Message) []string {
	seen := make(map[string]bool)
	var names []string
	for _, m := range messages {
		if m.Type != types.MsgTypeSystem {
			continue
		}
		if !strings.HasPrefix(m.Content, joinMsgPrefix) {
			continue
		}
		rest := m.Content[len(joinMsgPrefix):]
		idx := strings.Index(rest, joinMsgInfix)
		if idx <= 0 {
			continue
		}
		name := rest[:idx]
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

	agents := r.copyAgentsLocked()
	var historical []string
	if len(agents) == 0 {
		historical = deriveHistoricalAgents(r.messages)
	}

	return types.RoomSummary{
		Name:             name,
		MessageCount:     len(r.messages),
		Agents:           agents,
		HistoricalAgents: historical,
		LastActivity:     lastActivity,
		IsDefault:        isDefault,
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
			// A live connection is proof of life that no timestamp carries: the
			// agent may simply have been working, not gone. An agent inside its
			// grace window is likewise not ours to delete — its departure is
			// already scheduled and a reconnect may still reclaim it.
			protect := r.protectedFn
			if protect == nil {
				protect = r.connectedFn
			}
			if protect != nil && protect(name) {
				continue
			}
			delete(r.agents, name)
			r.dirty = true
			if r.evictFn != nil {
				r.evictFn(name, now-info.LastSeen)
			}
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
