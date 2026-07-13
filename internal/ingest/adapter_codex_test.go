package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Among same-day rollouts, discovery must pick the one whose session_meta.cwd
// matches THIS terminal — a concurrent Codex session in another cwd, even if
// newer, must be ignored (#65 / Codex P2).
func TestCodexDiscover_MatchesByCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	spawn := time.Now()
	day := filepath.Join(home, ".codex", "sessions", spawn.Format("2006"), spawn.Format("01"), spawn.Format("02"))
	if err := os.MkdirAll(day, 0755); err != nil {
		t.Fatal(err)
	}
	metaMine := `{"timestamp":"t","type":"session_meta","payload":{"cwd":"/work/mine","cli_version":"0.142.0"}}` + "\n"
	metaOther := `{"timestamp":"t","type":"session_meta","payload":{"cwd":"/work/other"}}` + "\n"
	mine := writeFile(t, day, "rollout-mine.jsonl", metaMine)
	other := writeFile(t, day, "rollout-other.jsonl", metaOther)
	os.Chtimes(mine, spawn.Add(time.Second), spawn.Add(time.Second))
	os.Chtimes(other, spawn.Add(time.Hour), spawn.Add(time.Hour)) // newer but wrong cwd

	got, err := codexAdapter{}.DiscoverFile("/work/mine", spawn.UnixNano(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != mine {
		t.Fatalf("DiscoverFile = %q, want %q (matching cwd, not the newer wrong-cwd file)", got, mine)
	}
}

func TestCodexParse_NewFormatUsesEventMsgOnly(t *testing.T) {
	dir := t.TempDir()
	// New (2026) format: the user turn appears as BOTH an event_msg/user_message
	// AND a response_item/message(role:user). Only the event_msg counts (dedup).
	jsonl := `{"timestamp":"2026-06-23T17:00:00.000Z","type":"session_meta","payload":{"cwd":"/x","cli_version":"0.142.0"}}
{"timestamp":"2026-06-23T17:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"build the thing"}}
{"timestamp":"2026-06-23T17:00:05.001Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"build the thing"}]}}
{"timestamp":"2026-06-23T17:00:09.000Z","type":"event_msg","payload":{"type":"agent_message","message":"done"}}
`
	p := writeFile(t, dir, "rollout-x.jsonl", jsonl)
	msgs, _, err := codexAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "build the thing" {
		t.Fatalf("got %+v, want exactly one user_message (no response_item dup)", msgs)
	}
}

func TestCodexParse_OldFormatMessageRoleUser(t *testing.T) {
	dir := t.TempDir()
	jsonl := `{"type":"message","role":"user","content":"eski format mesaj"}
{"type":"message","role":"assistant","content":"cevap"}
`
	p := writeFile(t, dir, "rollout-y.jsonl", jsonl)
	msgs, _, err := codexAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "eski format mesaj" {
		t.Fatalf("got %+v, want the old-format user message", msgs)
	}
}

func TestCodexDiscover_SkipsNonRegularRollout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	spawn := time.Now()
	day := filepath.Join(home, ".codex", "sessions", spawn.Format("2006"), spawn.Format("01"), spawn.Format("02"))
	if err := os.MkdirAll(day, 0755); err != nil {
		t.Fatal(err)
	}

	target := writeFile(t, t.TempDir(), "target.jsonl", `{"timestamp":"t","type":"session_meta","payload":{"cwd":"/work/mine"}}`+"\n")
	link := filepath.Join(day, "rollout-linked.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := os.Chtimes(link, spawn.Add(time.Second), spawn.Add(time.Second)); err != nil {
		t.Fatalf("chtimes symlink rollout: %v", err)
	}
	regular := writeFile(t, day, "rollout-real.jsonl", `{"timestamp":"t","type":"session_meta","payload":{"cwd":"/work/mine"}}`+"\n")
	if err := os.Chtimes(regular, spawn.Add(2*time.Second), spawn.Add(2*time.Second)); err != nil {
		t.Fatalf("chtimes regular rollout: %v", err)
	}

	got, err := codexAdapter{}.DiscoverFile("/work/mine", spawn.UnixNano(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != regular {
		t.Fatalf("DiscoverFile = %q, want regular rollout %q (skip non-regular match)", got, regular)
	}
}

func TestCodexDiscover_ReturnsUnexpectedReadDirError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	spawn := time.Now()
	month := filepath.Join(home, ".codex", "sessions", spawn.Format("2006"), spawn.Format("01"))
	if err := os.MkdirAll(month, 0755); err != nil {
		t.Fatal(err)
	}
	dayPath := filepath.Join(month, spawn.Format("02"))
	if err := os.WriteFile(dayPath, []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := codexAdapter{}.DiscoverFile("/work/mine", spawn.UnixNano(), nil)
	if err == nil {
		t.Fatalf("DiscoverFile = %q,nil; want unexpected ReadDir error", got)
	}
}
