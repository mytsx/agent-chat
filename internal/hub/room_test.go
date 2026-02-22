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
