package watcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Message represents a chat message
type Message struct {
	ID           int    `json:"id"`
	From         string `json:"from"`
	To           string `json:"to"`
	Content      string `json:"content"`
	Timestamp    string `json:"timestamp"`
	Type         string `json:"type"`
	ExpectsReply bool   `json:"expects_reply"`
	Priority     string `json:"priority"`
}

// Agent represents an agent in the chat room
type Agent struct {
	Role     string  `json:"role"`
	JoinedAt string  `json:"joined_at"`
	LastSeen float64 `json:"last_seen"`
}

// MessageHandler is called when new messages are detected
type MessageHandler func(chatDir string, messages []Message)

// AgentHandler is called when agent list changes
type AgentHandler func(chatDir string, agents map[string]Agent)

// Watcher watches chat directories for changes
type Watcher struct {
	mu             sync.RWMutex
	fsWatcher      *fsnotify.Watcher
	dirs           map[string]int // chatDir -> lastMessageID
	onMessages     MessageHandler
	onAgents       AgentHandler
	done           chan struct{}
	debounceTimers map[string]*time.Timer
}

// New creates a new file watcher
func New(onMessages MessageHandler, onAgents AgentHandler) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &Watcher{
		fsWatcher:      fsw,
		dirs:           make(map[string]int),
		onMessages:     onMessages,
		onAgents:       onAgents,
		done:           make(chan struct{}),
		debounceTimers: make(map[string]*time.Timer),
	}, nil
}

// WatchDir starts watching a chat directory
func (w *Watcher) WatchDir(chatDir string) error {
	// Ensure directory exists
	os.MkdirAll(chatDir, 0755)

	w.mu.Lock()
	w.dirs[chatDir] = 0
	w.mu.Unlock()

	// Do initial read
	w.readMessages(chatDir)
	w.readAgents(chatDir)

	return w.fsWatcher.Add(chatDir)
}

// UnwatchDir stops watching a chat directory
func (w *Watcher) UnwatchDir(chatDir string) error {
	w.mu.Lock()
	delete(w.dirs, chatDir)
	w.mu.Unlock()

	return w.fsWatcher.Remove(chatDir)
}

// Start begins the watch loop
func (w *Watcher) Start() {
	go w.loop()
}

// Stop stops the watcher
func (w *Watcher) Stop() {
	close(w.done)
	w.fsWatcher.Close()
}

func (w *Watcher) loop() {
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				w.handleFileChange(event.Name)
			}
		case _, ok := <-w.fsWatcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *Watcher) handleFileChange(path string) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	// Debounce: aggregate rapid changes
	key := path
	w.mu.Lock()
	if timer, exists := w.debounceTimers[key]; exists {
		timer.Stop()
	}
	w.debounceTimers[key] = time.AfterFunc(100*time.Millisecond, func() {
		switch base {
		case "messages.json":
			w.readMessages(dir)
		case "agents.json":
			w.readAgents(dir)
		}
	})
	w.mu.Unlock()
}

func (w *Watcher) readMessages(chatDir string) {
	path := filepath.Join(chatDir, "messages.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var messages []Message
	if err := json.Unmarshal(data, &messages); err != nil {
		return
	}

	w.mu.RLock()
	lastID := w.dirs[chatDir]
	w.mu.RUnlock()

	// Find new messages
	var newMessages []Message
	for _, msg := range messages {
		if msg.ID > lastID {
			newMessages = append(newMessages, msg)
		}
	}

	if len(newMessages) > 0 {
		w.mu.Lock()
		w.dirs[chatDir] = newMessages[len(newMessages)-1].ID
		w.mu.Unlock()

		if w.onMessages != nil {
			w.onMessages(chatDir, newMessages)
		}
	}
}

func (w *Watcher) readAgents(chatDir string) {
	path := filepath.Join(chatDir, "agents.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var agents map[string]Agent
	if err := json.Unmarshal(data, &agents); err != nil {
		return
	}

	if w.onAgents != nil {
		w.onAgents(chatDir, agents)
	}
}

// GetAllMessages reads all messages from a chat directory
func (w *Watcher) GetAllMessages(chatDir string) []Message {
	path := filepath.Join(chatDir, "messages.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var messages []Message
	json.Unmarshal(data, &messages)
	return messages
}

// GetAllAgents reads all agents from a chat directory
func (w *Watcher) GetAllAgents(chatDir string) map[string]Agent {
	path := filepath.Join(chatDir, "agents.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var agents map[string]Agent
	json.Unmarshal(data, &agents)
	return agents
}
