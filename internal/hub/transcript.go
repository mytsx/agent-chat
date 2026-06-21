package hub

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"desktop/internal/types"
	"desktop/internal/validation"
)

// ReadFullTranscript reconstructs a room's complete message history from disk by
// merging every per-session snapshot (hub-state/sessions/{room}/*.json) with the
// rolling Phase-A archive (hub-state/archive/{room}.jsonl), deduplicated by
// message ID and ordered by ID ascending.
//
// This is the #29 read-path. A snapshot captures only the in-memory tail
// (~truncateToMessages) at a save point, while messages dropped before that live
// in the archive — so the full session transcript is the union of the two. The
// read is disk-only (no live RoomState), so it works uniformly for active rooms
// (snapshotted just before close), orphaned rooms, and historical sessions. For
// an active room, the caller should save a fresh snapshot first to capture any
// tail added since the last save.
//
// sinceID and limit filter the result exactly like RoomState.ReadAllMessages:
// only messages with ID > sinceID are returned, and a positive limit keeps the
// most recent `limit` of them.
func ReadFullTranscript(dataDir, room string, sinceID, limit int) ([]types.Message, error) {
	if dataDir == "" {
		return nil, nil
	}
	if err := validation.ValidateName(room); err != nil {
		return nil, fmt.Errorf("transcript: invalid room name %q: %w", room, err)
	}

	archived, err := readArchiveForRoom(dataDir, room)
	if err != nil {
		return nil, err
	}
	snapped, err := readSnapshotsForRoom(dataDir, room)
	if err != nil {
		return nil, err
	}

	// Dedup keyed by (ID, Timestamp), NOT ID alone: clear_room resets a room's IDs
	// back to 1, so the archive (pre-clear) and a later snapshot (post-clear) can
	// hold the SAME id for DISTINCT messages — keying on ID alone would silently
	// drop history. A genuine duplicate (same message in both the retained tail and
	// the archive) shares id AND timestamp, so it still collapses to one. Snapshots
	// are applied last so they win on a true-duplicate tie.
	type key struct {
		id int
		ts string
	}
	byKey := make(map[key]types.Message, len(archived)+len(snapped))
	for _, m := range archived {
		byKey[key{m.ID, m.Timestamp}] = m
	}
	for _, m := range snapped {
		byKey[key{m.ID, m.Timestamp}] = m
	}

	merged := make([]types.Message, 0, len(byKey))
	for _, m := range byKey {
		if m.ID > sinceID {
			merged = append(merged, m)
		}
	}
	// Order chronologically by timestamp (fixed-width, lexicographically sortable),
	// ID as tiebreaker. Timestamp order survives a clear's ID reset, where ID-only
	// ordering would interleave pre- and post-clear messages.
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Timestamp != merged[j].Timestamp {
			return merged[i].Timestamp < merged[j].Timestamp
		}
		return merged[i].ID < merged[j].ID
	})
	if limit > 0 && len(merged) > limit {
		merged = merged[len(merged)-limit:]
	}
	return merged, nil
}

// readArchiveForRoom reads hub-state/archive/{room}.jsonl, one types.Message per
// line. A missing archive yields an empty slice with no error. Generalizes the
// readArchiveLines test helper into production code.
func readArchiveForRoom(dataDir, room string) ([]types.Message, error) {
	dir := filepath.Join(dataDir, "hub-state", "archive")
	path := filepath.Join(dir, room+".jsonl")
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return nil, fmt.Errorf("transcript: archive room %q escapes archive dir", room)
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("transcript: open archive %q: %w", room, err)
	}
	defer f.Close()

	var out []types.Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m types.Message
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("transcript: parse archive line: %w", err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("transcript: scan archive %q: %w", room, err)
	}
	return out, nil
}

// readSnapshotsForRoom reads every hub-state/sessions/{room}/*.json snapshot and
// concatenates their messages (dedup happens in the caller). A missing sessions
// dir yields an empty slice with no error; a single snapshot that is unreadable
// or corrupt is skipped rather than failing the whole read (graceful degradation —
// one bad file must not lose every other session's history).
func readSnapshotsForRoom(dataDir, room string) ([]types.Message, error) {
	base := filepath.Join(dataDir, "hub-state", "sessions")
	dir := filepath.Join(base, room)
	if !strings.HasPrefix(dir, base+string(os.PathSeparator)) {
		return nil, fmt.Errorf("transcript: sessions room %q escapes sessions dir", room)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("transcript: read sessions dir %q: %w", room, err)
	}

	var out []types.Message
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue // skip an unreadable snapshot, keep the rest
		}
		var pr PersistedRoom
		if json.Unmarshal(data, &pr) != nil {
			continue // skip a corrupt snapshot, keep the rest
		}
		out = append(out, pr.Messages...)
	}
	return out, nil
}
