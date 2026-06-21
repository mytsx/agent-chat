package orchestrator

import (
	"testing"
	"time"

	ptymgr "desktop/internal/pty"
)

// The Claude/Gemini injection must hold the per-session write mutex across the
// whole paste→settle→CR sequence, so a user keystroke cannot be appended to the
// notification and submitted by its CR (review CR1). Verified against a real
// "cat"-backed PTY: a concurrent Write issued while tryInject is mid-settle must
// BLOCK until the injection (including the trailing CR) completes.
func TestTryInject_HoldsWriteMutexAcrossSettle(t *testing.T) {
	m := ptymgr.NewManager(func(string, []byte) {})
	id, err := m.Create("", "agent", "", nil, "cat", nil, "")
	if err != nil {
		t.Skipf("cannot start cat PTY: %v", err)
	}
	defer m.Close(id)

	o := New(m)
	o.pendingInputFunc = func(string) bool { return false }

	done := make(chan struct{})
	go func() {
		o.tryInject(id, "[agent-chat] hi")
		close(done)
	}()

	// Land inside the 200ms settle (after the paste write, before the CR).
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	if err := m.Write(id, []byte("x")); err != nil {
		t.Fatalf("concurrent Write: %v", err)
	}
	elapsed := time.Since(start)

	<-done
	if elapsed < 80*time.Millisecond {
		t.Errorf("concurrent write completed in %v — injection did NOT hold the mutex across the settle; a keystroke could be appended to the notification and submitted (CR1)", elapsed)
	}
}

// tryInject must report false when the PTY write fails (e.g. the terminal is
// closing), so the caller re-defers instead of treating the dropped
// notification as delivered (review GR1).
func TestTryInject_ReturnsFalseOnWriteError(t *testing.T) {
	m := ptymgr.NewManager(func(string, []byte) {})
	id, err := m.Create("", "agent", "", nil, "cat", nil, "")
	if err != nil {
		t.Skipf("cannot start cat PTY: %v", err)
	}
	defer m.Close(id)

	// Close the underlying PTY fd so writes fail, but keep the session in the map
	// (so tryInject reaches the write path rather than the not-found path).
	if s := m.GetSession(id); s != nil && s.PTY != nil {
		_ = s.PTY.Close()
	}

	o := New(m)
	o.pendingInputFunc = func(string) bool { return false }

	if o.tryInject(id, "[agent-chat] hi") {
		t.Error("tryInject should return false when the PTY write fails (notification not delivered)")
	}
}
