package team

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// newTestStore creates a Store backed by a temp dir for isolated tests.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	return s
}

func TestUpsertAgentAddsNewAgent(t *testing.T) {
	s := newTestStore(t)
	team, err := s.Create("TeamA", "2x2", nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	updated, err := s.UpsertAgent(team.ID, AgentConfig{
		Name:        "Frontend",
		PromptID:    "p1",
		WorkDir:     "/proj/web",
		CLIType:     "gemini",
		SlotIndex:   1,
		UseWorktree: true,
	})
	if err != nil {
		t.Fatalf("UpsertAgent failed: %v", err)
	}
	if len(updated.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(updated.Agents))
	}
	got := updated.Agents[0]
	if got.Name != "Frontend" || got.PromptID != "p1" || got.WorkDir != "/proj/web" ||
		got.CLIType != "gemini" || got.SlotIndex != 1 || !got.UseWorktree {
		t.Fatalf("agent fields not persisted correctly: %+v", got)
	}
}

func TestUpsertAgentUpdatesExistingByName(t *testing.T) {
	s := newTestStore(t)
	team, _ := s.Create("TeamA", "2x2", nil)

	if _, err := s.UpsertAgent(team.ID, AgentConfig{Name: "Backend", WorkDir: "/old", CLIType: "claude", SlotIndex: 0}); err != nil {
		t.Fatalf("first upsert failed: %v", err)
	}
	updated, err := s.UpsertAgent(team.ID, AgentConfig{Name: "Backend", WorkDir: "/new", CLIType: "gemini", SlotIndex: 2})
	if err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}

	if len(updated.Agents) != 1 {
		t.Fatalf("expected agent to be updated in place (1 agent), got %d", len(updated.Agents))
	}
	got := updated.Agents[0]
	if got.WorkDir != "/new" || got.CLIType != "gemini" || got.SlotIndex != 2 {
		t.Fatalf("agent not updated correctly: %+v", got)
	}
}

// Role-preservation invariant: CreateTerminal/wizard never supply Role, so an
// upsert with empty Role must NOT clobber a Role the user set earlier.
func TestUpsertAgentPreservesRoleWhenEmpty(t *testing.T) {
	s := newTestStore(t)
	team, _ := s.Create("TeamA", "2x2", nil)

	// Seed an agent with a Role via Update (simulates a user-set role).
	if _, err := s.Update(team.ID, "TeamA", "2x2", []AgentConfig{
		{Name: "Backend", Role: "Backend Developer", CLIType: "claude"},
	}); err != nil {
		t.Fatalf("seed Update failed: %v", err)
	}

	// Upsert without Role (as CreateTerminal does).
	updated, err := s.UpsertAgent(team.ID, AgentConfig{Name: "Backend", WorkDir: "/repo", CLIType: "claude", SlotIndex: 0})
	if err != nil {
		t.Fatalf("UpsertAgent failed: %v", err)
	}
	got := updated.Agents[0]
	if got.Role != "Backend Developer" {
		t.Fatalf("Role was clobbered: expected %q, got %q", "Backend Developer", got.Role)
	}
	if got.WorkDir != "/repo" {
		t.Fatalf("WorkDir not updated: %+v", got)
	}
}

// Manager identity matching must be case-insensitive: a manager saved as "Pilot"
// is the same agent as "pilot"/" pilot ". resolveManagerIntent and RestartTerminal
// rely on this so a case-only difference can't make a manager look like a normal
// agent (see Codex review on PR #22).
func TestIsManagerAgent(t *testing.T) {
	tm := Team{ManagerAgent: "Pilot"}
	cases := []struct {
		name string
		want bool
	}{
		{"Pilot", true},
		{"pilot", true},
		{"  pilot  ", true},
		{"PILOT", true},
		{"Backend", false},
		{"", false},
	}
	for _, c := range cases {
		if got := tm.IsManagerAgent(c.name); got != c.want {
			t.Errorf("IsManagerAgent(%q) = %v, want %v", c.name, got, c.want)
		}
	}
	// No manager set → always false.
	if (Team{}).IsManagerAgent("Pilot") {
		t.Error("empty ManagerAgent should never match")
	}
}

// UpsertAgent matches names case-insensitively, but must NOT rewrite the stored
// Name to the new casing: Team.ManagerAgent keeps the original spelling and
// resolveManagerIntent compares it case-sensitively, so a case-only re-create
// would otherwise break manager recognition for that agent.
func TestUpsertAgentPreservesNameCasing(t *testing.T) {
	s := newTestStore(t)
	tm, _ := s.Create("TeamA", "2x2", []AgentConfig{
		{Name: "Pilot", CLIType: "claude", WorkDir: "/old"},
	})

	// Re-create with different casing of the same name.
	updated, err := s.UpsertAgent(tm.ID, AgentConfig{Name: "pilot", CLIType: "claude", WorkDir: "/new"})
	if err != nil {
		t.Fatalf("UpsertAgent failed: %v", err)
	}
	if len(updated.Agents) != 1 {
		t.Fatalf("expected case-insensitive match (1 agent), got %d", len(updated.Agents))
	}
	if updated.Agents[0].Name != "Pilot" {
		t.Fatalf("stored Name casing not preserved: expected %q, got %q", "Pilot", updated.Agents[0].Name)
	}
	// Other fields still update.
	if updated.Agents[0].WorkDir != "/new" {
		t.Fatalf("WorkDir not updated: %+v", updated.Agents[0])
	}
}

// A non-empty Role in the upsert payload should overwrite the existing one.
func TestUpsertAgentOverwritesRoleWhenProvided(t *testing.T) {
	s := newTestStore(t)
	team, _ := s.Create("TeamA", "2x2", nil)
	if _, err := s.Update(team.ID, "TeamA", "2x2", []AgentConfig{
		{Name: "Backend", Role: "Old Role", CLIType: "claude"},
	}); err != nil {
		t.Fatalf("seed Update failed: %v", err)
	}

	updated, err := s.UpsertAgent(team.ID, AgentConfig{Name: "Backend", Role: "New Role", CLIType: "claude"})
	if err != nil {
		t.Fatalf("UpsertAgent failed: %v", err)
	}
	if updated.Agents[0].Role != "New Role" {
		t.Fatalf("expected Role to be overwritten, got %q", updated.Agents[0].Role)
	}
}

func TestUpsertAgentUnknownTeam(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.UpsertAgent("does-not-exist", AgentConfig{Name: "X"}); err == nil {
		t.Fatal("expected error for unknown team, got nil")
	}
}

// slot_index must be written even when it is 0 (no omitempty) so positional
// slots survive a round-trip. use_worktree round-trips as well.
func TestAgentConfigSerializationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	team, _ := s.Create("TeamA", "2x2", nil)
	if _, err := s.UpsertAgent(team.ID, AgentConfig{Name: "Slot0", CLIType: "claude", SlotIndex: 0, UseWorktree: false}); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "teams.json"))
	if err != nil {
		t.Fatalf("read teams.json failed: %v", err)
	}
	var disk []Team
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(disk) != 1 || len(disk[0].Agents) != 1 {
		t.Fatalf("unexpected disk shape: %+v", disk)
	}
	// slot_index key must be present in the serialized JSON even when 0.
	if !jsonHasKey(raw, "slot_index") {
		t.Fatalf("slot_index key missing from serialized teams.json (omitempty leak): %s", raw)
	}
	if !jsonHasKey(raw, "use_worktree") {
		t.Fatalf("use_worktree key missing from serialized teams.json: %s", raw)
	}

	// Reload from disk and verify values survive.
	s2, _ := NewStore(dir)
	reloaded, err := s2.Get(team.ID)
	if err != nil {
		t.Fatalf("reload Get failed: %v", err)
	}
	if reloaded.Agents[0].SlotIndex != 0 {
		t.Fatalf("slot_index lost on reload: %+v", reloaded.Agents[0])
	}
}

func jsonHasKey(raw []byte, key string) bool {
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return false
	}
	for _, obj := range arr {
		agentsRaw, ok := obj["agents"]
		if !ok {
			continue
		}
		var agents []map[string]json.RawMessage
		if err := json.Unmarshal(agentsRaw, &agents); err != nil {
			continue
		}
		for _, a := range agents {
			if _, ok := a[key]; ok {
				return true
			}
		}
	}
	return false
}

// Old teams.json without slot_index/use_worktree must deserialize cleanly with
// zero values (backward compatibility — no migration required).
func TestLoadLegacyTeamsJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := `[{"id":"t1","name":"Legacy","grid_layout":"2x2","manager_agent":"Pilot",
		"agents":[{"name":"Pilot","role":"","prompt_id":"","work_dir":"/p","cli_type":"claude"}]}]`
	if err := os.WriteFile(filepath.Join(dir, "teams.json"), []byte(legacy), 0644); err != nil {
		t.Fatalf("write legacy file failed: %v", err)
	}

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	team, err := s.Get("t1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if len(team.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(team.Agents))
	}
	if team.Agents[0].SlotIndex != 0 || team.Agents[0].UseWorktree != false {
		t.Fatalf("legacy zero-values wrong: %+v", team.Agents[0])
	}
}

// A no-op UpsertAgent (identical config) must skip the disk write. Proven
// deterministically and cross-platform: after seeding, the file is overwritten
// out-of-band with a sentinel. A no-op upsert skips save() so the sentinel
// survives; a changed upsert calls save() and replaces it with valid JSON.
func TestUpsertAgentSkipsWriteWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	tm, _ := s.Create("TeamA", "2x2", nil)
	cfg := AgentConfig{Name: "A", CLIType: "claude", WorkDir: "/repo", SlotIndex: 1}
	if _, err := s.UpsertAgent(tm.ID, cfg); err != nil {
		t.Fatalf("seed upsert failed: %v", err)
	}

	filePath := filepath.Join(dir, "teams.json")
	sentinel := []byte("SENTINEL-NOT-REWRITTEN")
	if err := os.WriteFile(filePath, sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel failed: %v", err)
	}

	// Identical cfg → no change → skip write → sentinel survives untouched.
	if _, err := s.UpsertAgent(tm.ID, cfg); err != nil {
		t.Fatalf("no-op upsert should skip the write and succeed, got: %v", err)
	}
	if raw, _ := os.ReadFile(filePath); !bytes.Equal(raw, sentinel) {
		t.Fatalf("no-op upsert rewrote the file (sentinel gone): %s", raw)
	}

	// Changed cfg → writes → sentinel replaced with valid JSON reflecting change.
	if _, err := s.UpsertAgent(tm.ID, AgentConfig{Name: "A", CLIType: "claude", WorkDir: "/changed", SlotIndex: 1}); err != nil {
		t.Fatalf("changed upsert failed: %v", err)
	}
	raw, _ := os.ReadFile(filePath)
	var disk []Team
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("changed upsert should rewrite valid JSON, got: %s", raw)
	}
	if len(disk) == 0 || len(disk[0].Agents) == 0 || disk[0].Agents[0].WorkDir != "/changed" {
		t.Fatalf("changed upsert not persisted to disk: %+v", disk)
	}
}

// When save() fails, UpsertAgent must roll back its in-memory mutation so memory
// and disk don't diverge. Forced cross-platform by removing the data dir before
// the upsert (the temp-file write then fails). Covers both the update and append
// paths.
func TestUpsertAgentRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	tm, _ := s.Create("TeamA", "2x2", []AgentConfig{{Name: "A", Role: "R", CLIType: "claude", WorkDir: "/old"}})

	// Remove the data dir so save()'s temp-file write fails.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll failed: %v", err)
	}

	// Update path: must fail and leave the existing agent untouched in memory.
	if _, err := s.UpsertAgent(tm.ID, AgentConfig{Name: "A", CLIType: "claude", WorkDir: "/new"}); err == nil {
		t.Fatal("expected save failure on update path")
	}
	got, _ := s.Get(tm.ID)
	if len(got.Agents) != 1 || got.Agents[0].WorkDir != "/old" {
		t.Fatalf("update not rolled back in memory: %+v", got.Agents)
	}

	// Append path: must fail and not leave a dangling appended agent.
	if _, err := s.UpsertAgent(tm.ID, AgentConfig{Name: "B", CLIType: "claude"}); err == nil {
		t.Fatal("expected save failure on append path")
	}
	got, _ = s.Get(tm.ID)
	if len(got.Agents) != 1 {
		t.Fatalf("append not rolled back in memory: %+v", got.Agents)
	}
}

// Get()/List() hand out a Team whose Agents slice shares the store's backing
// array. UpsertAgent must NOT mutate that array in place, or a reader iterating a
// previously-obtained Team races with the write. Run under -race.
func TestUpsertAgentNoRaceWithReaders(t *testing.T) {
	s := newTestStore(t)
	tm, _ := s.Create("TeamA", "custom", []AgentConfig{
		{Name: "A", CLIType: "claude", WorkDir: "/a"},
		{Name: "B", CLIType: "claude", WorkDir: "/b"},
	})

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := s.Get(tm.ID)
				if err != nil {
					continue
				}
				for _, a := range got.Agents {
					_ = a.WorkDir + a.Name // read shared backing array
				}
			}
		}()
	}

	var writers sync.WaitGroup
	for w := 0; w < 4; w++ {
		writers.Add(1)
		go func(id int) {
			defer writers.Done()
			for k := range 300 {
				// Vary WorkDir so each upsert actually mutates (not a no-op).
				_, _ = s.UpsertAgent(tm.ID, AgentConfig{Name: "A", CLIType: "claude", WorkDir: fmt.Sprintf("/a-%d-%d", id, k)})
			}
		}(w)
	}

	writers.Wait()
	close(stop)
	readers.Wait()
}

// save() must be atomic: a successful save never leaves a stray .tmp file
// behind and produces a fully-valid teams.json.
func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	team, _ := s.Create("TeamA", "2x2", nil)
	if _, err := s.UpsertAgent(team.ID, AgentConfig{Name: "A", CLIType: "claude", SlotIndex: 0}); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("stray temp file left after atomic save: %s", e.Name())
		}
	}
	// File must be valid JSON.
	raw, _ := os.ReadFile(filepath.Join(dir, "teams.json"))
	var disk []Team
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("teams.json is not valid JSON after save: %v", err)
	}
}

// Concurrent UpsertAgent calls must be race-free (mirrors orchestrator test
// pattern from CLAUDE.md "Testing Patterns").
func TestUpsertAgentConcurrent(t *testing.T) {
	s := newTestStore(t)
	team, _ := s.Create("TeamA", "custom", nil)

	var wg sync.WaitGroup
	const n = 60
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _ = s.UpsertAgent(team.ID, AgentConfig{
				Name:      "agent-" + string(rune('A'+idx%26)),
				CLIType:   "claude",
				SlotIndex: idx,
			})
		}(i)
	}
	wg.Wait()

	// Should not panic / data-race; at most 26 distinct names persisted.
	reloaded, err := s.Get(team.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if len(reloaded.Agents) == 0 || len(reloaded.Agents) > 26 {
		t.Fatalf("unexpected agent count after concurrent upserts: %d", len(reloaded.Agents))
	}
}
