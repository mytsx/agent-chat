package cli

import (
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
