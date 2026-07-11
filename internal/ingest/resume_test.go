package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResumeSeedForRoot(t *testing.T) {
	// Isolated home root: write the Copilot transcript under a t.TempDir() so the
	// test never touches the real ~/.copilot (Copilot/Codex review).
	root := t.TempDir()
	const copilotID = "7f32dcf3-11c6-4ca1-9461-fe8590e164e0"
	dir := filepath.Join(root, ".copilot", "session-state", copilotID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte(`{"type":"user.message","data":{"content":"prior"}}` + "\n")
	evPath := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(evPath, body, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Run("copilot snapshots existing transcript size", func(t *testing.T) {
		seed := resumeSeedForRoot(root, "copilot", copilotID)
		if seed == nil {
			t.Fatal("got nil, want a seed for the existing transcript")
		}
		if seed.Path != evPath {
			t.Errorf("seed.Path = %q, want %q", seed.Path, evPath)
		}
		if seed.Cur.Offset != int64(len(body)) {
			t.Errorf("seed.Cur.Offset = %d, want %d", seed.Cur.Offset, len(body))
		}
	})

	nilCases := []struct {
		name    string
		cliType string
		id      string
	}{
		{"claude resumes into a new file", "claude", copilotID},
		{"codex resumes into a new rollout", "codex", copilotID},
		{"gemini not supported", "gemini", copilotID},
		{"shell", "shell", copilotID},
		{"empty id", "copilot", ""},
		{"path traversal id", "copilot", "../evil"},
		{"glob metacharacter id", "copilot", "*"},
		{"copilot but file missing", "copilot", "no-such-session"},
	}
	for _, tt := range nilCases {
		t.Run(tt.name, func(t *testing.T) {
			if got := resumeSeedForRoot(root, tt.cliType, tt.id); got != nil {
				t.Errorf("resumeSeedForRoot(%q,%q) = %+v, want nil", tt.cliType, tt.id, got)
			}
		})
	}
}

// Smoke-test the public wrapper delegates without touching the filesystem (claude
// returns nil regardless of home, so this never reads ~/.copilot).
func TestResumeSeedFor_PublicWrapperDelegates(t *testing.T) {
	if got := ResumeSeedFor("claude", "some-id"); got != nil {
		t.Errorf("ResumeSeedFor(claude) = %+v, want nil", got)
	}
}
