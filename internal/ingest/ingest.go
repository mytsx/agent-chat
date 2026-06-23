// Package ingest reconstructs the user's typed messages to AI-CLI agents by
// reading each CLI's own session/transcript file, so they can be logged into the
// room transcript (#65). The terminal stays an untouched raw passthrough; this
// package only observes the CLI's session files.
package ingest

// Cursor records how far a session file has been ingested. JSONL adapters use
// Offset (byte offset of the next unread line). The monolithic-JSON Gemini
// adapter uses Count (number of messages already emitted) plus ModTime (the file
// mtime at the last parse, in unix-nanos) so it can skip re-reading+re-parsing
// the whole file on ticks where it hasn't changed. Zero value = start.
type Cursor struct {
	Offset  int64
	Count   int
	ModTime int64
}

// ParsedMessage is one extracted user message plus After — the cursor to commit
// once this message has been handled (emitted or suppressed). Per-message cursors
// let the watcher advance exactly past each successfully-delivered message, so a
// failed emit (hub down mid-RPC) leaves the cursor before the lost message and it
// is retried rather than silently skipped (#65 / Codex P2).
type ParsedMessage struct {
	Content   string
	Timestamp string
	After     Cursor
}

// SessionAdapter is the per-CLI strategy for locating and parsing a session file.
type SessionAdapter interface {
	// DiscoverFile returns the path to the session file for a CLI spawned in cwd
	// at spawnedAtUnixNano, or "" (no error) if none is found yet. Candidate files
	// for which claimed(path) reports true (already locked by another live watcher)
	// are skipped, so two terminals launched from the SAME cwd don't both lock onto
	// the same file (#65 / Codex P2). A nil claimed claims nothing.
	DiscoverFile(cwd string, spawnedAtUnixNano int64, claimed func(path string) bool) (string, error)
	// ParseNewUserMessages reads the file starting from cur and returns verbatim
	// human user messages (each with its commit cursor) plus final — the cursor
	// past the whole read, including trailing non-user lines. It must skip assistant
	// output, tool results, and system/preamble entries, and gracefully skip records
	// whose schema version it does not recognize (returning no error).
	ParseNewUserMessages(path string, cur Cursor) (msgs []ParsedMessage, final Cursor, err error)
}

// EmitFunc receives one extracted, non-suppressed user message and reports
// whether it was delivered. A false return (e.g. the hub is unavailable) makes
// the watcher keep the cursor before this message so it is retried (#65).
type EmitFunc func(content, timestamp string) bool
