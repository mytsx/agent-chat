package cli

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// mcpServerEntry is the config entry for agent-chat MCP server
type mcpServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Type    string            `json:"type,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// GetRoomsDir returns the fixed shared rooms directory.
// All teams use this as AGENT_CHAT_DIR; team name = room name for separation.
func GetRoomsDir(dataDir string) string {
	return filepath.Join(dataDir, "rooms")
}

// EnsureMCPConfig ensures the agent-chat MCP server is configured for the given CLI.
// Uses the locally installed MCP server from dataDir.
// roomName sets AGENT_CHAT_ROOM so the MCP server uses the correct team room.
func EnsureMCPConfig(cliType CLIType, dataDir, roomName string) error {
	if cliType == CLIShell {
		return nil
	}
	if cliType == CLICodex {
		return upsertCodexMCPConfig(dataDir, roomName)
	}

	configPath, err := getConfigPath(cliType)
	if err != nil {
		return err
	}

	entry := buildMCPEntry(dataDir, roomName)
	// Always force update: room may differ between terminals
	return upsertMCPConfig(configPath, entry, true)
}

// ResetMCPConfig deletes and recreates the MCP config entry with the current binary path.
// Called at startup to ensure all CLIs point to the Go binary (not stale Python venv).
// Also cleans up per-project MCP overrides that shadow the global config.
func ResetMCPConfig(cliType CLIType, dataDir string) error {
	if cliType == CLIShell {
		return nil
	}
	if cliType == CLICodex {
		return upsertCodexMCPConfig(dataDir, "")
	}

	configPath, err := getConfigPath(cliType)
	if err != nil {
		return err
	}

	entry := buildMCPEntry(dataDir, "")
	return upsertMCPConfig(configPath, entry, true)
}

func buildMCPEntry(dataDir, roomName string) mcpServerEntry {
	env := map[string]string{
		"AGENT_CHAT_DATA_DIR": dataDir,
	}
	if roomName != "" {
		env["AGENT_CHAT_ROOM"] = roomName
	}
	return mcpServerEntry{
		Command: GetMCPBinaryPath(dataDir),
		Args:    []string{},
		Type:    "stdio",
		Env:     env,
	}
}

func getConfigPath(cliType CLIType) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}

	switch cliType {
	case CLIClaude:
		return filepath.Join(home, ".claude.json"), nil
	case CLIGemini:
		return filepath.Join(home, ".gemini", "settings.json"), nil
	case CLICopilot:
		return filepath.Join(home, ".copilot", "mcp-config.json"), nil
	case CLICodex:
		return filepath.Join(home, ".codex", "config.toml"), nil
	default:
		return "", fmt.Errorf("unsupported CLI type: %s", cliType)
	}
}

// upsertCodexMCPConfig updates Codex MCP config using `codex mcp` subcommands.
// Codex uses TOML config; we delegate write/merge behavior to Codex CLI itself.
func upsertCodexMCPConfig(dataDir, roomName string) error {
	if _, err := exec.LookPath("codex"); err != nil {
		// Codex is not installed; nothing to configure.
		return nil
	}

	envs := []string{"AGENT_CHAT_DATA_DIR=" + dataDir}
	if roomName != "" {
		envs = append(envs, "AGENT_CHAT_ROOM="+roomName)
	}

	// Best-effort cleanup of previous entry.
	removeCmd := exec.Command("codex", "mcp", "remove", "agent-chat")
	if out, err := removeCmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		msgLower := strings.ToLower(msg)
		if msg != "" && !strings.Contains(msgLower, "not found") && !strings.Contains(msgLower, "no mcp server") {
			log.Printf("codex mcp remove warning: %s", msg)
		}
	}

	args := []string{"mcp", "add", "agent-chat"}
	for _, envKV := range envs {
		args = append(args, "--env", envKV)
	}
	args = append(args, "--", GetMCPBinaryPath(dataDir))

	addCmd := exec.Command("codex", args...)
	if out, err := addCmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("codex mcp add failed: %w: %s", err, msg)
		}
		return fmt.Errorf("codex mcp add failed: %w", err)
	}

	return nil
}

func upsertMCPConfig(configPath string, entry mcpServerEntry, forceUpdate bool) error {
	// Read existing config or start fresh
	config := make(map[string]any)
	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, &config); err != nil {
			backupPath := configPath + ".bak"
			os.WriteFile(backupPath, data, 0644)
			config = make(map[string]any)
		}
	}

	// --- 1. Update global mcpServers ---
	mcpServers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = make(map[string]any)
	}

	if !forceUpdate {
		if _, exists := mcpServers["agent-chat"]; exists {
			return nil
		}
	}

	// Delete old entry, write fresh
	delete(mcpServers, "agent-chat")
	mcpServers["agent-chat"] = entry
	config["mcpServers"] = mcpServers

	// --- 2. Clean per-project "agent-chat" overrides ---
	// Claude Code stores per-project MCP configs under projects[path].mcpServers.
	// Old agent-chat entries there shadow our global config, so remove them.
	cleanProjectMCPOverrides(config)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Write config
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(configPath, out, 0644)
}

// cleanProjectMCPOverrides removes "agent-chat" from all per-project mcpServers
// sections in the config. This prevents stale project-level overrides from
// shadowing the correct global MCP entry.
func cleanProjectMCPOverrides(config map[string]any) {
	projects, ok := config["projects"].(map[string]any)
	if !ok {
		return
	}

	for projectPath, projectData := range projects {
		project, ok := projectData.(map[string]any)
		if !ok {
			continue
		}
		projectMCP, ok := project["mcpServers"].(map[string]any)
		if !ok {
			continue
		}
		if _, exists := projectMCP["agent-chat"]; exists {
			delete(projectMCP, "agent-chat")
			project["mcpServers"] = projectMCP
			projects[projectPath] = project
			log.Printf("Cleaned stale agent-chat MCP override from project: %s", projectPath)
		}
	}
}
