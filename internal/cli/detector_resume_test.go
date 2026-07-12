package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestNVMNodeVersionDirsPreferSemanticNewest(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".nvm", "versions", "node")
	for _, version := range []string{"v9.11.2", "v20.1.0", "v18.19.1"} {
		if err := os.MkdirAll(filepath.Join(base, version, "bin"), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", version, err)
		}
	}

	got := nvmNodeVersionDirs(home)
	want := []string{
		filepath.Join(base, "v20.1.0", "bin"),
		filepath.Join(base, "v18.19.1", "bin"),
		filepath.Join(base, "v9.11.2", "bin"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nvm dirs = %v, want %v", got, want)
	}
}

func TestEnsureFullPATHWithEmptyPATHDoesNotAddCurrentDirectory(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0755); err != nil {
		t.Fatalf("mkdir local bin: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")

	ensureFullPATH()

	got := os.Getenv("PATH")
	if got == "" {
		t.Fatal("expected PATH to include discovered user bin directory")
	}
	pathSeparator := string(os.PathListSeparator)
	if strings.HasPrefix(got, pathSeparator) || strings.HasSuffix(got, pathSeparator) {
		t.Fatalf("PATH must not contain leading/trailing empty entries that resolve to the current directory: %q", got)
	}
	for _, entry := range filepath.SplitList(got) {
		if entry == "" {
			t.Fatalf("PATH must not contain empty entries that resolve to the current directory: %q", got)
		}
	}
	if !strings.Contains(got, localBin) {
		t.Fatalf("PATH = %q, want it to include %q", got, localBin)
	}
}

func TestEnsureFullPATHRemovesEmptyAndDuplicateEntriesFromExistingPATH(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0755); err != nil {
		t.Fatalf("mkdir local bin: %v", err)
	}
	first := filepath.Join(home, "first-bin")
	second := filepath.Join(home, "second-bin")
	pathSeparator := string(os.PathListSeparator)
	t.Setenv("HOME", home)
	t.Setenv("PATH", strings.Join([]string{first, "", second, first, ""}, pathSeparator))

	ensureFullPATH()

	got := filepath.SplitList(os.Getenv("PATH"))
	wantPrefix := []string{first, second}
	if len(got) < len(wantPrefix) || !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("PATH entries = %v, want prefix %v", got, wantPrefix)
	}
	seen := make(map[string]bool, len(got))
	for _, entry := range got {
		if entry == "" {
			t.Fatalf("PATH must not contain empty entries that resolve to the current directory: %q", os.Getenv("PATH"))
		}
		if seen[entry] {
			t.Fatalf("PATH must not contain duplicate entry %q: %q", entry, os.Getenv("PATH"))
		}
		seen[entry] = true
	}
	if !seen[localBin] {
		t.Fatalf("PATH entries = %v, want discovered user bin %q", got, localBin)
	}
}

func TestResolveUserShellFallsBackWhenSHELLIsInvalid(t *testing.T) {
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "missing-shell"))

	got := resolveUserShell()
	if got == "" {
		t.Fatal("resolveUserShell returned empty shell")
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("resolveUserShell returned non-existent shell %q: %v", got, err)
	}
	if got == os.Getenv("SHELL") {
		t.Fatalf("resolveUserShell returned invalid SHELL %q", got)
	}
}

func TestGetCommandShellUsesResolvedFallback(t *testing.T) {
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "missing-shell"))

	cmd, args := GetCommand(CLIShell)
	if cmd == os.Getenv("SHELL") {
		t.Fatalf("GetCommand(CLIShell) returned invalid SHELL %q", cmd)
	}
	if _, err := os.Stat(cmd); err != nil {
		t.Fatalf("GetCommand(CLIShell) returned non-existent shell %q: %v", cmd, err)
	}
	if !reflect.DeepEqual(args, []string{"-l"}) {
		t.Fatalf("args = %v, want [-l]", args)
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
