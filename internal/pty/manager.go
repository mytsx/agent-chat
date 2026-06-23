package pty

import (
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"desktop/internal/sanitize"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

// PTYSession represents a single pseudo-terminal session
type PTYSession struct {
	ID        string
	Cmd       *exec.Cmd
	PTY       *os.File
	TeamID    string
	AgentName string
	CLIType   string
	// Room is the chat room name pinned at creation (the team name used for the
	// session's AGENT_CHAT_ROOM env). Logging reads this instead of re-resolving
	// the mutable Team.Name, so a team rename mid-life can't reroute a logged
	// prompt to a room the agent's MCP session isn't in (#58).
	Room           string
	WorkDir        string // stored for restart
	PromptID       string // stored for restart
	SlotIndex      int    // grid slot the terminal occupies (stored for restart)
	WorktreeDir    string // worktree directory path (empty if not using worktree)
	WorktreeRepo   string // main repo directory (for worktree cleanup)
	done           chan struct{}
	lastOutputNano atomic.Int64 // unix nano timestamp of last PTY output

	// lastUserInputNano is the unix nano timestamp of the last *user* keystroke
	// (set on the WriteToTerminal path), kept separate from lastOutputNano which
	// only tracks CLI output. A value of 0 means the input line is considered
	// empty (never typed, or submitted via Enter) — see ClearUserInput.
	lastUserInputNano atomic.Int64
	// writeMu serializes writes to this session's PTY so a multi-write
	// notification injection is not interleaved with user keystrokes.
	writeMu sync.Mutex
}

// OutputHandler is called when PTY produces output
type OutputHandler func(sessionID string, data []byte)

// Manager manages multiple PTY sessions
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*PTYSession
	onOutput OutputHandler
}

// NewManager creates a new PTY manager
func NewManager(onOutput OutputHandler) *Manager {
	return &Manager{
		sessions: make(map[string]*PTYSession),
		onOutput: onOutput,
	}
}

// Create creates a new PTY session and returns its ID.
// If cmdName is empty, falls back to the user's login shell.
func (m *Manager) Create(teamID, agentName, room, workDir string, env []string, cmdName string, cmdArgs []string, cliType string) (string, error) {
	id := uuid.New().String()

	// Fallback to login shell
	if cmdName == "" {
		cmdName = os.Getenv("SHELL")
		if cmdName == "" {
			cmdName = "/bin/zsh"
		}
		cmdArgs = []string{"-l"}
	}

	cmd := exec.Command(cmdName, cmdArgs...)
	if workDir != "" {
		cmd.Dir = workDir
	}

	// Merge environment, filtering out vars that cause nested session issues.
	// VSCODE_* and ELECTRON_* prevent child processes from communicating
	// with the parent VS Code instance (which can steal window focus).
	baseEnv := filterEnv(os.Environ(),
		"CLAUDECODE",
		"NODE_OPTIONS",
		"NODE_INSPECT_RESUME_ON_START",
		"VSCODE_*",
		"ELECTRON_*",
		"TERM_PROGRAM",
		"TERM_PROGRAM_VERSION",
		"GIT_ASKPASS",
	)
	cmd.Env = append(baseEnv, env...)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to start pty: %w", err)
	}

	session := &PTYSession{
		ID:        id,
		Cmd:       cmd,
		PTY:       ptmx,
		TeamID:    teamID,
		AgentName: agentName,
		CLIType:   cliType,
		Room:      room,
		WorkDir:   workDir,
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[id] = session
	m.mu.Unlock()

	// Read PTY output in background
	go m.readLoop(session)

	return id, nil
}

// readLoop continuously reads from PTY and calls the output handler.
// It buffers incomplete UTF-8 sequences across reads to prevent garbled output.
func (m *Manager) readLoop(session *PTYSession) {
	defer close(session.done)

	buf := make([]byte, 8192)
	var carry []byte // incomplete UTF-8 bytes from previous read

	for {
		n, err := session.PTY.Read(buf)
		if n > 0 {
			session.lastOutputNano.Store(time.Now().UnixNano())
		}
		if n > 0 && m.onOutput != nil {
			// Prepend any carried-over bytes from previous read
			var chunk []byte
			if len(carry) > 0 {
				chunk = append(carry, buf[:n]...)
				carry = nil
			} else {
				chunk = buf[:n]
			}

			// Find the last valid UTF-8 boundary
			sendLen := validUTF8Len(chunk)
			if sendLen < len(chunk) {
				carry = make([]byte, len(chunk)-sendLen)
				copy(carry, chunk[sendLen:])
			}

			if sendLen > 0 {
				data := make([]byte, sendLen)
				copy(data, chunk[:sendLen])
				m.onOutput(session.ID, data)
			}
		}
		if err != nil {
			// Flush any remaining carry bytes before exiting
			if len(carry) > 0 && m.onOutput != nil {
				m.onOutput(session.ID, carry)
			}
			return
		}
	}
}

// validUTF8Len returns the length of b that ends on a complete UTF-8 boundary.
// Any trailing incomplete multi-byte sequence is excluded.
func validUTF8Len(b []byte) int {
	n := len(b)
	if n == 0 {
		return 0
	}

	// Scan backwards from the end (up to 3 bytes) looking for
	// a leading byte that starts an incomplete multi-byte sequence.
	end := n - 1
	start := n - 4
	if start < 0 {
		start = 0
	}

	for i := end; i >= start; i-- {
		c := b[i]
		if c < 0x80 {
			// ASCII byte — everything up to n is on a valid boundary
			return n
		}
		if c >= 0xC0 {
			// Leading byte: determine expected sequence length
			var seqLen int
			switch {
			case c < 0xE0:
				seqLen = 2
			case c < 0xF0:
				seqLen = 3
			default:
				seqLen = 4
			}
			if n-i >= seqLen {
				return n // sequence is complete
			}
			return i // incomplete — exclude from this send
		}
		// 0x80–0xBF: continuation byte, keep scanning backward
	}
	// Only continuation bytes in the trailing window — send everything
	return n
}

// Write writes data to a PTY session's stdin. The per-session write mutex
// ensures this write is not interleaved with a concurrent notification
// injection (see WriteAtomic) or another Write.
func (m *Manager) Write(sessionID string, data []byte) error {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	return m.writeLocked(session, data)
}

// WriteAtomic runs fn while holding the session's per-session write mutex, so
// every write fn performs lands as one uninterrupted block relative to user
// keystrokes and other injections. fn is given a write function that performs
// raw PTY writes — the mutex is already held, so do NOT call Manager.Write from
// inside fn (it would deadlock).
func (m *Manager) WriteAtomic(sessionID string, fn func(write func([]byte) error) error) error {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	return fn(func(data []byte) error {
		return m.writeLocked(session, data)
	})
}

// writeLocked performs the raw PTY write plus debug logging. Callers must hold
// session.writeMu.
func (m *Manager) writeLocked(session *PTYSession, data []byte) error {
	// Debug logging for CLI sessions
	if session.CLIType != "" {
		preview := data
		if len(preview) > 120 {
			preview = preview[:120]
		}
		log.Printf("[PTY-WRITE] session=%s cli=%s agent=%s len=%d hex=%s",
			ShortID(session.ID), session.CLIType, session.AgentName, len(data), hex.EncodeToString(preview))
	}

	_, err := session.PTY.Write(data)
	return err
}

// WaitForIdle waits until `idleDuration` has passed since the last PTY output,
// or until `maxWait` is exceeded. Returns true if idle was reached.
func (m *Manager) WaitForIdle(sessionID string, idleDuration, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		m.mu.RLock()
		session, ok := m.sessions[sessionID]
		m.mu.RUnlock()
		if !ok {
			return false
		}
		nano := session.lastOutputNano.Load()
		if nano > 0 {
			lastOut := time.Unix(0, nano)
			if time.Since(lastOut) >= idleDuration {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// RegisterUserInput records that the user just typed into a session's terminal.
// Used by the orchestrator to defer notification injection while the user is
// actively typing, so notifications don't split the user's half-typed input.
func (m *Manager) RegisterUserInput(sessionID string) {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return
	}
	session.lastUserInputNano.Store(time.Now().UnixNano())
}

// WriteUserInput writes a user keystroke to the PTY and updates the pending-input
// flag ATOMICALLY under the per-session write mutex. submit=true means the
// keystroke submits/clears the line (Enter or Ctrl+C) → flag cleared; otherwise
// the flag is set (input pending). Doing the write and the flag update under the
// same lock keeps the flag consistent with concurrent keystrokes' write ordering,
// so a racing keystroke can't have its flag update separated from its write and
// clobbered (review CX4).
func (m *Manager) WriteUserInput(sessionID string, data []byte, submit bool) error {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	err := m.writeLocked(session, data)
	if submit {
		session.lastUserInputNano.Store(0)
	} else {
		session.lastUserInputNano.Store(time.Now().UnixNano())
	}
	return err
}

// InjectText writes text into a session's input line as if the user typed it,
// using the same bracketed-paste mechanics as a notification injection. It is the
// fan-out primitive behind App.BroadcastToTeam.
//
// submit=true appends a settle + CR to submit the line and clears the
// pending-input flag; submit=false leaves the text un-submitted (flag SET) so the
// user can confirm in each terminal and a later notification injection won't
// split it. Unlike the orchestrator's tryInject this does NOT skip on pending
// input — a broadcast is an explicit, deliberate user action.
//
// The whole sequence runs under the per-session write mutex so it lands as one
// uninterrupted block relative to user keystrokes and other injections.
func (m *Manager) InjectText(sessionID, text string, submit bool) error {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.writeMu.Lock()
	defer session.writeMu.Unlock()

	if session.CLIType == "copilot" {
		// Copilot's Ink/React TUI needs character-by-character input with no
		// bracketed paste; the inter-char sleeps mirror the orchestrator's copilot
		// injection path. Sent raw, control characters would act as live keys —
		// a newline submits the line (splitting a multiline broadcast even with
		// submit=false), Tab triggers autocomplete, ESC starts an escape sequence.
		// So every C0/C1 control / DEL is flattened to a space, keeping the injected
		// text literal (review: Codex P2 + completeness sweep). Invisible bidi/format
		// controls are dropped outright (Trojan-Source hygiene). \r\n collapses to a
		// single space first so a CRLF doesn't become two.
		flat := strings.ReplaceAll(text, "\r\n", " ")
		flat = strings.Map(func(r rune) rune {
			if sanitize.IsInvisibleFormat(r) {
				return -1
			}
			if sanitize.IsControl(r) {
				return ' '
			}
			return r
		}, flat)
		// Nothing left after sanitization (e.g. an all-invisible payload): no-op so
		// we never write a focus-in or leave a sticky pending flag for content that
		// was never visible.
		if flat == "" {
			return nil
		}
		if err := m.writeLocked(session, []byte("\x1b[I")); err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
		for _, c := range flat {
			if err := m.writeLocked(session, []byte(string(c))); err != nil {
				return err
			}
			time.Sleep(5 * time.Millisecond)
		}
		if submit {
			time.Sleep(100 * time.Millisecond)
			if err := m.writeLocked(session, []byte("\r")); err != nil {
				return err
			}
		}
	} else {
		// claude/gemini/shell: text is delivered as one bracketed-paste block (modern
		// shell readline treats it as a literal paste too). Strip any bracketed-paste
		// markers the user's text itself contains so an embedded close sequence can't
		// end paste mode early and let the tail run as live input (review:
		// completeness sweep — paste integrity).
		const (
			bracketOpen  = "\x1b[200~"
			bracketClose = "\x1b[201~"
		)
		safe := strings.ReplaceAll(text, bracketOpen, "")
		safe = strings.ReplaceAll(safe, bracketClose, "")
		// Strip control/format runes that survive the marker removal: C0/C1/DEL (a
		// stray ESC could still start an escape sequence inside the paste) and the
		// invisible Unicode format set (Trojan-Source). \n and \t are preserved —
		// paste mode delivers multiline content literally.
		safe = strings.Map(func(r rune) rune {
			switch r {
			case '\n', '\t':
				return r
			}
			if sanitize.IsControl(r) || sanitize.IsInvisibleFormat(r) {
				return -1
			}
			return r
		}, safe)
		// Nothing left after sanitization: no-op rather than writing an empty paste
		// (and, for submit=true, a blank Enter) or leaving a sticky pending flag.
		if safe == "" {
			return nil
		}
		if err := m.writeLocked(session, []byte(bracketOpen+safe+bracketClose)); err != nil {
			return err
		}
		if submit {
			// Let the Ink TUI register the paste before Enter (same settle as the
			// startup prompt and tryInject paths).
			time.Sleep(200 * time.Millisecond)
			if err := m.writeLocked(session, []byte("\r")); err != nil {
				return err
			}
		}
	}

	// Mirror WriteUserInput's flag bookkeeping: a submitted line is cleared; an
	// un-submitted broadcast becomes pending input so a notification can't split it.
	if submit {
		session.lastUserInputNano.Store(0)
	} else {
		session.lastUserInputNano.Store(time.Now().UnixNano())
	}
	return nil
}

// ClearUserInput marks a session's input line as empty (e.g. the user pressed
// Enter and submitted their line). After this, HasPendingInput reports false,
// so a pending notification can be injected safely.
func (m *Manager) ClearUserInput(sessionID string) {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return
	}
	session.lastUserInputNano.Store(0)
}

// HasPendingInput reports whether the session has user-typed input that has not
// yet been submitted (Enter). It is true from the first keystroke until
// ClearUserInput is called (on Enter). There is deliberately NO quiet window: a
// line the user typed and then paused on is still sitting in the input buffer,
// so injecting into it would corrupt it (issue #15 review: C3). Returns false
// for unknown sessions or when the buffer is empty.
func (m *Manager) HasPendingInput(sessionID string) bool {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	return session.lastUserInputNano.Load() != 0
}

// Resize resizes a PTY session
func (m *Manager) Resize(sessionID string, cols, rows uint16) error {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	return pty.Setsize(session.PTY, &pty.Winsize{
		Cols: cols,
		Rows: rows,
	})
}

func gracefulExitCommand(cliType string) string {
	switch strings.ToLower(strings.TrimSpace(cliType)) {
	case "claude":
		return "/exit\r"
	case "gemini":
		return "/quit\r"
	case "copilot":
		return "/exit\r"
	case "codex":
		return "/exit\r"
	case "shell":
		return "exit\r"
	default:
		return ""
	}
}

// Close closes a PTY session
func (m *Manager) Close(sessionID string) error {
	m.mu.Lock()
	session, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session not found: %s", sessionID)
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	// Hold the per-session write mutex across the graceful-exit write and the
	// fd close so we never tear the PTY down mid-injection: an in-flight
	// Write/WriteAtomic finishes its whole block first, and the graceful-exit
	// command itself is not interleaved with another writer.
	session.writeMu.Lock()
	// Ask the CLI to exit itself first for clean shutdown.
	if exitCmd := gracefulExitCommand(session.CLIType); exitCmd != "" && session.PTY != nil {
		if _, err := session.PTY.Write([]byte(exitCmd)); err != nil {
			log.Printf("[PTY-CLOSE] graceful exit write failed session=%s cli=%s: %v",
				ShortID(session.ID), session.CLIType, err)
		} else {
			time.Sleep(250 * time.Millisecond)
		}
	}

	// Close PTY file descriptor to stop IO loop.
	if session.PTY != nil {
		_ = session.PTY.Close()
	}
	session.writeMu.Unlock()

	// Terminate only this terminal's command/process group.
	if err := terminateCommandTree(session.Cmd, 2*time.Second); err != nil {
		log.Printf("[PTY-CLOSE] force terminate failed session=%s cli=%s: %v",
			ShortID(session.ID), session.CLIType, err)
	}

	// Prevent read goroutine leaks.
	select {
	case <-session.done:
	case <-time.After(1 * time.Second):
	}

	return nil
}

// GetSession returns session info
func (m *Manager) GetSession(sessionID string) *PTYSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

// GetSessionsByTeam returns all sessions for a team
func (m *Manager) GetSessionsByTeam(teamID string) []*PTYSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*PTYSession
	for _, s := range m.sessions {
		if s.TeamID == teamID {
			result = append(result, s)
		}
	}
	return result
}

// filterEnv removes specified keys from an environment variable slice.
// Keys ending with "*" are treated as prefix filters (e.g. "VSCODE_*" removes
// all variables starting with "VSCODE_").
func filterEnv(env []string, keys ...string) []string {
	result := make([]string, 0, len(env))
	for _, e := range env {
		skip := false
		for _, key := range keys {
			if strings.HasSuffix(key, "*") {
				prefix := key[:len(key)-1]
				if strings.HasPrefix(e, prefix) {
					skip = true
					break
				}
			} else if len(e) > len(key) && e[:len(key)+1] == key+"=" {
				skip = true
				break
			}
		}
		if !skip {
			result = append(result, e)
		}
	}
	return result
}

// CloseAll closes all sessions
func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.Close(id)
	}
}

// ShortID safely returns the first 8 characters of a session ID for logging.
func ShortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
