package main

import (
	"os"
	"os/exec"
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
