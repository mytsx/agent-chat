package pty

import "testing"

func TestGracefulExitCommand(t *testing.T) {
	cases := []struct {
		cliType string
		want    string
	}{
		{cliType: "claude", want: "/exit\r"},
		{cliType: "gemini", want: "/quit\r"},
		{cliType: "copilot", want: "/exit\r"},
		{cliType: "codex", want: "/exit\r"},
		{cliType: "shell", want: "exit\r"},
		{cliType: "unknown", want: ""},
		{cliType: "  CODEX  ", want: "/exit\r"},
	}

	for _, tc := range cases {
		if got := gracefulExitCommand(tc.cliType); got != tc.want {
			t.Fatalf("gracefulExitCommand(%q) = %q, want %q", tc.cliType, got, tc.want)
		}
	}
}

func TestResizeRejectsZeroDimensionBeforeSyscall(t *testing.T) {
	m := NewManager(nil)
	newSessionForTest(m, "s1")

	if err := m.Resize("s1", 0, 24); err == nil {
		t.Fatal("expected zero columns to be rejected")
	}
	if err := m.Resize("s1", 80, 0); err == nil {
		t.Fatal("expected zero rows to be rejected")
	}
}
