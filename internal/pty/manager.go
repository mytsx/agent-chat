package pty

import (
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

// PTYSession represents a single pseudo-terminal session
type PTYSession struct {
	ID             string
	Cmd            *exec.Cmd
	PTY            *os.File
	TeamID         string
	AgentName      string
	CLIType        string
	done           chan struct{}
	lastOutputNano atomic.Int64 // unix nano timestamp of last PTY output
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
func (m *Manager) Create(teamID, agentName, workDir string, env []string, cmdName string, cmdArgs []string, cliType string) (string, error) {
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

	// Merge environment, filtering out vars that cause nested session issues
	baseEnv := filterEnv(os.Environ(), "CLAUDECODE")
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

// Write writes data to a PTY session's stdin
func (m *Manager) Write(sessionID string, data []byte) error {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	// Debug logging for CLI sessions
	if session.CLIType != "" {
		preview := data
		if len(preview) > 120 {
			preview = preview[:120]
		}
		log.Printf("[PTY-WRITE] session=%s cli=%s agent=%s len=%d hex=%s",
			sessionID[:8], session.CLIType, session.AgentName, len(data), hex.EncodeToString(preview))
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

	// Close PTY file descriptor
	session.PTY.Close()

	// Kill process
	if session.Cmd.Process != nil {
		session.Cmd.Process.Kill()
	}
	session.Cmd.Wait()

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

// filterEnv removes specified keys from an environment variable slice
func filterEnv(env []string, keys ...string) []string {
	result := make([]string, 0, len(env))
	for _, e := range env {
		skip := false
		for _, key := range keys {
			if len(e) > len(key) && e[:len(key)+1] == key+"=" {
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
