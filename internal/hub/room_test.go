package hub

import (
	"testing"

	"desktop/internal/types"
)

func TestRoomJoin_DuplicateNameRejected(t *testing.T) {
	r := NewRoomState()

	if _, _, err := r.Join("alice", "developer"); err != nil {
		t.Fatalf("first join should succeed: %v", err)
	}
	if _, _, err := r.Join("alice", "developer"); err == nil {
		t.Fatalf("duplicate join should fail")
	}
}

func TestRoomJoin_SecondManagerRejected(t *testing.T) {
	r := NewRoomState()

	if _, _, err := r.Join("manager-1", "manager"); err != nil {
		t.Fatalf("first manager join should succeed: %v", err)
	}
	if _, _, err := r.Join("manager-2", "manager"); err == nil {
		t.Fatalf("second manager join should fail while first is active")
	}
}

func TestRoomManagerTimeoutClearsLock(t *testing.T) {
	r := NewRoomState()

	if _, _, err := r.Join("manager-1", "manager"); err != nil {
		t.Fatalf("first manager join should succeed: %v", err)
	}

	r.mu.Lock()
	r.managerLastSeen = types.Now() - float64(managerTimeoutSec) - 1
	r.mu.Unlock()

	if got := r.GetActiveManager(); got != "" {
		t.Fatalf("expected manager timeout to clear lock, got %q", got)
	}
	if _, _, err := r.Join("manager-2", "manager"); err != nil {
		t.Fatalf("new manager should be able to claim lock after timeout: %v", err)
	}
}

func TestRoomTouchManagerHeartbeat_CaseInsensitive(t *testing.T) {
	r := NewRoomState()

	if _, _, err := r.Join("Pilot", "manager"); err != nil {
		t.Fatalf("manager join should succeed: %v", err)
	}
	r.mu.Lock()
	staleRosterSeen := types.Now() - float64(staleTimeout) - 1
	agent := r.agents["Pilot"]
	agent.LastSeen = staleRosterSeen
	r.agents["Pilot"] = agent
	r.mu.Unlock()

	// The manager is locked under its join spelling ("Pilot"), but a caller may
	// pass the configured spelling ("pilot"). Manager identity is casing-independent.
	if !r.TouchManagerHeartbeat("pilot") {
		t.Fatalf("TouchManagerHeartbeat should recognize a case-variant manager name")
	}
	r.mu.RLock()
	refreshedRosterSeen := r.agents["Pilot"].LastSeen
	r.mu.RUnlock()
	if refreshedRosterSeen <= staleRosterSeen {
		t.Fatalf("manager heartbeat should refresh roster LastSeen too, got %v <= %v", refreshedRosterSeen, staleRosterSeen)
	}

	// A subsequent stale cleanup must not evict a manager that is still actively
	// polling read_all_messages and refreshing only the manager heartbeat path.
	if got := r.ListAgents(""); len(got) != 1 {
		t.Fatalf("active manager should remain in roster after cleanup, got %#v", got)
	}
}

func TestRoomGetActiveManagerAndTouch_CaseInsensitiveRefresh(t *testing.T) {
	r := NewRoomState()

	if _, _, err := r.Join("Pilot", "manager"); err != nil {
		t.Fatalf("manager join should succeed: %v", err)
	}

	r.mu.Lock()
	stale := types.Now() - 100
	r.managerLastSeen = stale
	staleRosterSeen := types.Now() - float64(staleTimeout) - 1
	agent := r.agents["Pilot"]
	agent.LastSeen = staleRosterSeen
	r.agents["Pilot"] = agent
	r.mu.Unlock()

	if active := r.GetActiveManagerAndTouch("pilot"); active != "Pilot" {
		t.Fatalf("expected active manager Pilot, got %q", active)
	}

	r.mu.RLock()
	refreshed := r.managerLastSeen
	refreshedRosterSeen := r.agents["Pilot"].LastSeen
	r.mu.RUnlock()
	if refreshed <= stale {
		t.Fatalf("expected heartbeat refresh for case-variant manager name, lastSeen %v not refreshed from %v", refreshed, stale)
	}
	if refreshedRosterSeen <= staleRosterSeen {
		t.Fatalf("expected roster LastSeen refresh for active manager, got %v <= %v", refreshedRosterSeen, staleRosterSeen)
	}
}

func TestRoomResetManagerLockIfDifferent_CaseInsensitiveKeepsLock(t *testing.T) {
	r := NewRoomState()

	if _, _, err := r.Join("Pilot", "manager"); err != nil {
		t.Fatalf("manager join should succeed: %v", err)
	}

	// Re-affirming the same manager via its configured (lowercase) spelling must
	// NOT clear the active lock.
	r.ResetManagerLockIfDifferent("pilot")
	if got := r.GetActiveManager(); got != "Pilot" {
		t.Fatalf("expected manager lock to survive case-variant re-affirm, got %q", got)
	}

	// A genuinely different manager name still clears the lock.
	r.ResetManagerLockIfDifferent("someone-else")
	if got := r.GetActiveManager(); got != "" {
		t.Fatalf("expected lock cleared for a different manager, got %q", got)
	}
}

func TestRoomSendMessage_InterceptionMetadata(t *testing.T) {
	r := NewRoomState()

	if _, _, err := r.Join("alice", "developer"); err != nil {
		t.Fatalf("join should succeed: %v", err)
	}

	msg, err := r.SendMessage("alice", "manager", "hello", true, "normal", SendOptions{
		OriginalTo:      "bob",
		RoutedByManager: true,
	})
	if err != nil {
		t.Fatalf("send should succeed: %v", err)
	}
	if msg.OriginalTo != "bob" {
		t.Fatalf("expected original_to=bob, got %q", msg.OriginalTo)
	}
	if !msg.RoutedByManager {
		t.Fatalf("expected routed_by_manager=true")
	}
	if msg.To != "manager" {
		t.Fatalf("expected to=manager, got %q", msg.To)
	}
}
