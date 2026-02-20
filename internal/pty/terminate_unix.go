//go:build !windows

package pty

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// terminateCommandTree shuts down the session process group.
// PTY-launched commands run in their own session/process group on Unix,
// so killing -PID is scoped to this terminal only.
func terminateCommandTree(cmd *exec.Cmd, grace time.Duration) error {
	if cmd == nil {
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	if p := cmd.Process; p != nil && p.Pid > 0 {
		if err := syscall.Kill(-p.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			_ = p.Signal(syscall.SIGTERM)
		}
	}

	select {
	case <-done:
		return nil
	case <-time.After(grace):
	}

	if p := cmd.Process; p != nil && p.Pid > 0 {
		if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			_ = p.Kill()
		}
	}

	select {
	case <-done:
		return nil
	case <-time.After(1500 * time.Millisecond):
		return fmt.Errorf("timed out waiting for process tree to exit")
	}
}
