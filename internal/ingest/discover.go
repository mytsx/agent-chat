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

// nearestSessionFileAfter returns the file in dir matching glob whose mtime is
// CLOSEST to spawn (among files at/after spawn minus a small skew that aren't
// already claimed by another watcher), or "" if none. A missing dir yields
// ("", nil); a nil claimed treats every candidate as unclaimed.
//
// Closest-to-spawn — not newest — so two terminals launched from the same cwd
// close together each lock onto THEIR OWN file (the one created right after their
// own spawn) instead of both gravitating to the newest file on screen (#65).
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
	var best string
	var bestDiff time.Duration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ok, _ := filepath.Match(glob, e.Name()); !ok {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		mt := info.ModTime()
		if mt.Before(cutoff) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if claimed != nil && claimed(p) {
			continue // already locked by another terminal's watcher (#65)
		}
		diff := mt.Sub(spawn)
		if diff < 0 {
			diff = -diff
		}
		if best == "" || diff < bestDiff {
			best, bestDiff = p, diff
		}
	}
	return best, nil
}
