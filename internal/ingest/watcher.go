package ingest

import (
	"log"
	"sync"
	"time"
)

// pollInterval is how often a watcher re-reads its session file. 700ms balances
// log latency against churn; the CLI append is durable so nothing is lost.
const pollInterval = 700 * time.Millisecond

// pollOnce performs one ingest tick: parse new user messages from path starting
// at cur, suppress any that match a recorded self-injection, emit the rest, and
// return the advanced cursor. The single testable unit of a watcher.
func pollOnce(ad SessionAdapter, path string, cur Cursor, fp *fingerprintStore, emit EmitFunc) Cursor {
	msgs, next, err := ad.ParseNewUserMessages(path, cur)
	if err != nil {
		log.Printf("[INGEST] parse error (%s): %v", path, err)
		// next is still advanced past the read region by the adapter; keep it.
	}
	for _, m := range msgs {
		if fp.Consume(m.Content) {
			continue // app's own injection (startup/broadcast/prompt-send)
		}
		emit(m.Content, m.Timestamp)
	}
	return next
}

// session is one terminal's running watcher.
type session struct {
	cancel chan struct{}
	fp     *fingerprintStore
}

// Manager owns one watcher per AI terminal and the per-terminal fingerprint
// stores. Safe for concurrent use from the app (StartSession/RecordInjection/
// StopSession run on the Wails/event goroutines).
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*session
}

// New creates an empty Manager.
func New() *Manager {
	return &Manager{sessions: make(map[string]*session)}
}

// StartSession begins watching the CLI session file for a terminal. The watcher
// discovers the file (retrying until it appears), then polls it on an interval.
// A duplicate sessionID is ignored (idempotent). A nil adapter / empty id no-ops.
//
// ready reports whether emits can be delivered right now (e.g. the hub is
// connected). When it returns false the tick is skipped WITHOUT advancing the
// cursor, so a prompt parsed while the hub is down is retried once it returns
// rather than silently dropped (#65 / Codex P2). A nil ready means always-ready.
func (m *Manager) StartSession(sessionID string, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, ready func() bool, emit EmitFunc) {
	if m == nil || ad == nil || sessionID == "" || emit == nil {
		return
	}
	m.mu.Lock()
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		return
	}
	s := &session{cancel: make(chan struct{}), fp: newFingerprintStore()}
	m.sessions[sessionID] = s
	m.mu.Unlock()

	go m.run(s, ad, cwd, spawnedAtUnixNano, ready, emit)
}

func (m *Manager) run(s *session, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, ready func() bool, emit EmitFunc) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var path string
	var cur Cursor
	poll := func() {
		// Skip (don't advance the cursor) while emits can't be delivered, so a
		// prompt isn't parsed-and-dropped while the hub is unavailable (#65).
		if ready != nil && !ready() {
			return
		}
		if path == "" {
			p, err := ad.DiscoverFile(cwd, spawnedAtUnixNano)
			if err != nil {
				log.Printf("[INGEST] discover error: %v", err)
				return
			}
			if p == "" {
				return // not created yet — keep waiting
			}
			path = p
		}
		cur = pollOnce(ad, path, cur, s.fp, emit)
	}
	for {
		select {
		case <-s.cancel:
			// Final drain: a prompt the user submitted just before close/restart may
			// have been appended since the last tick — catch it before stopping (#65).
			poll()
			return
		case <-ticker.C:
			poll()
		}
	}
}

// RecordInjection notes that the app injected text into this terminal's PTY, so
// the watcher suppresses the CLI's recorded copy. No-op for an unknown session.
func (m *Manager) RecordInjection(sessionID, text string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s != nil {
		s.fp.Add(text)
	}
}

// StopSession stops and forgets a terminal's watcher.
func (m *Manager) StopSession(sessionID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	s := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	if s != nil {
		close(s.cancel)
	}
}

// StopAll stops every watcher (app shutdown).
func (m *Manager) StopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	all := m.sessions
	m.sessions = make(map[string]*session)
	m.mu.Unlock()
	for _, s := range all {
		close(s.cancel)
	}
}
