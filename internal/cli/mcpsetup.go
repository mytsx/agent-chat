package cli

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

var mcpSetupMu sync.Mutex

// EnsureMCPServerBinary extracts the embedded MCP server binary to disk.
// Safe to call concurrently - uses a mutex and content check to avoid redundant work.
func EnsureMCPServerBinary(binaryData []byte, dataDir string) error {
	mcpSetupMu.Lock()
	defer mcpSetupMu.Unlock()

	binPath := GetMCPBinaryPath(dataDir)

	// Quick check: if file exists and content matches, just ensure signing
	if existing, err := os.ReadFile(binPath); err == nil {
		if bytes.Equal(existing, binaryData) {
			if runtime.GOOS == "darwin" {
				ensureSigned(binPath)
			}
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

	// On macOS, clear quarantine/provenance attributes and re-sign the binary.
	// When extracted from a .app bundle, the binary inherits com.apple.provenance
	// which causes Gatekeeper to silently block execution by child processes.
	if runtime.GOOS == "darwin" {
		clearMacOSQuarantine(binPath)
	}

	log.Println("MCP server binary installed")
	return nil
}

// clearMacOSQuarantine removes quarantine/provenance xattrs and ad-hoc signs the binary
// so that macOS Gatekeeper allows child processes (like Claude Code) to spawn it.
func clearMacOSQuarantine(binPath string) {
	// Remove quarantine and provenance attributes
	for _, attr := range []string{"com.apple.quarantine", "com.apple.provenance"} {
		exec.Command("xattr", "-d", attr, binPath).Run()
	}
	// Ad-hoc codesign so Gatekeeper accepts it
	if err := exec.Command("codesign", "--force", "--sign", "-", binPath).Run(); err != nil {
		log.Printf("codesign warning: %v", err)
	}
}

// ensureSigned checks if the binary passes Gatekeeper and signs it if not.
func ensureSigned(binPath string) {
	if exec.Command("spctl", "--assess", "--type", "execute", binPath).Run() != nil {
		clearMacOSQuarantine(binPath)
	}
}

// GetMCPBinaryPath returns the path to the MCP server binary.
func GetMCPBinaryPath(dataDir string) string {
	return filepath.Join(dataDir, "mcp-server-bin")
}
