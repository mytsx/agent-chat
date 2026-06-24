package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionID(t *testing.T) {
	dir := t.TempDir()

	// codex: session_meta first line with payload.id
	codexPath := filepath.Join(dir, "rollout-2026-06-23T20-34-23-019ef58c-27d5-7e43-9902-8a02b5517bf1.jsonl")
	codexLine := `{"timestamp":"2026-06-23T20:34:23.000Z","type":"session_meta","payload":{"id":"019ef58c-27d5-7e43-9902-8a02b5517bf1","cwd":"/x"}}` + "\n"
	if err := os.WriteFile(codexPath, []byte(codexLine), 0644); err != nil {
		t.Fatal(err)
	}

	// gemini: monolithic JSON with top-level sessionId
	geminiPath := filepath.Join(dir, "session-2026-02-22T10-49-65a26031.json")
	geminiBody := `{"sessionId":"65a26031-dcf1-4a40-aff0-42b7d84dc7b4","messages":[]}`
	if err := os.WriteFile(geminiPath, []byte(geminiBody), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		ad   SessionAdapter
		path string
		want string
	}{
		{"claude filename stem", claudeAdapter{}, "/home/u/.claude/projects/-x/5c6e4e64-3305-45b3-a70e-f0a97a974ab2.jsonl", "5c6e4e64-3305-45b3-a70e-f0a97a974ab2"},
		{"claude empty path", claudeAdapter{}, "", ""},
		{"copilot parent dir", copilotAdapter{}, "/home/u/.copilot/session-state/c96cde26-3b35-4a82-b1e9-c40747f9346e/events.jsonl", "c96cde26-3b35-4a82-b1e9-c40747f9346e"},
		{"copilot empty path", copilotAdapter{}, "", ""},
		{"codex session_meta id", codexAdapter{}, codexPath, "019ef58c-27d5-7e43-9902-8a02b5517bf1"},
		{"codex missing file", codexAdapter{}, filepath.Join(dir, "nope.jsonl"), ""},
		{"gemini sessionId field", geminiAdapter{}, geminiPath, "65a26031-dcf1-4a40-aff0-42b7d84dc7b4"},
		{"gemini missing file", geminiAdapter{}, filepath.Join(dir, "nope.json"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ad.SessionID(tt.path); got != tt.want {
				t.Errorf("SessionID(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
