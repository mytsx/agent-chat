package main

import (
	"context"
	"testing"

	"desktop/internal/voice"
)

type fakeRecorder struct{ wav []byte }

func (f *fakeRecorder) Start(ctx context.Context) error { return nil }
func (f *fakeRecorder) Stop() ([]byte, error) {
	if f.wav != nil {
		return f.wav, nil
	}
	return loudWAV(), nil
}

// loudWAV / silentWAV build minimal 16-bit mono WAVs (44-byte header + samples) so
// the no-speech energy gate can be exercised without real audio. loudWAV is well
// above the silence threshold; silentWAV is all zeros.
func loudWAV() []byte {
	b := make([]byte, 44+2000)
	for i := 44; i+1 < len(b); i += 2 {
		b[i], b[i+1] = 0x40, 0x40 // 0x4040 = 16448, ≈ -6 dBFS
	}
	return b
}
func silentWAV() []byte { return make([]byte, 44+2000) }

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

func TestStopVoiceCaptureSilentSkipsTranscribe(t *testing.T) {
	a := newVoiceTestApp()
	a.newVoiceRecorder = func() (voice.Recorder, error) { return &fakeRecorder{wav: silentWAV()}, nil }
	transcribed := false
	a.voiceTranscribe = func(ctx context.Context, wav []byte) (string, error) {
		transcribed = true
		return "uydurma metin", nil
	}
	var injectN int
	a.voiceInject = func(sessionID, text string, submit bool) error { injectN++; return nil }
	if err := a.StartVoiceCapture("s"); err != nil {
		t.Fatal(err)
	}
	if err := a.StopVoiceCapture("s"); err != nil {
		t.Fatal(err)
	}
	if transcribed {
		t.Error("silent capture must NOT be sent to transcription")
	}
	if injectN != 0 {
		t.Errorf("silent capture must not inject; got %d", injectN)
	}
}

func TestStopVoiceCaptureHallucinationSkips(t *testing.T) {
	a := newVoiceTestApp() // loud wav by default → passes the energy gate
	a.voiceTranscribe = func(ctx context.Context, wav []byte) (string, error) {
		return "Altyazı M.K.", nil
	}
	var injectN int
	a.voiceInject = func(sessionID, text string, submit bool) error { injectN++; return nil }
	if err := a.StartVoiceCapture("s"); err != nil {
		t.Fatal(err)
	}
	if err := a.StopVoiceCapture("s"); err != nil {
		t.Fatal(err)
	}
	if injectN != 0 {
		t.Errorf("hallucination transcript must not inject; got %d", injectN)
	}
}

func TestGetVoiceStatusMasksKey(t *testing.T) {
	dir := t.TempDir()
	if err := voice.SaveConfig(dir, voice.Config{OpenAIAPIKey: "sk-abcd1234wxyz"}); err != nil {
		t.Fatal(err)
	}
	a := &App{dataDir: dir}
	st, err := a.GetVoiceStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !st.HasKey {
		t.Error("HasKey should be true")
	}
	if st.KeyHint != "…wxyz" {
		t.Errorf("KeyHint = %q, want …wxyz", st.KeyHint)
	}
}

func TestGetVoiceStatusNoKey(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	st, err := a.GetVoiceStatus()
	if err != nil {
		t.Fatal(err)
	}
	if st.HasKey {
		t.Error("HasKey should be false with no key")
	}
	if st.KeyHint != "" {
		t.Errorf("KeyHint = %q, want empty", st.KeyHint)
	}
}

func TestSetVoiceConfigPersistsTrimmed(t *testing.T) {
	dir := t.TempDir()
	a := &App{dataDir: dir}
	if err := a.SetVoiceConfig("  sk-trim-me  "); err != nil {
		t.Fatal(err)
	}
	cfg, _ := voice.LoadConfig(dir)
	if cfg.OpenAIAPIKey != "sk-trim-me" {
		t.Errorf("key = %q, want sk-trim-me (trimmed)", cfg.OpenAIAPIKey)
	}
}
