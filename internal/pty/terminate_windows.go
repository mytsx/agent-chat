//go:build windows

package pty

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

func terminateCommandTree(cmd *exec.Cmd, grace time.Duration) error {
	if cmd == nil {
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	if cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
	}

	select {
	case <-done:
		return nil
	case <-time.After(grace):
	}

	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}

	select {
	case <-done:
		return nil
	case <-time.After(1500 * time.Millisecond):
		return fmt.Errorf("timed out waiting for process tree to exit")
	}
}
