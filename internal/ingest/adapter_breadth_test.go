package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdapterFor_Breadth(t *testing.T) {
	cases := []struct {
		cli  string
		want bool
	}{
		{"claude", true},
		{"codex", true},
		{"copilot", true},
		{"gemini", true},
		{"", false},
		{"shell", false},
		{"unknown", false},
	}
	for _, tc := range cases {
		if got := AdapterFor(tc.cli); (got != nil) != tc.want {
			t.Fatalf("AdapterFor(%q) presence = %v, want %v", tc.cli, got != nil, tc.want)
		}
	}
}

func TestAdapters_SessionIDBreadth(t *testing.T) {
	dir := t.TempDir()

	claudePath := filepath.Join(dir, "claude-session.jsonl")
	if got := (claudeAdapter{}).SessionID(claudePath); got != "claude-session" {
		t.Fatalf("claude SessionID = %q, want claude-session", got)
	}
	if got := (claudeAdapter{}).SessionID(""); got != "" {
		t.Fatalf("claude empty SessionID = %q, want empty", got)
	}

	codexPath := writeFile(t, dir, "rollout-2026-07-11.jsonl", `{"type":"session_meta","payload":{"id":"codex-id","cwd":"/repo"}}`+"\n")
	if got := (codexAdapter{}).SessionID(codexPath); got != "codex-id" {
		t.Fatalf("codex SessionID = %q, want codex-id", got)
	}

	copilotPath := filepath.Join(dir, "copilot-id", "events.jsonl")
	if got := (copilotAdapter{}).SessionID(copilotPath); got != "copilot-id" {
		t.Fatalf("copilot SessionID = %q, want copilot-id", got)
	}
	if got := (copilotAdapter{}).SessionID(""); got != "" {
		t.Fatalf("copilot empty SessionID = %q, want empty", got)
	}

	geminiPath := writeFile(t, dir, "session-abcdef12.json", `{"sessionId":"gemini-id"}`)
	if got := (geminiAdapter{}).SessionID(geminiPath); got != "gemini-id" {
		t.Fatalf("gemini SessionID = %q, want gemini-id", got)
	}
}

func TestDiscover_ClaimedCandidatesSkippedBreadth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	spawn := time.Now()
	cwd := "/work/repo"

	// Claude/Gemini use nearestSessionFileAfter under provider-specific dirs.
	claudeDir := filepath.Join(home, ".claude", "projects", claudeSlug(cwd))
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	claudeClaimed := writeFile(t, claudeDir, "claimed.jsonl", "{}\n")
	claudeFree := writeFile(t, claudeDir, "free.jsonl", "{}\n")
	os.Chtimes(claudeClaimed, spawn.Add(100*time.Millisecond), spawn.Add(100*time.Millisecond))
	os.Chtimes(claudeFree, spawn.Add(200*time.Millisecond), spawn.Add(200*time.Millisecond))
	gotClaude, err := (claudeAdapter{}).DiscoverFile(cwd, spawn.UnixNano(), func(p string) bool { return p == claudeClaimed })
	if err != nil {
		t.Fatal(err)
	}
	if gotClaude != claudeFree {
		t.Fatalf("claude DiscoverFile = %q, want unclaimed %q", gotClaude, claudeFree)
	}

	sum := sha256.Sum256([]byte(cwd))
	geminiDir := filepath.Join(home, ".gemini", "tmp", hex.EncodeToString(sum[:]), "chats")
	if err := os.MkdirAll(geminiDir, 0755); err != nil {
		t.Fatal(err)
	}
	geminiClaimed := writeFile(t, geminiDir, "session-claimed.json", "{}")
	geminiFree := writeFile(t, geminiDir, "session-free.json", "{}")
	os.Chtimes(geminiClaimed, spawn.Add(100*time.Millisecond), spawn.Add(100*time.Millisecond))
	os.Chtimes(geminiFree, spawn.Add(200*time.Millisecond), spawn.Add(200*time.Millisecond))
	gotGemini, err := (geminiAdapter{}).DiscoverFile(cwd, spawn.UnixNano(), func(p string) bool { return p == geminiClaimed })
	if err != nil {
		t.Fatal(err)
	}
	if gotGemini != geminiFree {
		t.Fatalf("gemini DiscoverFile = %q, want unclaimed %q", gotGemini, geminiFree)
	}

	// Codex/Copilot have provider-specific cwd filters plus claimed filtering.
	codexDay := filepath.Join(home, ".codex", "sessions", spawn.Format("2006"), spawn.Format("01"), spawn.Format("02"))
	if err := os.MkdirAll(codexDay, 0755); err != nil {
		t.Fatal(err)
	}
	codexMeta := `{"type":"session_meta","payload":{"cwd":"` + cwd + `"}}` + "\n"
	codexClaimed := writeFile(t, codexDay, "rollout-claimed.jsonl", codexMeta)
	codexFree := writeFile(t, codexDay, "rollout-free.jsonl", codexMeta)
	os.Chtimes(codexClaimed, spawn.Add(100*time.Millisecond), spawn.Add(100*time.Millisecond))
	os.Chtimes(codexFree, spawn.Add(200*time.Millisecond), spawn.Add(200*time.Millisecond))
	gotCodex, err := (codexAdapter{}).DiscoverFile(cwd, spawn.UnixNano(), func(p string) bool { return p == codexClaimed })
	if err != nil {
		t.Fatal(err)
	}
	if gotCodex != codexFree {
		t.Fatalf("codex DiscoverFile = %q, want unclaimed %q", gotCodex, codexFree)
	}

	copilotBase := filepath.Join(home, ".copilot", "session-state")
	copilotClaimedDir := filepath.Join(copilotBase, "claimed")
	copilotFreeDir := filepath.Join(copilotBase, "free")
	if err := os.MkdirAll(copilotClaimedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(copilotFreeDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, copilotClaimedDir, "workspace.yaml", "cwd: "+cwd+"\n")
	writeFile(t, copilotFreeDir, "workspace.yaml", "cwd: "+cwd+"\n")
	copilotClaimed := writeFile(t, copilotClaimedDir, "events.jsonl", "{}\n")
	copilotFree := writeFile(t, copilotFreeDir, "events.jsonl", "{}\n")
	os.Chtimes(copilotClaimed, spawn.Add(100*time.Millisecond), spawn.Add(100*time.Millisecond))
	os.Chtimes(copilotFree, spawn.Add(200*time.Millisecond), spawn.Add(200*time.Millisecond))
	gotCopilot, err := (copilotAdapter{}).DiscoverFile(cwd, spawn.UnixNano(), func(p string) bool { return p == copilotClaimed })
	if err != nil {
		t.Fatal(err)
	}
	if gotCopilot != copilotFree {
		t.Fatalf("copilot DiscoverFile = %q, want unclaimed %q", gotCopilot, copilotFree)
	}
}

func TestCodexDiscover_ChecksNextDayForJustBeforeMidnightSpawn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/work/repo"
	spawn := time.Date(2026, 7, 11, 23, 59, 59, 900_000_000, time.UTC)
	nextDay := spawn.Add(24 * time.Hour)
	dir := filepath.Join(home, ".codex", "sessions", nextDay.Format("2006"), nextDay.Format("01"), nextDay.Format("02"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	meta := `{"type":"session_meta","payload":{"cwd":"` + cwd + `"}}` + "\n"
	rollout := writeFile(t, dir, "rollout-next-day.jsonl", meta)
	mod := spawn.Add(200 * time.Millisecond)
	os.Chtimes(rollout, mod, mod)

	got, err := (codexAdapter{}).DiscoverFile(cwd, spawn.UnixNano(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != rollout {
		t.Fatalf("DiscoverFile = %q, want next-day rollout %q", got, rollout)
	}
}
