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
func (m *Manager) StartSession(sessionID string, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, emit EmitFunc) {
	if ad == nil || sessionID == "" || emit == nil {
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

	go m.run(s, ad, cwd, spawnedAtUnixNano, emit)
}

func (m *Manager) run(s *session, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, emit EmitFunc) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var path string
	var cur Cursor
	for {
		select {
		case <-s.cancel:
			return
		case <-ticker.C:
			if path == "" {
				p, err := ad.DiscoverFile(cwd, spawnedAtUnixNano)
				if err != nil {
					log.Printf("[INGEST] discover error: %v", err)
					continue
				}
				if p == "" {
					continue // not created yet — keep waiting
				}
				path = p
			}
			cur = pollOnce(ad, path, cur, s.fp, emit)
		}
	}
}

// RecordInjection notes that the app injected text into this terminal's PTY, so
// the watcher suppresses the CLI's recorded copy. No-op for an unknown session.
func (m *Manager) RecordInjection(sessionID, text string) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s != nil {
		s.fp.Add(text)
	}
}

// StopSession stops and forgets a terminal's watcher.
func (m *Manager) StopSession(sessionID string) {
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
	m.mu.Lock()
	all := m.sessions
	m.sessions = make(map[string]*session)
	m.mu.Unlock()
	for _, s := range all {
		close(s.cancel)
	}
}
