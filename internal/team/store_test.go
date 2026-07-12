package team

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
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

func TestTeamStoreDefensiveCopiesAgentSlices(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)

	agents := []AgentConfig{{Name: "agent-a", Role: "writer"}}
	tm, err := s.Create("TeamA", "2x2", agents)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	agents[0].Role = "mutated-after-create"
	got, _ := s.Get(tm.ID)
	if got.Agents[0].Role != "writer" {
		t.Fatalf("Create retained caller-owned agents slice: %+v", got.Agents)
	}
	tm.Agents[0].Role = "mutated-create-result"
	gotAfterCreateResultMutation, _ := s.Get(tm.ID)
	if gotAfterCreateResultMutation.Agents[0].Role != "writer" {
		t.Fatalf("Create returned the store's agents backing array: %+v", gotAfterCreateResultMutation.Agents)
	}

	got.Agents[0].Role = "mutated-get-result"
	gotAgain, _ := s.Get(tm.ID)
	if gotAgain.Agents[0].Role != "writer" {
		t.Fatalf("Get exposed the store's agents backing array: %+v", gotAgain.Agents)
	}

	listed := s.List()
	listed[0].Agents[0].Role = "mutated-list-result"
	listedAgain := s.List()
	if listedAgain[0].Agents[0].Role != "writer" {
		t.Fatalf("List exposed the store's agents backing array: %+v", listedAgain[0].Agents)
	}

	updatedAgents := []AgentConfig{{Name: "agent-b", Role: "reviewer"}}
	if _, err := s.Update(tm.ID, "TeamA", "2x2", updatedAgents); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	updatedAgents[0].Role = "mutated-after-update"
	updated, _ := s.Get(tm.ID)
	if updated.Agents[0].Role != "reviewer" {
		t.Fatalf("Update retained caller-owned agents slice: %+v", updated.Agents)
	}

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reload NewStore failed: %v", err)
	}
	reloaded, _ := s2.Get(tm.ID)
	if reloaded.Agents[0].Role != "reviewer" {
		t.Fatalf("memory/disk diverged after external mutation: %+v", reloaded.Agents)
	}
}

func TestCreateRejectsCaseInsensitiveDuplicateTeamName(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("Alpha", "2x2", nil); err != nil {
		t.Fatalf("Create Alpha failed: %v", err)
	}
	if _, err := s.Create("alpha", "2x2", nil); err == nil {
		t.Fatal("expected case-insensitive duplicate team name error, got nil")
	}
}

func TestStoreRejectsBlankRequiredNames(t *testing.T) {
	s := newTestStore(t)
	tm, err := s.Create("TeamA", "2x2", nil)
	if err != nil {
		t.Fatalf("Create TeamA failed: %v", err)
	}

	cases := []struct {
		name string
		fn   func() error
	}{
		{"create blank team", func() error {
			_, err := s.Create("", "2x2", nil)
			return err
		}},
		{"create whitespace team", func() error {
			_, err := s.Create("   ", "2x2", nil)
			return err
		}},
		{"update whitespace team", func() error {
			_, err := s.Update(tm.ID, "	", "2x2", nil)
			return err
		}},
		{"create blank agent config", func() error {
			_, err := s.Create("TeamWithBlankAgent", "2x2", []AgentConfig{{Name: ""}})
			return err
		}},
		{"update blank agent config", func() error {
			_, err := s.Update(tm.ID, "TeamA", "2x2", []AgentConfig{{Name: ""}})
			return err
		}},
		{"upsert blank agent", func() error {
			_, err := s.UpsertAgent(tm.ID, AgentConfig{Name: "", CLIType: "claude"})
			return err
		}},
		{"set whitespace manager", func() error {
			_, err := s.SetManager(tm.ID, "  ")
			return err
		}},
		{"set blank observer", func() error {
			_, err := s.SetObserver(tm.ID, "")
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); err == nil {
				t.Fatal("expected blank required name to be rejected")
			}
		})
	}
}

func TestStoreRejectsDuplicateAgentConfigNames(t *testing.T) {
	s := newTestStore(t)
	tm, err := s.Create("TeamA", "2x2", nil)
	if err != nil {
		t.Fatalf("Create TeamA failed: %v", err)
	}

	cases := []struct {
		name string
		fn   func() error
	}{
		{"create exact duplicate", func() error {
			_, err := s.Create("TeamExactDup", "2x2", []AgentConfig{{Name: "Pilot"}, {Name: "Pilot"}})
			return err
		}},
		{"create case duplicate", func() error {
			_, err := s.Create("TeamCaseDup", "2x2", []AgentConfig{{Name: "Pilot"}, {Name: "pilot"}})
			return err
		}},
		{"update trimmed duplicate", func() error {
			_, err := s.Update(tm.ID, "TeamA", "2x2", []AgentConfig{{Name: " Pilot "}, {Name: "pilot"}})
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); err == nil {
				t.Fatal("expected duplicate agent config names to be rejected")
			}
		})
	}
}

func TestStoreNormalizesRequiredNamesAtBoundary(t *testing.T) {
	s := newTestStore(t)
	tm, err := s.Create(" TeamA ", "2x2", []AgentConfig{{Name: " Pilot ", CLIType: "claude"}})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if tm.Name != "TeamA" || len(tm.Agents) != 1 || tm.Agents[0].Name != "Pilot" {
		t.Fatalf("Create did not trim persisted team/agent names: %+v", tm)
	}

	updated, err := s.Update(tm.ID, " TeamB ", "2x2", []AgentConfig{{Name: " Reviewer ", CLIType: "gemini"}})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if updated.Name != "TeamB" || len(updated.Agents) != 1 || updated.Agents[0].Name != "Reviewer" {
		t.Fatalf("Update did not trim persisted team/agent names: %+v", updated)
	}

	updated, err = s.UpsertAgent(tm.ID, AgentConfig{Name: " reviewer ", WorkDir: "/repo", CLIType: "claude"})
	if err != nil {
		t.Fatalf("UpsertAgent failed: %v", err)
	}
	if len(updated.Agents) != 1 || updated.Agents[0].Name != "Reviewer" || updated.Agents[0].WorkDir != "/repo" {
		t.Fatalf("UpsertAgent did not trim before matching existing agent: %+v", updated.Agents)
	}

	updated, err = s.SetManager(tm.ID, " Reviewer ")
	if err != nil {
		t.Fatalf("SetManager failed: %v", err)
	}
	if updated.ManagerAgent != "Reviewer" {
		t.Fatalf("SetManager did not trim manager name: %q", updated.ManagerAgent)
	}
	updated, err = s.SetObserver(tm.ID, " Reviewer ")
	if err != nil {
		t.Fatalf("SetObserver failed: %v", err)
	}
	if updated.ManagerAgent != "" || updated.Agents[0].Name != "Reviewer" || updated.Agents[0].Role != RoleObserver {
		t.Fatalf("SetObserver did not trim before clearing manager/setting observer: %+v", updated)
	}
}

func TestStoreRejectsTrimmedInvalidNames(t *testing.T) {
	s := newTestStore(t)
	tm, err := s.Create("TeamA", "2x2", nil)
	if err != nil {
		t.Fatalf("Create TeamA failed: %v", err)
	}
	cases := []struct {
		name string
		fn   func() error
	}{
		{"create trimmed leading dot team", func() error {
			_, err := s.Create(" .hidden", "2x2", nil)
			return err
		}},
		{"update trimmed leading dot agent", func() error {
			_, err := s.Update(tm.ID, "TeamA", "2x2", []AgentConfig{{Name: " .agent"}})
			return err
		}},
		{"upsert trimmed leading dot agent", func() error {
			_, err := s.UpsertAgent(tm.ID, AgentConfig{Name: " .agent"})
			return err
		}},
		{"set trimmed leading dot manager", func() error {
			_, err := s.SetManager(tm.ID, " .manager")
			return err
		}},
		{"set trimmed leading dot observer", func() error {
			_, err := s.SetObserver(tm.ID, " .observer")
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); err == nil {
				t.Fatal("expected trimmed invalid name to be rejected")
			}
		})
	}
}

func TestCreateReportsChatDirFailureAndDoesNotPersistTeam(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rooms"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed rooms file failed: %v", err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	_, err = s.Create("TeamA", "2x2", nil)
	if err == nil {
		t.Fatal("expected Create to report chat directory creation failure")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "rooms")) {
		t.Fatalf("expected error to include rooms path, got %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("failed Create persisted team in memory: %+v", got)
	}
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reload NewStore failed: %v", err)
	}
	if got := s2.List(); len(got) != 0 {
		t.Fatalf("failed Create persisted team to disk: %+v", got)
	}
}

func TestUpdateAllowsSameTeamNameAndRejectsOtherCaseDuplicate(t *testing.T) {
	s := newTestStore(t)
	alpha, err := s.Create("Alpha", "2x2", nil)
	if err != nil {
		t.Fatalf("Create Alpha failed: %v", err)
	}
	beta, err := s.Create("Beta", "2x2", nil)
	if err != nil {
		t.Fatalf("Create Beta failed: %v", err)
	}

	if _, err := s.Update(alpha.ID, "alpha", "2x3", nil); err != nil {
		t.Fatalf("Update should allow a case-only rename of the same team: %v", err)
	}
	if _, err := s.Update(beta.ID, "ALPHA", "2x2", nil); err == nil {
		t.Fatal("expected update to reject another team's case-insensitive name collision")
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

func TestNewStoreReportsCorruptTeamsJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"truncated json", `[{"id":"t1","name":"Broken"}`},
		{"wrong top-level shape", `{"id":"t1","name":"Broken"}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "teams.json"), []byte(c.raw), 0o644); err != nil {
				t.Fatalf("write corrupt teams.json failed: %v", err)
			}

			_, err := NewStore(dir)
			if err == nil {
				t.Fatal("NewStore must report corrupt teams.json instead of starting with an empty store")
			}
			msg := err.Error()
			if !strings.Contains(msg, "parse teams file") || !strings.Contains(msg, filepath.Join(dir, "teams.json")) {
				t.Fatalf("expected parse error to include teams.json path, got %v", err)
			}
		})
	}
}

func TestNewStoreReportsTeamsJSONReadPath(t *testing.T) {
	dir := t.TempDir()
	teamsPath := filepath.Join(dir, "teams.json")
	if err := os.Mkdir(teamsPath, 0o755); err != nil {
		t.Fatalf("create directory at teams.json path: %v", err)
	}

	_, err := NewStore(dir)
	if err == nil {
		t.Fatal("expected directory teams.json to return a read error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "read teams file") || !strings.Contains(msg, teamsPath) {
		t.Fatalf("expected read error to include teams.json path, got %v", err)
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

// The charter (custom_prompt) is free text but is pasted verbatim into the agent
// PTY at startup via bracketed paste (sendStartupPrompt). sanitizeCharter must
// strip anything that could break that paste — raw ESC, the bracketed-paste
// markers themselves, and other C0 control bytes — while preserving newline and
// tab so multi-line charters survive. Mirrors the InjectText sanitization
// (internal/pty/manager.go).
func TestSanitizeCharter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unchanged", "Bu oda X projesi için kuruldu.", "Bu oda X projesi için kuruldu."},
		{"newline and tab preserved", "satır1\nsatır2\tson", "satır1\nsatır2\tson"},
		{"strips raw ESC", "a\x1bb", "ab"},
		{"strips bracketed-paste close marker", "a\x1b[201~b", "ab"},
		{"strips bracketed-paste open marker", "a\x1b[200~b", "ab"},
		{"strips carriage return (keeps newline)", "a\r\nb", "a\nb"},
		{"strips NUL and bell", "a\x00\x07b", "ab"},
		{"strips DEL", "a\x7fb", "ab"},
		{"empty stays empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeCharter(c.in); got != c.want {
				t.Errorf("sanitizeCharter(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The charter has a soft length cap. The cap is rune-based so a multibyte
// (Turkish) character is never split mid-encoding.
func TestSanitizeCharterCapsLengthRuneSafe(t *testing.T) {
	in := strings.Repeat("ç", maxCharterLen+500)
	got := sanitizeCharter(in)
	if n := len([]rune(got)); n != maxCharterLen {
		t.Fatalf("expected %d runes after cap, got %d", maxCharterLen, n)
	}
	// Must remain valid UTF-8 (no split multibyte rune at the boundary).
	if !utf8.ValidString(got) {
		t.Fatalf("capped charter is not valid UTF-8")
	}
}

// C1 control bytes (U+0080-U+009F), Unicode bidi/format controls, and the
// line/paragraph separators all sit above the C0/DEL range the base table covers
// and must also be stripped: they are never legitimate in charter prose, and the
// bidi overrides in particular could make the charter a human reviews in the modal
// differ from the bytes pasted into the agent (Trojan-Source class). Runes are
// built from code points so the test source stays pure ASCII (a literal U+FEFF
// would be an illegal BOM in Go source).
func TestSanitizeCharterStripsControlAndFormatRunes(t *testing.T) {
	strip := []struct {
		name string
		r    rune
	}{
		{"C1 CSI", 0x009b},
		{"C1 NEL", 0x0085},
		{"C1 low bound", 0x0080},
		{"C1 high bound", 0x009f},
		{"bidi RLO", 0x202e},
		{"bidi LRO", 0x202d},
		{"bidi isolate LRI", 0x2066},
		{"bidi isolate PDI", 0x2069},
		{"LRM", 0x200e},
		{"RLM", 0x200f},
		{"ALM (Arabic Letter Mark)", 0x061c},
		{"ZWSP", 0x200b},
		{"ZWNJ", 0x200c},
		{"ZWJ", 0x200d},
		{"WORD JOINER", 0x2060},
		{"BOM/ZWNBSP", 0xfeff},
		{"line separator", 0x2028},
		{"paragraph separator", 0x2029},
	}
	for _, c := range strip {
		in := "a" + string(c.r) + "b"
		if got := sanitizeCharter(in); got != "ab" {
			t.Errorf("%s (U+%04X): sanitizeCharter(%q) = %q, want %q", c.name, c.r, in, got, "ab")
		}
	}

	// Turkish letters (Latin-1 supplement + Latin Extended-A) must survive — they
	// are outside every stripped range.
	const tr = "çğıöşü ÇĞİÖŞÜ"
	if got := sanitizeCharter(tr); got != tr {
		t.Errorf("Turkish letters altered: got %q, want %q", got, tr)
	}
}

// Pin the strip-before-cap ordering: a bracketed-paste marker straddling the rune
// cap boundary must be removed entirely (no stray ESC), and the result must be
// exactly maxCharterLen runes. If the cap ran before the strip, truncation could
// slice through the marker and leave a live ESC in the output.
func TestSanitizeCharterStripsMarkerStraddlingCap(t *testing.T) {
	in := strings.Repeat("a", maxCharterLen-2) + "\x1b[201~" + strings.Repeat("b", 50)
	got := sanitizeCharter(in)

	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("result contains a stray ESC byte (strip ran after cap?): %q", got)
	}
	if n := len([]rune(got)); n != maxCharterLen {
		t.Fatalf("expected %d runes after cap, got %d", maxCharterLen, n)
	}
}

// SetCustomPrompt writes the room charter via a targeted single-field endpoint
// (SetManager pattern) rather than the positional Update, which would reset the
// charter on every grid-layout change. The value must round-trip through Get.
func TestSetManagerClearsObserverRoleCaseInsensitive(t *testing.T) {
	s := newTestStore(t)
	tm, _ := s.Create("TeamA", "2x2", []AgentConfig{
		{Name: "Pilot", Role: RoleObserver, CLIType: "claude"},
	})

	updated, err := s.SetManager(tm.ID, "pilot")
	if err != nil {
		t.Fatalf("SetManager failed: %v", err)
	}
	if updated.ManagerAgent != "pilot" {
		t.Fatalf("ManagerAgent = %q, want %q", updated.ManagerAgent, "pilot")
	}
	if len(updated.Agents) != 1 || updated.Agents[0].Role != "" {
		t.Fatalf("manager observer role was not cleared: %+v", updated.Agents)
	}
}

func TestSetManagerSkipsWriteWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	tm, err := s.Create("TeamA", "2x2", []AgentConfig{{Name: "Pilot", Role: "Lead", CLIType: "claude"}})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if _, err := s.SetManager(tm.ID, "Pilot"); err != nil {
		t.Fatalf("seed SetManager failed: %v", err)
	}

	filePath := filepath.Join(dir, "teams.json")
	sentinel := []byte("SENTINEL-NOT-REWRITTEN")
	if err := os.WriteFile(filePath, sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel failed: %v", err)
	}
	if _, err := s.SetManager(tm.ID, "Pilot"); err != nil {
		t.Fatalf("no-op SetManager should succeed: %v", err)
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read sentinel failed: %v", err)
	}
	if !bytes.Equal(raw, sentinel) {
		t.Fatalf("unchanged SetManager rewrote the file: %s", raw)
	}
}

func TestSetManagerWritesWhenUnchangedManagerClearsObserver(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	tm, err := s.Create("TeamA", "2x2", []AgentConfig{{Name: "Pilot", Role: RoleObserver, CLIType: "claude"}})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	s.mu.Lock()
	s.teams[0].ManagerAgent = "Pilot"
	s.mu.Unlock()

	filePath := filepath.Join(dir, "teams.json")
	sentinel := []byte("SENTINEL-SHOULD-BE-REWRITTEN")
	if err := os.WriteFile(filePath, sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel failed: %v", err)
	}
	updated, err := s.SetManager(tm.ID, "Pilot")
	if err != nil {
		t.Fatalf("SetManager failed: %v", err)
	}
	if updated.Agents[0].Role != "" {
		t.Fatalf("observer role was not cleared for unchanged manager: %+v", updated.Agents)
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read rewritten file failed: %v", err)
	}
	if bytes.Equal(raw, sentinel) {
		t.Fatal("SetManager skipped write even though it cleared observer role")
	}
}

func TestSetObserverSkipsWriteWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	tm, err := s.Create("TeamA", "2x2", []AgentConfig{{Name: "Pilot", Role: RoleObserver, CLIType: "claude"}})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	filePath := filepath.Join(dir, "teams.json")
	sentinel := []byte("SENTINEL-NOT-REWRITTEN")
	if err := os.WriteFile(filePath, sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel failed: %v", err)
	}
	if _, err := s.SetObserver(tm.ID, "pilot"); err != nil {
		t.Fatalf("no-op SetObserver should succeed: %v", err)
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read sentinel failed: %v", err)
	}
	if !bytes.Equal(raw, sentinel) {
		t.Fatalf("unchanged SetObserver rewrote the file: %s", raw)
	}
}

func TestSetObserverWritesWhenClearingManager(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	tm, err := s.Create("TeamA", "2x2", []AgentConfig{{Name: "Pilot", Role: RoleObserver, CLIType: "claude"}})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if _, err := s.SetManager(tm.ID, "Pilot"); err != nil {
		t.Fatalf("SetManager failed: %v", err)
	}

	filePath := filepath.Join(dir, "teams.json")
	sentinel := []byte("SENTINEL-SHOULD-BE-REWRITTEN")
	if err := os.WriteFile(filePath, sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel failed: %v", err)
	}
	updated, err := s.SetObserver(tm.ID, "pilot")
	if err != nil {
		t.Fatalf("SetObserver failed: %v", err)
	}
	if updated.ManagerAgent != "" {
		t.Fatalf("observer should clear manager assignment, got %q", updated.ManagerAgent)
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read rewritten file failed: %v", err)
	}
	if bytes.Equal(raw, sentinel) {
		t.Fatal("SetObserver skipped write even though it cleared manager assignment")
	}
}

func TestSetCustomPromptRoundTrip(t *testing.T) {
	s := newTestStore(t)
	tm, err := s.Create("TeamA", "2x2", nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	const charter = "Bu oda ödeme servisi refactor'u için.\nKurallar: küçük PR'lar."
	updated, err := s.SetCustomPrompt(tm.ID, charter)
	if err != nil {
		t.Fatalf("SetCustomPrompt failed: %v", err)
	}
	if updated.CustomPrompt != charter {
		t.Fatalf("returned CustomPrompt = %q, want %q", updated.CustomPrompt, charter)
	}

	got, err := s.Get(tm.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.CustomPrompt != charter {
		t.Fatalf("Get CustomPrompt = %q, want %q", got.CustomPrompt, charter)
	}
}

// The charter must survive a store reload (persisted to teams.json, not just held
// in memory).
func TestSetCustomPromptPersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	tm, _ := s.Create("TeamA", "2x2", nil)

	const charter = "Kalıcı oda misyonu."
	if _, err := s.SetCustomPrompt(tm.ID, charter); err != nil {
		t.Fatalf("SetCustomPrompt failed: %v", err)
	}

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reload NewStore failed: %v", err)
	}
	reloaded, err := s2.Get(tm.ID)
	if err != nil {
		t.Fatalf("reload Get failed: %v", err)
	}
	if reloaded.CustomPrompt != charter {
		t.Fatalf("reloaded CustomPrompt = %q, want %q", reloaded.CustomPrompt, charter)
	}
}

// SetCustomPrompt must sanitize its input (it is pasted into the PTY at startup).
func TestSetCustomPromptSanitizes(t *testing.T) {
	s := newTestStore(t)
	tm, _ := s.Create("TeamA", "2x2", nil)

	updated, err := s.SetCustomPrompt(tm.ID, "misyon\x1b[201~\x00 bitiş")
	if err != nil {
		t.Fatalf("SetCustomPrompt failed: %v", err)
	}
	if want := "misyon bitiş"; updated.CustomPrompt != want {
		t.Fatalf("CustomPrompt = %q, want sanitized %q", updated.CustomPrompt, want)
	}
}

// Setting the charter must not disturb the team's other fields (name, agents,
// manager). It is a single-field update.
func TestSetCustomPromptPreservesOtherFields(t *testing.T) {
	s := newTestStore(t)
	tm, _ := s.Create("TeamA", "2x2", []AgentConfig{
		{Name: "Pilot", Role: "Lead", CLIType: "claude", SlotIndex: 0},
	})
	if _, err := s.SetManager(tm.ID, "Pilot"); err != nil {
		t.Fatalf("SetManager failed: %v", err)
	}

	updated, err := s.SetCustomPrompt(tm.ID, "yeni misyon")
	if err != nil {
		t.Fatalf("SetCustomPrompt failed: %v", err)
	}
	if updated.Name != "TeamA" {
		t.Fatalf("Name changed: %q", updated.Name)
	}
	if updated.ManagerAgent != "Pilot" {
		t.Fatalf("ManagerAgent changed: %q", updated.ManagerAgent)
	}
	if len(updated.Agents) != 1 || updated.Agents[0].Name != "Pilot" || updated.Agents[0].Role != "Lead" {
		t.Fatalf("Agents changed: %+v", updated.Agents)
	}
}

func TestSetCustomPromptUnknownTeam(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetCustomPrompt("does-not-exist", "x"); err == nil {
		t.Fatal("expected error for unknown team, got nil")
	}
}

// SetCustomPrompt must skip the disk write when the sanitized charter equals the
// stored one (matches UpsertAgent's no-op optimization). This also covers the case
// where the raw input differs but sanitizes to the same value (e.g. a trailing
// control char is stripped), which the frontend isDirty check cannot detect.
// Proven with the sentinel-file technique: a skipped write leaves the sentinel.
func TestSetCustomPromptSkipsWriteWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	tm, _ := s.Create("TeamA", "2x2", nil)
	if _, err := s.SetCustomPrompt(tm.ID, "misyon"); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	filePath := filepath.Join(dir, "teams.json")
	sentinel := []byte("SENTINEL-NOT-REWRITTEN")

	// Identical charter → skip write → sentinel survives.
	if err := os.WriteFile(filePath, sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel failed: %v", err)
	}
	if _, err := s.SetCustomPrompt(tm.ID, "misyon"); err != nil {
		t.Fatalf("no-op SetCustomPrompt should succeed: %v", err)
	}
	if raw, _ := os.ReadFile(filePath); !bytes.Equal(raw, sentinel) {
		t.Fatalf("identical SetCustomPrompt rewrote the file: %s", raw)
	}

	// Raw differs but sanitizes to the same value → also skip.
	if err := os.WriteFile(filePath, sentinel, 0o644); err != nil {
		t.Fatalf("rewrite sentinel failed: %v", err)
	}
	if _, err := s.SetCustomPrompt(tm.ID, "misyon\x00"); err != nil {
		t.Fatalf("sanitize-equal SetCustomPrompt should succeed: %v", err)
	}
	if raw, _ := os.ReadFile(filePath); !bytes.Equal(raw, sentinel) {
		t.Fatalf("sanitize-equal SetCustomPrompt rewrote the file: %s", raw)
	}

	// A genuinely different charter must write valid JSON.
	if _, err := s.SetCustomPrompt(tm.ID, "yeni misyon"); err != nil {
		t.Fatalf("changed SetCustomPrompt failed: %v", err)
	}
	raw, _ := os.ReadFile(filePath)
	var disk []Team
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("changed SetCustomPrompt should rewrite valid JSON: %s", raw)
	}
	if len(disk) != 1 || disk[0].CustomPrompt != "yeni misyon" {
		t.Fatalf("changed charter not persisted: %+v", disk)
	}
}

// When save() fails, SetCustomPrompt must roll back its in-memory mutation so the
// store doesn't diverge from teams.json — composeAgentPrompt reads the in-memory
// store, so a charter the UI thinks failed to save would otherwise still be
// injected into new agents until restart. Mirrors UpsertAgent's rollback
// (forced cross-platform by removing the data dir so the temp-file write fails).
func TestSetCustomPromptRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	tm, _ := s.Create("TeamA", "2x2", nil)
	if _, err := s.SetCustomPrompt(tm.ID, "ilk misyon"); err != nil {
		t.Fatalf("seed SetCustomPrompt failed: %v", err)
	}

	// Remove the data dir so save()'s temp-file write fails.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll failed: %v", err)
	}

	if _, err := s.SetCustomPrompt(tm.ID, "yeni misyon"); err == nil {
		t.Fatal("expected save failure, got nil")
	}
	got, _ := s.Get(tm.ID)
	if got.CustomPrompt != "ilk misyon" {
		t.Fatalf("CustomPrompt not rolled back in memory: got %q, want %q", got.CustomPrompt, "ilk misyon")
	}
}

func TestUpdateRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	tm, _ := s.Create("TeamA", "2x2", []AgentConfig{{Name: "agent-a", SlotIndex: 1}})

	// Remove the data dir so save()'s temp-file write fails.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll failed: %v", err)
	}

	if _, err := s.Update(tm.ID, "TeamB", "1x1", []AgentConfig{{Name: "agent-b", SlotIndex: 2}}); err == nil {
		t.Fatal("expected save failure, got nil")
	}
	got, _ := s.Get(tm.ID)
	if got.Name != "TeamA" || got.GridLayout != "2x2" || len(got.Agents) != 1 || got.Agents[0].Name != "agent-a" {
		t.Fatalf("Update did not roll back in memory: %+v", got)
	}
}

func TestCreateRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// Remove the data dir so save()'s temp-file write fails.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll failed: %v", err)
	}

	if _, err := s.Create("TeamA", "2x2", nil); err == nil {
		t.Fatal("expected save failure, got nil")
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("Create did not roll back in memory: %+v", got)
	}
}

func TestNewStoreReturnsDataDirCreationError(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(notDir, []byte("file"), 0o644); err != nil {
		t.Fatalf("write sentinel file: %v", err)
	}

	if _, err := NewStore(filepath.Join(notDir, "teams-data")); err == nil {
		t.Fatal("NewStore must surface data-dir creation errors instead of starting with an unsaveable empty store")
	}
}

func TestDeleteRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	a, err := s.Create("TeamA", "2x2", nil)
	if err != nil {
		t.Fatalf("create TeamA failed: %v", err)
	}
	b, err := s.Create("TeamB", "1x1", nil)
	if err != nil {
		t.Fatalf("create TeamB failed: %v", err)
	}

	// Remove the data dir so save()'s temp-file write fails.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll failed: %v", err)
	}

	if err := s.Delete(a.ID); err == nil {
		t.Fatal("expected save failure, got nil")
	}
	if _, err := s.Get(a.ID); err != nil {
		t.Fatalf("Delete did not roll back removed team in memory: %v", err)
	}
	if _, err := s.Get(b.ID); err != nil {
		t.Fatalf("Delete rollback lost unaffected team: %v", err)
	}
}

func TestCreate_RejectsDuplicateName(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("Alpha", "2x2", nil); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := s.Create("Alpha", "2x2", nil); err == nil {
		t.Fatalf("ikinci 'Alpha' Create hata döndürmeliydi")
	}
	// Case-insensitive FS (macOS/Windows): "alpha" ve "Alpha" aynı dosyaya yazar → reddedilmeli.
	if _, err := s.Create("alpha", "2x2", nil); err == nil {
		t.Fatalf("case-insensitive 'alpha' de reddedilmeliydi")
	}
}

func TestUpdate_RejectsDuplicateName(t *testing.T) {
	s := newTestStore(t)
	a, err := s.Create("Alpha", "2x2", nil)
	if err != nil {
		t.Fatalf("create Alpha: %v", err)
	}
	b, err := s.Create("Beta", "2x2", nil)
	if err != nil {
		t.Fatalf("create Beta: %v", err)
	}
	// Beta'yı "alpha"ya yeniden adlandırma (Alpha ile case-insensitive çakışır) → reddedilmeli.
	if _, err := s.Update(b.ID, "alpha", "2x2", nil); err == nil {
		t.Fatalf("Beta'yı 'alpha'ya yeniden adlandırma reddedilmeliydi")
	}
	// Takımı kendi adıyla (case değişimi dahil) güncellemek izinli olmalı.
	if _, err := s.Update(a.ID, "Alpha", "2x2", nil); err != nil {
		t.Fatalf("takımı kendi adıyla güncelleme izinli olmalı: %v", err)
	}
}
