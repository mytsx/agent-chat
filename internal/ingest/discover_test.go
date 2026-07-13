package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A post-spawn file must beat a pre-spawn file even when the pre-spawn one is
// closer in absolute time — otherwise a quick restart locks onto the just-closed
// session file (mtime a few ms before the new spawn) instead of the new CLI's
// file (#65 / Codex round-4).
func TestPickNearestPostSpawn_PrefersPostSpawnOverCloserPreSpawn(t *testing.T) {
	spawn := time.Now()
	cands := []fileCandidate{
		{path: "stale", mod: spawn.Add(-1 * time.Millisecond)}, // pre-spawn, very close
		{path: "mine", mod: spawn.Add(50 * time.Millisecond)},  // post-spawn, farther
	}
	if got := pickNearestPostSpawn(cands, spawn); got != "mine" {
		t.Fatalf("got %q, want mine (post-spawn beats a closer pre-spawn file)", got)
	}
}

func TestPickNearestPostSpawn_EarliestPostSpawn(t *testing.T) {
	spawn := time.Now()
	cands := []fileCandidate{
		{path: "sibling", mod: spawn.Add(500 * time.Millisecond)},
		{path: "mine", mod: spawn.Add(50 * time.Millisecond)},
	}
	if got := pickNearestPostSpawn(cands, spawn); got != "mine" {
		t.Fatalf("got %q, want mine (earliest post-spawn is this terminal's own file)", got)
	}
}

// Only pre-spawn candidates → return "" and WAIT for the real post-spawn file,
// rather than permanently locking onto a stale/sibling file (#65 / Codex round-5).
func TestPickNearestPostSpawn_IgnoresPreSpawnOnly(t *testing.T) {
	spawn := time.Now()
	cands := []fileCandidate{
		{path: "older", mod: spawn.Add(-2 * time.Second)},
		{path: "stale", mod: spawn.Add(-5 * time.Millisecond)},
	}
	if got := pickNearestPostSpawn(cands, spawn); got != "" {
		t.Fatalf("got %q, want empty (only pre-spawn candidates → wait for this terminal's own file)", got)
	}
}

func TestPickNearestPostSpawn_Empty(t *testing.T) {
	if got := pickNearestPostSpawn(nil, time.Now()); got != "" {
		t.Fatalf("got %q, want empty for no candidates", got)
	}
}

func TestNearestSessionFileAfter_IgnoresNonRegularCandidate(t *testing.T) {
	dir := t.TempDir()
	spawn := time.Now()
	target := writeFile(t, dir, "target.jsonl", "{}\n")
	symlink := filepath.Join(dir, "session-symlink.jsonl")
	if err := os.Symlink(target, symlink); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	got, err := nearestSessionFileAfter(dir, "session-*.jsonl", spawn.UnixNano(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("nearestSessionFileAfter = %q, want no regular candidate", got)
	}
}
