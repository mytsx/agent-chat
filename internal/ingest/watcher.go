package ingest

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"desktop/internal/usage"
)

// isCLIExitCommand reports whether content is exactly one of the graceful-exit
// commands the app injects into an AI CLI's PTY on close (see
// pty.gracefulExitCommand). Unlike startup/broadcast/prompt-send injections these
// carry NO self-injection fingerprint, so a CLI that records the command in its
// transcript would have it ingested and logged as a user_prompt, polluting room
// history and session summaries with a shutdown keystroke (Codex PR #76 P2).
//
// Only the AI shutdown commands "/exit" (Claude/Copilot/Codex) and "/quit" (Gemini)
// belong here. The shell variant is bare "exit", but a shell session has no ingest
// adapter (AdapterFor → nil) so it is never watched — including "exit" would instead
// wrongly drop a real AI user prompt whose entire content is "exit" (Codex PR #77).
//
// The WHOLE trimmed content must match, so a genuine prompt that merely mentions
// /exit is still emitted — and a bare /exit is never meaningful room content anyway,
// since it exits the CLI.
func isCLIExitCommand(content string) bool {
	switch strings.TrimSpace(content) {
	case "/exit", "/quit":
		return true
	default:
		return false
	}
}

// pollInterval is how often a watcher re-reads its session file. 700ms balances
// log latency against churn; the CLI append is durable so nothing is lost.
const pollInterval = 700 * time.Millisecond

// stopDrainBudget bounds how long StopAll waits for watchers' final drains to
// deliver before the caller (shutdown) proceeds to snapshot the rooms (#65).
const stopDrainBudget = 2 * time.Second

// usageParseInterval throttles the usage-piggyback re-read (see run's
// discoverAndPoll): an active session's file changes on nearly every poll
// tick, so without a floor a multi-MB rollout would be re-scanned every
// pollInterval (700ms) just to extract usage (#10).
const usageParseInterval = 2 * time.Second

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
		if isCLIExitCommand(m.Content) {
			cur = m.After // app-injected graceful-exit command (/exit,/quit) — never a room prompt (#76 P2)
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
	// muted, when set, makes the watcher still discover+CLAIM its file (so a sibling
	// same-cwd watcher can't grab it) but DISCARD every message instead of emitting
	// it — used for observer terminals, whose typed prompts are private (#17/#65).
	muted atomic.Bool
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
// onUsage, when non-nil and ad implements UsageParser, receives a fresh
// usage.Snapshot (SessionID pre-filled) piggybacked on the message-poll tick, at
// most once per usageParseInterval. A nil onUsage (or an adapter that doesn't
// implement UsageParser) skips usage parsing entirely (#10).
// resume, when non-nil, makes the watcher SKIP content already present in the
// CLI's existing transcript on RESUME (#40): a CLI that appends to its prior
// ID-keyed file (Copilot) would otherwise be read from offset 0 and re-log every
// past user message into the room. The seed is applied ONLY when the discovered
// file is exactly resume.Path, so a CLI that resumes into a NEW file (Claude/
// Codex) is unaffected and starts fresh. resume.Cur is snapshotted at spawn —
// before the resumed CLI writes — so a prompt typed right after resume (which
// lands past the snapshot) is still ingested, not skipped (Codex P2-round2).
func (m *Manager) StartSession(sessionID string, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, ready func() bool, exited <-chan struct{}, emit EmitFunc, onSessionID func(id string), onUsage func(*usage.Snapshot), resume *ResumeSeed) {
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

	go m.run(s, ad, cwd, spawnedAtUnixNano, ready, exited, emit, onSessionID, onUsage, resume)
}

func (m *Manager) run(s *session, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, ready func() bool, exited <-chan struct{}, emit EmitFunc, onSessionID func(id string), onUsage func(*usage.Snapshot), resume *ResumeSeed) {
	defer close(s.done)  // registered first → runs LAST (after finish), so a StopAll
	defer m.finish(s.id) //   waiter sees a fully cleaned-up session.
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var path string
	var cur Cursor
	var lastUsageParse time.Time
	// em discards every message when the session is muted (observer claim-only),
	// otherwise delegates to the real emit (#17/#65).
	em := func(content, ts string) bool {
		if s.muted.Load() {
			return true
		}
		return emit(content, ts)
	}
	discoverAndPoll := func() {
		// Discover + claim FIRST, regardless of hub state: claiming this terminal's
		// file (so a sibling same-cwd watcher can't grab it) doesn't need the hub, and
		// a muted observer watcher must claim even while the hub is down (#65).
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
			// If another watcher claimed it between discovery and here, retry next tick
			// (its claimed-check will now exclude p) (#65).
			if !m.tryClaim(s.id, p) {
				return
			}
			path = p
			// #40: surface the CLI's own session ID once, right after the file is
			// discovered+claimed, so the app can store it for opt-in resume.
			if onSessionID != nil {
				if id := ad.SessionID(path); id != "" {
					onSessionID(id)
				}
			}
			// #40 resume (Codex P1): if this is the SAME transcript the resumed CLI is
			// appending to (Copilot), start past the content that existed at spawn so
			// the prior conversation isn't re-logged. Applied only on an exact path
			// match — a resume into a NEW file (Claude/Codex) discovers a different
			// path and is left at offset 0 (a fresh file has nothing to skip).
			if resume != nil && path == resume.Path {
				cur = resume.Cur
			}
		}
		// Gate only the poll/emit on hub readiness: don't advance the cursor (drop a
		// prompt) while emits can't be delivered (#65). A muted watcher's em discards,
		// so the gate just defers harmless no-op work.
		if ready != nil && !ready() {
			return
		}
		cur = pollOnce(ad, path, cur, s.fp, em)

		// Usage piggyback: after the message poll, if this adapter can extract usage,
		// re-read the file at most every usageParseInterval and hand a fresh snapshot to
		// onUsage. Throttled so an active session (file changes every tick) doesn't
		// re-scan a multi-MB rollout on every 700ms poll (spec §4). Skipped entirely
		// while muted (observer): an observer terminal is the user's private space
		// (#17), so its usage must not be parsed or emitted either, same as its
		// messages.
		up, canUsage := ad.(UsageParser)
		if canUsage && onUsage != nil && path != "" && !s.muted.Load() {
			if lastUsageParse.IsZero() || time.Since(lastUsageParse) >= usageParseInterval {
				lastUsageParse = time.Now()
				if snap, uerr := up.ParseUsage(path); uerr != nil {
					log.Printf("[USAGE] parse error (%s): %v", path, uerr)
				} else if snap != nil {
					snap.SessionID = s.id
					onUsage(snap)
				}
			}
		}
	}
	for {
		select {
		case <-s.cancel:
			// Final drain via the full discover+poll: a CLI that exits before the first
			// tick (a quick prompt then immediate close) may have flushed its transcript
			// on the way out, so the drain must DISCOVER the file too, not just poll an
			// already-found one. The claim it may take is released right after by the
			// deferred finish(), so there's no leak (#65 / Codex round-5).
			discoverAndPoll()
			return
		case <-exited:
			discoverAndPoll()
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

// Mute switches a session's watcher to claim-only: it keeps holding its file
// claim (so no sibling same-cwd watcher can ingest it) but discards every message
// instead of emitting it. Used for observer terminals — at creation, or when a
// running agent is promoted to observer in place (#17/#65). No-op for an unknown
// session.
func (m *Manager) Mute(sessionID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s != nil {
		s.muted.Store(true)
	}
}

// Unmute reverses Mute: the watcher resumes emitting messages. Used when an agent
// is demoted from observer back to manager/worker while running (#17/#65). No-op
// for an unknown session.
func (m *Manager) Unmute(sessionID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s != nil {
		s.muted.Store(false)
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

// StopAndWait stops a single terminal's watcher and waits — bounded by timeout —
// for its final drain to finish and its file claim to be released. Used before a
// same-file RESUME (Copilot appends to the prior session's events.jsonl): without
// this, the old watcher could still be alive when the resumed CLI writes its
// bootstrap prompt and ingest it under the OLD fingerprint store, logging the
// app's startup prompt into the room as a user message (#40, Codex round-3). A
// nil/unknown/already-finished session is a no-op.
func (m *Manager) StopAndWait(sessionID string, timeout time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s == nil {
		return // already finished (or never started) — nothing to drain
	}
	m.StopSession(sessionID) // closes cancel + releases the claim (safe-once via map delete)
	select {
	case <-s.done:
	case <-time.After(timeout):
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
