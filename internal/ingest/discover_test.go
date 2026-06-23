package ingest

import (
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

func TestPickNearestPostSpawn_FallsBackToLatestPreSpawn(t *testing.T) {
	spawn := time.Now()
	cands := []fileCandidate{
		{path: "older", mod: spawn.Add(-2 * time.Second)},
		{path: "jitter", mod: spawn.Add(-5 * time.Millisecond)}, // closest pre-spawn
	}
	if got := pickNearestPostSpawn(cands, spawn); got != "jitter" {
		t.Fatalf("got %q, want jitter (latest pre-spawn when no post-spawn candidate)", got)
	}
}

func TestPickNearestPostSpawn_Empty(t *testing.T) {
	if got := pickNearestPostSpawn(nil, time.Now()); got != "" {
		t.Fatalf("got %q, want empty for no candidates", got)
	}
}
