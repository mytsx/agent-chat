package main

import (
	"testing"

	"desktop/internal/ingest"
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
	m.StartSession("s", ingest.AdapterFor("claude"), "/tmp", 0, func() bool { return true }, nil, func(string, string) bool { return true }, nil, false)
}
