package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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

	// Also try to resolve nvm-installed Node versions. os.ReadDir is lexical, so
	// picking the last entry chooses v9 over v20 and can make Finder-launched CLI
	// terminals miss modern node-based tools. Add semantic-newest bins first so PATH
	// resolution sees the newest installed version.
	if os.Getenv("NVM_DIR") == "" && home != "" {
		extraDirs = append(extraDirs, nvmNodeVersionDirs(home)...)
	}

	currentPATH := os.Getenv("PATH")
	pathSeparator := string(os.PathListSeparator)
	pathEntries, pathChanged := cleanPATHEntries(currentPATH)
	pathSet := make(map[string]bool, len(pathEntries))
	for _, p := range pathEntries {
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

	if len(toAdd) > 0 || pathChanged {
		newPATH := strings.Join(append(pathEntries, toAdd...), pathSeparator)
		os.Setenv("PATH", newPATH)
	}
}

func cleanPATHEntries(pathValue string) ([]string, bool) {
	entries := filepath.SplitList(pathValue)
	cleaned := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	changed := false
	for _, entry := range entries {
		if entry == "" || !filepath.IsAbs(entry) || seen[entry] {
			changed = true
			continue
		}
		seen[entry] = true
		cleaned = append(cleaned, entry)
	}
	return cleaned, changed
}

func nvmNodeVersionDirs(home string) []string {
	base := filepath.Join(home, ".nvm", "versions", "node")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	type versionDir struct {
		name  string
		parts []int
	}
	var versions []versionDir
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "v") {
			continue
		}
		binDir := filepath.Join(base, entry.Name(), "bin")
		if fi, err := os.Stat(binDir); err != nil || !fi.IsDir() {
			continue
		}
		versions = append(versions, versionDir{name: entry.Name(), parts: parseNodeVersion(entry.Name())})
	}
	sort.SliceStable(versions, func(i, j int) bool {
		for p := 0; p < len(versions[i].parts) || p < len(versions[j].parts); p++ {
			iv, jv := 0, 0
			if p < len(versions[i].parts) {
				iv = versions[i].parts[p]
			}
			if p < len(versions[j].parts) {
				jv = versions[j].parts[p]
			}
			if iv != jv {
				return iv > jv
			}
		}
		return versions[i].name > versions[j].name
	})
	dirs := make([]string, 0, len(versions))
	for _, v := range versions {
		dirs = append(dirs, filepath.Join(base, v.name, "bin"))
	}
	return dirs
}

func parseNodeVersion(name string) []int {
	name = strings.TrimPrefix(name, "v")
	fields := strings.Split(name, ".")
	parts := make([]int, len(fields))
	for i, field := range fields {
		// Be tolerant of suffixes like v20.11.1-nightly while preserving useful
		// numeric ordering for normal nvm directory names.
		field = strings.TrimLeftFunc(field, func(r rune) bool { return r < '0' || r > '9' })
		field = strings.TrimRightFunc(field, func(r rune) bool { return r < '0' || r > '9' })
		if n, err := strconv.Atoi(field); err == nil {
			parts[i] = n
		}
	}
	return parts
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
	shell := resolveUserShell()
	result = append(result, CLIInfo{
		Type:       CLIShell,
		Name:       "Shell",
		Binary:     shell,
		Available:  true,
		BinaryPath: shell,
	})
	return result
}

func resolveUserShell() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		if path, err := exec.LookPath(shell); err == nil {
			return path
		}
	}
	for _, fallback := range []string{"/bin/zsh", "/bin/sh"} {
		if path, err := exec.LookPath(fallback); err == nil {
			return path
		}
	}
	return "/bin/sh"
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
		return resolveUserShell(), []string{"-l"}
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
