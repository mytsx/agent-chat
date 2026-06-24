package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionFilePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	tests := []struct {
		name, cliType, cwd, id, wantSuffix string
		wantOK                             bool
	}{
		{"claude", "claude", "/x/y", "uuid-1", filepath.Join(".claude", "projects", "-x-y", "uuid-1.jsonl"), true},
		{"copilot", "copilot", "/ignored", "uuid-2", filepath.Join(".copilot", "session-state", "uuid-2", "events.jsonl"), true},
		{"gemini unsupported", "gemini", "/x", "uuid", "", false},
		{"shell", "shell", "/x", "uuid", "", false},
		{"empty id", "claude", "/x", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SessionFilePath(tt.cliType, tt.cwd, tt.id)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && got != filepath.Join(home, tt.wantSuffix) {
				t.Fatalf("path = %q, want suffix %q", got, tt.wantSuffix)
			}
		})
	}
}

func TestSessionStats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"ilk mesaj burada"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"ikinci"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	count, snippet := SessionStats("claude", path)
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if snippet != "ilk mesaj burada" {
		t.Fatalf("snippet = %q", snippet)
	}
}

func TestSessionStatsUnknownCLI(t *testing.T) {
	if c, s := SessionStats("shell", "/nope"); c != 0 || s != "" {
		t.Fatalf("unknown cli = %d,%q", c, s)
	}
}
