package ingest

import (
	"os"
	"path/filepath"
	"time"
)

// discoverSkew tolerates a small clock gap between the recorded spawn time and
// the session file's mtime (the CLI may create the file a moment before/after).
const discoverSkew = 5 * time.Second

// newestJSONLAfter returns the most-recently-modified file in dir matching glob
// whose modtime is at/after spawn (minus a small skew), or "" if none. A missing
// dir yields ("", nil).
func newestJSONLAfter(dir, glob string, spawnedAtUnixNano int64) (string, error) {
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
		if best == "" || info.ModTime().After(bestMod) {
			best, bestMod = filepath.Join(dir, e.Name()), info.ModTime()
		}
	}
	return best, nil
}
