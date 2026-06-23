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
	Agents       map[string]Agent `json:"agents"` // persisted agent names + roles (no cleanup)
	// HistoricalAgents lists distinct agent names derived from join system-messages,
	// populated only when the persisted roster (Agents) is empty (archived rooms whose
	// agents were stale-cleaned). These are PAST participants, not current members.
	HistoricalAgents []string `json:"historical_agents"`
	LastActivity     string   `json:"last_activity"` // last message timestamp (ISO), "" if empty
	IsDefault        bool     `json:"is_default"`
}

// Message Type values. system/broadcast/direct are produced by agent traffic;
// user_prompt records an out-of-band human→agent prompt so the instructions the
// user gave each agent become part of the summarized transcript (#29).
const (
	MsgTypeSystem     = "system"
	MsgTypeBroadcast  = "broadcast"
	MsgTypeDirect     = "direct"
	MsgTypeUserPrompt = "user_prompt"
)

// UserPromptFrom is the sentinel "from" identity stamped on logged user prompts.
const UserPromptFrom = "user"

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

// timestampLayout is the canonical message-timestamp format: local time, no
// timezone suffix, microsecond precision. The whole transcript sorts message
// Timestamp strings lexicographically, so every source MUST use this exact layout
// (and timezone) or messages interleave wrongly (#58/#65).
const timestampLayout = "2006-01-02T15:04:05.000000"

// Timestamp returns current time in ISO format.
func Timestamp() string {
	return time.Now().Format(timestampLayout)
}

// NormalizeTimestamp converts an external timestamp (e.g. a CLI session file's
// RFC3339/UTC value like "2026-06-23T10:00:00.000Z") into the canonical
// local-time timestampLayout so an ingested message sorts correctly against
// hub-stamped messages (#65). An empty or unparseable input falls back to the
// current time — never the raw string, which would break lexical ordering.
func NormalizeTimestamp(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Local().Format(timestampLayout)
	}
	return Timestamp()
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
