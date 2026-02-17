package cli

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

const mcpServerVersion = "1.0.0"

var mcpSetupMu sync.Mutex

// EnsureMCPServer extracts the embedded MCP server and sets up a Python venv.
// Safe to call concurrently - uses a mutex and version check to avoid redundant work.
func EnsureMCPServer(mcpFS fs.FS, dataDir string) error {
	mcpSetupMu.Lock()
	defer mcpSetupMu.Unlock()

	pythonPath := GetMCPPythonPath(dataDir)
	versionFile := filepath.Join(dataDir, "mcp-server", ".version")

	// Quick check: if version matches and python exists, skip
	if v, err := os.ReadFile(versionFile); err == nil && string(v) == mcpServerVersion {
		if _, err := os.Stat(pythonPath); err == nil {
			return nil
		}
	}

	return setupMCPServer(mcpFS, dataDir)
}

func setupMCPServer(mcpFS fs.FS, dataDir string) error {
	serverDir := filepath.Join(dataDir, "mcp-server")
	venvDir := filepath.Join(serverDir, ".venv")
	versionFile := filepath.Join(serverDir, ".version")

	log.Println("Setting up MCP server...")

	// Extract embedded files
	if err := extractFS(mcpFS, serverDir); err != nil {
		return fmt.Errorf("extract mcp-server: %w", err)
	}

	// Find python3
	python3, err := findPython3()
	if err != nil {
		return err
	}

	// Create venv (if not exists)
	if _, err := os.Stat(filepath.Join(venvDir, "bin", "python")); os.IsNotExist(err) {
		log.Println("Creating Python venv...")
		cmd := exec.Command(python3, "-m", "venv", venvDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("create venv: %s: %w", string(out), err)
		}
	}

	// Install the package (includes mcp[cli] dependency)
	log.Println("Installing MCP server dependencies...")
	pipPath := filepath.Join(venvDir, "bin", "pip")
	cmd := exec.Command(pipPath, "install", "--upgrade", "--quiet", serverDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pip install: %s: %w", string(out), err)
	}

	// Write version file
	os.WriteFile(versionFile, []byte(mcpServerVersion), 0644)
	log.Println("MCP server setup complete")

	return nil
}

// extractFS extracts all files from an fs.FS to a destination directory.
func extractFS(srcFS fs.FS, destDir string) error {
	return fs.WalkDir(srcFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		destPath := filepath.Join(destDir, path)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		data, err := fs.ReadFile(srcFS, path)
		if err != nil {
			return err
		}

		return os.WriteFile(destPath, data, 0644)
	})
}

func findPython3() (string, error) {
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("python3 not found in PATH")
}

// GetMCPPythonPath returns the expected path to the venv python executable.
func GetMCPPythonPath(dataDir string) string {
	return filepath.Join(dataDir, "mcp-server", ".venv", "bin", "python")
}
