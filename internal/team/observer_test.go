package team

import "testing"

func TestSetObserverSetsRole(t *testing.T) {
	s := newTestStore(t)
	tm, _ := s.Create("TeamA", "2x2", nil)
	if _, err := s.UpsertAgent(tm.ID, AgentConfig{Name: "watcher", CLIType: "claude"}); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	updated, err := s.SetObserver(tm.ID, "watcher")
	if err != nil {
		t.Fatalf("SetObserver failed: %v", err)
	}
	for _, a := range updated.Agents {
		if a.Name == "watcher" {
			if a.Role != "observer" {
				t.Fatalf("expected Role=observer, got %q", a.Role)
			}
			return
		}
	}
	t.Fatalf("watcher not found in team agents")
}

func TestSetObserverCreatesAgentIfMissing(t *testing.T) {
	s := newTestStore(t)
	tm, _ := s.Create("TeamA", "2x2", nil)

	updated, err := s.SetObserver(tm.ID, "watcher")
	if err != nil {
		t.Fatalf("SetObserver failed: %v", err)
	}
	found := false
	for _, a := range updated.Agents {
		if a.Name == "watcher" && a.Role == "observer" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SetObserver should append a missing agent with Role=observer, got %+v", updated.Agents)
	}
}

// An agent cannot be both manager and observer: making it an observer must clear
// the manager assignment if it held it (otherwise resolveAgentMode's manager
// precedence would keep treating it as manager).
func TestSetObserverClearsManagerForSameAgent(t *testing.T) {
	s := newTestStore(t)
	tm, _ := s.Create("TeamA", "2x2", nil)
	if _, err := s.UpsertAgent(tm.ID, AgentConfig{Name: "x", CLIType: "claude"}); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if _, err := s.SetManager(tm.ID, "x"); err != nil {
		t.Fatalf("SetManager failed: %v", err)
	}

	updated, err := s.SetObserver(tm.ID, "x")
	if err != nil {
		t.Fatalf("SetObserver failed: %v", err)
	}
	if updated.ManagerAgent != "" {
		t.Fatalf("expected manager cleared when x becomes observer, got %q", updated.ManagerAgent)
	}
}

// The reverse: making a former observer the manager must clear its observer Role,
// otherwise broadcastRoleLookup would wrongly skip the manager from broadcasts.
func TestSetManagerClearsObserverRole(t *testing.T) {
	s := newTestStore(t)
	tm, _ := s.Create("TeamA", "2x2", nil)
	if _, err := s.UpsertAgent(tm.ID, AgentConfig{Name: "x", Role: "observer", CLIType: "claude"}); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	updated, err := s.SetManager(tm.ID, "x")
	if err != nil {
		t.Fatalf("SetManager failed: %v", err)
	}
	if !updated.IsManagerAgent("x") {
		t.Fatalf("expected x to be manager")
	}
	for _, a := range updated.Agents {
		if a.Name == "x" && a.Role == "observer" {
			t.Fatalf("expected observer Role cleared when x becomes manager, got Role=%q", a.Role)
		}
	}
}
