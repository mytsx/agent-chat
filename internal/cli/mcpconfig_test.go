package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertMCPConfigReturnsReadErrors(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "settings.json")
	if err := upsertMCPConfig(configPath, buildMCPEntry(dir, "team-a"), true); err != nil {
		t.Fatalf("upsertMCPConfig missing config returned error: %v", err)
	}

	badConfigPath := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Mkdir(badConfigPath, 0755); err != nil {
		t.Fatalf("create directory at config path: %v", err)
	}

	err := upsertMCPConfig(badConfigPath, buildMCPEntry(dir, "team-a"), true)
	if err == nil {
		t.Fatal("expected read error for directory config path")
	}
	if !strings.Contains(err.Error(), "read config") {
		t.Fatalf("expected read config context, got %v", err)
	}
}

func TestUpsertMCPConfigPreservesExistingFileMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "settings.json")
	initialConfig := map[string]any{
		"mcpServers": map[string]any{},
	}
	data, err := json.Marshal(initialConfig)
	if err != nil {
		t.Fatalf("marshal initial config: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	if err := upsertMCPConfig(configPath, buildMCPEntry(dir, "team-a"), true); err != nil {
		t.Fatalf("upsertMCPConfig returned error: %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("expected existing config mode to remain 0600, got %v", got)
	}
}

func TestUpsertMCPConfigPreservesMalformedBackupFileMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":`), 0600); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}

	if err := upsertMCPConfig(configPath, buildMCPEntry(dir, "team-a"), true); err != nil {
		t.Fatalf("upsertMCPConfig returned error: %v", err)
	}

	info, err := os.Stat(configPath + ".bak")
	if err != nil {
		t.Fatalf("stat backup config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("expected malformed config backup mode to remain 0600, got %v", got)
	}
}
