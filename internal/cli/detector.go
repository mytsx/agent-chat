package cli

import (
	"os"
	"os/exec"
)

// CLIType represents a supported CLI tool
type CLIType string

const (
	CLIClaude  CLIType = "claude"
	CLIGemini  CLIType = "gemini"
	CLICopilot CLIType = "copilot"
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
	default:
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		return shell, []string{"-l"}
	}
}
