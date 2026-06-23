package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGeminiParse_UserMessagesWithCountCursor(t *testing.T) {
	dir := t.TempDir()
	obj := `{"sessionId":"s","messages":[
{"id":1,"timestamp":"2026-02-17T12:01:19.989Z","type":"user","content":[{"text":"merhaba "},{"text":"gemini"}]},
{"id":2,"timestamp":"2026-02-17T12:01:25.000Z","type":"gemini","content":[{"text":"selam"}]},
{"id":3,"timestamp":"2026-02-17T12:02:00.000Z","type":"user","content":[{"text":"ikinci"}]}
]}`
	p := writeFile(t, dir, "session-x.json", obj)

	msgs, cur, err := geminiAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Content != "merhaba gemini" || msgs[1].Content != "ikinci" {
		t.Fatalf("got %+v, want [merhaba gemini, ikinci] (gemini-type skipped, text joined)", msgs)
	}
	// Re-parse with the returned cursor — nothing new.
	again, _, _ := geminiAdapter{}.ParseNewUserMessages(p, cur)
	if len(again) != 0 {
		t.Fatalf("re-parse with cursor returned %+v, want none", again)
	}
}

// The monolithic Gemini file must NOT be re-read+re-parsed on every tick when it
// hasn't changed — the parse is gated on mtime (spec §3/§7; #65 / Copilot). Proven
// by rewriting with new content but FORCING the same mtime: the gate skips it.
func TestGeminiParse_SkipsReparseWhenMtimeUnchanged(t *testing.T) {
	dir := t.TempDir()
	obj1 := `{"messages":[{"timestamp":"t1","type":"user","content":[{"text":"bir"}]}]}`
	p := writeFile(t, dir, "session-x.json", obj1)
	fixed := time.Now().Add(-time.Minute)
	os.Chtimes(p, fixed, fixed)

	msgs, cur, err := geminiAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("first parse got %d, want 1", len(msgs))
	}

	// New content but SAME mtime → the gate must skip the re-parse.
	obj2 := `{"messages":[{"timestamp":"t1","type":"user","content":[{"text":"bir"}]},{"timestamp":"t2","type":"user","content":[{"text":"iki"}]}]}`
	if err := os.WriteFile(p, []byte(obj2), 0644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, fixed, fixed) // force unchanged mtime
	msgs2, _, _ := geminiAdapter{}.ParseNewUserMessages(p, cur)
	if len(msgs2) != 0 {
		t.Fatalf("unchanged mtime must skip re-parse, got %d new messages", len(msgs2))
	}

	// A real mtime bump → the new message is seen.
	later := fixed.Add(time.Minute)
	os.Chtimes(p, later, later)
	msgs3, _, _ := geminiAdapter{}.ParseNewUserMessages(p, cur)
	if len(msgs3) != 1 || msgs3[0].Content != "iki" {
		t.Fatalf("after mtime bump got %+v, want [iki]", msgs3)
	}
}

func TestGeminiDiscover_Sha256Folder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/tmp/myrepo"
	sum := sha256.Sum256([]byte(cwd))
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(home, ".gemini", "tmp", hash, "chats")
	os.MkdirAll(dir, 0755)
	f := writeFile(t, dir, "session-x.json", "{}")
	spawn := time.Now()
	os.Chtimes(f, spawn.Add(time.Second), spawn.Add(time.Second))

	got, err := geminiAdapter{}.DiscoverFile(cwd, spawn.UnixNano(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != f {
		t.Fatalf("DiscoverFile = %q, want %q", got, f)
	}
}
