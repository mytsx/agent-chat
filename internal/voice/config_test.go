package voice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{OpenAIAPIKey: "sk-test-123"}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.OpenAIAPIKey != "sk-test-123" {
		t.Errorf("key = %q, want sk-test-123", got.OpenAIAPIKey)
	}
}

func TestLoadConfigMissingFileIsZeroNoError(t *testing.T) {
	got, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if got.OpenAIAPIKey != "" {
		t.Errorf("expected empty key, got %q", got.OpenAIAPIKey)
	}
}

func TestLoadConfigMalformedJSONIncludesPathContext(t *testing.T) {
	dir := t.TempDir()
	path := configPath(dir)
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("expected malformed JSON to return an error")
	}
	if msg := err.Error(); !strings.Contains(msg, "parse voice config") || !strings.Contains(msg, path) {
		t.Fatalf("expected parse error to include config path, got %v", err)
	}
}

func TestSaveConfigIsAtomicNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{OpenAIAPIKey: "sk-test-123"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("stray temp file: %s", e.Name())
		}
	}
}

func TestSaveConfigDataDirFileIncludesPathContext(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(dataDir, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}

	err := SaveConfig(dataDir, Config{OpenAIAPIKey: "sk-test-123"})
	if err == nil {
		t.Fatal("expected dataDir file to return an error")
	}
	if msg := err.Error(); !strings.Contains(msg, "create voice config dir") || !strings.Contains(msg, dataDir) {
		t.Fatalf("expected create-dir error to include dataDir, got %v", err)
	}
}

func TestSaveConfigRejectsEmbeddedWhitespaceKey(t *testing.T) {
	dir := t.TempDir()
	err := SaveConfig(dir, Config{OpenAIAPIKey: "sk-good\nsecond-line"})
	if err == nil {
		t.Fatal("expected embedded newline in API key to be rejected")
	}
	if msg := err.Error(); !strings.Contains(msg, "whitespace/control") {
		t.Fatalf("expected validation error to explain rejected characters, got %v", err)
	}
	if _, statErr := os.Stat(configPath(dir)); !os.IsNotExist(statErr) {
		t.Fatalf("invalid key must not write config file; stat error = %v", statErr)
	}
}
