package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
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
		h.mu.RUnlock()
		if !ok || !room.IsDirty() {
			continue
		}

		h.persistRoom(name, room)
	}
}

// persistAll writes all rooms to disk (called on shutdown).
func (h *Hub) persistAll() {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for name, room := range h.rooms {
		h.persistRoom(name, room)
	}
}

func (h *Hub) persistRoom(name string, room *RoomState) {
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
