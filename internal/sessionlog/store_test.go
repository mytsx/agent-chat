package sessionlog

import (
	"os"
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

func TestRecordSameRunPreservesFirstSeen(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("sid", "r", "a", "claude", "/c")
	clock = 100 + newWindowGapSec/2 // within the gap → same run
	s.Record("sid", "r", "a", "claude", "/c")
	got := s.ListSessions("r", "a")
	if len(got) != 1 || got[0].FirstSeen != 100 || got[0].LastSeen != clock {
		t.Fatalf("same-run entry = %+v, want firstSeen=100 lastSeen=%v", got[0], clock)
	}
}

func TestRecordNewRunStartsFreshWindow(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("sid", "r", "a", "copilot", "/c") // run 1
	clock = 100 + newWindowGapSec + 10         // gap > threshold → new run (e.g. Copilot resume)
	s.Record("sid", "r", "a", "copilot", "/c")
	got := s.ListSessions("r", "a")
	// FirstSeen RESETS to the new run's start so correlation uses the latest window,
	// not one spanning the idle gap (Codex P2).
	if len(got) != 1 || got[0].FirstSeen != clock || got[0].LastSeen != clock {
		t.Fatalf("new-run entry = %+v, want firstSeen=lastSeen=%v", got[0], clock)
	}
}

func TestRecordDoesNotRegressLastSeen(t *testing.T) {
	s := newTestStore(t)
	clock := 200.0
	s.now = func() float64 { return clock }
	s.Record("sid", "r", "a", "claude", "/c")

	clock = 150 // clock moved backwards; metadata may refresh, but time must not regress
	s.Record("sid", "r", "a", "codex", "/new")
	got := s.ListSessions("r", "a")
	if len(got) != 1 {
		t.Fatalf("ListSessions len = %d, want 1", len(got))
	}
	if got[0].FirstSeen != 200 || got[0].LastSeen != 200 {
		t.Fatalf("regressed record window = %+v, want firstSeen=lastSeen=200", got[0])
	}
	if got[0].CLIType != "codex" || got[0].Cwd != "/new" {
		t.Fatalf("metadata was not refreshed on backward clock re-record: %+v", got[0])
	}
}

func TestListMatchesAgentCaseInsensitively(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("sid", "Room", "alice", "claude", "/c") // launched lower-case
	// Querying with the config casing ("Alice") must still find the "alice" record.
	if got := s.ListSessions("room", "Alice"); len(got) != 1 {
		t.Fatalf("case-insensitive ListSessions = %d, want 1", len(got))
	}
	agents := s.ListAgents("ROOM")
	if len(agents) != 1 || agents[0] != "alice" {
		t.Fatalf("ListAgents = %v, want [alice]", agents)
	}
}

func TestReRecordRefreshesMetadataOnNewWindow(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("sid", "old-room", "alice", "copilot", "/old")
	clock = 100 + newWindowGapSec + 10 // new run after a gap (e.g. team renamed)
	s.Record("sid", "new-room", "alice", "copilot", "/new")
	// The record must now be indexed under the CURRENT room/cwd, not the old ones.
	if got := s.ListSessions("old-room", "alice"); len(got) != 0 {
		t.Fatalf("old-room still has the record: %v", got)
	}
	got := s.ListSessions("new-room", "alice")
	if len(got) != 1 || got[0].Cwd != "/new" {
		t.Fatalf("new-room record = %+v, want cwd=/new", got)
	}
}

func TestRenameRoomReindexesHistory(t *testing.T) {
	s := newTestStore(t)
	s.Record("sid", "old-room", "alice", "claude", "/c")
	s.RenameRoom("OLD-ROOM", "new-room") // case-insensitive match
	if got := s.ListSessions("old-room", "alice"); len(got) != 0 {
		t.Fatalf("old-room still has records: %v", got)
	}
	if got := s.ListSessions("new-room", "alice"); len(got) != 1 {
		t.Fatalf("new-room ListSessions = %d, want 1", len(got))
	}
	s.RenameRoom("x", "x") // no-op, no panic
}

func TestGetReturnsRecordedCwd(t *testing.T) {
	s := newTestStore(t)
	s.Record("sid", "r", "a", "claude", "/recorded/cwd")
	r, ok := s.Get("sid")
	if !ok || r.Cwd != "/recorded/cwd" {
		t.Fatalf("Get = %+v ok=%v, want cwd=/recorded/cwd", r, ok)
	}
	if _, ok := s.Get("unknown"); ok {
		t.Fatal("Get(unknown) must be false")
	}
}

func TestNilStoreListsAreSafe(t *testing.T) {
	var s *Store // nil (failed New)
	if got := s.ListSessions("r", "a"); got != nil {
		t.Fatalf("nil ListSessions = %v, want nil", got)
	}
	if got := s.ListAgents("r"); got != nil {
		t.Fatalf("nil ListAgents = %v, want nil", got)
	}
	if _, ok := s.Get("x"); ok {
		t.Fatal("nil Get must be false")
	}
	s.RenameRoom("a", "b") // nil, must not panic
	s.TouchAt("x", 1)      // nil, must not panic
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

func TestTouchAt(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("sid", "r", "a", "claude", "/c") // firstSeen=lastSeen=100

	s.TouchAt("sid", 250) // newer → advances
	if got := s.ListSessions("r", "a")[0]; got.LastSeen != 250 || got.FirstSeen != 100 {
		t.Fatalf("after TouchAt(250) = %+v, want lastSeen=250 firstSeen=100", got)
	}
	s.TouchAt("sid", 150) // older → must NOT regress
	if got := s.ListSessions("r", "a")[0]; got.LastSeen != 250 {
		t.Fatalf("TouchAt(150) regressed lastSeen to %v, want 250", got.LastSeen)
	}
	s.TouchAt("unknown", 999) // no-op, no panic
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

func TestListAgentsIncludesLoadedZeroLastSeenRecord(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{
  "sid-zero": {"session_id":"sid-zero","room":"r","agent_name":"legacy","cli_type":"claude","cwd":"/c","first_seen":0,"last_seen":0}
}`)
	if err := os.WriteFile(filepath.Join(dir, "session-history.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s.ListAgents("r")
	if len(got) != 1 || got[0] != "legacy" {
		t.Fatalf("ListAgents = %v, want [legacy] for a loaded zero-last_seen record", got)
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
}

func TestNewNormalizesLoadedSessionIDs(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{
  "sid-from-key": {"session_id":"","room":"r","agent_name":"a","cli_type":"claude","cwd":"/c","first_seen":1,"last_seen":2},
  "sid-authoritative": {"session_id":"stale-embedded","room":"r","agent_name":"a","cli_type":"codex","cwd":"/c","first_seen":3,"last_seen":4},
  "": {"session_id":"empty-key","room":"r","agent_name":"a","cli_type":"claude","cwd":"/c","first_seen":5,"last_seen":6}
}`)
	if err := os.WriteFile(filepath.Join(dir, "session-history.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s.ListSessions("r", "a")
	if len(got) != 2 {
		t.Fatalf("ListSessions len = %d, want 2", len(got))
	}
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.SessionID] = true
		if r.SessionID == "" || r.SessionID == "stale-embedded" || r.SessionID == "empty-key" {
			t.Fatalf("loaded malformed session id in record: %+v", r)
		}
	}
	if !ids["sid-from-key"] || !ids["sid-authoritative"] {
		t.Fatalf("loaded ids = %v, want map-key ids", ids)
	}
}

func TestNewReturnsReadErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "session-history.json"), 0700); err != nil {
		t.Fatal(err)
	}

	if _, err := New(dir); err == nil {
		t.Fatal("New must return non-not-exist read errors instead of silently starting with empty history")
	}
}

func TestRecordRollsBackWhenSaveFails(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("existing", "r", "a", "claude", "/old")

	s.filePath = filepath.Join(dir, "missing", "session-history.json")
	clock = 200
	s.Record("new", "r", "a", "codex", "/new")
	if got := s.ListSessions("r", "a"); len(got) != 1 || got[0].SessionID != "existing" {
		t.Fatalf("failed new record was kept: %+v", got)
	}

	s.Record("existing", "r", "a", "copilot", "/changed")
	got := s.ListSessions("r", "a")
	if len(got) != 1 || got[0].CLIType != "claude" || got[0].Cwd != "/old" || got[0].LastSeen != 100 {
		t.Fatalf("failed existing record update was not rolled back: %+v", got)
	}
}

func TestTouchAndRenameRollBackWhenSaveFails(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("sid", "old-room", "alice", "claude", "/c")

	s.filePath = filepath.Join(dir, "missing", "session-history.json")
	clock = 300
	s.Touch("sid")
	if got := s.ListSessions("old-room", "alice")[0]; got.LastSeen != 100 {
		t.Fatalf("failed Touch was not rolled back: %+v", got)
	}

	s.TouchAt("sid", 400)
	if got := s.ListSessions("old-room", "alice")[0]; got.LastSeen != 100 {
		t.Fatalf("failed TouchAt was not rolled back: %+v", got)
	}

	s.RenameRoom("old-room", "new-room")
	if got := s.ListSessions("old-room", "alice"); len(got) != 1 {
		t.Fatalf("failed RenameRoom did not restore old room: %+v", got)
	}
	if got := s.ListSessions("new-room", "alice"); len(got) != 0 {
		t.Fatalf("failed RenameRoom kept new room records: %+v", got)
	}
}
