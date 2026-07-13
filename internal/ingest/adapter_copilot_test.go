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

func TestCopilotDiscover_PicksNewestMatchingCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Join(home, ".copilot", "session-state")
	d1 := filepath.Join(base, "uuid-old")
	d2 := filepath.Join(base, "uuid-new")
	os.MkdirAll(d1, 0755)
	os.MkdirAll(d2, 0755)
	const cwd = "/work/repo"
	writeFile(t, d1, "workspace.yaml", "cwd: "+cwd+"\ngit_root: "+cwd+"\n")
	writeFile(t, d2, "workspace.yaml", "cwd: \""+cwd+"\"\n")
	f1 := writeFile(t, d1, "events.jsonl", "{}\n")
	f2 := writeFile(t, d2, "events.jsonl", "{}\n")
	spawn := time.Now()
	os.Chtimes(f1, spawn.Add(-time.Hour), spawn.Add(-time.Hour))
	os.Chtimes(f2, spawn.Add(time.Second), spawn.Add(time.Second))

	got, err := copilotAdapter{}.DiscoverFile(cwd, spawn.UnixNano(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != f2 {
		t.Fatalf("DiscoverFile = %q, want %q (newest with matching cwd)", got, f2)
	}
}

// A concurrent Copilot session in a DIFFERENT cwd, even if newer, must not be
// chosen for this terminal — workspace.yaml cwd disambiguates (#65 / Codex P2).
func TestCopilotDiscover_IgnoresOtherCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Join(home, ".copilot", "session-state")
	mine := filepath.Join(base, "uuid-mine")
	other := filepath.Join(base, "uuid-other")
	os.MkdirAll(mine, 0755)
	os.MkdirAll(other, 0755)
	writeFile(t, mine, "workspace.yaml", "cwd: /work/mine\n")
	writeFile(t, other, "workspace.yaml", "cwd: /work/other\n")
	fmine := writeFile(t, mine, "events.jsonl", "{}\n")
	fother := writeFile(t, other, "events.jsonl", "{}\n")
	spawn := time.Now()
	os.Chtimes(fmine, spawn.Add(time.Second), spawn.Add(time.Second))
	os.Chtimes(fother, spawn.Add(time.Hour), spawn.Add(time.Hour)) // newer but wrong cwd

	got, err := copilotAdapter{}.DiscoverFile("/work/mine", spawn.UnixNano(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != fmine {
		t.Fatalf("DiscoverFile = %q, want %q (must ignore the newer wrong-cwd session)", got, fmine)
	}
}

func TestCopilotDiscover_IgnoresNonRegularTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Join(home, ".copilot", "session-state")
	dir := filepath.Join(base, "uuid-dir")
	eventsDir := filepath.Join(dir, "events.jsonl")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatalf("mkdir events.jsonl dir: %v", err)
	}
	const cwd = "/work/repo"
	writeFile(t, dir, "workspace.yaml", "cwd: "+cwd+"\n")
	spawn := time.Now()
	if err := os.Chtimes(eventsDir, spawn.Add(time.Second), spawn.Add(time.Second)); err != nil {
		t.Fatalf("chtimes events.jsonl dir: %v", err)
	}

	got, err := copilotAdapter{}.DiscoverFile(cwd, spawn.UnixNano(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("DiscoverFile = %q, want no candidate for directory transcript", got)
	}
}
