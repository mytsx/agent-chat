package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"desktop/internal/validation"
)

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

// resetSessionTracking forgets a room's last-snapshot ID so the next saveSession
// always writes. Called when the room's message IDs are reset (clear_room): the
// new conversation restarts at ID 1 and could otherwise coincide with the
// pre-clear max ID, wrongly tripping the unchanged-skip optimization.
func (h *Hub) resetSessionTracking(room string) {
	h.sessionMu.Lock()
	delete(h.sessionLastID, room)
	h.sessionMu.Unlock()
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
	// different max IDs and then write out of order (which would regress
	// sessionLastID and order files contrary to the conversation). Taking the
	// snapshot here means sessionMu is acquired before roomState's read lock;
	// nothing ever acquires sessionMu while holding a room lock, so there is no
	// lock-ordering cycle. Held across disk I/O like archiveMu — the session path
	// is low-frequency (termination hooks / manual save), not the hot message path.
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()

	snapshot := roomState.Snapshot()

	// Skip an empty room: a session with no messages is noise, not history.
	if len(snapshot.Messages) == 0 {
		return "", 0, true, nil
	}
	// Messages are append-ordered by ID, so the last one carries the max ID. Any
	// new message — and any join/leave, which appends a system message — bumps it,
	// so for the conversation-history purpose it is a reliable "changed since last
	// snapshot" signal. (A stale-agent cleanup drops an agent without a message, so
	// that lone roster delta can be missed; the captured conversation is unaffected
	// and re-snapshotting an otherwise-identical room adds no historical value.)
	maxID := snapshot.Messages[len(snapshot.Messages)-1].ID
	if last, seen := h.sessionLastID[room]; seen && last == maxID {
		return "", 0, true, nil // unchanged since the last snapshot
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
	h.sessionLastID[room] = maxID
	return finalPath, len(snapshot.Messages), false, nil
}
