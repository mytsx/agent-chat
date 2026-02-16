package pty

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

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
	done      chan struct{}
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

// Create creates a new PTY session and returns its ID
func (m *Manager) Create(teamID, agentName, workDir string, env []string) (string, error) {
	id := uuid.New().String()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}

	cmd := exec.Command(shell, "-l")
	if workDir != "" {
		cmd.Dir = workDir
	}

	// Merge environment
	cmd.Env = append(os.Environ(), env...)

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
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[id] = session
	m.mu.Unlock()

	// Read PTY output in background
	go m.readLoop(session)

	return id, nil
}

// readLoop continuously reads from PTY and calls the output handler
func (m *Manager) readLoop(session *PTYSession) {
	defer close(session.done)

	buf := make([]byte, 8192)
	for {
		n, err := session.PTY.Read(buf)
		if n > 0 && m.onOutput != nil {
			data := make([]byte, n)
			copy(data, buf[:n])
			m.onOutput(session.ID, data)
		}
		if err != nil {
			if err != io.EOF {
				// PTY closed
			}
			return
		}
	}
}

// Write writes data to a PTY session's stdin
func (m *Manager) Write(sessionID string, data []byte) error {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	_, err := session.PTY.Write(data)
	return err
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
