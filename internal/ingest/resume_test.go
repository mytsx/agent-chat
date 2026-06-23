package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResumeSeedFor_NilCases(t *testing.T) {
	tests := []struct {
		name    string
		cliType string
		id      string
	}{
		{"claude resumes into a new file", "claude", "some-uuid"},
		{"codex resumes into a new rollout", "codex", "some-uuid"},
		{"gemini not supported", "gemini", "some-uuid"},
		{"shell", "shell", "some-uuid"},
		{"empty id", "copilot", ""},
		{"copilot but file missing", "copilot", "definitely-not-a-real-session-id-xyz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResumeSeedFor(tt.cliType, tt.id); got != nil {
				t.Errorf("ResumeSeedFor(%q,%q) = %+v, want nil", tt.cliType, tt.id, got)
			}
		})
	}
}

func TestResumeSeedFor_CopilotSnapshotsExistingSize(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	// Unique, test-only session id under the real session-state root; cleaned up.
	id := "ingest-resume-test-7f32dcf3"
	dir := filepath.Join(home, ".copilot", "session-state", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	body := []byte(`{"type":"user.message","data":{"content":"prior"}}` + "\n")
	evPath := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(evPath, body, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	seed := ResumeSeedFor("copilot", id)
	if seed == nil {
		t.Fatal("ResumeSeedFor(copilot) = nil, want a seed for the existing transcript")
	}
	if seed.Path != evPath {
		t.Errorf("seed.Path = %q, want %q", seed.Path, evPath)
	}
	if seed.Cur.Offset != int64(len(body)) {
		t.Errorf("seed.Cur.Offset = %d, want %d (snapshot of existing size)", seed.Cur.Offset, len(body))
	}
}
