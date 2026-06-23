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
