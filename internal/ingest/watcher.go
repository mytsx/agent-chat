package ingest

import (
	"log"
	"sync"
	"time"
)

// pollInterval is how often a watcher re-reads its session file. 700ms balances
// log latency against churn; the CLI append is durable so nothing is lost.
const pollInterval = 700 * time.Millisecond

// stopDrainBudget bounds how long StopAll waits for watchers' final drains to
// deliver before the caller (shutdown) proceeds to snapshot the rooms (#65).
const stopDrainBudget = 2 * time.Second

// pollOnce performs one ingest tick: parse new user messages from path starting
// at startCur, suppress any that match a recorded self-injection, emit the rest,
// and return the cursor to commit. The cursor advances PER MESSAGE, so if an emit
// fails (hub unavailable mid-RPC) the returned cursor stays before that message
// and it is retried next tick rather than silently lost (#65).
func pollOnce(ad SessionAdapter, path string, startCur Cursor, fp *fingerprintStore, emit EmitFunc) Cursor {
	msgs, final, err := ad.ParseNewUserMessages(path, startCur)
	if err != nil {
		log.Printf("[INGEST] parse error (%s): %v", path, err)
	}
	cur := startCur
	for _, m := range msgs {
		if fp.Consume(m.Content) {
			cur = m.After // app's own injection (startup/broadcast/prompt-send) — handled
			continue
		}
		if !emit(m.Content, m.Timestamp) {
			return cur // emit failed (hub down) — stop, retry from here next tick
		}
		cur = m.After
	}
	// Every message delivered; also commit past trailing non-user lines. Only when
	// the parse itself didn't error (on error, keep the per-message cursor so a
	// partially-read region is re-read).
	if err == nil {
		return final
	}
	return cur
}

// session is one terminal's running watcher.
type session struct {
	id     string
	cancel chan struct{}
	done   chan struct{} // closed when run() returns (after its final drain)
	fp     *fingerprintStore
}

// Manager owns one watcher per AI terminal, the per-terminal fingerprint stores,
// and the set of session-file paths currently claimed by a live watcher (so two
// terminals from the same cwd don't lock onto the same file). Safe for concurrent
// use from the app (StartSession/RecordInjection/StopSession run on the
// Wails/event goroutines).
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*session
	claims   map[string]string // session-file path → owning sessionID (#65)
}

// New creates an empty Manager.
func New() *Manager {
	return &Manager{sessions: make(map[string]*session), claims: make(map[string]string)}
}

// StartSession begins watching the CLI session file for a terminal. The watcher
// discovers the file (retrying until it appears), then polls it on an interval.
// A duplicate sessionID is ignored (idempotent). A nil adapter / empty id no-ops.
//
// ready reports whether emits can be delivered right now (e.g. the hub is
// connected). When it returns false the tick is skipped WITHOUT advancing the
// cursor, so a prompt parsed while the hub is down is retried once it returns
// rather than silently dropped (#65). A nil ready means always-ready.
// exited is a channel that closes when the terminal's PTY process dies (e.g. the
// user typed /exit inside the CLI) so the watcher stops even without an explicit
// StopSession; nil means "no PTY-death signal" (#65).
func (m *Manager) StartSession(sessionID string, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, ready func() bool, exited <-chan struct{}, emit EmitFunc) {
	if m == nil || ad == nil || sessionID == "" || emit == nil {
		return
	}
	m.mu.Lock()
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		return
	}
	s := &session{id: sessionID, cancel: make(chan struct{}), done: make(chan struct{}), fp: newFingerprintStore()}
	m.sessions[sessionID] = s
	m.mu.Unlock()

	go m.run(s, ad, cwd, spawnedAtUnixNano, ready, exited, emit)
}

func (m *Manager) run(s *session, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, ready func() bool, exited <-chan struct{}, emit EmitFunc) {
	defer close(s.done)  // registered first → runs LAST (after finish), so a StopAll
	defer m.finish(s.id) //   waiter sees a fully cleaned-up session.
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var path string
	var cur Cursor
	discoverAndPoll := func() {
		// Skip (don't advance the cursor) while emits can't be delivered, so a
		// prompt isn't parsed-and-dropped while the hub is unavailable (#65).
		if ready != nil && !ready() {
			return
		}
		if path == "" {
			p, err := ad.DiscoverFile(cwd, spawnedAtUnixNano, func(cp string) bool {
				return m.isClaimedByOther(s.id, cp)
			})
			if err != nil {
				log.Printf("[INGEST] discover error: %v", err)
				return
			}
			if p == "" {
				return // not created yet — keep waiting
			}
			// Claim the file so a sibling terminal in the same cwd picks a different
			// one. If another watcher claimed it between discovery and here, retry
			// next tick (its claimed-check will now exclude p) (#65).
			if !m.tryClaim(s.id, p) {
				return
			}
			path = p
		}
		cur = pollOnce(ad, path, cur, s.fp, emit)
	}
	// drain catches a prompt submitted just before stop, on an ALREADY-discovered
	// file only — it must NOT discover/claim on the way out, or a late claim added
	// after StopSession released this session's claims would leak and block a
	// same-cwd restart's watcher (#65).
	drain := func() {
		if path != "" && (ready == nil || ready()) {
			pollOnce(ad, path, cur, s.fp, emit)
		}
	}
	for {
		select {
		case <-s.cancel:
			drain()
			return
		case <-exited:
			drain()
			return
		case <-ticker.C:
			discoverAndPoll()
		}
	}
}

// finish removes a session and releases its file claim. Idempotent — runs from
// run()'s defer (covers a PTY-death exit with no StopSession) and is safe if
// StopSession already removed the session.
func (m *Manager) finish(sessionID string) {
	m.mu.Lock()
	delete(m.sessions, sessionID)
	m.releaseClaims(sessionID)
	m.mu.Unlock()
}

func (m *Manager) tryClaim(sessionID, path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if owner := m.claims[path]; owner == "" || owner == sessionID {
		m.claims[path] = sessionID
		return true
	}
	return false
}

func (m *Manager) isClaimedByOther(sessionID, path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner := m.claims[path]
	return owner != "" && owner != sessionID
}

func (m *Manager) releaseClaims(sessionID string) {
	for p, owner := range m.claims {
		if owner == sessionID {
			delete(m.claims, p)
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

// StopSession stops and forgets a terminal's watcher and releases its file claim.
func (m *Manager) StopSession(sessionID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	s := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.releaseClaims(sessionID)
	m.mu.Unlock()
	if s != nil {
		close(s.cancel)
	}
}

// StopAll stops every watcher (app shutdown) and waits — bounded — for their
// final drains to deliver, so a prompt typed just before quit isn't lost from the
// post-shutdown room snapshot (#65).
func (m *Manager) StopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	all := m.sessions
	m.sessions = make(map[string]*session)
	m.claims = make(map[string]string)
	m.mu.Unlock()
	for _, s := range all {
		close(s.cancel)
	}
	deadline := time.Now().Add(stopDrainBudget)
	for _, s := range all {
		select {
		case <-s.done:
		case <-time.After(time.Until(deadline)):
		}
	}
}
