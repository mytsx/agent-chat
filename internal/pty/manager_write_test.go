package pty

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestWriteAtomic_SerializesWithWrite verifies that a multi-segment injection
// performed via WriteAtomic is not interleaved by a concurrent Write (a user
// keystroke). Without the per-session write mutex the keystroke "X" would land
// between the two atomic segments ("AAAA" / "BBBB").
func TestWriteAtomic_SerializesWithWrite(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()

	m := NewManager(nil)
	sess := newSessionForTest(m, "s1")
	sess.PTY = pw

	midBlock := make(chan struct{})
	done := make(chan struct{})

	go func() {
		_ = m.WriteAtomic("s1", func(write func([]byte) error) error {
			if err := write([]byte("AAAA")); err != nil {
				return err
			}
			close(midBlock) // signal: we are now inside the atomic block
			time.Sleep(50 * time.Millisecond)
			return write([]byte("BBBB"))
		})
		close(done)
	}()

	<-midBlock
	// This must block until WriteAtomic releases the per-session write mutex.
	if err := m.Write("s1", []byte("X")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	<-done

	pw.Close()
	out, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(out), "AAAABBBB") {
		t.Errorf("atomic block was interleaved: got %q, want contiguous \"AAAABBBB\"", string(out))
	}
}

func TestWriteAtomic_UnknownSession(t *testing.T) {
	m := NewManager(nil)
	err := m.WriteAtomic("ghost", func(write func([]byte) error) error { return nil })
	if err == nil {
		t.Error("expected error for unknown session")
	}
}

// Close must not tear down the PTY while an injection is in flight: it has to
// wait for the per-session write mutex so the in-flight WriteAtomic finishes its
// full block first (otherwise the trailing bytes are lost to a closed fd).
func TestClose_WaitsForInFlightWriteAtomic(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()

	m := NewManager(nil)
	doneCh := make(chan struct{})
	close(doneCh) // Close()'s <-session.done returns immediately
	sess := &PTYSession{ID: "s1", PTY: pw, done: doneCh}
	m.mu.Lock()
	m.sessions["s1"] = sess
	m.mu.Unlock()

	midBlock := make(chan struct{})
	writeDone := make(chan struct{})
	go func() {
		_ = m.WriteAtomic("s1", func(write func([]byte) error) error {
			if err := write([]byte("AAAA")); err != nil {
				return err
			}
			close(midBlock)
			time.Sleep(50 * time.Millisecond)
			return write([]byte("BBBB"))
		})
		close(writeDone)
	}()

	<-midBlock
	_ = m.Close("s1")
	<-writeDone

	pw.Close()
	out, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(out), "AAAABBBB") {
		t.Errorf("Close tore down PTY mid-injection: got %q, want contiguous \"AAAABBBB\"", string(out))
	}
}
