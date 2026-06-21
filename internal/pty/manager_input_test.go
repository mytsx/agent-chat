package pty

import (
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

func TestUserTypingRecently_AfterRegister(t *testing.T) {
	m := NewManager(nil)
	newSessionForTest(m, "s1")

	m.RegisterUserInput("s1")

	if !m.UserTypingRecently("s1", time.Second) {
		t.Error("expected UserTypingRecently=true right after RegisterUserInput")
	}
}

func TestUserTypingRecently_AfterWindow(t *testing.T) {
	m := NewManager(nil)
	newSessionForTest(m, "s1")

	m.RegisterUserInput("s1")

	// Window is tiny; sleep past it so the input is no longer "recent".
	time.Sleep(20 * time.Millisecond)
	if m.UserTypingRecently("s1", 5*time.Millisecond) {
		t.Error("expected UserTypingRecently=false after the window elapsed")
	}
}

func TestUserTypingRecently_NeverTyped(t *testing.T) {
	m := NewManager(nil)
	newSessionForTest(m, "s1")

	if m.UserTypingRecently("s1", time.Second) {
		t.Error("expected UserTypingRecently=false when the user never typed")
	}
}

func TestUserTypingRecently_UnknownSession(t *testing.T) {
	m := NewManager(nil)
	if m.UserTypingRecently("ghost", time.Second) {
		t.Error("expected UserTypingRecently=false for an unknown session")
	}
}

func TestClearUserInput_ResetsTyping(t *testing.T) {
	m := NewManager(nil)
	newSessionForTest(m, "s1")

	m.RegisterUserInput("s1")
	// Simulate the user pressing Enter: the input line is submitted/cleared, so
	// injection becomes safe immediately even though typing just happened.
	m.ClearUserInput("s1")

	if m.UserTypingRecently("s1", time.Second) {
		t.Error("expected UserTypingRecently=false after ClearUserInput")
	}
}

// RegisterUserInput / ClearUserInput on an unknown session must not panic.
func TestRegisterClear_UnknownSessionNoPanic(t *testing.T) {
	m := NewManager(nil)
	m.RegisterUserInput("ghost")
	m.ClearUserInput("ghost")
}
