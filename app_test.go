package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"desktop/internal/prompt"
	ptymgr "desktop/internal/pty"
	"desktop/internal/sessionlog"
	"desktop/internal/team"
)

func TestListKnownAgents_UnionAndCaseInsensitiveDedup(t *testing.T) {
	store, err := team.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tm, err := store.Create("MyTeam", "2x2", []team.AgentConfig{
		{Name: "Alice", CLIType: "claude"}, // same agent as history's "alice"
		{Name: "carol", CLIType: "codex"},  // config-only
	})
	if err != nil {
		t.Fatal(err)
	}
	sl, err := sessionlog.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{teamStore: store, sessionLog: sl}
	sl.Record("sid-a", "MyTeam", "alice", "claude", "/nope") // history, lower-case
	sl.Record("sid-b", "MyTeam", "bob", "codex", "/nope")    // history-only

	known := a.ListKnownAgents(tm.ID)
	// "alice"/"Alice" collapse case-insensitively → {alice, bob, carol} (3 distinct).
	if len(known) != 3 {
		t.Fatalf("ListKnownAgents = %v, want 3 distinct (alice/bob/carol)", known)
	}
	seen := map[string]bool{}
	for _, n := range known {
		seen[strings.ToLower(n)] = true
	}
	if !seen["alice"] || !seen["bob"] || !seen["carol"] {
		t.Fatalf("ListKnownAgents = %v, missing one of alice/bob/carol", known)
	}
}

func TestListAgentSessions_CaseInsensitiveAndFileMissing(t *testing.T) {
	store, err := team.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tm, err := store.Create("MyTeam", "2x2", []team.AgentConfig{{Name: "Alice", CLIType: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	sl, err := sessionlog.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{teamStore: store, sessionLog: sl}
	sl.Record("sid-a", "MyTeam", "alice", "claude", "/nonexistent") // lower-case launch name

	// Query with the config casing ("Alice") — must still find the "alice" record, and
	// since the transcript file doesn't exist, FileMissing must be true.
	got := a.ListAgentSessions(tm.ID, "Alice")
	if len(got) != 1 {
		t.Fatalf("ListAgentSessions = %d, want 1 (case-insensitive)", len(got))
	}
	if got[0].SessionID != "sid-a" || !got[0].FileMissing {
		t.Fatalf("session = %+v, want sid-a with FileMissing=true", got[0])
	}
}

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

func TestRenderSummaryPromptText_SanitizesOutput(t *testing.T) {
	// The editable prompt template (and inserted content) reach the clipboard the
	// user pastes into a neutral CLI, so the rendered output must be free of
	// bracketed-paste terminators / control runes.
	template := "Özetle:\x1b[201~kötü\nTranscript:\n{{TRANSCRIPT}}"
	got := renderSummaryPromptText(template, "r", "merhaba")
	if strings.Contains(got, "\x1b[201~") || strings.Contains(got, "\x1b") {
		t.Fatalf("template paste-escape not stripped from rendered prompt: %q", got)
	}
	if !strings.Contains(got, "merhaba") || !strings.Contains(got, "Transcript:") {
		t.Fatalf("expected content missing from rendered prompt: %q", got)
	}
}

func TestRenderSummaryPromptText_AppendsTranscriptWhenPlaceholderMissing(t *testing.T) {
	// If the user-edited template loses {{TRANSCRIPT}} (typo/removal), the rendered
	// prompt must still contain the conversation — otherwise the neutral agent
	// summarizes nothing / hallucinates.
	template := "Lütfen özetle. (placeholder yok)"
	got := renderSummaryPromptText(template, "r", "ÖNEMLİ KONUŞMA")
	if !strings.Contains(got, "ÖNEMLİ KONUŞMA") {
		t.Fatalf("transcript must be included even when template lacks {{TRANSCRIPT}}: %q", got)
	}
}

func TestSendPromptToAgent_SanitizesBeforeTerminalInjection(t *testing.T) {
	var gotSession, gotText string
	var gotSubmit bool
	a := &App{
		promptInject: func(sessionID, text string, submit bool) error {
			gotSession, gotText, gotSubmit = sessionID, text, submit
			return nil
		},
	}

	err := a.SendPromptToAgent("s1", "before\x1b[201~{{NAME}}\x00\u202Eafter", map[string]string{"NAME": "Alice"})
	if err != nil {
		t.Fatalf("SendPromptToAgent: %v", err)
	}
	if gotSession != "s1" || !gotSubmit {
		t.Fatalf("prompt injection call = session %q submit %v, want session s1 submit true", gotSession, gotSubmit)
	}
	if gotText != "beforeAliceafter" {
		t.Fatalf("injected text = %q, want sanitized rendered prompt", gotText)
	}
}

func TestSendPromptToAgent_EmptyAfterSanitizeNoops(t *testing.T) {
	called := false
	a := &App{
		promptInject: func(sessionID, text string, submit bool) error {
			called = true
			return nil
		},
	}

	if err := a.SendPromptToAgent("s1", string(rune(0x202e)), nil); err != nil {
		t.Fatalf("SendPromptToAgent: %v", err)
	}
	if called {
		t.Fatal("all-stripped prompt must not be injected or submitted")
	}
}

func TestResizeTerminalRejectsInvalidDimensions(t *testing.T) {
	a := &App{}
	cases := []struct {
		name string
		cols int
		rows int
	}{
		{name: "zero columns", cols: 0, rows: 24},
		{name: "zero rows", cols: 80, rows: 0},
		{name: "negative columns", cols: -1, rows: 24},
		{name: "negative rows", cols: 80, rows: -1},
		{name: "columns overflow uint16", cols: 1 << 16, rows: 24},
		{name: "rows overflow uint16", cols: 80, rows: 1 << 16},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := a.ResizeTerminal("s1", tc.cols, tc.rows); err == nil {
				t.Fatalf("ResizeTerminal(%d, %d) expected error", tc.cols, tc.rows)
			}
		})
	}
}

// aiDelivered reflects AI-target delivery only: a failing plain shell must not
// flip it false when every AI agent received the broadcast (#29 Codex review).
func TestIsAICLIType(t *testing.T) {
	for _, ai := range []string{"claude", "gemini", "copilot", "codex"} {
		if !isAICLIType(ai) {
			t.Errorf("isAICLIType(%q) = false, want true", ai)
		}
	}
	for _, notAI := range []string{"shell", "", "bash", "zsh", "unknown"} {
		if isAICLIType(notAI) {
			t.Errorf("isAICLIType(%q) = true, want false (not an MCP room participant)", notAI)
		}
	}
}

// An empty/unknown CLI type is a plain shell (login-shell fallback, no MCP startup)
// and must not count as an AI broadcast target (#29 Codex review).
func TestBroadcastToSessions_AIDeliveredFalseForEmptyCLIType(t *testing.T) {
	sessions := []*ptymgr.PTYSession{
		{ID: "x", AgentName: "Legacy", CLIType: ""},
	}
	inject, _ := recordingInject(nil)
	_, _, aiDelivered := broadcastToSessions(sessions, "x", true, noRoles, inject)
	if aiDelivered {
		t.Fatal("empty CLIType must not count as an AI target")
	}
}

func TestBroadcastToSessions_AIDeliveredIgnoresShellFailure(t *testing.T) {
	sessions := []*ptymgr.PTYSession{
		{ID: "ai", AgentName: "Alice", CLIType: "claude"},
		{ID: "sh", AgentName: "Shelly", CLIType: "shell"},
	}
	inject, _ := recordingInject(map[string]bool{"sh": true}) // only the shell errors

	injected, errs, aiDelivered := broadcastToSessions(sessions, "x", true, noRoles, inject)

	if !aiDelivered {
		t.Fatalf("aiDelivered = false, want true (AI got it; only the shell failed). injected=%d errs=%v", injected, errs)
	}
}

func TestBroadcastToSessions_AIDeliveredFalseWhenAIFails(t *testing.T) {
	sessions := []*ptymgr.PTYSession{
		{ID: "ai", AgentName: "Alice", CLIType: "claude"},
		{ID: "sh", AgentName: "Shelly", CLIType: "shell"},
	}
	inject, _ := recordingInject(map[string]bool{"ai": true}) // the AI agent errors

	_, _, aiDelivered := broadcastToSessions(sessions, "x", true, noRoles, inject)

	if aiDelivered {
		t.Fatal("aiDelivered = true, want false (the AI target failed)")
	}
}

func TestBroadcastToSessions_AIDeliveredFalseForShellOnlyTeam(t *testing.T) {
	sessions := []*ptymgr.PTYSession{
		{ID: "sh1", AgentName: "S1", CLIType: "shell"},
		{ID: "sh2", AgentName: "S2", CLIType: "shell"},
	}
	inject, _ := recordingInject(nil)

	_, _, aiDelivered := broadcastToSessions(sessions, "x", true, noRoles, inject)

	if aiDelivered {
		t.Fatal("aiDelivered = true, want false (no AI participant in a shell-only team)")
	}
}

func TestBroadcastToSessions_InjectsAllNonObserver(t *testing.T) {
	sessions := []*ptymgr.PTYSession{
		{ID: "a", AgentName: "Alice"},
		{ID: "b", AgentName: "Bob"},
		{ID: "c", AgentName: "Carol"},
	}
	inject, calls := recordingInject(nil)

	injected, errs, _ := broadcastToSessions(sessions, "merhaba", false, noRoles, inject)

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

	injected, errs, _ := broadcastToSessions(sessions, "x", false, roleOf, inject)

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

	injected, errs, _ := broadcastToSessions(sessions, "x", false, noRoles, inject)

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

// --- #58: room pinned at PTY creation (no mutable Team.Name re-read on log) ---

// logRoomForSession uses the room PINNED on the session at creation, so a team
// rename mid-life can't reroute a logged prompt to a different room than the
// agent's MCP session (fixed at spawn via AGENT_CHAT_ROOM) actually uses (#58).
func TestLogRoomForSession_PrefersPinnedRoom(t *testing.T) {
	a := &App{}
	sess := &ptymgr.PTYSession{TeamID: "t1", AgentName: "alice", Room: "pinned-room"}
	if got := a.logRoomForSession(sess); got != "pinned-room" {
		t.Fatalf("logRoomForSession = %q, want pinned-room (must not re-read Team.Name)", got)
	}
}

// A legacy session created before room-pinning (empty Room) falls back to the
// team's current room name, preserving prior behavior.
func TestLogRoomForSession_FallsBackToTeamRoom(t *testing.T) {
	store, err := team.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tm, err := store.Create("TeamA", "2x2", []team.AgentConfig{{Name: "alice", CLIType: "claude"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	a := &App{teamStore: store}
	sess := &ptymgr.PTYSession{TeamID: tm.ID, AgentName: "alice", Room: ""}
	if got := a.logRoomForSession(sess); got != "TeamA" {
		t.Fatalf("logRoomForSession = %q, want TeamA (fallback when unpinned)", got)
	}
}

func TestPinnedRoomForSessions_PrefersPinned(t *testing.T) {
	sessions := []*ptymgr.PTYSession{
		{ID: "a", Room: "room-x"},
		{ID: "b", Room: "room-x"},
	}
	if got := pinnedRoomForSessions(sessions, "fallback"); got != "room-x" {
		t.Fatalf("pinnedRoomForSessions = %q, want room-x", got)
	}
}

func TestPinnedRoomForSessions_FallbackWhenNonePinned(t *testing.T) {
	sessions := []*ptymgr.PTYSession{{ID: "a", Room: ""}}
	if got := pinnedRoomForSessions(sessions, "fallback"); got != "fallback" {
		t.Fatalf("pinnedRoomForSessions = %q, want fallback (no session pinned a room)", got)
	}
}

// --- #17 observer: app-layer mode resolution + prompt composition ---

func TestResolveAgentMode(t *testing.T) {
	store, err := team.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tm, err := store.Create("TeamA", "2x2", []team.AgentConfig{
		{Name: "watcher", Role: "observer", CLIType: "claude"},
		{Name: "dev", Role: "Developer", CLIType: "claude"},
		{Name: "boss", CLIType: "claude"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetManager(tm.ID, "boss"); err != nil {
		t.Fatalf("SetManager: %v", err)
	}

	a := &App{teamStore: store}

	cases := []struct {
		agent string
		want  string
	}{
		{"watcher", "observer"},
		{"dev", ""},
		{"boss", "manager"},
	}
	for _, c := range cases {
		got, err := a.resolveAgentMode(tm.ID, c.agent, "")
		if err != nil {
			t.Fatalf("resolveAgentMode(%q): %v", c.agent, err)
		}
		if got != c.want {
			t.Errorf("resolveAgentMode(%q) = %q, want %q", c.agent, got, c.want)
		}
	}
}

func TestIsObserverAgent(t *testing.T) {
	store, err := team.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tm, _ := store.Create("TeamA", "2x2", []team.AgentConfig{
		{Name: "Watcher", Role: "OBSERVER", CLIType: "claude"}, // case-insensitive
		{Name: "dev", Role: "Developer", CLIType: "claude"},
	})
	a := &App{teamStore: store}

	if !a.isObserverAgent(tm.ID, "watcher") {
		t.Error("watcher should be an observer (case-insensitive)")
	}
	if a.isObserverAgent(tm.ID, "dev") {
		t.Error("dev is not an observer")
	}
	if a.isObserverAgent(tm.ID, "absent") {
		t.Error("absent agent is not an observer")
	}
	if a.isObserverAgent("", "watcher") {
		t.Error("empty teamID must resolve to not-observer")
	}
}

func TestComposeAgentPrompt_ObserverInjectsObserverPrompt(t *testing.T) {
	store, err := team.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tm, _ := store.Create("TeamA", "2x2", []team.AgentConfig{
		{Name: "watcher", Role: "observer", CLIType: "claude"},
	})
	a := &App{teamStore: store, dataDir: t.TempDir()}

	got := a.composeAgentPrompt(tm.ID, "watcher", "", "observer")
	if !strings.Contains(got, `join_room("watcher", "observer")`) {
		t.Fatalf("expected observer join instruction, got:\n%s", got)
	}
	if !strings.Contains(got, "outside eye") {
		t.Fatalf("expected observer role prompt to be injected, got:\n%s", got)
	}
	if strings.Contains(got, "MANAGER agent for this room") {
		t.Fatalf("observer prompt must not include the manager prompt:\n%s", got)
	}
}

// #17 (Codex P2): an explicitly-selected observer must win over a manager-tagged
// startup prompt. Otherwise resolveManagerIntent auto-promotes from the prompt tag
// (persisting the team manager + clearing the observer role) and the read-only
// observer terminal would start with manager routing authority.
func TestResolveAgentMode_ExplicitObserverBeatsManagerPrompt(t *testing.T) {
	store, err := team.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tm, _ := store.Create("TeamA", "2x2", []team.AgentConfig{
		{Name: "watcher", Role: "observer", CLIType: "claude"},
	})
	ps, err := prompt.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("prompt.NewStore: %v", err)
	}
	mgrPrompt, err := ps.Create("Manager", "manage stuff", "task", []string{"manager"})
	if err != nil {
		t.Fatalf("prompt Create: %v", err)
	}

	a := &App{teamStore: store, promptStore: ps}

	mode, err := a.resolveAgentMode(tm.ID, "watcher", mgrPrompt.ID)
	if err != nil {
		t.Fatalf("resolveAgentMode: %v", err)
	}
	if mode != "observer" {
		t.Fatalf("explicit observer must win over a manager-tagged prompt, got %q", mode)
	}
	// The observer resolution must NOT have auto-set the team manager as a side effect.
	updated, _ := store.Get(tm.ID)
	if updated.ManagerAgent != "" {
		t.Fatalf("observer resolution must not auto-set team manager, got %q", updated.ManagerAgent)
	}
}

// #17 (Codex P2): a half-created observer leaves a phantom AgentConfig (Role
// observer + empty CLIType); OpenTeamFromConfig skips it. The check must be narrow:
// a blank CLIType alone is a legitimate login-shell fallback that must be preserved.
func TestIsObserverPhantom(t *testing.T) {
	if !isObserverPhantom(team.AgentConfig{Name: "x", Role: "observer", CLIType: ""}) {
		t.Error("observer with empty CLIType must be a phantom")
	}
	if isObserverPhantom(team.AgentConfig{Name: "x", Role: "", CLIType: ""}) {
		t.Error("a legacy blank-CLI shell (non-observer) must be preserved, not treated as a phantom")
	}
	if isObserverPhantom(team.AgentConfig{Name: "x", Role: "observer", CLIType: "claude"}) {
		t.Error("a fully-configured observer is not a phantom")
	}
}

// #17 (Codex P2): a worktree-backed agent later marked manager OR observer must
// restart in the MAIN repo, not the stale worktree (both roles run from main repo).
func TestRestartWorkDir(t *testing.T) {
	if got := restartWorkDir(true, "/repo", "", ""); got != "/repo" {
		t.Errorf("non-worktree: got %q, want /repo", got)
	}
	if got := restartWorkDir(true, "/wt", "/wt", "/repo"); got != "/repo" {
		t.Errorf("manager/observer worktree-backed must move to main repo: got %q, want /repo", got)
	}
	if got := restartWorkDir(true, "/x", "/wt", ""); got != "/x" {
		t.Errorf("main-repo role with no recorded repo falls back to workDir: got %q, want /x", got)
	}
	if got := restartWorkDir(false, "/x", "/wt", "/repo"); got != "/wt" {
		t.Errorf("normal agent reuses worktree: got %q, want /wt", got)
	}
}

// #17 (Codex P2): an empty team name resolves to the "default" room, so hub sync
// targets it instead of being skipped.
func TestRoomNameOrDefault(t *testing.T) {
	if got := roomNameOrDefault(""); got != "default" {
		t.Errorf("empty → %q, want default", got)
	}
	if got := roomNameOrDefault("   "); got != "default" {
		t.Errorf("whitespace → %q, want default", got)
	}
	if got := roomNameOrDefault("TeamA"); got != "TeamA" {
		t.Errorf("named → %q, want TeamA", got)
	}
}
