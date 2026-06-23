package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestClaudeParse_ExtractsOnlyHumanUserMessages(t *testing.T) {
	dir := t.TempDir()
	// A realistic Claude JSONL: a human user line (content is a STRING),
	// an assistant line (content is an array), and a tool_result line
	// (type "user" but content is an ARRAY of tool_result blocks — NOT human).
	jsonl := `{"type":"user","timestamp":"2026-06-23T10:00:00.000Z","message":{"role":"user","content":"merhaba claude"}}
{"type":"assistant","timestamp":"2026-06-23T10:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"selam"}]}}
{"type":"user","timestamp":"2026-06-23T10:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}
{"type":"user","timestamp":"2026-06-23T10:00:03.000Z","message":{"role":"user","content":"ikinci mesaj"}}
`
	p := writeFile(t, dir, "s.jsonl", jsonl)

	msgs, next, err := claudeAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (only string-content user lines): %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "merhaba claude" || msgs[1].Content != "ikinci mesaj" {
		t.Fatalf("contents = %q, %q", msgs[0].Content, msgs[1].Content)
	}
	if msgs[0].Timestamp != "2026-06-23T10:00:00.000Z" {
		t.Fatalf("timestamp = %q", msgs[0].Timestamp)
	}
	if next.Offset != int64(len(jsonl)) {
		t.Fatalf("next.Offset = %d, want %d (whole file consumed)", next.Offset, len(jsonl))
	}
}

func TestClaudeParse_ResumesFromCursorNoDuplicates(t *testing.T) {
	dir := t.TempDir()
	first := `{"type":"user","timestamp":"2026-06-23T10:00:00.000Z","message":{"role":"user","content":"bir"}}` + "\n"
	p := writeFile(t, dir, "s.jsonl", first)
	_, cur, err := claudeAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	// Append a second line and re-parse from the cursor — only the new line returns.
	appended := first + `{"type":"user","timestamp":"2026-06-23T10:00:05.000Z","message":{"role":"user","content":"iki"}}` + "\n"
	if err := os.WriteFile(p, []byte(appended), 0644); err != nil {
		t.Fatal(err)
	}
	msgs, _, err := claudeAdapter{}.ParseNewUserMessages(p, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "iki" {
		t.Fatalf("resume returned %+v, want only [iki]", msgs)
	}
}

func TestClaudeParse_SkipsCorruptLine(t *testing.T) {
	dir := t.TempDir()
	jsonl := `{"type":"user","timestamp":"t1","message":{"role":"user","content":"iyi"}}
{bozuk json
{"type":"user","timestamp":"t2","message":{"role":"user","content":"devam"}}
`
	p := writeFile(t, dir, "s.jsonl", jsonl)
	msgs, _, err := claudeAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatalf("a corrupt line must not fail the parse: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d, want 2 (corrupt line skipped)", len(msgs))
	}
}

// A poll landing while the CLI has written only PART of the final JSON line
// must NOT advance the cursor past those partial bytes — otherwise when the line
// completes the next poll seeks into the middle of it and the message is lost
// forever (#65 / Codex P2). The partial line is re-read intact once finished.
func TestClaudeParse_DoesNotLosePartialFinalLine(t *testing.T) {
	dir := t.TempDir()
	full := `{"type":"user","timestamp":"t1","message":{"role":"user","content":"tam satır"}}` + "\n"
	partial := `{"type":"user","timestamp":"t2","message":{"role":"user","content":"yarım` // no close, no newline
	p := writeFile(t, dir, "s.jsonl", full+partial)

	msgs, cur, err := claudeAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "tam satır" {
		t.Fatalf("first poll: got %+v, want only the complete line", msgs)
	}

	// The partial line now completes (CLI finished writing it).
	rest := ` tamamlandı"}}` + "\n"
	if err := os.WriteFile(p, []byte(full+partial+rest), 0644); err != nil {
		t.Fatal(err)
	}
	msgs2, _, err := claudeAdapter{}.ParseNewUserMessages(p, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs2) != 1 || msgs2[0].Content != "yarım tamamlandı" {
		t.Fatalf("second poll: got %+v, want the completed line (must not be lost)", msgs2)
	}
}

func TestClaudeSlug(t *testing.T) {
	cases := map[string]string{
		"/Users/yerli/Developer/MAPEG/YtkService": "-Users-yerli-Developer-MAPEG-YtkService",
		"/a/b.c":                               "-a-b-c", // dots become dashes too
		"/Users/yerli/.agent-chat/worktrees/x": "-Users-yerli--agent-chat-worktrees-x",
	}
	for in, want := range cases {
		if got := claudeSlug(in); got != want {
			t.Errorf("claudeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaudeDiscover_PicksNewestAfterSpawn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/tmp/myrepo"
	dir := filepath.Join(home, ".claude", "projects", claudeSlug(cwd))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	old := writeFile(t, dir, "old.jsonl", "{}\n")
	newer := writeFile(t, dir, "new.jsonl", "{}\n")
	// Make 'old' clearly older than spawn, 'new' clearly newer.
	spawn := time.Now()
	past := spawn.Add(-time.Hour)
	future := spawn.Add(time.Second)
	os.Chtimes(old, past, past)
	os.Chtimes(newer, future, future)

	got, err := claudeAdapter{}.DiscoverFile(cwd, spawn.UnixNano(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != newer {
		t.Fatalf("DiscoverFile = %q, want %q (newest at/after spawn)", got, newer)
	}
}

// With two session files both created after spawn (a sibling same-cwd terminal),
// discovery picks the one CLOSEST to this spawn — not simply the newest — so each
// terminal locks onto its own file (#65 / Codex P2).
func TestClaudeDiscover_PicksNearestToSpawnNotNewest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/tmp/myrepo"
	dir := filepath.Join(home, ".claude", "projects", claudeSlug(cwd))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	mine := writeFile(t, dir, "mine.jsonl", "{}\n")
	sibling := writeFile(t, dir, "sibling.jsonl", "{}\n")
	spawn := time.Now()
	// mine created ~0.3s after this spawn; sibling (a later terminal) ~1.5s after.
	near := spawn.Add(300 * time.Millisecond)
	far := spawn.Add(1500 * time.Millisecond)
	os.Chtimes(mine, near, near)
	os.Chtimes(sibling, far, far)

	got, err := claudeAdapter{}.DiscoverFile(cwd, spawn.UnixNano(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != mine {
		t.Fatalf("DiscoverFile = %q, want %q (nearest to spawn, not the newer sibling)", got, mine)
	}
}

func TestClaudeDiscover_MissingDirReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := claudeAdapter{}.DiscoverFile("/tmp/never", time.Now().UnixNano(), nil)
	if err != nil || got != "" {
		t.Fatalf("missing dir → (%q, %v), want (\"\", nil)", got, err)
	}
}
