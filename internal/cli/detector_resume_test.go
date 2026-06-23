package cli

import (
	"reflect"
	"testing"
)

func TestResumeSupported(t *testing.T) {
	tests := []struct {
		cliType CLIType
		want    bool
	}{
		{CLIClaude, true},
		{CLICopilot, true},
		{CLICodex, true},
		{CLIGemini, false},
		{CLIShell, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.cliType), func(t *testing.T) {
			if got := ResumeSupported(tt.cliType); got != tt.want {
				t.Errorf("ResumeSupported(%s) = %v, want %v", tt.cliType, got, tt.want)
			}
		})
	}
}

func TestGetCommandResume(t *testing.T) {
	const id = "7f32dcf3-11c6-4ca1-9461-fe8590e164e0"
	tests := []struct {
		name     string
		cliType  CLIType
		id       string
		wantCmd  string
		wantArgs []string
	}{
		{"claude flag", CLIClaude, id, "claude", []string{"--resume", id, "--dangerously-skip-permissions"}},
		{"copilot eq-syntax", CLICopilot, id, "copilot", []string{"--resume=" + id, "--yolo"}},
		{"codex subcommand-first", CLICodex, id, "codex", []string{"resume", id, "--dangerously-bypass-approvals-and-sandbox"}},
		// Unsupported CLI → fresh GetCommand (Gemini stays a normal launch this round).
		{"gemini falls back to fresh", CLIGemini, id, "gemini", []string{"--approval-mode", "yolo"}},
		// Empty id → fresh GetCommand even for a supported CLI.
		{"claude empty id falls back", CLIClaude, "", "claude", []string{"--dangerously-skip-permissions"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCmd, gotArgs := GetCommandResume(tt.cliType, tt.id)
			if gotCmd != tt.wantCmd {
				t.Errorf("cmd = %q, want %q", gotCmd, tt.wantCmd)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}
