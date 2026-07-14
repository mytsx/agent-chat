package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestHubShouldRestart locks down monitorHub's restart decision (#56 review / #60):
// only a non-shutdown, unsuccessful process exit warrants a restart. A shutdown in
// progress must suppress the restart (otherwise SIGTERM's signaled exit is mistaken
// for a crash and an orphaned hub is spawned), and a nil state (Wait error) or clean
// exit must not restart.
func TestHubShouldRestart(t *testing.T) {
	okCmd := exec.Command("true")
	if err := okCmd.Run(); err != nil {
		t.Fatalf("`true` çalıştırılamadı: %v", err)
	}
	okState := okCmd.ProcessState

	failCmd := exec.Command("false")
	_ = failCmd.Run() // non-zero exit beklenir
	failState := failCmd.ProcessState

	if !okState.Success() {
		t.Fatalf("`true` Success() true olmalı")
	}
	if failState.Success() {
		t.Fatalf("`false` Success() false olmalı")
	}

	tests := []struct {
		name         string
		shuttingDown bool
		state        *os.ProcessState
		want         bool
	}{
		{"çöküş → restart", false, failState, true},
		{"temiz çıkış → restart yok", false, okState, false},
		{"shutdown çöküşü bastırır", true, failState, false},
		{"shutdown + temiz çıkış", true, okState, false},
		{"nil state (wait hatası) → restart yok", false, nil, false},
		{"shutdown + nil state", true, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hubShouldRestart(tt.shuttingDown, tt.state); got != tt.want {
				t.Errorf("hubShouldRestart(%v, state) = %v, want %v", tt.shuttingDown, got, tt.want)
			}
		})
	}
}

func TestStartHubTimeoutCleansUpProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell and signal-0 process probe")
	}

	dataDir := t.TempDir()
	binPath := filepath.Join(dataDir, "mcp-server-bin")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho $$ > \"$AGENT_CHAT_DATA_DIR/fake-hub.pid\"\nexec sleep 30\n"), 0755); err != nil {
		t.Fatalf("fake hub binary yazılamadı: %v", err)
	}

	a := &App{dataDir: dataDir}
	err := a.startHub()
	if err == nil {
		t.Fatal("startHub succeeded unexpectedly without hub.port")
	}
	if !strings.Contains(err.Error(), "hub.port") {
		t.Fatalf("startHub error = %v, want hub.port timeout", err)
	}

	pidBytes, readErr := os.ReadFile(filepath.Join(dataDir, "fake-hub.pid"))
	if readErr != nil {
		t.Fatalf("fake hub pid okunamadı: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil {
		t.Fatalf("fake hub pid parse edilemedi: %v", parseErr)
	}
	proc, findErr := os.FindProcess(pid)
	if findErr != nil {
		t.Fatalf("fake hub process bulunamadı: %v", findErr)
	}
	t.Cleanup(func() {
		_ = proc.Kill()
		_, _ = proc.Wait()
	})
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		t.Fatalf("hub process pid=%d still alive after startHub timeout", pid)
	}
}
