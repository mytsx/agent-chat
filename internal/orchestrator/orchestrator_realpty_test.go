package orchestrator

import (
	"testing"
	"time"

	ptymgr "desktop/internal/pty"
)

// The default (Claude/Gemini) injection must NOT hold the per-session write
// mutex across its 200ms settle sleep — otherwise a user keystroke arriving
// during an injection is blocked for the whole sleep (perceptible input lag).
// Verified against a real "cat"-backed PTY: a concurrent Write issued while
// sendToTerminal is mid-settle must return promptly.
func TestSendToTerminal_DoesNotBlockUserDuringSettle(t *testing.T) {
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
	if elapsed > 80*time.Millisecond {
		t.Errorf("user keystroke blocked %v during settle — writeMu held across the sleep", elapsed)
	}
}
