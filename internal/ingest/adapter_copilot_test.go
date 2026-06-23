package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCopilotParse_ExtractsRawUserContent(t *testing.T) {
	dir := t.TempDir()
	jsonl := `{"type":"session.start","timestamp":"2026-06-22T06:00:00.000Z","data":{}}
{"type":"user.message","timestamp":"2026-06-22T06:14:51.587Z","data":{"content":"review my PRs","transformedContent":"<reminder>review my PRs"}}
{"type":"assistant.message","timestamp":"2026-06-22T06:14:55.000Z","data":{"content":"sure"}}
`
	p := writeFile(t, dir, "events.jsonl", jsonl)
	msgs, _, err := copilotAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "review my PRs" {
		t.Fatalf("got %+v, want one msg with raw data.content (not transformedContent)", msgs)
	}
	if msgs[0].Timestamp != "2026-06-22T06:14:51.587Z" {
		t.Fatalf("timestamp = %q", msgs[0].Timestamp)
	}
}

func TestCopilotDiscover_PicksNewestEventsFileAfterSpawn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Join(home, ".copilot", "session-state")
	d1 := filepath.Join(base, "uuid-old")
	d2 := filepath.Join(base, "uuid-new")
	os.MkdirAll(d1, 0755)
	os.MkdirAll(d2, 0755)
	f1 := writeFile(t, d1, "events.jsonl", "{}\n")
	f2 := writeFile(t, d2, "events.jsonl", "{}\n")
	spawn := time.Now()
	os.Chtimes(f1, spawn.Add(-time.Hour), spawn.Add(-time.Hour))
	os.Chtimes(f2, spawn.Add(time.Second), spawn.Add(time.Second))

	got, err := copilotAdapter{}.DiscoverFile("/anything", spawn.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if got != f2 {
		t.Fatalf("DiscoverFile = %q, want %q", got, f2)
	}
}
