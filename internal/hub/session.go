package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"desktop/internal/validation"
)

// sessionsDir returns the directory holding a room's immutable per-session
// snapshots: hub-state/sessions/{room}/.
func (h *Hub) sessionsDir(room string) string {
	return filepath.Join(h.dataDir, "hub-state", "sessions", room)
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
	snapshot := roomState.Snapshot()

	// Skip an empty room: a session with no messages is noise, not history.
	if len(snapshot.Messages) == 0 {
		return "", 0, true, nil
	}
	// Messages are append-ordered by ID, so the last one carries the max ID. Any
	// change to the room — a new message OR a roster change (join/leave appends a
	// system message) — bumps this, so it is a reliable "changed since last
	// snapshot" signal for the messages+roster scope.
	maxID := snapshot.Messages[len(snapshot.Messages)-1].ID

	// Serialize the unchanged-check, the epoch-collision check, and the write so
	// two concurrent saves of the same room can't both write (or collide on an
	// epoch). Held across disk I/O like archiveMu — this path is low-frequency.
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()

	if last, seen := h.sessionLastID[room]; seen && last == maxID {
		return "", 0, true, nil // unchanged since the last snapshot
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", 0, false, fmt.Errorf("marshal session %q: %w", room, err)
	}

	dir := h.sessionsDir(room)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", 0, false, fmt.Errorf("create sessions dir: %w", err)
	}

	// Immutability: never overwrite an existing snapshot. Two saves in the same
	// wall-clock second collide on epoch, so advance to the next free epoch.
	// Safe under sessionMu: no concurrent save can create the file between the
	// stat and the write. The loop terminates (epochs are unbounded; files finite).
	epoch := time.Now().Unix()
	finalPath := filepath.Join(dir, fmt.Sprintf("%d.json", epoch))
	for {
		if _, statErr := os.Stat(finalPath); os.IsNotExist(statErr) {
			break
		}
		epoch++
		finalPath = filepath.Join(dir, fmt.Sprintf("%d.json", epoch))
	}
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return "", 0, false, fmt.Errorf("write session temp %q: %w", room, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return "", 0, false, fmt.Errorf("rename session %q: %w", room, err)
	}
	h.sessionLastID[room] = maxID
	return finalPath, len(snapshot.Messages), false, nil
}
