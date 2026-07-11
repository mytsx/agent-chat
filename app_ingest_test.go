package main

import (
	"context"
	"testing"

	"desktop/internal/ingest"
	ptymgr "desktop/internal/pty"
)

// AdapterFor gates which terminals get ingested; the wiring must only start
// watchers for AI CLIs. This guards the CreateTerminal condition.
func TestIngestAdapterForGate(t *testing.T) {
	if ingest.AdapterFor("claude") == nil {
		t.Error("claude must be ingestable")
	}
	if ingest.AdapterFor("shell") != nil {
		t.Error("shell must NOT be ingestable")
	}
}

// A hand-constructed App leaves ingestMgr nil; the wiring calls (RecordInjection
// via SendPromptToAgent/BroadcastToTeam, StopSession via closeTerminalInternal,
// StopAll via shutdown) must not panic on a nil Manager.
func TestNilIngestManagerMethodsAreSafe(t *testing.T) {
	var m *ingest.Manager // nil
	m.RecordInjection("s", "x")
	m.StopSession("s")
	m.StopAll()
	m.StartSession("s", ingest.AdapterFor("claude"), "/tmp", 0, func() bool { return true }, nil, func(string, string) bool { return true }, nil, nil)
}

// If session-history initialization failed, App.sessionLog is nil. Shutdown still
// sees captured CLI session IDs from live terminals and must close those PTYs
// instead of panicking while trying to Touch the missing history store.
func TestShutdownWithCapturedSessionIDsAndNilSessionLogIsSafe(t *testing.T) {
	m := ptymgr.NewManager(nil)
	sid, err := m.Create("team", "alice", "room", "", nil, "/bin/sh", []string{"-c", "sleep 30"}, "claude")
	if err != nil {
		t.Fatalf("create PTY session: %v", err)
	}
	m.SetCLISessionID(sid, "cli-session-id")

	a := &App{ptyManager: m, sessionLog: nil, ingestMgr: nil}
	a.shutdown(context.Background())

	if got := m.GetSession(sid); got != nil {
		t.Fatalf("shutdown left PTY session open: %+v", got)
	}
}
