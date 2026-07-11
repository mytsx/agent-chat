package hub

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunFailsWhenHubPortCannotBeWritten(t *testing.T) {
	t.Parallel()

	dataDirParent := t.TempDir()
	dataDir := filepath.Join(dataDirParent, "not-a-directory")
	if err := os.WriteFile(dataDir, []byte("file blocks hub.port directory"), 0644); err != nil {
		t.Fatalf("create dataDir file: %v", err)
	}

	h := New(dataDir, "default", log.New(io.Discard, "", 0))
	err := h.Run(0)
	if err == nil {
		t.Fatal("Run() error = nil, want hub.port write failure")
	}
	if !strings.Contains(err.Error(), "write hub.port") {
		t.Fatalf("Run() error = %q, want hub.port write context", err.Error())
	}
	if got := h.Port(); got != 0 {
		t.Fatalf("Port() = %d after startup failure, want listener cleaned up", got)
	}
}
