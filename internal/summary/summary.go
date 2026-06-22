// Package summary stores and retrieves per-session room summaries (#29).
//
// Each saved summary is an immutable Markdown file at
// hub-state/summaries/{room}/{epoch}.md, mirroring the per-session snapshot
// layout of internal/hub (sessions/{room}/{epoch}.json). "Continue" always
// resumes from the newest summary ("devam = en son"). The package is a leaf:
// it is shared by both the desktop app (writes + injection reads) and the hub
// (the read_summary RPC), so it depends only on stdlib + validation.
package summary

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"desktop/internal/validation"
)

const summaryExt = ".md"

// writeMu serializes summary writes so the epoch-collision check (os.Stat) and the
// subsequent temp-write+rename happen atomically as a unit. Without it, two
// concurrent writes to the same room can pick the same epoch (TOCTOU), then clobber
// each other's temp file or silently overwrite via rename (POSIX rename replaces an
// existing destination). The desktop is the only writer, so a process-wide lock is
// sufficient and mirrors hub.saveSession's sessionMu.
var writeMu sync.Mutex

// Doc is a single saved per-session room summary.
type Doc struct {
	Room      string `json:"room"`
	Epoch     string `json:"epoch"` // unix-second filename stem; fixed-width so lexicographic order == chronological
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"` // RFC3339, derived from the epoch
}

// summaryDir builds and validates the per-room summaries directory. The room is
// a user-influenced path segment, so beyond ValidateName a cleaned-prefix
// containment check guarantees it cannot escape the summaries base (mirrors
// hub.sessionsDir / appendArchive — defense-in-depth + provably path-safe).
func summaryDir(dataDir, room string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("summary: empty data dir")
	}
	if err := validation.ValidateName(room); err != nil {
		return "", fmt.Errorf("summary: invalid room name %q: %w", room, err)
	}
	base := filepath.Join(dataDir, "hub-state", "summaries")
	dir := filepath.Join(base, room)
	if !strings.HasPrefix(dir, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("summary: room %q escapes summaries dir", room)
	}
	return dir, nil
}

func docFromEpoch(room string, epoch int64, text string) Doc {
	return Doc{
		Room:      room,
		Epoch:     strconv.FormatInt(epoch, 10),
		Text:      text,
		CreatedAt: time.Unix(epoch, 0).Format(time.RFC3339),
	}
}

// Write persists text as a new immutable per-session summary and returns the
// stored Doc. Two writes in the same wall-clock second collide on epoch and
// advance to the next free one, so the newest file is always unambiguous.
func Write(dataDir, room, text string) (Doc, error) {
	dir, err := summaryDir(dataDir, room)
	if err != nil {
		return Doc{}, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Doc{}, fmt.Errorf("summary: create dir: %w", err)
	}

	// Serialize the collision check + write so concurrent saves of the same room
	// can't pick the same epoch and clobber each other (see writeMu).
	writeMu.Lock()
	defer writeMu.Unlock()

	epoch := time.Now().Unix()
	path := filepath.Join(dir, strconv.FormatInt(epoch, 10)+summaryExt)
	for {
		if _, statErr := os.Stat(path); statErr != nil {
			if os.IsNotExist(statErr) {
				break // free slot
			}
			return Doc{}, fmt.Errorf("summary: stat %q: %w", path, statErr)
		}
		epoch++ // taken: try the next epoch
		path = filepath.Join(dir, strconv.FormatInt(epoch, 10)+summaryExt)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0644); err != nil {
		os.Remove(tmp) // don't leave an orphan temp on a partial/failed write
		return Doc{}, fmt.Errorf("summary: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return Doc{}, fmt.Errorf("summary: rename: %w", err)
	}
	return docFromEpoch(room, epoch, text), nil
}

// Latest returns the newest saved summary for the room. ok is false (with no
// error) when the room has no summaries yet, or when dataDir is empty.
//
// It reads ONLY the newest readable file (not the whole directory): Latest is on
// the hot path (every agent startup via composeAgentPrompt + read_summary), so it
// scans filenames newest-first and reads the first one that opens — skipping an
// unreadable newest file the same way List does (graceful degradation).
func Latest(dataDir, room string) (Doc, bool, error) {
	if dataDir == "" {
		return Doc{}, false, nil
	}
	dir, err := summaryDir(dataDir, room)
	if err != nil {
		return Doc{}, false, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Doc{}, false, nil
		}
		return Doc{}, false, fmt.Errorf("summary: read dir: %w", err)
	}

	stems := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), summaryExt) {
			continue
		}
		stems = append(stems, strings.TrimSuffix(e.Name(), summaryExt))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(stems))) // newest first

	for _, stem := range stems {
		epoch, perr := strconv.ParseInt(stem, 10, 64)
		if perr != nil {
			continue // ignore foreign filenames
		}
		data, rerr := os.ReadFile(filepath.Join(dir, stem+summaryExt))
		if rerr != nil {
			continue // skip an unreadable file, fall back to the next-newest
		}
		return docFromEpoch(room, epoch, string(data)), true, nil
	}
	return Doc{}, false, nil
}

// List returns all saved summaries for the room, newest first. A missing room
// (or empty dataDir) yields an empty slice with no error.
func List(dataDir, room string) ([]Doc, error) {
	if dataDir == "" {
		return nil, nil
	}
	dir, err := summaryDir(dataDir, room)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("summary: read dir: %w", err)
	}

	stems := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), summaryExt) {
			continue
		}
		stems = append(stems, strings.TrimSuffix(e.Name(), summaryExt))
	}
	// Newest first: fixed-width unix-second stems sort lexicographically the same
	// as chronologically.
	sort.Sort(sort.Reverse(sort.StringSlice(stems)))

	docs := make([]Doc, 0, len(stems))
	for _, stem := range stems {
		epoch, perr := strconv.ParseInt(stem, 10, 64)
		if perr != nil {
			continue // ignore foreign filenames defensively
		}
		data, rerr := os.ReadFile(filepath.Join(dir, stem+summaryExt))
		if rerr != nil {
			continue // skip an unreadable summary file, keep the rest (graceful degradation)
		}
		docs = append(docs, docFromEpoch(room, epoch, string(data)))
	}
	return docs, nil
}
