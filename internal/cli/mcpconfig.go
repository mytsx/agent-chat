package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// mcpServerEntry is the config entry for agent-chat MCP server
type mcpServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
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

	configPath, err := getConfigPath(cliType)
	if err != nil {
		return err
	}

	entry := buildMCPEntry(dataDir, roomName)
	// Always force update: room may differ between terminals
	return upsertMCPConfig(configPath, entry, true)
}

// ResetMCPConfig forces re-writing the MCP config entry (for migration from uvx to local).
func ResetMCPConfig(cliType CLIType, dataDir string) error {
	if cliType == CLIShell {
		return nil
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
		"AGENT_CHAT_DIR": GetRoomsDir(dataDir),
	}
	if roomName != "" {
		env["AGENT_CHAT_ROOM"] = roomName
	}
	return mcpServerEntry{
		Command: GetMCPPythonPath(dataDir),
		Args:    []string{"-m", "agent_chat_mcp"},
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
	default:
		return "", fmt.Errorf("unsupported CLI type: %s", cliType)
	}
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

	// Get or create mcpServers section
	mcpServers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = make(map[string]any)
	}

	// Don't overwrite if already exists (unless force update)
	if _, exists := mcpServers["agent-chat"]; exists && !forceUpdate {
		return nil
	}

	// Add agent-chat entry
	mcpServers["agent-chat"] = entry
	config["mcpServers"] = mcpServers

	// Backup existing file before writing
	if _, err := os.Stat(configPath); err == nil {
		data, _ := os.ReadFile(configPath)
		if len(data) > 0 {
			os.WriteFile(configPath+".bak", data, 0644)
		}
	}

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
