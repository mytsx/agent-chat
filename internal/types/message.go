package types

import "time"

// Agent represents an agent in the chat room.
type Agent struct {
	Role     string  `json:"role"`
	JoinedAt string  `json:"joined_at"`
	LastSeen float64 `json:"last_seen"`
}

// RoomSummary is structured metadata about a room, used by the desktop room browser.
type RoomSummary struct {
	Name         string           `json:"name"`
	MessageCount int              `json:"message_count"`
	Agents       map[string]Agent `json:"agents"`        // persisted agent names + roles (no cleanup)
	LastActivity string           `json:"last_activity"` // last message timestamp (ISO), "" if empty
	IsDefault    bool             `json:"is_default"`
}

// Message represents a chat message.
type Message struct {
	ID              int    `json:"id"`
	From            string `json:"from"`
	To              string `json:"to"`
	OriginalTo      string `json:"original_to,omitempty"`
	Content         string `json:"content"`
	Timestamp       string `json:"timestamp"`
	Type            string `json:"type"`
	RoutedByManager bool   `json:"routed_by_manager,omitempty"`
	ExpectsReply    bool   `json:"expects_reply"`
	Priority        string `json:"priority"`
}

// Now returns current time as float64 (Python time.time() compatible).
func Now() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// Timestamp returns current time in ISO format.
func Timestamp() string {
	return time.Now().Format("2006-01-02T15:04:05.000000")
}

// CleanupStaleAgents removes agents inactive for more than timeout seconds.
func CleanupStaleAgents(agents map[string]Agent, timeout int) map[string]Agent {
	now := float64(time.Now().UnixNano()) / 1e9
	clean := make(map[string]Agent)
	for name, info := range agents {
		if now-info.LastSeen < float64(timeout) {
			clean[name] = info
		}
	}
	return clean
}
