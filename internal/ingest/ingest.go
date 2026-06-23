// Package ingest reconstructs the user's typed messages to AI-CLI agents by
// reading each CLI's own session/transcript file, so they can be logged into the
// room transcript (#65). The terminal stays an untouched raw passthrough; this
// package only observes the CLI's session files.
package ingest

// UserMessage is one verbatim human message extracted from a CLI session file.
type UserMessage struct {
	Content   string // verbatim user text (no tool results / assistant output)
	Timestamp string // the CLI file's own ISO-8601 timestamp for this message
}

// Cursor records how far a session file has been ingested. JSONL adapters use
// Offset (byte offset of the next unread line). The monolithic-JSON Gemini
// adapter uses Count (number of messages already emitted). Zero value = start.
type Cursor struct {
	Offset int64
	Count  int
}

// SessionAdapter is the per-CLI strategy for locating and parsing a session file.
type SessionAdapter interface {
	// DiscoverFile returns the path to the session file for a CLI spawned in cwd
	// at spawnedAtUnixNano, or "" (no error) if none is found yet.
	DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error)
	// ParseNewUserMessages reads the file starting from cur and returns verbatim
	// human user messages plus the advanced cursor. It must skip assistant output,
	// tool results, and system/preamble entries, and gracefully skip records whose
	// schema version it does not recognize (returning no error).
	ParseNewUserMessages(path string, cur Cursor) (msgs []UserMessage, next Cursor, err error)
}

// EmitFunc receives one extracted, non-suppressed user message.
type EmitFunc func(content, timestamp string)
