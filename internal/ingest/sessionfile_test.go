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
	// msg[0] is the app bootstrap/join prompt (always first) → skipped. The two real
	// user messages remain: count=2, snippet="ilk gerçek mesaj".
	body := `{"type":"user","message":{"role":"user","content":"BOOTSTRAP join prompt"}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"ilk gerçek mesaj"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"ikinci"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	count, snippet := SessionStats("claude", path)
	if count != 2 {
		t.Fatalf("count = %d, want 2 (bootstrap skipped)", count)
	}
	if snippet != "ilk gerçek mesaj" {
		t.Fatalf("snippet = %q, want first real message (bootstrap skipped)", snippet)
	}
}

func TestSessionStatsOnlyBootstrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.jsonl")
	// Only the bootstrap prompt → no real user message yet → 0 / "".
	body := `{"type":"user","message":{"role":"user","content":"BOOTSTRAP only"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if c, s := SessionStats("claude", path); c != 0 || s != "" {
		t.Fatalf("only-bootstrap = %d,%q, want 0,\"\"", c, s)
	}
}

func TestSessionStatsUnknownCLI(t *testing.T) {
	if c, s := SessionStats("shell", "/nope"); c != 0 || s != "" {
		t.Fatalf("unknown cli = %d,%q", c, s)
	}
}
