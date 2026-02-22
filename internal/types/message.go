package types

import "time"

// Agent represents an agent in the chat room.
type Agent struct {
	Role     string  `json:"role"`
	JoinedAt string  `json:"joined_at"`
	LastSeen float64 `json:"last_seen"`
}

// Message represents a chat message.
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
