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

// pickNearestPostSpawn chooses this terminal's session file from candidates: the
// EARLIEST file created at/after spawn (the first file the freshly-spawned CLI
// wrote is this terminal's own). Pre-spawn files are IGNORED — returning "" until
// a post-spawn file appears is correct, because permanently locking onto a stale
// pre-spawn fallback (a just-closed session or a sibling's older file) would read
// the wrong transcript forever (#65 / Codex rounds 4-5). spawnedAt is captured
// before the CLI starts, so its real file is always post-spawn.
func pickNearestPostSpawn(cands []fileCandidate, spawn time.Time) string {
	var best string
	var bestMod time.Time
	for _, c := range cands {
		if c.mod.Before(spawn) {
			continue // pre-spawn: never lock onto a stale/sibling fallback
		}
		if best == "" || c.mod.Before(bestMod) {
			best, bestMod = c.path, c.mod // earliest post-spawn = this terminal's own file
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
		if ierr != nil || !info.Mode().IsRegular() || info.ModTime().Before(cutoff) {
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
