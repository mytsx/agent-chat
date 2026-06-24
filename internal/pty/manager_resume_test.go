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

func TestCapturedSessionIDs(t *testing.T) {
	m := NewManager(nil)
	if got := m.CapturedSessionIDs(); len(got) != 0 {
		t.Fatalf("empty manager = %v, want none", got)
	}
	// Two sessions captured an id, one never captured (nil pointer), one captured
	// then cleared to "" — only the two non-empty ids must be returned (#40 Faz-2).
	m.sessions["a"] = &PTYSession{ID: "a"}
	m.SetCLISessionID("a", "id-a")
	m.sessions["b"] = &PTYSession{ID: "b"}
	m.SetCLISessionID("b", "id-b")
	m.sessions["c"] = &PTYSession{ID: "c"} // never captured → nil → skipped
	m.sessions["d"] = &PTYSession{ID: "d"}
	m.SetCLISessionID("d", "") // empty → skipped
	// A dead-but-lingering session (in-CLI /exit) must be skipped so shutdown can't
	// re-extend its already-closed history window (#40 Faz-2).
	dead := &PTYSession{ID: "e", done: make(chan struct{})}
	close(dead.done)
	m.sessions["e"] = dead
	m.SetCLISessionID("e", "id-e")

	got := m.CapturedSessionIDs()
	set := map[string]bool{}
	for _, id := range got {
		set[id] = true
	}
	if len(got) != 2 || !set["id-a"] || !set["id-b"] {
		t.Fatalf("CapturedSessionIDs = %v, want exactly {id-a, id-b}", got)
	}
}

func TestSessionExitedAt(t *testing.T) {
	m := NewManager(nil)
	if at, ok := m.SessionExitedAt("nope"); ok || at != 0 {
		t.Fatalf("unknown = %v,%v, want 0,false", at, ok)
	}
	// Still-running session (exitedAtNano==0) → not exited.
	m.sessions["live"] = &PTYSession{ID: "live"}
	if at, ok := m.SessionExitedAt("live"); ok || at != 0 {
		t.Fatalf("live = %v,%v, want 0,false", at, ok)
	}
	// Exited session → exit time in unix seconds. 2e18 ns == 2e9 s exactly.
	exited := &PTYSession{ID: "dead"}
	exited.exitedAtNano.Store(2_000_000_000_000_000_000)
	m.sessions["dead"] = exited
	if at, ok := m.SessionExitedAt("dead"); !ok || at != 2_000_000_000 {
		t.Fatalf("dead = %v,%v, want 2e9,true", at, ok)
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
