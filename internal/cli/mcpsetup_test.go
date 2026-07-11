package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureMCPServerBinaryRestoresExecutableModeForMatchingBinary(t *testing.T) {
	dataDir := t.TempDir()
	binPath := filepath.Join(dataDir, "mcp-server-bin")
	binaryData := []byte("fake test binary")

	if err := os.WriteFile(binPath, binaryData, 0644); err != nil {
		t.Fatalf("write existing binary: %v", err)
	}

	if err := EnsureMCPServerBinary(binaryData, dataDir); err != nil {
		t.Fatalf("EnsureMCPServerBinary returned error: %v", err)
	}

	info, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("stat installed binary: %v", err)
	}
	if info.Mode().Perm()&0111 == 0 {
		t.Fatalf("expected installed binary to be executable, mode=%v", info.Mode().Perm())
	}
}

func TestEnsureMCPServerBinaryRestoresExecutableModeWhenRewritingExistingFile(t *testing.T) {
	dataDir := t.TempDir()
	binPath := filepath.Join(dataDir, "mcp-server-bin")

	if err := os.WriteFile(binPath, []byte("stale"), 0644); err != nil {
		t.Fatalf("write stale binary: %v", err)
	}

	if err := EnsureMCPServerBinary([]byte("new binary"), dataDir); err != nil {
		t.Fatalf("EnsureMCPServerBinary returned error: %v", err)
	}

	info, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("stat installed binary: %v", err)
	}
	if info.Mode().Perm()&0111 == 0 {
		t.Fatalf("expected rewritten binary to be executable, mode=%v", info.Mode().Perm())
	}
}
