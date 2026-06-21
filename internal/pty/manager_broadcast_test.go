package pty

import (
	"io"
	"os"
	"testing"
)

// newPipeSession registers a session backed by an os.Pipe so a test can read
// exactly what InjectText wrote to the PTY. The returned read end must be drained
// after the write end (sess.PTY) is closed.
func newPipeSession(t *testing.T, m *Manager, id, cliType string) (read *os.File, write *os.File) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	sess := &PTYSession{ID: id, CLIType: cliType, PTY: pw}
	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()
	return pr, pw
}

// readAll closes the write end and reads everything buffered in the pipe.
func readAll(t *testing.T, pr, pw *os.File) string {
	t.Helper()
	pw.Close()
	out, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(out)
}

const (
	bpOpen  = "\x1b[200~"
	bpClose = "\x1b[201~"
)

// claude/gemini/shell: text is wrapped in a bracketed-paste block. With
// submit=false no CR is sent (the broadcast is left pending for the user to
// confirm), and the session is marked pending so a later notification injection
// won't split it.
func TestInjectText_BracketedPasteNoSubmit(t *testing.T) {
	m := NewManager(nil)
	pr, pw := newPipeSession(t, m, "s1", "claude")
	defer pr.Close()

	if err := m.InjectText("s1", "merhaba", false); err != nil {
		t.Fatalf("InjectText: %v", err)
	}
	if !m.HasPendingInput("s1") {
		t.Error("un-submitted broadcast text must mark the session pending")
	}

	got := readAll(t, pr, pw)
	want := bpOpen + "merhaba" + bpClose
	if got != want {
		t.Errorf("output = %q, want %q (bracketed paste, no CR)", got, want)
	}
}

// submit=true appends a CR after the paste and clears the pending flag (the line
// is submitted).
func TestInjectText_BracketedPasteSubmit(t *testing.T) {
	m := NewManager(nil)
	pr, pw := newPipeSession(t, m, "s1", "gemini")
	defer pr.Close()

	if err := m.InjectText("s1", "deploy", true); err != nil {
		t.Fatalf("InjectText: %v", err)
	}
	if m.HasPendingInput("s1") {
		t.Error("submitted broadcast must clear the pending flag")
	}

	got := readAll(t, pr, pw)
	want := bpOpen + "deploy" + bpClose + "\r"
	if got != want {
		t.Errorf("output = %q, want %q (bracketed paste + CR)", got, want)
	}
}

// shell terminals use the same bracketed-paste path as claude/gemini (modern
// shell readline treats the block as a literal paste).
func TestInjectText_ShellUsesBracketedPaste(t *testing.T) {
	m := NewManager(nil)
	pr, pw := newPipeSession(t, m, "s1", "shell")
	defer pr.Close()

	if err := m.InjectText("s1", "ls -la", false); err != nil {
		t.Fatalf("InjectText: %v", err)
	}

	got := readAll(t, pr, pw)
	want := bpOpen + "ls -la" + bpClose
	if got != want {
		t.Errorf("shell output = %q, want bracketed paste %q", got, want)
	}
}

// copilot's Ink/React TUI needs char-by-char input with no bracketed paste; the
// sequence is a focus-in followed by each character.
func TestInjectText_CopilotCharByCharNoSubmit(t *testing.T) {
	m := NewManager(nil)
	pr, pw := newPipeSession(t, m, "s1", "copilot")
	defer pr.Close()

	if err := m.InjectText("s1", "hi", false); err != nil {
		t.Fatalf("InjectText: %v", err)
	}
	if !m.HasPendingInput("s1") {
		t.Error("un-submitted copilot broadcast must mark the session pending")
	}

	got := readAll(t, pr, pw)
	want := "\x1b[I" + "hi"
	if got != want {
		t.Errorf("copilot output = %q, want %q (focus-in + chars, no bracketed paste)", got, want)
	}
}

func TestInjectText_CopilotSubmit(t *testing.T) {
	m := NewManager(nil)
	pr, pw := newPipeSession(t, m, "s1", "copilot")
	defer pr.Close()

	if err := m.InjectText("s1", "hi", true); err != nil {
		t.Fatalf("InjectText: %v", err)
	}
	if m.HasPendingInput("s1") {
		t.Error("submitted copilot broadcast must clear the pending flag")
	}

	got := readAll(t, pr, pw)
	want := "\x1b[I" + "hi" + "\r"
	if got != want {
		t.Errorf("copilot output = %q, want %q (focus-in + chars + CR)", got, want)
	}
}

// Copilot input is raw char-by-char with no bracketed paste, so a literal
// newline byte would submit the line. Embedded newlines must be flattened to
// spaces to preserve the no-premature-submit contract (submit=false) and keep a
// multiline broadcast as one prompt.
func TestInjectText_CopilotFlattensNewlines(t *testing.T) {
	m := NewManager(nil)
	pr, pw := newPipeSession(t, m, "s1", "copilot")
	defer pr.Close()

	if err := m.InjectText("s1", "line1\nline2", false); err != nil {
		t.Fatalf("InjectText: %v", err)
	}

	got := readAll(t, pr, pw)
	want := "\x1b[I" + "line1 line2"
	if got != want {
		t.Errorf("copilot output = %q, want %q (newline flattened to space, no submit)", got, want)
	}
}

func TestInjectText_CopilotFlattenSubmit(t *testing.T) {
	m := NewManager(nil)
	pr, pw := newPipeSession(t, m, "s1", "copilot")
	defer pr.Close()

	if err := m.InjectText("s1", "a\r\nb", true); err != nil {
		t.Fatalf("InjectText: %v", err)
	}

	got := readAll(t, pr, pw)
	want := "\x1b[I" + "a b" + "\r"
	if got != want {
		t.Errorf("copilot output = %q, want %q (CRLF→space, single trailing CR)", got, want)
	}
}

// The bracketed-paste path (claude/gemini/shell) must PRESERVE newlines — paste
// mode delivers multiline content without submitting, so no flattening there.
func TestInjectText_BracketedPastePreservesNewline(t *testing.T) {
	m := NewManager(nil)
	pr, pw := newPipeSession(t, m, "s1", "claude")
	defer pr.Close()

	if err := m.InjectText("s1", "a\nb", false); err != nil {
		t.Fatalf("InjectText: %v", err)
	}

	got := readAll(t, pr, pw)
	want := bpOpen + "a\nb" + bpClose
	if got != want {
		t.Errorf("output = %q, want %q (newline preserved inside paste)", got, want)
	}
}

// A bracketed-paste close sequence embedded in the user's text must be
// neutralized — otherwise it would end paste mode early and let the tail execute
// as live terminal input (premature submit / command execution).
func TestInjectText_NeutralizesPasteCloseSequence(t *testing.T) {
	m := NewManager(nil)
	pr, pw := newPipeSession(t, m, "s1", "claude")
	defer pr.Close()

	if err := m.InjectText("s1", "before"+bpClose+"after", false); err != nil {
		t.Fatalf("InjectText: %v", err)
	}

	got := readAll(t, pr, pw)
	want := bpOpen + "beforeafter" + bpClose
	if got != want {
		t.Errorf("output = %q, want %q (embedded close sequence stripped)", got, want)
	}
}

// The copilot char-by-char path has no bracketed paste, so control characters
// would act as live keys (Tab = autocomplete, ESC = escape). They must be
// neutralized to spaces just like newlines, keeping the injected text literal.
func TestInjectText_CopilotNeutralizesControlChars(t *testing.T) {
	m := NewManager(nil)
	pr, pw := newPipeSession(t, m, "s1", "copilot")
	defer pr.Close()

	if err := m.InjectText("s1", "a\tb\x1bc", false); err != nil {
		t.Fatalf("InjectText: %v", err)
	}

	got := readAll(t, pr, pw)
	want := "\x1b[I" + "a b c"
	if got != want {
		t.Errorf("copilot output = %q, want %q (Tab/ESC neutralized to space)", got, want)
	}
}

func TestInjectText_UnknownSession(t *testing.T) {
	m := NewManager(nil)
	if err := m.InjectText("ghost", "x", false); err == nil {
		t.Error("expected error for unknown session")
	}
}
