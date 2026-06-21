package pty

import (
	"os"
	"testing"
	"time"
)

// newSessionForTest inserts a bare PTYSession into the manager without starting
// a real process. Tests for user-input bookkeeping only touch in-memory state.
func newSessionForTest(m *Manager, id string) *PTYSession {
	sess := &PTYSession{ID: id}
	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()
	return sess
}

func TestHasPendingInput_AfterRegister(t *testing.T) {
	m := NewManager(nil)
	newSessionForTest(m, "s1")

	m.RegisterUserInput("s1")

	if !m.HasPendingInput("s1") {
		t.Error("expected HasPendingInput=true right after RegisterUserInput")
	}
}

// A typed-but-unsubmitted line stays "pending" no matter how old it is — there
// is no quiet window, because the user's text is still sitting in the input
// buffer until they press Enter (issue #15 review: C3).
func TestHasPendingInput_StaysPendingWhenOld(t *testing.T) {
	m := NewManager(nil)
	newSessionForTest(m, "s1")

	m.RegisterUserInput("s1")
	time.Sleep(20 * time.Millisecond)

	if !m.HasPendingInput("s1") {
		t.Error("old unsubmitted input must still count as pending (no quiet window)")
	}
}

func TestHasPendingInput_NeverTyped(t *testing.T) {
	m := NewManager(nil)
	newSessionForTest(m, "s1")

	if m.HasPendingInput("s1") {
		t.Error("expected HasPendingInput=false when the user never typed")
	}
}

func TestHasPendingInput_UnknownSession(t *testing.T) {
	m := NewManager(nil)
	if m.HasPendingInput("ghost") {
		t.Error("expected HasPendingInput=false for an unknown session")
	}
}

func TestClearUserInput_ClearsPending(t *testing.T) {
	m := NewManager(nil)
	newSessionForTest(m, "s1")

	m.RegisterUserInput("s1")
	// Simulate the user pressing Enter: the input line is submitted/cleared, so
	// injection becomes safe immediately even though typing just happened.
	m.ClearUserInput("s1")

	if m.HasPendingInput("s1") {
		t.Error("expected HasPendingInput=false after ClearUserInput")
	}
}

// RegisterUserInput / ClearUserInput on an unknown session must not panic.
func TestRegisterClear_UnknownSessionNoPanic(t *testing.T) {
	m := NewManager(nil)
	m.RegisterUserInput("ghost")
	m.ClearUserInput("ghost")
}

func TestWriteUserInput_SetsAndClearsPending(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()

	m := NewManager(nil)
	sess := &PTYSession{ID: "s1", PTY: pw}
	m.mu.Lock()
	m.sessions["s1"] = sess
	m.mu.Unlock()

	if err := m.WriteUserInput("s1", []byte("a"), false); err != nil {
		t.Fatalf("WriteUserInput: %v", err)
	}
	if !m.HasPendingInput("s1") {
		t.Error("non-submit keystroke should mark input pending")
	}

	if err := m.WriteUserInput("s1", []byte("\r"), true); err != nil {
		t.Fatalf("WriteUserInput submit: %v", err)
	}
	if m.HasPendingInput("s1") {
		t.Error("submit keystroke should clear the pending flag")
	}
}

func TestWriteUserInput_UnknownSession(t *testing.T) {
	m := NewManager(nil)
	if err := m.WriteUserInput("ghost", []byte("x"), false); err == nil {
		t.Error("expected error for unknown session")
	}
}
