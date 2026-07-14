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

// A secret-bearing CLI config (0600) with invalid JSON forces the backup path; the
// backup must inherit the original's restrictive mode, not a hardcoded world-readable
// 0644, or it would leak API keys/tokens.
func TestUpsertMCPConfigBackupPreservesMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(configPath, []byte("{not-valid-json"), 0600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := upsertMCPConfig(configPath, buildMCPEntry(dir, "team-a"), true); err != nil {
		t.Fatalf("upsertMCPConfig returned error: %v", err)
	}
	info, err := os.Stat(configPath + ".bak")
	if err != nil {
		t.Fatalf("backup file should exist: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("backup mode = %o, want 0600 (must not widen access to secrets)", perm)
	}
}
