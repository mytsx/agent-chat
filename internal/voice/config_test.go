package voice

import (
	"os"
	"path/filepath"
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

func TestSaveConfigIsAtomicNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{OpenAIAPIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("stray temp file: %s", e.Name())
		}
	}
}
