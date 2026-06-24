package sessionlog

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecordAndListSessions(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }

	s.Record("sid-1", "room-a", "agent-1", "claude", "/cwd/1")
	clock = 200
	s.Record("sid-2", "room-a", "agent-1", "codex", "/cwd/1")
	s.Record("sid-x", "room-b", "agent-1", "claude", "/cwd/1") // başka oda

	got := s.ListSessions("room-a", "agent-1")
	if len(got) != 2 {
		t.Fatalf("ListSessions len = %d, want 2", len(got))
	}
	// lastSeen yeniden→eskiye: sid-2 (200) önce
	if got[0].SessionID != "sid-2" || got[1].SessionID != "sid-1" {
		t.Fatalf("order = %s,%s, want sid-2,sid-1", got[0].SessionID, got[1].SessionID)
	}
	if got[1].FirstSeen != 100 || got[1].CLIType != "claude" {
		t.Fatalf("sid-1 entry = %+v", got[1])
	}
}

func TestRecordIdempotentPreservesFirstSeen(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("sid", "r", "a", "claude", "/c")
	clock = 500
	s.Record("sid", "r", "a", "claude", "/c") // tekrar capture
	got := s.ListSessions("r", "a")
	if len(got) != 1 || got[0].FirstSeen != 100 || got[0].LastSeen != 500 {
		t.Fatalf("entry = %+v, want firstSeen=100 lastSeen=500", got[0])
	}
}

func TestTouch(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("sid", "r", "a", "claude", "/c")
	clock = 300
	s.Touch("sid")
	s.Touch("unknown") // no-op, panik yok
	got := s.ListSessions("r", "a")
	if got[0].LastSeen != 300 || got[0].FirstSeen != 100 {
		t.Fatalf("after touch = %+v", got[0])
	}
}

func TestListAgents(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("s1", "r", "alice", "claude", "/c")
	clock = 200
	s.Record("s2", "r", "bob", "codex", "/c")
	clock = 300
	s.Record("s3", "r", "alice", "claude", "/c")
	got := s.ListAgents("r")
	// son-görülme yeniden→eskiye distinct: alice(300), bob(200)
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("ListAgents = %v, want [alice bob]", got)
	}
}

func TestEmptySessionIDNoOp(t *testing.T) {
	s := newTestStore(t)
	s.Record("", "r", "a", "claude", "/c")
	if len(s.ListSessions("r", "a")) != 0 {
		t.Fatal("empty sessionID must not be recorded")
	}
}

func TestPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	s1, _ := New(dir)
	s1.Record("sid", "r", "a", "claude", "/c")
	s2, err := New(dir) // aynı dizinden yeniden yükle
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.ListSessions("r", "a")) != 1 {
		t.Fatal("reload must restore persisted record")
	}
	_ = filepath.Join(dir, "session-history.json")
}
