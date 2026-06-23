package ingest

import (
	"os"
	"path/filepath"
	"time"
)

// discoverSkew tolerates a small clock/filesystem-granularity gap between the
// recorded spawn time and the session file's mtime. Kept small because spawnedAt
// is captured BEFORE the CLI process starts, so the new file's mtime is at/after
// it — a large skew would risk locking onto a prior session's file in the same
// cwd on a quick restart (#65 / Codex P2).
const discoverSkew = 2 * time.Second

// fileCandidate is a discovered session-file path plus its mtime.
type fileCandidate struct {
	path string
	mod  time.Time
}

// pickNearestPostSpawn chooses this terminal's session file from candidates.
// A file created at/after spawn always beats one before it, and among post-spawn
// files the EARLIEST wins (the first file created after the spawn is this
// terminal's own). A pre-spawn file (admitted only within discoverSkew, for clock
// jitter) is a last resort, and among those the LATEST (closest to spawn) wins.
// This prevents a quick restart from locking onto the just-closed session file,
// whose mtime can fall a few ms before the new spawn (#65 / Codex round-4).
func pickNearestPostSpawn(cands []fileCandidate, spawn time.Time) string {
	var best string
	var bestPost bool
	var bestMod time.Time
	for _, c := range cands {
		post := !c.mod.Before(spawn) // mtime >= spawn
		switch {
		case best == "":
			best, bestPost, bestMod = c.path, post, c.mod
		case post && !bestPost:
			best, bestPost, bestMod = c.path, true, c.mod // a post-spawn file beats any pre-spawn one
		case post && bestPost:
			if c.mod.Before(bestMod) {
				best, bestMod = c.path, c.mod // earliest post-spawn
			}
		case !post && !bestPost:
			if c.mod.After(bestMod) {
				best, bestMod = c.path, c.mod // latest pre-spawn (closest to spawn)
			}
		}
	}
	return best
}

// nearestSessionFileAfter returns this terminal's session file in dir matching
// glob — the candidate nearest to spawn per pickNearestPostSpawn — among files
// at/after spawn minus a small skew that aren't already claimed by another
// watcher, or "" if none. A missing dir yields ("", nil); a nil claimed treats
// every candidate as unclaimed.
func nearestSessionFileAfter(dir, glob string, spawnedAtUnixNano int64, claimed func(string) bool) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	spawn := time.Unix(0, spawnedAtUnixNano)
	cutoff := spawn.Add(-discoverSkew)
	var cands []fileCandidate
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ok, _ := filepath.Match(glob, e.Name()); !ok {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || info.ModTime().Before(cutoff) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if claimed != nil && claimed(p) {
			continue // already locked by another terminal's watcher (#65)
		}
		cands = append(cands, fileCandidate{path: p, mod: info.ModTime()})
	}
	return pickNearestPostSpawn(cands, spawn), nil
}
