package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Agent represents an agent in the chat room (matches watcher.Agent).
type Agent struct {
	Role     string  `json:"role"`
	JoinedAt string  `json:"joined_at"`
	LastSeen float64 `json:"last_seen"`
}

// Message represents a chat message (matches watcher.Message).
type Message struct {
	ID           int    `json:"id"`
	From         string `json:"from"`
	To           string `json:"to"`
	Content      string `json:"content"`
	Timestamp    string `json:"timestamp"`
	Type         string `json:"type"`
	ExpectsReply bool   `json:"expects_reply,omitempty"`
	Priority     string `json:"priority,omitempty"`
}

// Storage handles JSON file I/O with file locking.
type Storage struct {
	chatDir     string
	defaultRoom string
}

// NewStorage creates a new Storage instance.
func NewStorage(chatDir, defaultRoom string) *Storage {
	os.MkdirAll(chatDir, 0700)
	return &Storage{
		chatDir:     chatDir,
		defaultRoom: defaultRoom,
	}
}

// getRoomDir returns the room directory, creating it if needed.
func (s *Storage) getRoomDir(room string) string {
	roomName := room
	if roomName == "" {
		roomName = s.defaultRoom
	}
	dir := filepath.Join(s.chatDir, roomName)
	os.MkdirAll(dir, 0700)
	return dir
}

// readJSON reads a JSON file with a shared lock.
func (s *Storage) readJSON(path string, target any) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // file doesn't exist, target keeps its zero value
		}
		return err
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return fmt.Errorf("flock shared: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}

	return json.Unmarshal(data, target)
}

// writeJSON writes data to a JSON file with an exclusive lock.
func (s *Storage) writeJSON(path string, data any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock exclusive: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		return err
	}

	_, err = f.Write(buf.Bytes())
	return err
}

// GetAgents reads agents from the room's agents.json.
func (s *Storage) GetAgents(room string) (map[string]Agent, error) {
	agents := make(map[string]Agent)
	path := filepath.Join(s.getRoomDir(room), "agents.json")
	if err := s.readJSON(path, &agents); err != nil {
		return nil, err
	}
	return agents, nil
}

// SaveAgents writes agents to the room's agents.json.
func (s *Storage) SaveAgents(agents map[string]Agent, room string) error {
	path := filepath.Join(s.getRoomDir(room), "agents.json")
	return s.writeJSON(path, agents)
}

// GetMessages reads messages from the room's messages.json.
func (s *Storage) GetMessages(room string) ([]Message, error) {
	var messages []Message
	path := filepath.Join(s.getRoomDir(room), "messages.json")
	if err := s.readJSON(path, &messages); err != nil {
		return nil, err
	}
	if messages == nil {
		messages = []Message{}
	}
	return messages, nil
}

// SaveMessages writes messages to the room's messages.json.
func (s *Storage) SaveMessages(messages []Message, room string) error {
	path := filepath.Join(s.getRoomDir(room), "messages.json")
	return s.writeJSON(path, messages)
}

// CleanupStaleAgents removes agents inactive for more than timeout seconds.
func (s *Storage) CleanupStaleAgents(agents map[string]Agent, timeout int) map[string]Agent {
	now := float64(time.Now().UnixNano()) / 1e9
	clean := make(map[string]Agent)
	for name, info := range agents {
		if now-info.LastSeen < float64(timeout) {
			clean[name] = info
		}
	}
	return clean
}

// Now returns current time as float64 (Python time.time() compatible).
func Now() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// Timestamp returns current time in ISO format (Python datetime.now().isoformat() compatible).
func Timestamp() string {
	return time.Now().Format("2006-01-02T15:04:05.000000")
}
