package cli

import (
	"os"
	"os/exec"
	"strings"
)

func init() {
	ensureFullPATH()
}

// ensureFullPATH adds common binary directories to PATH so that
// CLI tools are discoverable when the app is launched from Finder/Launchpad
// (macOS GUI apps inherit a minimal PATH: /usr/bin:/bin:/usr/sbin:/sbin)
func ensureFullPATH() {
	home, _ := os.UserHomeDir()
	extraDirs := []string{
		"/usr/local/bin",
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		home + "/.local/bin",
		home + "/.npm-global/bin",
		home + "/.nvm/versions/node/default/bin",
		home + "/.volta/bin",
		home + "/.cargo/bin",
	}

	// Also try to resolve nvm current node path
	if nvmDir := os.Getenv("NVM_DIR"); nvmDir == "" && home != "" {
		entries, _ := os.ReadDir(home + "/.nvm/versions/node")
		if len(entries) > 0 {
			// Pick the last entry (usually highest version)
			last := entries[len(entries)-1]
			extraDirs = append(extraDirs, home+"/.nvm/versions/node/"+last.Name()+"/bin")
		}
	}

	currentPATH := os.Getenv("PATH")
	pathSet := make(map[string]bool)
	for _, p := range strings.Split(currentPATH, ":") {
		pathSet[p] = true
	}

	var toAdd []string
	for _, d := range extraDirs {
		if d == "" {
			continue
		}
		if !pathSet[d] {
			if _, err := os.Stat(d); err == nil {
				toAdd = append(toAdd, d)
			}
		}
	}

	if len(toAdd) > 0 {
		newPATH := currentPATH + ":" + strings.Join(toAdd, ":")
		os.Setenv("PATH", newPATH)
	}
}

// CLIType represents a supported CLI tool
type CLIType string

const (
	CLIClaude  CLIType = "claude"
	CLIGemini  CLIType = "gemini"
	CLICopilot CLIType = "copilot"
	CLICodex   CLIType = "codex"
	CLIShell   CLIType = "shell"
)

// CLIInfo contains information about a detected CLI
type CLIInfo struct {
	Type       CLIType `json:"type"`
	Name       string  `json:"name"`
	Binary     string  `json:"binary"`
	Available  bool    `json:"available"`
	BinaryPath string  `json:"binary_path"`
}

var knownCLIs = []struct {
	cliType CLIType
	name    string
	binary  string
}{
	{CLIClaude, "Claude Code", "claude"},
	{CLIGemini, "Gemini CLI", "gemini"},
	{CLICopilot, "GitHub Copilot", "copilot"},
	{CLICodex, "Codex CLI", "codex"},
}

// DetectAll checks which AI CLIs are available on the system
func DetectAll() []CLIInfo {
	var result []CLIInfo
	for _, k := range knownCLIs {
		info := CLIInfo{
			Type:   k.cliType,
			Name:   k.name,
			Binary: k.binary,
		}
		if path, err := exec.LookPath(k.binary); err == nil {
			info.Available = true
			info.BinaryPath = path
		}
		result = append(result, info)
	}
	// Always add shell
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	result = append(result, CLIInfo{
		Type:       CLIShell,
		Name:       "Shell",
		Binary:     shell,
		Available:  true,
		BinaryPath: shell,
	})
	return result
}

// GetCommand returns the command and args to start a CLI
func GetCommand(cliType CLIType) (string, []string) {
	switch cliType {
	case CLIClaude:
		return "claude", []string{"--dangerously-skip-permissions"}
	case CLIGemini:
		return "gemini", []string{"--approval-mode", "yolo"}
	case CLICopilot:
		return "copilot", []string{"--yolo"}
	case CLICodex:
		return "codex", []string{"--dangerously-bypass-approvals-and-sandbox"}
	default:
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		return shell, []string{"-l"}
	}
}

// ResumeSupported reports whether GetCommandResume can build a native resume
// invocation for cliType. Claude/Copilot/Codex are supported (resume-by-id
// empirically verified, 2026-06-24); Gemini/shell are not this round (#40).
func ResumeSupported(cliType CLIType) bool {
	switch cliType {
	case CLIClaude, CLICopilot, CLICodex:
		return true
	default:
		return false
	}
}

// GetCommandResume returns the command and args to resume cliType from sessionID.
// Codex's `resume` is a SUBCOMMAND (positional, first); the others use a flag
// (Copilot needs the `=` form). An unsupported cliType or empty sessionID falls
// back to a fresh GetCommand so callers never accidentally launch a broken
// resume (#40).
func GetCommandResume(cliType CLIType, sessionID string) (string, []string) {
	if sessionID == "" || !ResumeSupported(cliType) {
		return GetCommand(cliType)
	}
	switch cliType {
	case CLIClaude:
		return "claude", []string{"--resume", sessionID, "--dangerously-skip-permissions"}
	case CLICopilot:
		return "copilot", []string{"--resume=" + sessionID, "--yolo"}
	case CLICodex:
		return "codex", []string{"resume", sessionID, "--dangerously-bypass-approvals-and-sandbox"}
	default:
		return GetCommand(cliType)
	}
}
