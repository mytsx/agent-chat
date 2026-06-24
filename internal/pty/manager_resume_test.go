package pty

import (
	"sync"
	"testing"
)

func TestCLISessionID_RoundTrip(t *testing.T) {
	m := NewManager(nil)
	// Inject a session directly (no real PTY needed for the field).
	m.sessions["s1"] = &PTYSession{ID: "s1"}

	if got := m.GetCLISessionID("s1"); got != "" {
		t.Fatalf("initial GetCLISessionID = %q, want empty", got)
	}
	m.SetCLISessionID("s1", "uuid-123")
	if got := m.GetCLISessionID("s1"); got != "uuid-123" {
		t.Fatalf("GetCLISessionID = %q, want uuid-123", got)
	}
}

func TestCLISessionID_UnknownSession(t *testing.T) {
	m := NewManager(nil)
	m.SetCLISessionID("ghost", "x") // must not panic
	if got := m.GetCLISessionID("ghost"); got != "" {
		t.Fatalf("unknown session GetCLISessionID = %q, want empty", got)
	}
}

func TestCLISessionID_ConcurrentAccess(t *testing.T) {
	m := NewManager(nil)
	m.sessions["s1"] = &PTYSession{ID: "s1"}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); m.SetCLISessionID("s1", "uuid-x") }()
		go func() { defer wg.Done(); _ = m.GetCLISessionID("s1") }()
	}
	wg.Wait()
	if got := m.GetCLISessionID("s1"); got != "uuid-x" {
		t.Fatalf("final GetCLISessionID = %q, want uuid-x", got)
	}
}
