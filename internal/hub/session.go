package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"desktop/internal/validation"
)

// sessionSignature derives a change key for a room snapshot that covers BOTH the
// conversation (every retained message's persisted fields) and the agent roster
// (sorted name:role). The unchanged-skip compares this signature, so a
// roster-only change — e.g. a stale-agent cleanup that mutates the roster WITHOUT
// appending a message, which a max-ID-only check would miss — still differs and
// triggers a fresh snapshot. Including full message fields also keeps restart
// seeding from treating a loaded room as already captured when it merely has the
// same max ID as the latest snapshot but different persisted content (user-edited
// state, crash recovery, or a post-clear ID coincidence).
func sessionSignature(pr PersistedRoom) string {
	names := make([]string, 0, len(pr.Agents))
	for name := range pr.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "messages:%d", len(pr.Messages))
	for _, msg := range pr.Messages {
		fmt.Fprintf(&b, "|m:%d:%q:%q:%q:%q:%q:%q:%t:%t:%q",
			msg.ID,
			msg.From,
			msg.To,
			msg.OriginalTo,
			msg.Content,
			msg.Timestamp,
			msg.Type,
			msg.RoutedByManager,
			msg.ExpectsReply,
			msg.Priority,
		)
	}
	fmt.Fprintf(&b, "|agents:%d", len(names))
	for _, name := range names {
		b.WriteByte('|')
		fmt.Fprintf(&b, "a:%q:%q", name, pr.Agents[name].Role)
	}
	return b.String()
}

// sessionsDir returns the directory holding a room's immutable per-session
// snapshots: hub-state/sessions/{room}/. The room is a user-influenced path
// segment, so the joined directory is confined to the sessions base with a
// cleaned-prefix containment check. This is defense-in-depth beyond
// ValidateName (which already rejects traversal) and makes the path provably
// safe to static path-injection analysis. Returns an error if the room would
// escape the base.
func (h *Hub) sessionsDir(room string) (string, error) {
	base := filepath.Join(h.dataDir, "hub-state", "sessions")
	dir := filepath.Join(base, room) // filepath.Join cleans the result
	if !strings.HasPrefix(dir, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("session room %q escapes sessions dir", room)
	}
	return dir, nil
}

// resetSessionTracking forgets a room's last-snapshot signature so the next
// saveSession always writes. Called when the room's message IDs are reset
// (clear_room): the new conversation restarts at ID 1 and could otherwise
// coincide with the pre-clear max ID, wrongly tripping the unchanged-skip.
func (h *Hub) resetSessionTracking(room string) {
	h.sessionMu.Lock()
	delete(h.sessionLastSig, room)
	h.sessionMu.Unlock()
}

// seedSessionTracking primes sessionLastSig from the latest EXISTING session
// snapshot per room, but ONLY when the loaded room is byte-for-byte that
// snapshot (identical signature: same messages AND roster). This makes a restart
// skip re-snapshotting a genuinely unchanged room, while:
//   - a never-snapshotted room (crash before any save) stays unseeded → its first
//     post-restart save still writes; and
//   - a cleared room (IDs restarted) or one with post-snapshot activity has a
//     different loaded signature → also unseeded, so the new session's snapshot is
//     never skipped just because its max ID coincides with a stale one.
//
// Seeding from ordinary room persistence (hub-state/{room}.json) alone would be
// wrong on the crash case; seeding unconditionally from the snapshot would be
// wrong on the clear case. Called once at startup, after loadPersistedState.
func (h *Hub) seedSessionTracking() {
	if h.dataDir == "" {
		return
	}
	base := filepath.Join(h.dataDir, "hub-state", "sessions")
	roomDirs, err := os.ReadDir(base)
	if err != nil {
		return // no sessions dir yet — nothing to seed
	}
	for _, rd := range roomDirs {
		if !rd.IsDir() {
			continue
		}
		room := rd.Name()
		snap, ok := h.latestSnapshot(room)
		if !ok {
			continue
		}
		rs := h.getRoom(room)
		if rs == nil {
			continue // no loaded state to compare against
		}
		snapSig := sessionSignature(snap)
		if sessionSignature(rs.Snapshot()) != snapSig {
			continue // loaded state differs from the snapshot → let the next save write
		}
		h.sessionMu.Lock()
		h.sessionLastSig[room] = snapSig
		h.sessionMu.Unlock()
	}
}

// latestSnapshot reads and parses a room's most recent readable session snapshot.
// Corrupt/foreign newest files are skipped so one damaged write cannot disable
// restart seeding and cause duplicate idle snapshots.
func (h *Hub) latestSnapshot(room string) (PersistedRoom, bool) {
	dir, err := h.sessionsDir(room)
	if err != nil {
		return PersistedRoom{}, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return PersistedRoom{}, false
	}
	type candidate struct {
		name  string
		epoch int64
	}
	candidates := make([]candidate, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".json")
		epoch, perr := strconv.ParseInt(stem, 10, 64)
		if perr != nil {
			continue
		}
		candidates = append(candidates, candidate{name: e.Name(), epoch: epoch})
	}
	if len(candidates) == 0 {
		return PersistedRoom{}, false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].epoch > candidates[j].epoch })
	for _, c := range candidates {
		data, err := os.ReadFile(filepath.Join(dir, c.name))
		if err != nil {
			continue
		}
		var pr PersistedRoom
		if err := json.Unmarshal(data, &pr); err != nil {
			continue
		}
		if len(pr.Messages) == 0 {
			continue
		}
		return pr, true
	}
	return PersistedRoom{}, false
}

// saveSession writes an immutable snapshot of the room's full current state
// (messages + agent roster, via RoomState.Snapshot → PersistedRoom) to
// hub-state/sessions/{room}/{epoch}.json. Unlike the rolling Phase-A archive,
// each call produces a distinct per-epoch file that is never overwritten or
// pruned, so past sessions are preserved verbatim for later summarization (#29).
func (h *Hub) saveSession(room string) (path string, count int, skipped bool, err error) {
	// No data dir: silent no-op so unit hubs (New("", ...)) never touch the CWD.
	if h.dataDir == "" {
		return "", 0, true, nil
	}

	// Validate the room name before touching the filesystem: it becomes a path
	// segment (sessions/{room}/), so a traversal name must never reach disk.
	if err := validation.ValidateName(room); err != nil {
		return "", 0, false, fmt.Errorf("invalid session room name %q: %w", room, err)
	}

	// getRoom (not getOrCreateRoom) so saving a never-used room writes nothing
	// instead of materializing a phantom empty room that would later persist.
	roomState := h.getRoom(room)
	if roomState == nil {
		return "", 0, true, nil
	}

	// Serialize the snapshot, the unchanged-check, the epoch-collision check, and
	// the write under one lock so concurrent saves of the same room cannot observe
	// different signatures and then write out of order (which would regress
	// sessionLastSig and order files contrary to the conversation). Taking the
	// snapshot here means sessionMu is acquired before roomState's read lock;
	// nothing ever acquires sessionMu while holding a room lock, so there is no
	// lock-ordering cycle. Held across disk I/O like archiveMu — the session path
	// is low-frequency (termination hooks / manual save), not the hot message path.
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()

	// Snapshot captures the room's in-memory state. For a room past the cap
	// (maxMessagesInRoom) this is only the retained tail (~truncateToMessages); the
	// older messages were already streamed to the Phase-A rolling archive
	// (hub-state/archive/{room}.jsonl). The full per-session transcript is therefore
	// the snapshot ∪ archive — #29 reconstructs it from both when summarizing.
	snapshot := roomState.Snapshot()

	// Flush the async archive backlog to disk AFTER taking the snapshot, so a
	// transcript read right after (GetRoomTranscript calls SaveSession first) sees
	// the full snapshot ∪ archive. Doing it after the snapshot drains every batch
	// a truncation dropped before this point, narrowing the window to the tiny
	// (pre-existing, documented) gap between appendMessageLocked dropping a batch
	// and SendMessage enqueueing it after the room unlock — a microsecond race that
	// self-heals on the next read. flushArchive takes neither sessionMu nor a room
	// lock, so holding sessionMu across it cannot deadlock.
	h.flushArchive()

	// Skip an empty room: a session with no messages is noise, not history.
	if len(snapshot.Messages) == 0 {
		return "", 0, true, nil
	}
	// Skip a room unchanged in BOTH messages and roster since its last snapshot.
	// The signature covers the max message ID and the sorted roster, so a
	// roster-only change (stale-agent cleanup, which mutates agents without a
	// message) is NOT mistaken for "unchanged".
	sig := sessionSignature(snapshot)
	if last, seen := h.sessionLastSig[room]; seen && last == sig {
		return "", 0, true, nil
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", 0, false, fmt.Errorf("marshal session %q: %w", room, err)
	}

	dir, derr := h.sessionsDir(room)
	if derr != nil {
		return "", 0, false, derr
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", 0, false, fmt.Errorf("create sessions dir: %w", err)
	}

	// Immutability: never overwrite an existing snapshot. Two saves in the same
	// wall-clock second collide on epoch, so advance to the next free epoch.
	// Safe under sessionMu: no concurrent save can create the file between the
	// stat and the write. A stat error other than "not exist" (e.g. an
	// unsearchable directory) is surfaced rather than treated as a collision, so
	// the loop can't spin forever. It terminates (epochs are unbounded; files finite).
	epoch := time.Now().Unix()
	finalPath := filepath.Join(dir, fmt.Sprintf("%d.json", epoch))
	for {
		_, statErr := os.Stat(finalPath)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				break // free slot
			}
			return "", 0, false, fmt.Errorf("stat session path %q: %w", room, statErr)
		}
		epoch++ // file exists: this epoch is taken, try the next
		finalPath = filepath.Join(dir, fmt.Sprintf("%d.json", epoch))
	}
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return "", 0, false, fmt.Errorf("write session temp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return "", 0, false, fmt.Errorf("rename session %s -> %s: %w", tmpPath, finalPath, err)
	}
	h.sessionLastSig[room] = sig
	return finalPath, len(snapshot.Messages), false, nil
}
