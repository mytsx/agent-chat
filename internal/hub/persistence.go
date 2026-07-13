package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"desktop/internal/validation"
)

const persistInterval = 5 * time.Second

// loadPersistedState loads room state from disk at startup.
func (h *Hub) loadPersistedState() {
	stateDir := filepath.Join(h.dataDir, "hub-state")
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		roomName := strings.TrimSuffix(e.Name(), ".json")
		data, err := os.ReadFile(filepath.Join(stateDir, e.Name()))
		if err != nil {
			h.logger.Printf("Failed to read persisted state for room %s: %v", roomName, err)
			continue
		}

		var pr PersistedRoom
		if err := json.Unmarshal(data, &pr); err != nil {
			h.logger.Printf("Failed to parse persisted state for room %s: %v", roomName, err)
			continue
		}

		room := NewRoomState()
		room.SetArchiveFn(h.archiveFnFor(roomName))
		room.mu.Lock()
		if pr.Messages != nil {
			room.messages = pr.Messages
		}
		if pr.Agents != nil {
			room.agents = pr.Agents
		}
		room.mu.Unlock()

		h.mu.Lock()
		h.rooms[roomName] = room
		h.mu.Unlock()

		h.logger.Printf("Loaded persisted state for room %s: %d messages, %d agents",
			roomName, len(pr.Messages), len(pr.Agents))
	}
}

// persistLoop runs the periodic persistence goroutine.
func (h *Hub) persistLoop() {
	ticker := time.NewTicker(persistInterval)
	defer ticker.Stop()

	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			h.persistDirtyRooms()
		}
	}
}

// persistDirtyRooms writes dirty rooms to disk.
func (h *Hub) persistDirtyRooms() {
	h.mu.RLock()
	roomNames := make([]string, 0, len(h.rooms))
	for name := range h.rooms {
		roomNames = append(roomNames, name)
	}
	h.mu.RUnlock()

	for _, name := range roomNames {
		h.mu.RLock()
		room, ok := h.rooms[name]
		deleted := h.deletedRooms[name]
		h.mu.RUnlock()
		if !ok || deleted || !room.IsDirty() {
			continue
		}

		h.persistRoom(name, room)
	}
}

// persistAll writes all rooms to disk (called on shutdown). It snapshots the room set
// under the lock and releases it BEFORE calling persistRoom, because persistRoom now
// takes h.mu.RLock itself — holding it here would be a recursive RLock (deadlock-prone
// if a writer arrives between the two acquisitions).
func (h *Hub) persistAll() {
	type entry struct {
		name string
		room *RoomState
	}
	h.mu.RLock()
	entries := make([]entry, 0, len(h.rooms))
	for name, room := range h.rooms {
		entries = append(entries, entry{name, room})
	}
	h.mu.RUnlock()

	for _, e := range entries {
		h.persistRoom(e.name, e.room)
	}
}

func (h *Hub) persistRoom(name string, room *RoomState) {
	// name becomes the hub-state/{name}.json path segment below. Every other
	// file-touching path (session.go, summary.go, archive.go, delete_room) already
	// rejects traversal names; this is the last write path that could turn an
	// unvalidated in-memory room (materialized via getOrCreateRoom) into a write
	// outside hub-state. Handlers validate the room before getOrCreateRoom, so this
	// guard is defense-in-depth and should never fire in normal operation.
	if err := validation.ValidateName(name); err != nil {
		h.logger.Printf("persistRoom: geçersiz oda adı %q atlanıyor: %v", name, err)
		return
	}

	stateDir := filepath.Join(h.dataDir, "hub-state")
	os.MkdirAll(stateDir, 0700)

	snapshot := room.Snapshot()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		h.logger.Printf("Failed to marshal room %s: %v", name, err)
		return
	}

	// Atomic write: temp file + rename
	tmpPath := filepath.Join(stateDir, name+".json.tmp")
	finalPath := filepath.Join(stateDir, name+".json")

	// Hold the read lock across the tombstone check AND the write/rename so a concurrent
	// delete_room cannot interleave between the check and the rename to resurrect the
	// file. delete_room sets the tombstone under h.mu.Lock and only os.Remove's the file
	// AFTER that lock section — which is blocked until this RUnlock. So either we see the
	// tombstone and skip, or our rename completes fully before delete's remove runs.
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.deletedRooms[name] {
		return
	}

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		h.logger.Printf("Failed to write temp file for room %s: %v", name, err)
		return
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		h.logger.Printf("Failed to rename temp file for room %s: %v", name, err)
		os.Remove(tmpPath)
		return
	}

	room.MarkClean()
}
