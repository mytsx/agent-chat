package hub

import (
	"testing"

	"desktop/internal/types"
)

func TestRoomIsObserver(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("watcher", "observer"); err != nil {
		t.Fatalf("observer join should succeed: %v", err)
	}
	if _, _, err := r.Join("dev", "developer"); err != nil {
		t.Fatalf("developer join should succeed: %v", err)
	}
	if _, _, err := r.Join("boss", "manager"); err != nil {
		t.Fatalf("manager join should succeed: %v", err)
	}

	if !r.IsObserver("watcher") {
		t.Errorf("IsObserver(watcher) = false, want true")
	}
	if r.IsObserver("dev") {
		t.Errorf("IsObserver(dev) = true, want false")
	}
	if r.IsObserver("boss") {
		t.Errorf("IsObserver(boss) = true, want false")
	}
	if r.IsObserver("absent") {
		t.Errorf("IsObserver(absent) = true, want false (not in roster)")
	}
}

func TestRoomIsObserver_CaseInsensitive(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("Watcher", "Observer"); err != nil {
		t.Fatalf("observer join should succeed: %v", err)
	}
	if !r.IsObserver("Watcher") {
		t.Errorf("IsObserver should match a mixed-case observer role")
	}
}

func TestObserverJoin_DoesNotClaimManagerLock(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("watcher", "observer"); err != nil {
		t.Fatalf("observer join should succeed: %v", err)
	}
	if got := r.GetActiveManager(); got != "" {
		t.Fatalf("observer must not claim manager lock, got %q", got)
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
	if !r.IsObserver("watcher") || !r.IsObserver("watcher2") {
		t.Fatalf("both observers should be recognized")
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

	// A subsequent stale-cleanup must NOT evict the observer.
	rs.ListAgents("")
	if !rs.IsObserver("watcher") {
		t.Fatalf("observer evicted after staleTimeout despite read_all heartbeat refresh")
	}
}
