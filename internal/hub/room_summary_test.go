package hub

import (
	"testing"

	"desktop/internal/types"
)

func TestRoomSummary_EmptyRoom(t *testing.T) {
	r := NewRoomState()

	s := r.Summary("empty", false)

	if s.Name != "empty" {
		t.Fatalf("name = %q, want %q", s.Name, "empty")
	}
	if s.MessageCount != 0 {
		t.Fatalf("message count = %d, want 0", s.MessageCount)
	}
	if s.LastActivity != "" {
		t.Fatalf("last activity = %q, want empty", s.LastActivity)
	}
	if len(s.Agents) != 0 {
		t.Fatalf("agents = %d, want 0", len(s.Agents))
	}
	if s.IsDefault {
		t.Fatalf("isDefault = true, want false")
	}
}

func TestRoomSummary_WithMessagesAndAgents(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("Coder1", "developer"); err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := r.SendMessage("Coder1", "all", "hello", false, "", SendOptions{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	last, err := r.SendMessage("Coder1", "all", "world", false, "", SendOptions{})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	s := r.Summary("proj", true)

	if s.Name != "proj" {
		t.Fatalf("name = %q, want %q", s.Name, "proj")
	}
	// 1 system join message + 2 sends
	if s.MessageCount != 3 {
		t.Fatalf("message count = %d, want 3", s.MessageCount)
	}
	if s.LastActivity != last.Timestamp {
		t.Fatalf("last activity = %q, want %q", s.LastActivity, last.Timestamp)
	}
	if _, ok := s.Agents["Coder1"]; !ok {
		t.Fatalf("expected Coder1 in agents, got %v", s.Agents)
	}
	if !s.IsDefault {
		t.Fatalf("isDefault = false, want true")
	}
}

// Summary must reflect the persisted agent map verbatim — it must NOT run stale
// cleanup, otherwise rooms whose agents went idle would lose their names.
func TestRoomSummary_KeepsStaleAgents(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("OldCoder", "developer"); err != nil {
		t.Fatalf("join: %v", err)
	}
	r.mu.Lock()
	a := r.agents["OldCoder"]
	a.LastSeen = types.Now() - float64(staleTimeout) - 1
	r.agents["OldCoder"] = a
	r.mu.Unlock()

	s := r.Summary("proj", false)

	if _, ok := s.Agents["OldCoder"]; !ok {
		t.Fatalf("Summary must keep stale agents (no cleanup), got %v", s.Agents)
	}
}

func TestListRoomSummaries_SortedByLastActivityDescEmptyLast(t *testing.T) {
	older := NewRoomState()
	if _, err := older.SendMessage("a", "all", "old", false, "", SendOptions{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	older.mu.Lock()
	older.messages[len(older.messages)-1].Timestamp = "2020-01-01T00:00:00.000000"
	older.mu.Unlock()

	newer := NewRoomState()
	if _, err := newer.SendMessage("a", "all", "new", false, "", SendOptions{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	newer.mu.Lock()
	newer.messages[len(newer.messages)-1].Timestamp = "2025-01-01T00:00:00.000000"
	newer.mu.Unlock()

	empty := NewRoomState()

	rooms := map[string]*RoomState{"older": older, "newer": newer, "empty": empty}

	got := ListRoomSummaries(rooms, "newer")

	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Name != "newer" {
		t.Fatalf("got[0] = %q, want newer (most recent first)", got[0].Name)
	}
	if got[1].Name != "older" {
		t.Fatalf("got[1] = %q, want older", got[1].Name)
	}
	if got[2].Name != "empty" {
		t.Fatalf("got[2] = %q, want empty (empty activity sorts last)", got[2].Name)
	}
	if !got[0].IsDefault {
		t.Fatalf("newer should be marked as default room")
	}
	if got[1].IsDefault || got[2].IsDefault {
		t.Fatalf("non-default rooms must not be marked default")
	}
}
