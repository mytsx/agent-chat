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

// newestJSONLAfter returns the most-recently-modified file in dir matching glob
// whose modtime is at/after spawn (minus a small skew) and which claimed reports
// is not already locked by another watcher, or "" if none. A missing dir yields
// ("", nil). A nil claimed treats every candidate as unclaimed.
func newestJSONLAfter(dir, glob string, spawnedAtUnixNano int64, claimed func(string) bool) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	cutoff := time.Unix(0, spawnedAtUnixNano).Add(-discoverSkew)
	var best string
	var bestMod time.Time
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
		if info.ModTime().Before(cutoff) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if claimed != nil && claimed(p) {
			continue // already locked by another terminal's watcher (#65)
		}
		if best == "" || info.ModTime().After(bestMod) {
			best, bestMod = p, info.ModTime()
		}
	}
	return best, nil
}
