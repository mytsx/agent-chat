package hub

import (
	"testing"

	"desktop/internal/types"
)

func TestObserverJoin_DoesNotClaimManagerLock(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("watcher", "observer"); err != nil {
		t.Fatalf("observer join should succeed: %v", err)
	}
	if got := r.GetActiveManager(); got != "" {
		t.Fatalf("observer must not claim manager lock, got %q", got)
	}
}

// #17 (Codex P2): like a manager, an observer's own join must NOT truncate a
// capped room — otherwise the oldest messages are dropped before the observer's
// first read_all_messages(limit=1000) can see them.
func TestObserverJoin_DoesNotTruncate(t *testing.T) {
	r := NewRoomState()
	for i := 0; i < maxMessagesInRoom; i++ {
		r.SendMessage("a", "all", "msg", false, "normal", SendOptions{})
	}
	if got := len(r.GetMessages()); got != maxMessagesInRoom {
		t.Fatalf("setup: expected room filled to cap %d, got %d", maxMessagesInRoom, got)
	}

	if _, _, err := r.Join("watcher", "observer"); err != nil {
		t.Fatalf("observer join: %v", err)
	}

	if got := len(r.GetMessages()); got <= truncateToMessages {
		t.Fatalf("observer join truncated room to %d; must preserve full history like manager", got)
	}
}

func TestManagerAndObserverCoexist(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("boss", "manager"); err != nil {
		t.Fatalf("manager join should succeed: %v", err)
	}
	if _, _, err := r.Join("watcher", "observer"); err != nil {
		t.Fatalf("observer should join while a manager is active: %v", err)
	}
	if _, _, err := r.Join("watcher2", "observer"); err != nil {
		t.Fatalf("multiple observers allowed (no lock): %v", err)
	}
	if got := r.GetActiveManager(); got != "boss" {
		t.Fatalf("manager lock must be unaffected by observers, got %q", got)
	}
	agents := r.GetAgents()
	if agents["watcher"].Role != "observer" || agents["watcher2"].Role != "observer" {
		t.Fatalf("both observers should be recorded in the roster with the observer role")
	}
}

// TestObserverHeartbeat_TouchAgentLastSeenKeepsAlive guards the audit's critical
// finding: an observer that only polls read_all_messages must have its last_seen
// refreshed (via TouchAgentLastSeen), otherwise stale cleanup (>staleTimeout)
// evicts it and it loses read_all access.
func TestObserverHeartbeat_TouchAgentLastSeenKeepsAlive(t *testing.T) {
	t.Run("touch keeps observer alive past staleTimeout", func(t *testing.T) {
		r := NewRoomState()
		if _, _, err := r.Join("watcher", "observer"); err != nil {
			t.Fatalf("observer join should succeed: %v", err)
		}
		r.mu.Lock()
		a := r.agents["watcher"]
		a.LastSeen = types.Now() - float64(staleTimeout) - 1
		r.agents["watcher"] = a
		r.mu.Unlock()

		r.TouchAgentLastSeen("watcher") // simulate a read_all poll refresh

		agents := r.ListAgents("") // runs cleanupStaleLocked
		if _, ok := agents["watcher"]; !ok {
			t.Fatalf("observer evicted despite heartbeat touch")
		}
	})

	t.Run("without touch a stale observer is evicted (control)", func(t *testing.T) {
		r := NewRoomState()
		if _, _, err := r.Join("watcher", "observer"); err != nil {
			t.Fatalf("observer join should succeed: %v", err)
		}
		r.mu.Lock()
		a := r.agents["watcher"]
		a.LastSeen = types.Now() - float64(staleTimeout) - 1
		r.agents["watcher"] = a
		r.mu.Unlock()

		agents := r.ListAgents("")
		if _, ok := agents["watcher"]; ok {
			t.Fatalf("control: stale observer should be evicted without a touch")
		}
	})
}

// -- protocol-level observer tests --

func joinObserver(t *testing.T, h *Hub, c *Client, room, name string) {
	t.Helper()
	// Observer join is desktop-gated like manager (#17 P1): the agent must be a
	// configured observer for the room before it may join with role "observer".
	h.setConfiguredObservers(room, []string{name})
	h.handleRequest(c, types.Request{
		ID:   "join-" + name,
		Type: "join_room",
		Room: room,
		Data: mustRawJSON(t, map[string]any{"agent_name": name, "role": "observer"}),
	})
	resp := readResponse(t, c, "join_room")
	if !resp.Success {
		t.Fatalf("observer %q join should succeed: %s", name, resp.Error)
	}
}

// #17 P1: a self-asserted observer must not get read-all access. join_room with
// role "observer" is rejected unless the desktop has configured that agent as an
// observer for the room (mirrors the manager gate).
func TestHandleJoinRoom_ObserverRequiresConfiguration(t *testing.T) {
	h, c := newTestHubClient()
	h.handleRequest(c, types.Request{
		ID:   "join-1",
		Type: "join_room",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"agent_name": "watcher", "role": "observer"}),
	})
	if resp := readResponse(t, c, "join_room"); resp.Success {
		t.Fatalf("unconfigured observer join must be rejected")
	}

	h.setConfiguredObservers("r1", []string{"watcher"})
	c2 := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleRequest(c2, types.Request{
		ID:   "join-2",
		Type: "join_room",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"agent_name": "watcher", "role": "observer"}),
	})
	if resp := readResponse(t, c2, "join_room"); !resp.Success {
		t.Fatalf("configured observer should be allowed to join: %s", resp.Error)
	}
}

func TestIsConfiguredObserver_CaseInsensitive(t *testing.T) {
	h, _ := newTestHubClient()
	h.setConfiguredObservers("r1", []string{"Watcher", "Eye"})
	if !h.isConfiguredObserver("r1", "watcher") {
		t.Error("configured observer match must be case-insensitive")
	}
	if !h.isConfiguredObserver("r1", "EYE") {
		t.Error("second configured observer must match")
	}
	if h.isConfiguredObserver("r1", "stranger") {
		t.Error("unconfigured name must not match")
	}
	// Replacing the set drops the old members.
	h.setConfiguredObservers("r1", []string{"Eye"})
	if h.isConfiguredObserver("r1", "watcher") {
		t.Error("replaced set must no longer contain the old observer")
	}
}

func TestHandleSetObservers_RequiresDesktopAuth(t *testing.T) {
	h, c := newTestHubClient() // a plain (non-desktop) client
	h.handleRequest(c, types.Request{
		ID:   "set-obs",
		Type: "set_observers",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"observers": []string{"watcher"}}),
	})
	if resp := readResponse(t, c, "set_observers"); resp.Success {
		t.Fatalf("set_observers must require desktop authorization")
	}
}

func TestHandleSendMessage_ObserverRejected(t *testing.T) {
	h, obs := newTestHubClient()
	joinObserver(t, h, obs, "r1", "watcher")

	h.handleRequest(obs, types.Request{
		ID:   "msg-1",
		Type: "send_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "watcher", "to": "all", "content": "hi"}),
	})
	resp := readResponse(t, obs, "send_message")
	if resp.Success {
		t.Fatalf("expected observer send_message to be rejected")
	}

	// The message must not be recorded in the room.
	for _, m := range h.getOrCreateRoom("r1").GetMessages() {
		if m.From == "watcher" && m.Type != "system" {
			t.Fatalf("observer message should not be recorded, found: %+v", m)
		}
	}
}

func TestHandleSendMessage_ObserverRejectedBeforeManagerGateway(t *testing.T) {
	h, manager := newTestHubClient()
	h.setConfiguredManager("r1", "manager")

	h.handleRequest(manager, types.Request{
		ID:   "join-mgr",
		Type: "join_room",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"agent_name": "manager", "role": "manager"}),
	})
	_ = readResponse(t, manager, "join_room")

	obs := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	joinObserver(t, h, obs, "r1", "watcher")

	rs := h.getOrCreateRoom("r1")
	// Push the manager heartbeat into the past (but within the timeout so the lock
	// survives), so we can prove the observer rejection does NOT refresh it.
	stale := types.Now() - 100
	rs.mu.Lock()
	rs.managerLastSeen = stale
	rs.mu.Unlock()

	h.handleRequest(obs, types.Request{
		ID:   "msg-obs",
		Type: "send_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "watcher", "to": "all", "content": "should not route"}),
	})
	resp := readResponse(t, obs, "send_message")
	if resp.Success {
		t.Fatalf("observer send must be rejected even with an active manager")
	}

	// Rejected BEFORE the manager gateway: heartbeat untouched, nothing rerouted.
	rs.mu.RLock()
	after := rs.managerLastSeen
	rs.mu.RUnlock()
	if after != stale {
		t.Fatalf("observer rejection must not touch manager heartbeat: got %v, want %v", after, stale)
	}
	for _, m := range rs.GetMessages() {
		if m.From == "watcher" && m.Type != "system" {
			t.Fatalf("observer message must not be rerouted/recorded, found: %+v", m)
		}
	}
}

func TestHandleGetAllMessages_ObserverAllowed(t *testing.T) {
	h, obs := newTestHubClient()
	joinObserver(t, h, obs, "r1", "watcher")

	h.handleRequest(obs, types.Request{
		ID:   "all-1",
		Type: "get_all_messages",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"since_id": 0, "limit": 50}),
	})
	resp := readResponse(t, obs, "get_all_messages")
	if !resp.Success {
		t.Fatalf("observer should be allowed to read_all_messages, got error: %s", resp.Error)
	}
}

// TestObserverReadAll_Over300sNoEvict proves end-to-end that an observer polling
// only get_all_messages refreshes its heartbeat, so a later stale-cleanup keeps it
// in the roster (and thus keeps read_all access). Without the TouchAgentLastSeen
// wiring the observer would be evicted after staleTimeout.
func TestObserverReadAll_Over300sNoEvict(t *testing.T) {
	h, obs := newTestHubClient()
	joinObserver(t, h, obs, "r1", "watcher")

	rs := h.getOrCreateRoom("r1")
	rs.mu.Lock()
	a := rs.agents["watcher"]
	a.LastSeen = types.Now() - float64(staleTimeout) - 1
	rs.agents["watcher"] = a
	rs.mu.Unlock()

	// Observer polls read_all while still in the roster: this must refresh last_seen.
	h.handleRequest(obs, types.Request{
		ID:   "all-poll",
		Type: "get_all_messages",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"since_id": 0, "limit": 50}),
	})
	if resp := readResponse(t, obs, "get_all_messages"); !resp.Success {
		t.Fatalf("observer poll should succeed: %s", resp.Error)
	}

	// A subsequent stale-cleanup must NOT evict the observer (it stays visible in
	// list_agents). read_all authorization itself is the allow-list, not the roster.
	rs.ListAgents("")
	if _, ok := rs.GetAgents()["watcher"]; !ok {
		t.Fatalf("observer evicted after staleTimeout despite read_all heartbeat refresh")
	}
}

// #17 (Codex P2): observer authorization is the desktop allow-list, not the room
// roster, so a clear_room (which empties the roster) must NOT lift the send block on
// a still-connected observer.
func TestHandleSendMessage_ObserverBlockedAfterRosterClear(t *testing.T) {
	h, obs := newTestHubClient()
	joinObserver(t, h, obs, "r1", "watcher")

	// Simulate clear_room: ClearArchived empties the roster (and messages).
	h.getOrCreateRoom("r1").ClearArchived(1 << 30)

	h.handleRequest(obs, types.Request{
		ID:   "msg-after-clear",
		Type: "send_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "watcher", "to": "all", "content": "x"}),
	})
	if resp := readResponse(t, obs, "send_message"); resp.Success {
		t.Fatalf("observer send must stay blocked after a roster clear")
	}
}

// #17 (Codex P2): removing an agent from the observer allow-list must immediately
// revoke its read-all access, even while it stays connected.
func TestHandleGetAllMessages_ObserverRevokedWhenDeconfigured(t *testing.T) {
	h, obs := newTestHubClient()
	joinObserver(t, h, obs, "r1", "watcher")

	getAll := types.Request{
		ID:   "all",
		Type: "get_all_messages",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"since_id": 0, "limit": 50}),
	}
	h.handleRequest(obs, getAll)
	if resp := readResponse(t, obs, "get_all_messages"); !resp.Success {
		t.Fatalf("configured observer should read_all: %s", resp.Error)
	}

	// Desktop removes the observer from the allow-list.
	h.setConfiguredObservers("r1", nil)

	h.handleRequest(obs, types.Request{ID: "all2", Type: "get_all_messages", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"since_id": 0, "limit": 50})})
	if resp := readResponse(t, obs, "get_all_messages"); resp.Success {
		t.Fatalf("read_all must be revoked once the observer is de-configured")
	}
}
