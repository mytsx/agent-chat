package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	ptymgr "desktop/internal/pty"
	"desktop/internal/team"
)

// recordingInject returns an inject func that records every call and optionally
// fails for specific session IDs. broadcastToSessions injects concurrently, so
// the calls slice is mutex-guarded against the racing appends.
func recordingInject(failFor map[string]bool) (func(string, string, bool) error, *[]string) {
	var mu sync.Mutex
	var calls []string
	fn := func(sessionID, text string, submit bool) error {
		if failFor[sessionID] {
			return fmt.Errorf("session not found: %s", sessionID)
		}
		mu.Lock()
		calls = append(calls, fmt.Sprintf("%s|%s|%v", sessionID, text, submit))
		mu.Unlock()
		return nil
	}
	return fn, &calls
}

func noRoles(string) string { return "" }

// All non-observer sessions receive the injection with the broadcast text.
func TestRenderSummaryPromptText_DoesNotReprocessTranscript(t *testing.T) {
	// A transcript that itself mentions {{ROOM}}/{{TRANSCRIPT}} (agents editing
	// prompt templates) must survive verbatim — the fixed fields are rendered
	// first and the transcript is inserted last, never reprocessed.
	template := "Oda: {{ROOM}}\n--- TRANSCRIPT ---\n{{TRANSCRIPT}}"
	transcript := "alice: lütfen {{ROOM}} ve {{TRANSCRIPT}} placeholderlarını koru"

	got := renderSummaryPromptText(template, "takim-a", transcript)

	if !strings.Contains(got, "Oda: takim-a") {
		t.Fatalf("ROOM not rendered: %q", got)
	}
	if !strings.Contains(got, "lütfen {{ROOM}} ve {{TRANSCRIPT}} placeholderlarını koru") {
		t.Fatalf("transcript content was reprocessed/corrupted: %q", got)
	}
}

func TestBroadcastToSessions_InjectsAllNonObserver(t *testing.T) {
	sessions := []*ptymgr.PTYSession{
		{ID: "a", AgentName: "Alice"},
		{ID: "b", AgentName: "Bob"},
		{ID: "c", AgentName: "Carol"},
	}
	inject, calls := recordingInject(nil)

	injected, errs := broadcastToSessions(sessions, "merhaba", false, noRoles, inject)

	if injected != 3 {
		t.Errorf("injected = %d, want 3", injected)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none", errs)
	}
	if len(*calls) != 3 {
		t.Fatalf("inject called %d times, want 3", len(*calls))
	}
}

// Observer-role agents are excluded from the broadcast (#17-forward-wired).
func TestBroadcastToSessions_SkipsObservers(t *testing.T) {
	sessions := []*ptymgr.PTYSession{
		{ID: "a", AgentName: "Alice"},
		{ID: "obs", AgentName: "Watcher"},
		{ID: "c", AgentName: "Carol"},
	}
	roleOf := func(name string) string {
		if name == "Watcher" {
			return "observer"
		}
		return "Developer"
	}
	inject, calls := recordingInject(nil)

	injected, errs := broadcastToSessions(sessions, "x", false, roleOf, inject)

	if injected != 2 {
		t.Errorf("injected = %d, want 2 (observer skipped)", injected)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none", errs)
	}
	for _, c := range *calls {
		if c == "obs|x|false" {
			t.Error("observer session must not be injected")
		}
	}
}

// A failing session is recorded as an error but does not abort the fan-out — the
// remaining sessions are still injected.
func TestBroadcastToSessions_SwallowsPerSessionErrors(t *testing.T) {
	sessions := []*ptymgr.PTYSession{
		{ID: "a", AgentName: "Alice"},
		{ID: "dead", AgentName: "Ghost"},
		{ID: "c", AgentName: "Carol"},
	}
	inject, calls := recordingInject(map[string]bool{"dead": true})

	injected, errs := broadcastToSessions(sessions, "x", false, noRoles, inject)

	if injected != 2 {
		t.Errorf("injected = %d, want 2 (one session failed)", injected)
	}
	if len(errs) != 1 {
		t.Errorf("errs = %v, want exactly 1", errs)
	}
	if len(*calls) != 2 {
		t.Errorf("inject recorded %d successes, want 2", len(*calls))
	}
}

func TestBroadcastToSessions_PassesSubmitFlag(t *testing.T) {
	sessions := []*ptymgr.PTYSession{{ID: "a", AgentName: "Alice"}}
	inject, calls := recordingInject(nil)

	broadcastToSessions(sessions, "go", true, noRoles, inject)

	if len(*calls) != 1 || (*calls)[0] != "a|go|true" {
		t.Errorf("calls = %v, want [a|go|true]", *calls)
	}
}

func TestIsObserverRole(t *testing.T) {
	cases := map[string]bool{
		"observer":    true,
		"Observer":    true,
		"OBSERVER":    true,
		"  observer ": true,
		"Developer":   false,
		"manager":     false,
		"":            false,
	}
	for role, want := range cases {
		if got := isObserverRole(role); got != want {
			t.Errorf("isObserverRole(%q) = %v, want %v", role, got, want)
		}
	}
}

func TestBroadcastToTeam_EmptyText(t *testing.T) {
	a := &App{ptyManager: ptymgr.NewManager(nil)}
	if err := a.BroadcastToTeam("team1", "   ", false); err == nil {
		t.Error("expected error for blank broadcast text")
	}
}

func TestBroadcastToTeam_NoSessions(t *testing.T) {
	a := &App{ptyManager: ptymgr.NewManager(nil)}
	if err := a.BroadcastToTeam("team1", "hello", false); err == nil {
		t.Error("expected error when the team has no open terminals")
	}
}

// The frontend caps the textarea at 1000 chars; the backend must enforce the
// same limit (defense-in-depth) so a programmatic caller can't fan an unbounded
// paste into every PTY. Rejected before the session lookup.
func TestBroadcastToTeam_TooLong(t *testing.T) {
	a := &App{ptyManager: ptymgr.NewManager(nil)}
	long := strings.Repeat("x", maxBroadcastChars+1)
	err := a.BroadcastToTeam("team1", long, false)
	if err == nil {
		t.Fatal("expected error for over-limit broadcast text")
	}
	if !strings.Contains(err.Error(), "uzun") {
		t.Errorf("error = %q, want a length-limit message", err.Error())
	}
}

// broadcastRoleLookup resolves each agent's team role so observers can be
// filtered; an unknown agent resolves to the empty (non-observer) role.
func TestBroadcastRoleLookup(t *testing.T) {
	store, err := team.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tm, err := store.Create("TeamA", "2x2", []team.AgentConfig{
		{Name: "Alice", Role: "Developer", CLIType: "claude"},
		{Name: "Watcher", Role: "observer", CLIType: "claude"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	a := &App{teamStore: store}
	roleOf := a.broadcastRoleLookup(tm.ID)

	if got := roleOf("Alice"); got != "Developer" {
		t.Errorf("roleOf(Alice) = %q, want Developer", got)
	}
	if !isObserverRole(roleOf("Watcher")) {
		t.Error("Watcher must resolve to the observer role")
	}
	if got := roleOf("Unknown"); got != "" {
		t.Errorf("roleOf(Unknown) = %q, want empty", got)
	}

	// Name matching must be case/whitespace-insensitive, mirroring
	// composeAgentPrompt's EqualFold+TrimSpace lookup — otherwise a PTY whose
	// AgentName drifts in casing would dodge the observer filter and leak a
	// broadcast to an observer.
	if got := roleOf("alice"); got != "Developer" {
		t.Errorf("roleOf(alice) = %q, want Developer (case-insensitive)", got)
	}
	if !isObserverRole(roleOf("  WATCHER ")) {
		t.Error("'  WATCHER ' must resolve to the observer role (case/space-insensitive)")
	}
}

// broadcastRoleLookup must not panic when teamStore is nil (defensive guard for
// tests / unexpected init failure); it returns an all-empty resolver.
func TestBroadcastRoleLookup_NilTeamStore(t *testing.T) {
	a := &App{teamStore: nil}
	roleOf := a.broadcastRoleLookup("any-team")
	if got := roleOf("Alice"); got != "" {
		t.Errorf("nil-teamStore roleOf(Alice) = %q, want empty", got)
	}
}

// A broadcast is only an error when EVERY target failed (injected==0 with
// errors); partial success or an all-observer no-op must return nil so the UI
// doesn't keep a false error.
func TestBroadcastOutcomeError(t *testing.T) {
	if err := broadcastOutcomeError(0, []string{"Ghost: session not found"}); err == nil {
		t.Error("all-failed broadcast (injected=0, errs>0) must return an error")
	}
	if err := broadcastOutcomeError(2, []string{"Ghost: session not found"}); err != nil {
		t.Errorf("partial success (injected>0) must return nil, got %v", err)
	}
	if err := broadcastOutcomeError(0, nil); err != nil {
		t.Errorf("no targets / all observers (injected=0, no errs) must return nil, got %v", err)
	}
	if err := broadcastOutcomeError(3, nil); err != nil {
		t.Errorf("full success must return nil, got %v", err)
	}
}
