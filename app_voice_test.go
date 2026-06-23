package main

import (
	"context"
	"testing"

	"desktop/internal/voice"
)

type fakeRecorder struct{ started, stopped bool }

func (f *fakeRecorder) Start(ctx context.Context) error { f.started = true; return nil }
func (f *fakeRecorder) Stop() ([]byte, error)           { f.stopped = true; return []byte("RIFFfake"), nil }

// newVoiceTestApp wires all voice seams to stubs: no ffmpeg, no network, no Wails
// runtime. ctx is Background so StopVoiceCapture's WithTimeout never derefs nil.
func newVoiceTestApp() *App {
	a := &App{}
	a.ctx = context.Background()
	a.newVoiceRecorder = func() (voice.Recorder, error) { return &fakeRecorder{}, nil }
	a.voiceTranscribe = func(ctx context.Context, wav []byte) (string, error) { return "stub", nil }
	a.voiceInject = func(sessionID, text string, submit bool) error { return nil }
	a.voiceEmit = func(event string, payload interface{}) {}
	return a
}

func TestStartVoiceCaptureGlobalLockRejectsSecond(t *testing.T) {
	a := newVoiceTestApp()
	if err := a.StartVoiceCapture("sess-A"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := a.StartVoiceCapture("sess-B"); err == nil {
		t.Fatal("second Start must be rejected while one capture is active (single mic)")
	}
}

func TestStopVoiceCaptureInjectsTranscriptNoSubmitToRightSession(t *testing.T) {
	a := newVoiceTestApp()
	a.voiceTranscribe = func(ctx context.Context, wav []byte) (string, error) { return "merhaba", nil }
	var gotSession, gotText string
	var gotSubmit bool
	var n int
	a.voiceInject = func(sessionID, text string, submit bool) error {
		n++
		gotSession, gotText, gotSubmit = sessionID, text, submit
		return nil
	}
	if err := a.StartVoiceCapture("sess-A"); err != nil {
		t.Fatal(err)
	}
	if err := a.StopVoiceCapture("sess-A"); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("inject called %d times, want 1", n)
	}
	if gotSession != "sess-A" {
		t.Errorf("injected into %q, want sess-A (panel isolation)", gotSession)
	}
	if gotText != "merhaba" {
		t.Errorf("text = %q, want merhaba", gotText)
	}
	if gotSubmit {
		t.Error("autosubmit must be false — user presses Enter")
	}
}

func TestStopVoiceCaptureWrongSessionIsNoop(t *testing.T) {
	a := newVoiceTestApp()
	var n int
	a.voiceInject = func(sessionID, text string, submit bool) error { n++; return nil }
	if err := a.StartVoiceCapture("sess-A"); err != nil {
		t.Fatal(err)
	}
	if err := a.StopVoiceCapture("sess-OTHER"); err != nil {
		t.Fatalf("wrong-session Stop should be a silent no-op, got %v", err)
	}
	if n != 0 {
		t.Errorf("inject called %d times for wrong session, want 0", n)
	}
	// Active capture for A must survive a wrong-session Stop, so a second Start is still rejected.
	if err := a.StartVoiceCapture("sess-B"); err == nil {
		t.Error("capture A should still be active after wrong-session Stop")
	}
}

func TestStopVoiceCaptureEmptyTranscriptDoesNotInject(t *testing.T) {
	a := newVoiceTestApp()
	a.voiceTranscribe = func(ctx context.Context, wav []byte) (string, error) { return "   ", nil }
	var n int
	a.voiceInject = func(sessionID, text string, submit bool) error { n++; return nil }
	if err := a.StartVoiceCapture("sess-A"); err != nil {
		t.Fatal(err)
	}
	if err := a.StopVoiceCapture("sess-A"); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("empty transcript must not inject; got %d", n)
	}
}
