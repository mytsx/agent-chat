package cli

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"sync"
)

var mcpSetupMu sync.Mutex

// EnsureMCPServerBinary extracts the embedded MCP server binary to disk.
// Safe to call concurrently - uses a mutex and content check to avoid redundant work.
func EnsureMCPServerBinary(binaryData []byte, dataDir string) error {
	mcpSetupMu.Lock()
	defer mcpSetupMu.Unlock()

	binPath := GetMCPBinaryPath(dataDir)

	// Quick check: if file exists and content matches, skip
	if existing, err := os.ReadFile(binPath); err == nil {
		if bytes.Equal(existing, binaryData) {
			return nil
		}
	}

	log.Println("Installing MCP server binary...")

	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		return err
	}

	if err := os.WriteFile(binPath, binaryData, 0755); err != nil {
		return err
	}

	log.Println("MCP server binary installed")
	return nil
}

// GetMCPBinaryPath returns the path to the MCP server binary.
func GetMCPBinaryPath(dataDir string) string {
	return filepath.Join(dataDir, "mcp-server-bin")
}
