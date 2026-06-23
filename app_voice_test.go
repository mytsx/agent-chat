package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"desktop/internal/voice"
)

type fakeRecorder struct {
	wav     []byte
	stopped bool
}

func (f *fakeRecorder) Start(ctx context.Context) error { return nil }
func (f *fakeRecorder) Stop() ([]byte, error) {
	f.stopped = true
	if f.wav != nil {
		return f.wav, nil
	}
	return loudWAV(), nil
}

// blockingRecorder's Stop blocks until release is closed, signalling on entered —
// used to prove the mic lock is held across the ffmpeg-finalize window (Codex P2 #4).
type blockingRecorder struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingRecorder) Start(ctx context.Context) error { return nil }
func (b *blockingRecorder) Stop() ([]byte, error) {
	close(b.entered)
	<-b.release
	return loudWAV(), nil
}

// wavMain builds a valid 16-bit/16kHz mono WAV (real RIFF/WAVE/fmt/data chunks) so
// the no-speech energy gate's chunk parser measures it correctly. loudWAV is well
// above the silence threshold; silentWAV is all zeros.
func wavMain(samples int, amp int16) []byte {
	data := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(amp))
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(4+24+8+len(data)))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint32(16000))
	binary.Write(&b, binary.LittleEndian, uint32(32000))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}
func loudWAV() []byte   { return wavMain(2000, 8000) }
func silentWAV() []byte { return wavMain(2000, 0) }

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

func TestStartVoiceCaptureRejectedWhileSessionTranscribing(t *testing.T) {
	a := newVoiceTestApp()
	entered := make(chan struct{})
	release := make(chan struct{})
	a.voiceTranscribe = func(ctx context.Context, wav []byte) (string, error) {
		close(entered)
		<-release
		return "merhaba", nil
	}
	a.voiceInject = func(sessionID, text string, submit bool) error { return nil }
	if err := a.StartVoiceCapture("A"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = a.StopVoiceCapture("A"); close(done) }()
	<-entered // session A is now transcribing (its recorder already stopped/cleared)

	// Same session must be rejected while its transcription is still pending — even
	// though the mic lock (activeRecorder) was released after ffmpeg exited.
	if err := a.StartVoiceCapture("A"); err == nil {
		t.Error("same-session Start must be rejected while its transcription is pending")
	}
	// A different session may still record — the mic is free during transcription.
	if err := a.StartVoiceCapture("B"); err != nil {
		t.Errorf("a different session should record during another's transcription: %v", err)
	}
	a.stopActiveVoice() // clear B's recorder without driving it through transcription

	close(release)
	<-done
	// After A's transcription resolves, the same session can record again.
	if err := a.StartVoiceCapture("A"); err != nil {
		t.Errorf("session A should record again once its transcription finished: %v", err)
	}
}

func TestStopActiveVoiceStopsRecorder(t *testing.T) {
	a := newVoiceTestApp()
	fr := &fakeRecorder{}
	a.newVoiceRecorder = func() (voice.Recorder, error) { return fr, nil }
	if err := a.StartVoiceCapture("A"); err != nil {
		t.Fatal(err)
	}
	a.stopActiveVoice()
	if !fr.stopped {
		t.Error("stopActiveVoice must stop the active recorder (no orphaned ffmpeg)")
	}
	if a.activeRecorder != nil {
		t.Error("activeRecorder must be cleared")
	}
	a.stopActiveVoice() // idempotent — must not panic when nothing is recording
}

func TestStopVoiceCaptureHoldsLockUntilFFmpegExits(t *testing.T) {
	a := newVoiceTestApp()
	br := &blockingRecorder{entered: make(chan struct{}), release: make(chan struct{})}
	first := true
	a.newVoiceRecorder = func() (voice.Recorder, error) {
		if first {
			first = false
			return br, nil
		}
		return &fakeRecorder{}, nil
	}
	a.voiceInject = func(sessionID, text string, submit bool) error { return nil }
	if err := a.StartVoiceCapture("A"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = a.StopVoiceCapture("A"); close(done) }()
	<-br.entered // ffmpeg (Stop) is now finalizing — mic still owned
	if err := a.StartVoiceCapture("B"); err == nil {
		t.Error("Start must be rejected while previous capture's ffmpeg is still finalizing")
	}
	close(br.release) // let Stop() return → lock released
	<-done
	if err := a.StartVoiceCapture("C"); err != nil {
		t.Errorf("Start should succeed once ffmpeg has exited: %v", err)
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
