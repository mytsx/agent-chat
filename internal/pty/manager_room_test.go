package pty

import "testing"

// The room a session belongs to is pinned at creation (from the team name used
// for AGENT_CHAT_ROOM), so a later team rename can't make logged prompts land in
// a different room than the agent's MCP session actually uses (#58).
func TestCreate_PinsRoom(t *testing.T) {
	m := NewManager(nil)
	id, err := m.Create("team-1", "agent", "team-alpha", "", nil, "cat", nil, "")
	if err != nil {
		t.Skipf("cannot start cat PTY: %v", err)
	}
	defer m.Close(id)

	sess := m.GetSession(id)
	if sess == nil {
		t.Fatal("session not found after Create")
	}
	if sess.Room != "team-alpha" {
		t.Fatalf("Room = %q, want team-alpha (pinned at creation)", sess.Room)
	}
}
