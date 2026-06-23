# Sesli Prompt / Mikrofon (STT) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Her terminal/agent paneline push-to-talk mikrofon butonu ekleyip Türkçe konuşmayı Whisper API ile metne çevirip o panelin PTY input satırına autosubmit olmadan yazmak.

**Architecture:** Frontend push-to-talk → `App.StartVoiceCapture/StopVoiceCapture` (global tek-kayıt kilidi) → `ffmpeg -f avfoundation` subprocess 16kHz mono WAV yakalar → `internal/voice` `Transcriber` (OpenAI Whisper, `language=tr`) → `pty.InjectText(sessionID, transcript, submit=false)` (mevcut, hazır) → `voice:state`/`voice:transcript` event'leri frontend'e. Ayarlar paneli OpenAI key'i `~/.agent-chat/voice.json`'a yazar.

**Tech Stack:** Go (saf, CGO yok) · Wails v2.12.0 · React 18 + TS + Vite · ffmpeg (dış runtime) · OpenAI Whisper API · xterm.js.

## Global Constraints

- **CGO YOK.** Yeni Go kodu saf Go olmalı; MCP server `GOOS/GOARCH go build` cross-compile'ı korunur (`malgo`/whisper.cpp gibi C bağımlılığı eklenmez). `os/exec` ile ffmpeg subprocess uygundur.
- **Go modül adı: `desktop`** (URL tabanlı değil). Yeni paket: `desktop/internal/voice`.
- **Autosubmit YOK:** transcript `InjectText(..., submit=false)` ile yazılır; `\r` gönderilmez. Kullanıcı kendi Enter'lar.
- **Dil sabit `tr`:** Whisper isteğinde `language=tr` her zaman gönderilir (UI'da dil seçimi yok).
- **Global tek-kayıt kilidi:** aynı anda yalnız bir panel kayıt yapar (tek mikrofon donanımı).
- **Panel izolasyonu:** transcript yalnızca konuşulan panelin `sessionID`'sine enjekte edilir (MEMORY "yanlış panel" bug sınıfı — testle korunur).
- **Gerçek API key frontend'e asla gönderilmez:** Settings yalnız `hasKey`/`keyHint`(son 4 hane)/`ffmpegFound` görür.
- **Config atomik yazılır** (temp + rename), key dosyası `0600`. `internal/team/store.go:save()` deseni.
- **Agent/kullanıcıya dönük metinler Türkçe + emoji.**
- **Embed kısıtı:** root paket (`package main`) `go test` öncesi `build/mcp-server-bin` var olmalı → gerekirse `make mcp-server`.

## Dosya Yapısı

| Dosya | Sorumluluk |
|---|---|
| `internal/voice/config.go` (YENİ) | `Config{OpenAIAPIKey}` + atomik `LoadConfig`/`SaveConfig` (`~/.agent-chat/voice.json`). |
| `internal/voice/transcriber.go` (YENİ) | `Transcriber` arayüzü (mock'lanabilir STT seam). |
| `internal/voice/whisper_api.go` (YENİ) | `WhisperClient` → OpenAI `/v1/audio/transcriptions`, `model=whisper-1`, `language=tr`. |
| `internal/voice/capture.go` (YENİ) | `Recorder` arayüzü + `ffmpegRecorder` (avfoundation) + `FFmpegAvailable` + `ErrFFmpegNotFound`. |
| `internal/voice/*_test.go` (YENİ) | config round-trip/atomik · whisper multipart(`httptest`) · ffmpegArgs/err. |
| `app.go` (DEĞİŞİKLİK) | Voice seam alanları + `StartVoiceCapture`/`StopVoiceCapture` + `GetVoiceStatus`/`SetVoiceConfig` + `emitVoiceState`. startup() default seam'leri kurar. |
| `app_voice_test.go` (YENİ) | global kilit · sessionID izolasyonu · autosubmit=false · boş transcript · Settings maskeleme. |
| `build/darwin/Info.plist` + `Info.dev.plist` (DEĞİŞİKLİK) | `NSMicrophoneUsageDescription`. |
| `build/darwin/entitlements.plist` (DEĞİŞİKLİK) | `com.apple.security.device.audio-input`. |
| `frontend/src/lib/types.ts` (DEĞİŞİKLİK) | `VoiceStatus`, `VoiceState`, event payload tipleri. |
| `frontend/src/components/TerminalPane.tsx` (DEĞİŞİKLİK) | Mikrofon butonu + push-to-talk + `voice:state`/`voice:transcript` dinleyicileri. |
| `frontend/src/components/SettingsModal.tsx` (YENİ) | OpenAI key girişi (maskeli durum). |
| `frontend/src/App.tsx` (DEĞİŞİKLİK) | ⚙️ Ayarlar butonu + `SettingsModal` mount. |
| `frontend/src/styles/globals.css` (DEĞİŞİKLİK) | recording pulse animasyonu + mic buton stilleri. |

---

### Task 1: Faz 0 — Native mikrofon izni (Info.plist ×2 + entitlements)

**Files:**
- Modify: `build/darwin/Info.plist` (top-level `<dict>`, `NSHighResolutionCapable`'dan sonra)
- Modify: `build/darwin/Info.dev.plist` (aynı yer)
- Modify: `build/darwin/entitlements.plist` (top-level `<dict>` içine)

**Interfaces:**
- Consumes: yok.
- Produces: imzalı/dev build'de mikrofon TCC izin istemi için native önkoşul (sonraki fazların native doğrulaması bunu kullanır).

- [ ] **Step 1: Info.plist'e mikrofon açıklaması ekle**

`build/darwin/Info.plist` içinde bu satırlardan hemen sonra:
```xml
        <key>NSHighResolutionCapable</key>
        <string>true</string>
```
şunu ekle:
```xml
        <key>NSMicrophoneUsageDescription</key>
        <string>Agent Chat, sesli prompt özelliği için mikrofona erişir; konuşmanız metne çevrilip seçili agent paneline yazılır.</string>
```

- [ ] **Step 2: Info.dev.plist'e aynı anahtarı ekle**

`build/darwin/Info.dev.plist` dosyasında aynı `NSHighResolutionCapable` bloğundan sonra Step 1'deki `NSMicrophoneUsageDescription` anahtarının aynısını ekle (dev build'de izin isteminin çıkması için).

- [ ] **Step 3: entitlements.plist'e audio-input entitlement'ı ekle**

`build/darwin/entitlements.plist` içinde `<dict>` kapanışından (`</dict>`) önce şunu ekle:
```xml
        <key>com.apple.security.device.audio-input</key>
        <true/>
```

- [ ] **Step 4: Plist'lerin geçerli XML olduğunu doğrula**

Run:
```bash
plutil -lint build/darwin/Info.plist build/darwin/Info.dev.plist build/darwin/entitlements.plist
```
Expected: her üç dosya için `OK`.

- [ ] **Step 5: Commit**

```bash
git add build/darwin/Info.plist build/darwin/Info.dev.plist build/darwin/entitlements.plist
git commit -m "feat(#16): Faz 0 — macOS mikrofon izni (Info.plist + entitlements)"
```

---

### Task 2: `internal/voice` config (voice.json load/save)

**Files:**
- Create: `internal/voice/config.go`
- Test: `internal/voice/config_test.go`

**Interfaces:**
- Consumes: yok.
- Produces: `voice.Config{OpenAIAPIKey string}`; `voice.LoadConfig(dataDir string) (Config, error)` (eksik dosya → boş Config, hata değil); `voice.SaveConfig(dataDir string, c Config) error` (atomik, `0600`).

- [ ] **Step 1: Failing test yaz**

Create `internal/voice/config_test.go`:
```go
package voice

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{OpenAIAPIKey: "sk-test-123"}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.OpenAIAPIKey != "sk-test-123" {
		t.Errorf("key = %q, want sk-test-123", got.OpenAIAPIKey)
	}
}

func TestLoadConfigMissingFileIsZeroNoError(t *testing.T) {
	got, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if got.OpenAIAPIKey != "" {
		t.Errorf("expected empty key, got %q", got.OpenAIAPIKey)
	}
}

func TestSaveConfigIsAtomicNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{OpenAIAPIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("stray temp file: %s", e.Name())
		}
	}
}
```

- [ ] **Step 2: Test'in fail ettiğini doğrula**

Run: `go test ./internal/voice/ -run TestSaveLoadRoundTrip -v`
Expected: FAIL — `undefined: SaveConfig` / `undefined: Config` (paket henüz yok).

- [ ] **Step 3: config.go'yu yaz**

Create `internal/voice/config.go`:
```go
// Package voice implements microphone capture and speech-to-text for the voice
// prompt feature (#16): an ffmpeg-based Recorder, an OpenAI Whisper Transcriber,
// and flat-file config for the API key. Pure Go — no CGO.
package voice

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds voice/STT settings persisted to ~/.agent-chat/voice.json.
type Config struct {
	OpenAIAPIKey string `json:"openai_api_key"`
}

func configPath(dataDir string) string {
	return filepath.Join(dataDir, "voice.json")
}

// LoadConfig reads voice.json from dataDir. A missing file yields a zero Config
// (no key set yet) and no error — first run is not a failure.
func LoadConfig(dataDir string) (Config, error) {
	data, err := os.ReadFile(configPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// SaveConfig writes voice.json atomically (temp + rename), mirroring
// team.Store.save. The key is a secret, so the file is mode 0600.
func SaveConfig(dataDir string, c Config) error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	fp := configPath(dataDir)
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, fp); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
```

- [ ] **Step 4: Test'in geçtiğini doğrula**

Run: `go test ./internal/voice/ -v`
Expected: PASS (3 test).

- [ ] **Step 5: Commit**

```bash
git add internal/voice/config.go internal/voice/config_test.go
git commit -m "feat(#16): voice.Config + atomik LoadConfig/SaveConfig (voice.json)"
```

---

### Task 3: `Transcriber` arayüzü + `WhisperClient`

**Files:**
- Create: `internal/voice/transcriber.go`
- Create: `internal/voice/whisper_api.go`
- Test: `internal/voice/whisper_api_test.go`

**Interfaces:**
- Consumes: yok.
- Produces: `voice.Transcriber` arayüzü = `Transcribe(ctx context.Context, wav []byte) (string, error)`; `voice.NewWhisperClient(apiKey string) *WhisperClient` (Transcriber'ı implemente eder).

- [ ] **Step 1: Failing test yaz**

Create `internal/voice/whisper_api_test.go`:
```go
package voice

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWhisperTranscribeSendsMultipartAndParses(t *testing.T) {
	var gotModel, gotLang, gotAuth, gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		gotModel = r.FormValue("model")
		gotLang = r.FormValue("language")
		if f, _, err := r.FormFile("file"); err == nil {
			b, _ := io.ReadAll(f)
			gotFile = string(b)
		} else {
			t.Errorf("FormFile: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"merhaba dünya"}`)
	}))
	defer srv.Close()

	old := whisperEndpoint
	whisperEndpoint = srv.URL
	defer func() { whisperEndpoint = old }()

	text, err := NewWhisperClient("sk-xyz").Transcribe(context.Background(), []byte("RIFFfake"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if text != "merhaba dünya" {
		t.Errorf("text = %q, want merhaba dünya", text)
	}
	if gotModel != "whisper-1" {
		t.Errorf("model = %q, want whisper-1", gotModel)
	}
	if gotLang != "tr" {
		t.Errorf("language = %q, want tr", gotLang)
	}
	if gotAuth != "Bearer sk-xyz" {
		t.Errorf("auth = %q, want Bearer sk-xyz", gotAuth)
	}
	if gotFile != "RIFFfake" {
		t.Errorf("file = %q, want RIFFfake", gotFile)
	}
}

func TestWhisperTranscribeErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"bad key"}`)
	}))
	defer srv.Close()
	old := whisperEndpoint
	whisperEndpoint = srv.URL
	defer func() { whisperEndpoint = old }()

	if _, err := NewWhisperClient("bad").Transcribe(context.Background(), []byte("x")); err == nil {
		t.Fatal("expected error on 401")
	}
}
```

- [ ] **Step 2: Test'in fail ettiğini doğrula**

Run: `go test ./internal/voice/ -run TestWhisper -v`
Expected: FAIL — `undefined: whisperEndpoint` / `undefined: NewWhisperClient`.

- [ ] **Step 3: transcriber.go ve whisper_api.go'yu yaz**

Create `internal/voice/transcriber.go`:
```go
package voice

import "context"

// Transcriber turns recorded audio (a WAV byte buffer) into text. Implemented by
// WhisperClient; mocked in app tests so voice orchestration runs without a network
// call.
type Transcriber interface {
	Transcribe(ctx context.Context, wav []byte) (string, error)
}
```

Create `internal/voice/whisper_api.go`:
```go
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// whisperEndpoint is the OpenAI transcription endpoint. A var (not const) so tests
// can point it at an httptest server.
var whisperEndpoint = "https://api.openai.com/v1/audio/transcriptions"

// WhisperClient calls OpenAI's /v1/audio/transcriptions with a fixed Turkish
// language hint. Implements Transcriber.
type WhisperClient struct {
	apiKey string
	http   *http.Client
}

// NewWhisperClient builds a client bound to an API key.
func NewWhisperClient(apiKey string) *WhisperClient {
	return &WhisperClient{apiKey: apiKey, http: &http.Client{}}
}

// Transcribe uploads wav as multipart/form-data (model=whisper-1, language=tr)
// and returns the transcribed text.
func (c *WhisperClient) Transcribe(ctx context.Context, wav []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wav); err != nil {
		return "", err
	}
	if err := w.WriteField("model", "whisper-1"); err != nil {
		return "", err
	}
	if err := w.WriteField("language", "tr"); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, whisperEndpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("⚠️ Whisper API hatası (%d): %s", resp.StatusCode, string(data))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("⚠️ Whisper yanıtı çözümlenemedi: %w", err)
	}
	return out.Text, nil
}
```

- [ ] **Step 4: Test'in geçtiğini doğrula**

Run: `go test ./internal/voice/ -v`
Expected: PASS (config + whisper testleri).

- [ ] **Step 5: Commit**

```bash
git add internal/voice/transcriber.go internal/voice/whisper_api.go internal/voice/whisper_api_test.go
git commit -m "feat(#16): Transcriber arayüzü + WhisperClient (language=tr, multipart)"
```

---

### Task 4: `Recorder` + ffmpeg ses yakalama

**Files:**
- Create: `internal/voice/capture.go`
- Test: `internal/voice/capture_test.go`

**Interfaces:**
- Consumes: yok.
- Produces: `voice.Recorder` arayüzü = `Start(ctx context.Context) error` + `Stop() ([]byte, error)`; `voice.NewFFmpegRecorder(dataDir, deviceSpec string) (Recorder, error)` (ffmpeg yoksa `ErrFFmpegNotFound`); `voice.FFmpegAvailable() bool`; `voice.ErrFFmpegNotFound error`.

- [ ] **Step 1: Failing test yaz**

Create `internal/voice/capture_test.go`:
```go
package voice

import (
	"strings"
	"testing"
)

func TestFFmpegArgsAudioOnly16kMono(t *testing.T) {
	joined := strings.Join(ffmpegArgs(":0", "/tmp/out.wav"), " ")
	for _, want := range []string{
		"-f avfoundation", "-i :0", "-ac 1", "-ar 16000",
		"-acodec pcm_s16le", "-y /tmp/out.wav",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q; got: %s", want, joined)
		}
	}
}

func TestErrFFmpegNotFoundHasInstallHint(t *testing.T) {
	if !strings.Contains(ErrFFmpegNotFound.Error(), "brew install ffmpeg") {
		t.Errorf("error should hint install: %v", ErrFFmpegNotFound)
	}
}
```

- [ ] **Step 2: Test'in fail ettiğini doğrula**

Run: `go test ./internal/voice/ -run "TestFFmpeg|TestErrFFmpeg" -v`
Expected: FAIL — `undefined: ffmpegArgs` / `undefined: ErrFFmpegNotFound`.

- [ ] **Step 3: capture.go'yu yaz**

Create `internal/voice/capture.go`:
```go
package voice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ErrFFmpegNotFound is returned by NewFFmpegRecorder when ffmpeg is not on PATH.
// The app surfaces its Turkish message (with install hint) to the user.
var ErrFFmpegNotFound = fmt.Errorf("⚠️ ffmpeg bulunamadı — sesli prompt için kurun: brew install ffmpeg")

// Recorder captures microphone audio to a WAV buffer. Implemented by
// ffmpegRecorder; the app holds it behind this interface so tests inject a fake.
type Recorder interface {
	Start(ctx context.Context) error
	Stop() ([]byte, error)
}

// FFmpegAvailable reports whether ffmpeg is on PATH.
func FFmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// ffmpegArgs builds the avfoundation capture args: audio-only device
// (":<index>" → empty video slot), mono 16 kHz signed-16 PCM WAV to outPath.
// Pure so it can be unit-tested without spawning ffmpeg.
func ffmpegArgs(deviceSpec, outPath string) []string {
	return []string{
		"-f", "avfoundation",
		"-i", deviceSpec, // ":0" = default mic, no video
		"-ac", "1",
		"-ar", "16000",
		"-acodec", "pcm_s16le",
		"-y", outPath,
	}
}

type ffmpegRecorder struct {
	deviceSpec string
	outPath    string
	cmd        *exec.Cmd
}

// NewFFmpegRecorder returns a Recorder writing to a unique temp WAV under dataDir.
// Returns ErrFFmpegNotFound if ffmpeg is not installed. deviceSpec is the
// avfoundation input (e.g. ":0").
func NewFFmpegRecorder(dataDir, deviceSpec string) (Recorder, error) {
	if !FFmpegAvailable() {
		return nil, ErrFFmpegNotFound
	}
	out := filepath.Join(dataDir, fmt.Sprintf("voice-%d.wav", time.Now().UnixNano()))
	return &ffmpegRecorder{deviceSpec: deviceSpec, outPath: out}, nil
}

func (r *ffmpegRecorder) Start(ctx context.Context) error {
	r.cmd = exec.Command("ffmpeg", ffmpegArgs(r.deviceSpec, r.outPath)...)
	return r.cmd.Start()
}

// Stop signals ffmpeg to finalize the WAV (SIGINT → it writes the trailer and
// exits), waits briefly, then reads and removes the temp file. A SIGKILL guards a
// hung process. ctx is unused here — Start does not bind ctx so a cancel can't
// truncate the file mid-finalize; Stop is the deliberate end.
func (r *ffmpegRecorder) Stop() ([]byte, error) {
	if r.cmd == nil || r.cmd.Process == nil {
		return nil, fmt.Errorf("kayıt başlatılmadı")
	}
	_ = r.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = r.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = r.cmd.Process.Kill()
		<-done
	}
	defer os.Remove(r.outPath)
	return os.ReadFile(r.outPath)
}
```

- [ ] **Step 4: Test'in geçtiğini doğrula**

Run: `go test ./internal/voice/ -v`
Expected: PASS (config + whisper + capture).

- [ ] **Step 5: Commit**

```bash
git add internal/voice/capture.go internal/voice/capture_test.go
git commit -m "feat(#16): voice.Recorder + ffmpeg avfoundation yakalama (CGO-suz)"
```

---

### Task 5: app.go voice orkestrasyonu — Start/StopVoiceCapture

**Files:**
- Modify: `app.go` (App struct ~`app.go:44-72`; startup ~`app.go:80-94`; yeni metodlar dosya sonuna)
- Test: `app_voice_test.go` (YENİ, `package main`)

**Interfaces:**
- Consumes: `voice.Recorder`, `voice.Transcriber`/`voice.NewWhisperClient`, `voice.NewFFmpegRecorder`, `voice.LoadConfig`, `pty.Manager.InjectText` (mevcut, `manager.go:343`).
- Produces: `(*App).StartVoiceCapture(sessionID string) error`; `(*App).StopVoiceCapture(sessionID string) error`; App seam alanları `newVoiceRecorder`, `voiceTranscribe`, `voiceInject`, `voiceEmit`.

- [ ] **Step 1: Failing test yaz**

Create `app_voice_test.go`:
```go
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
```

- [ ] **Step 2: Test'in fail ettiğini doğrula**

Run: `make mcp-server && go test . -run TestStartVoiceCapture -v`
Expected: FAIL — `a.newVoiceRecorder undefined` / `a.StartVoiceCapture undefined`.

- [ ] **Step 3: App struct'a voice seam alanlarını ekle**

`app.go` içinde App struct'ta `promptLogN atomic.Int64` satırından sonra, kapanış `}`'dan önce ekle:
```go
	// Voice/STT state (#16). voiceMu guards the single active microphone capture —
	// only one panel records at a time (one mic). activeRecorder/activeVoiceSession
	// are non-nil exactly while a capture is in flight; transcription runs after the
	// recorder is detached, so panel B can record while panel A's audio uploads.
	voiceMu            sync.Mutex
	activeRecorder     voice.Recorder
	activeVoiceSession string
	// Injectable seams (orchestrator SendFunc pattern), defaulted in startup(),
	// overridden in tests so the flow runs with no ffmpeg/network/Wails runtime.
	newVoiceRecorder func() (voice.Recorder, error)
	voiceTranscribe  func(ctx context.Context, wav []byte) (string, error)
	voiceInject      func(sessionID, text string, submit bool) error
	voiceEmit        func(event string, payload interface{})
```

Ardından dosyanın üst import bloğuna `"desktop/internal/voice"` ekle (zaten `context`, `fmt`, `strings`, `time`, `sync` ve `runtime` import edilmiş durumda — doğrula).

- [ ] **Step 4: startup()'ta default seam'leri kur**

`app.go` startup() içinde `a.orchestrator = orchestrator.New(a.ptyManager)` satırından sonra ekle:
```go
	// Voice seam defaults (#16). Tests replace these; production uses ffmpeg +
	// Whisper + the real PTY injection and Wails event bus.
	a.newVoiceRecorder = func() (voice.Recorder, error) {
		return voice.NewFFmpegRecorder(a.dataDir, ":0")
	}
	a.voiceTranscribe = func(ctx context.Context, wav []byte) (string, error) {
		cfg, err := voice.LoadConfig(a.dataDir)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
			return "", fmt.Errorf("⚠️ OpenAI API anahtarı yok — Ayarlar'dan girin")
		}
		return voice.NewWhisperClient(cfg.OpenAIAPIKey).Transcribe(ctx, wav)
	}
	a.voiceInject = a.ptyManager.InjectText
	a.voiceEmit = func(event string, payload interface{}) {
		runtime.EventsEmit(a.ctx, event, payload)
	}
```

- [ ] **Step 5: Start/StopVoiceCapture + emitVoiceState metodlarını ekle**

`app.go` sonuna ekle:
```go
// emitVoiceState pushes a voice:state:<sessionID> event for the panel's mic UI.
func (a *App) emitVoiceState(sessionID, state, message string) {
	a.voiceEmit("voice:state:"+sessionID, map[string]string{
		"state":   state,
		"message": message,
	})
}

// StartVoiceCapture begins recording the microphone for a session (push-to-talk
// down). Only one capture runs at a time (single mic): a second Start while one is
// active returns an error the frontend surfaces. Emits voice:state events.
func (a *App) StartVoiceCapture(sessionID string) error {
	a.voiceMu.Lock()
	if a.activeRecorder != nil {
		a.voiceMu.Unlock()
		return fmt.Errorf("⚠️ Zaten kayıt sürüyor")
	}
	rec, err := a.newVoiceRecorder()
	if err != nil {
		a.voiceMu.Unlock()
		a.emitVoiceState(sessionID, "error", err.Error())
		return err
	}
	if err := rec.Start(a.ctx); err != nil {
		a.voiceMu.Unlock()
		a.emitVoiceState(sessionID, "error", err.Error())
		return err
	}
	a.activeRecorder = rec
	a.activeVoiceSession = sessionID
	a.voiceMu.Unlock()
	a.emitVoiceState(sessionID, "recording", "")
	return nil
}

// StopVoiceCapture ends recording for a session (push-to-talk up), transcribes the
// audio, and injects the transcript into that session's PTY input line WITHOUT
// submitting (autosubmit off — the user reviews and presses Enter). A Stop for a
// session that isn't the active recording one is a silent no-op. The recorder is
// detached under the lock and released before the network call, so another panel
// may start recording while this transcript uploads.
func (a *App) StopVoiceCapture(sessionID string) error {
	a.voiceMu.Lock()
	if a.activeRecorder == nil || a.activeVoiceSession != sessionID {
		a.voiceMu.Unlock()
		return nil
	}
	rec := a.activeRecorder
	a.activeRecorder = nil
	a.activeVoiceSession = ""
	a.voiceMu.Unlock()

	wav, err := rec.Stop()
	if err != nil {
		a.emitVoiceState(sessionID, "error", "⚠️ Kayıt okunamadı: "+err.Error())
		return err
	}

	a.emitVoiceState(sessionID, "transcribing", "")
	base := a.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, 30*time.Second)
	defer cancel()
	text, err := a.voiceTranscribe(ctx, wav)
	if err != nil {
		a.emitVoiceState(sessionID, "error", err.Error())
		return err
	}
	if strings.TrimSpace(text) == "" {
		a.emitVoiceState(sessionID, "idle", "")
		return nil
	}
	if err := a.voiceInject(sessionID, text, false); err != nil {
		a.emitVoiceState(sessionID, "error", "⚠️ Enjeksiyon hatası: "+err.Error())
		return err
	}
	a.voiceEmit("voice:transcript:"+sessionID, text)
	a.emitVoiceState(sessionID, "idle", "")
	return nil
}
```

- [ ] **Step 6: Test'in geçtiğini doğrula**

Run: `go test . -run TestStartVoiceCapture -v && go test . -run TestStopVoiceCapture -v`
Expected: PASS (4 voice testi).

- [ ] **Step 7: Tüm Go testleri + vet yeşil mi**

Run: `go vet ./... && go test ./...`
Expected: PASS, vet temiz.

- [ ] **Step 8: Commit**

```bash
git add app.go app_voice_test.go
git commit -m "feat(#16): app voice orkestrasyonu — Start/StopVoiceCapture, global kilit, InjectText (autosubmit=false)"
```

---

### Task 6: app.go Ayarlar bindings — GetVoiceStatus / SetVoiceConfig

**Files:**
- Modify: `app.go` (yeni metodlar + `VoiceStatus` tipi)
- Test: `app_voice_test.go` (mevcut dosyaya ekle)

**Interfaces:**
- Consumes: `voice.LoadConfig`, `voice.SaveConfig`, `voice.FFmpegAvailable`, `voice.Config`.
- Produces: `VoiceStatus{HasKey bool, KeyHint string, FFmpegFound bool}`; `(*App).GetVoiceStatus() (VoiceStatus, error)`; `(*App).SetVoiceConfig(apiKey string) error`.

- [ ] **Step 1: Failing test ekle**

`app_voice_test.go` sonuna ekle:
```go
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
```

- [ ] **Step 2: Test'in fail ettiğini doğrula**

Run: `go test . -run "TestGetVoiceStatus|TestSetVoiceConfig" -v`
Expected: FAIL — `a.GetVoiceStatus undefined` / `VoiceStatus` yok.

- [ ] **Step 3: VoiceStatus + bindings'i yaz**

`app.go` sonuna ekle:
```go
// VoiceStatus is the Settings-panel view of voice config. The real API key never
// crosses to the frontend — only whether one is set, a short hint (last 4 chars),
// and whether ffmpeg is available.
type VoiceStatus struct {
	HasKey      bool   `json:"hasKey"`
	KeyHint     string `json:"keyHint"`
	FFmpegFound bool   `json:"ffmpegFound"`
}

// GetVoiceStatus reports voice config state for the Settings panel (no raw key).
func (a *App) GetVoiceStatus() (VoiceStatus, error) {
	cfg, err := voice.LoadConfig(a.dataDir)
	if err != nil {
		return VoiceStatus{}, err
	}
	key := strings.TrimSpace(cfg.OpenAIAPIKey)
	st := VoiceStatus{HasKey: key != "", FFmpegFound: voice.FFmpegAvailable()}
	if r := []rune(key); len(r) > 0 {
		last := r
		if len(r) > 4 {
			last = r[len(r)-4:]
		}
		st.KeyHint = "…" + string(last)
	}
	return st, nil
}

// SetVoiceConfig persists the OpenAI API key (set-only). An empty string clears it.
func (a *App) SetVoiceConfig(apiKey string) error {
	return voice.SaveConfig(a.dataDir, voice.Config{OpenAIAPIKey: strings.TrimSpace(apiKey)})
}
```

- [ ] **Step 4: Test'in geçtiğini doğrula**

Run: `go test . -run "TestGetVoiceStatus|TestSetVoiceConfig" -v`
Expected: PASS (3 test).

- [ ] **Step 5: Commit**

```bash
git add app.go app_voice_test.go
git commit -m "feat(#16): GetVoiceStatus (maskeli) + SetVoiceConfig bindings"
```

---

### Task 7: Frontend bindings regenerate + types.ts

**Files:**
- Generated: `frontend/wailsjs/go/main/App.*` (Wails üretir)
- Modify: `frontend/src/lib/types.ts`

**Interfaces:**
- Consumes: Task 5/6 Go metodları.
- Produces: TS tipleri `VoiceState`, `VoiceStatus`, `VoiceStateEvent`, `VoiceTranscriptEvent`; üretilmiş bindings `StartVoiceCapture`, `StopVoiceCapture`, `GetVoiceStatus`, `SetVoiceConfig`.

- [ ] **Step 1: Wails bindings'i yeniden üret**

Run: `make mcp-server && wails generate module`
Expected: `frontend/wailsjs/go/main/App.d.ts` içinde yeni metodlar belirir. Doğrula:
```bash
grep -E "StartVoiceCapture|StopVoiceCapture|GetVoiceStatus|SetVoiceConfig" frontend/wailsjs/go/main/App.d.ts
```
Expected: dört metodun hepsi listelenir.

- [ ] **Step 2: types.ts'e voice tiplerini ekle**

`frontend/src/lib/types.ts` sonuna (`gridCapacity` fonksiyonundan sonra) ekle:
```ts
// Voice / STT (#16)
export type VoiceState = "idle" | "recording" | "transcribing" | "error";

// Settings-panel view of voice config (mirrors main.VoiceStatus). Raw key never sent.
export interface VoiceStatus {
  hasKey: boolean;
  keyHint: string;
  ffmpegFound: boolean;
}

// voice:state:<sessionID> event payload (mirrors app.go emitVoiceState).
export interface VoiceStateEvent {
  state: VoiceState;
  message: string;
}

// voice:transcript:<sessionID> event payload — the transcribed text (UI feedback
// only; the text is already injected into the PTY by the backend).
export type VoiceTranscriptEvent = string;
```

- [ ] **Step 3: Frontend tip-kontrol + build**

Run: `cd frontend && npm run build`
Expected: TypeScript hatası yok, build başarılı.

- [ ] **Step 4: Commit**

```bash
git add frontend/wailsjs frontend/src/lib/types.ts
git commit -m "feat(#16): voice bindings (regenerate) + types.ts voice tipleri"
```

---

### Task 8: TerminalPane mikrofon butonu + push-to-talk

**Files:**
- Modify: `frontend/src/components/TerminalPane.tsx`

**Interfaces:**
- Consumes: `StartVoiceCapture`, `StopVoiceCapture` bindings; `voice:state:<sessionID>` / `voice:transcript:<sessionID>` event'leri; `VoiceState` tipi.
- Produces: panel header'ında push-to-talk mikrofon butonu + durum göstergesi. (Frontend test koşucusu yok → doğrulama `npm run build` + Task 11 native.)

- [ ] **Step 1: Import ve state ekle**

`frontend/src/components/TerminalPane.tsx` üstünde importları güncelle:
```ts
import { useEffect, useRef, useState } from "react";
```
ve mevcut `WriteToTerminal, ResizeTerminal` importunu genişlet:
```ts
import { WriteToTerminal, ResizeTerminal, StartVoiceCapture, StopVoiceCapture } from "../../wailsjs/go/main/App";
import { CLIType, VoiceState } from "../lib/types";
```

Bileşen gövdesinin başına (`const containerRef = ...` satırlarının yanına) ekle:
```ts
  const [voiceState, setVoiceState] = useState<VoiceState>("idle");
  const [voiceError, setVoiceError] = useState<string>("");
```

- [ ] **Step 2: voice:state / voice:transcript dinleyicilerini ekle**

`TerminalPane.tsx` içinde ayrı bir `useEffect` ekle (mevcut xterm `useEffect`'ten sonra), `sessionID`'ye bağlı:
```ts
  // Voice (#16) state/transcript events for this panel only (sessionID-scoped).
  // The transcript text is already injected into the PTY by the backend; these
  // events only drive the mic button UI, so we never write transcript to xterm.
  useEffect(() => {
    if (!sessionID) return;
    let cancelled = false;
    let cleanup = () => {};
    import("../../wailsjs/runtime/runtime").then(({ EventsOn, EventsOff }) => {
      if (cancelled) return;
      const stateEv = "voice:state:" + sessionID;
      EventsOn(stateEv, (data: { state: VoiceState; message: string }) => {
        setVoiceState(data.state);
        setVoiceError(data.state === "error" ? data.message : "");
      });
      cleanup = () => {
        try { EventsOff(stateEv); } catch (e) {
          if (import.meta.env.DEV) console.warn("voice EventsOff failed:", e);
        }
      };
    }).catch((e) => {
      if (import.meta.env.DEV) console.warn("voice runtime load failed:", e);
    });
    return () => { cancelled = true; cleanup(); };
  }, [sessionID]);
```

- [ ] **Step 3: Push-to-talk handler'larını ekle**

Bileşen gövdesinde, `return`'den önce ekle:
```ts
  // Push-to-talk: hold to record, release to transcribe. onMouseLeave also stops
  // so a drag-off doesn't leave the mic recording. Backend enforces the single
  // active-recording lock; a rejected Start just shows an error state.
  const startVoice = () => {
    if (voiceState === "recording" || voiceState === "transcribing") return;
    StartVoiceCapture(sessionID).catch((e) => {
      setVoiceState("error");
      setVoiceError(String(e));
    });
  };
  const stopVoice = () => {
    if (voiceState !== "recording") return;
    StopVoiceCapture(sessionID).catch((e) => {
      setVoiceState("error");
      setVoiceError(String(e));
    });
  };
```

- [ ] **Step 4: Mikrofon butonunu header'a ekle**

`terminal-header-actions` div'inde, `onRestart` butonundan ÖNCE ekle:
```tsx
          <button
            type="button"
            className={"terminal-btn-voice voice-" + voiceState}
            onMouseDown={startVoice}
            onMouseUp={stopVoice}
            onMouseLeave={stopVoice}
            title={
              voiceState === "recording"
                ? "Kaydediliyor… bırakınca yazılır"
                : voiceState === "transcribing"
                ? "Çevriliyor…"
                : voiceError
                ? voiceError
                : "Bas-konuş (sesli prompt)"
            }
          >
            {voiceState === "transcribing" ? "⋯" : "🎤"}
          </button>
```

- [ ] **Step 5: Frontend tip-kontrol + build**

Run: `cd frontend && npm run build`
Expected: TypeScript hatası yok, build başarılı.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/TerminalPane.tsx
git commit -m "feat(#16): TerminalPane mikrofon butonu + push-to-talk + voice:state dinleyici"
```

---

### Task 9: SettingsModal + App.tsx ⚙️ wiring

**Files:**
- Create: `frontend/src/components/SettingsModal.tsx`
- Modify: `frontend/src/App.tsx`

**Interfaces:**
- Consumes: `GetVoiceStatus`, `SetVoiceConfig` bindings; `VoiceStatus` tipi.
- Produces: `SettingsModal` bileşeni; App'te ⚙️ butonu + modal toggle state.

- [ ] **Step 1: SettingsModal.tsx'i yaz**

Create `frontend/src/components/SettingsModal.tsx`:
```tsx
import { useEffect, useState } from "react";
import { GetVoiceStatus, SetVoiceConfig } from "../../wailsjs/go/main/App";
import { VoiceStatus } from "../lib/types";

interface SettingsModalProps {
  onClose: () => void;
}

// SettingsModal edits the OpenAI Whisper API key for voice prompts (#16). The raw
// key is never read back from the backend — only a masked hint + ffmpeg presence.
// Reuses the shared .modal / .form-group / .modal-actions styles.
export default function SettingsModal({ onClose }: SettingsModalProps) {
  const [status, setStatus] = useState<VoiceStatus | null>(null);
  const [apiKey, setApiKey] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    GetVoiceStatus().then(setStatus).catch((e) => setError(String(e)));
  }, []);

  const handleSave = async () => {
    if (saving) return;
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      await SetVoiceConfig(apiKey.trim());
      setApiKey("");
      setSaved(true);
      const fresh = await GetVoiceStatus();
      setStatus(fresh);
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>⚙️ Ayarlar — Sesli Prompt</h3>

        <div className="form-group">
          <label>OpenAI API Anahtarı</label>
          <input
            type="password"
            autoFocus
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") handleSave();
              if (e.key === "Escape") onClose();
            }}
            placeholder={status?.hasKey ? `Kayıtlı: ${status.keyHint}` : "sk-..."}
          />
          <span className="form-hint">
            {status?.hasKey
              ? `✅ Anahtar kayıtlı (${status.keyHint}). Değiştirmek için yenisini girin.`
              : "ℹ️ Whisper STT için OpenAI anahtarı gerekir. ~/.agent-chat/voice.json'a kaydedilir."}
          </span>
          <span className="form-hint">
            {status?.ffmpegFound
              ? "✅ ffmpeg bulundu."
              : "⚠️ ffmpeg bulunamadı — sesli prompt için: brew install ffmpeg"}
          </span>
        </div>

        {error && <div className="form-error">⚠️ {error}</div>}
        {saved && <div className="form-hint">✅ Kaydedildi.</div>}

        <div className="modal-actions">
          <button className="btn" onClick={handleSave} disabled={saving || apiKey.trim() === ""}>
            Kaydet
          </button>
          <button className="btn btn-secondary" onClick={onClose}>
            Kapat
          </button>
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: App.tsx'e modal state + ⚙️ buton + mount ekle**

`frontend/src/App.tsx` üstüne import ekle (diğer component importlarının yanına):
```ts
import SettingsModal from "./components/SettingsModal";
```
Diğer modal/notice state'lerinin yanına ekle:
```ts
  const [showSettings, setShowSettings] = useState(false);
```
Sidebar toggle butonunun (`onClick={toggleSidebar}`, ~`App.tsx:218`) hemen yanına bir ⚙️ butonu ekle:
```tsx
            <button
              type="button"
              className="app-settings-btn"
              onClick={() => setShowSettings(true)}
              title="Ayarlar (sesli prompt / API anahtarı)"
            >
              ⚙️
            </button>
```
Diğer modal mount'larının yanına (örn. notice JSX'lerinin sonuna, `app-body`'den önce) ekle:
```tsx
      {showSettings && <SettingsModal onClose={() => setShowSettings(false)} />}
```

- [ ] **Step 3: Frontend tip-kontrol + build**

Run: `cd frontend && npm run build`
Expected: TypeScript hatası yok, build başarılı.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/SettingsModal.tsx frontend/src/App.tsx
git commit -m "feat(#16): SettingsModal (OpenAI key) + App.tsx ⚙️ wiring"
```

---

### Task 10: CSS — recording animasyonu + mic buton stilleri

**Files:**
- Modify: `frontend/src/styles/globals.css`

**Interfaces:**
- Consumes: Task 8 sınıfları (`terminal-btn-voice`, `voice-recording`, `voice-transcribing`, `voice-error`), Task 9 `app-settings-btn`.
- Produces: görsel stiller. (Doğrulama: `npm run build` + Task 11 native.)

- [ ] **Step 1: Stilleri ekle**

`frontend/src/styles/globals.css` sonuna ekle:
```css
/* Voice prompt (#16) — mic button + recording state */
.terminal-btn-voice {
  background: transparent;
  border: none;
  cursor: pointer;
  font-size: 12px;
  line-height: 1;
  padding: 2px 4px;
  opacity: 0.7;
  user-select: none;
}
.terminal-btn-voice:hover {
  opacity: 1;
}
.terminal-btn-voice.voice-recording {
  color: #ff7b72;
  animation: voice-pulse 1s ease-in-out infinite;
}
.terminal-btn-voice.voice-transcribing {
  color: #d29922;
  opacity: 1;
}
.terminal-btn-voice.voice-error {
  color: #ff7b72;
  opacity: 1;
}
@keyframes voice-pulse {
  0%, 100% { opacity: 0.5; transform: scale(1); }
  50% { opacity: 1; transform: scale(1.15); }
}
.app-settings-btn {
  background: transparent;
  border: none;
  cursor: pointer;
  font-size: 14px;
  padding: 4px 6px;
  opacity: 0.75;
}
.app-settings-btn:hover {
  opacity: 1;
}
```

- [ ] **Step 2: Build doğrula**

Run: `cd frontend && npm run build`
Expected: build başarılı.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/styles/globals.css
git commit -m "feat(#16): mic buton + recording pulse + ⚙️ CSS stilleri"
```

---

### Task 11: Faz 4 — Native uçtan-uca doğrulama (kullanıcıda)

**Files:** yok (manuel doğrulama + bulgu varsa düzeltme commit'leri).

**Interfaces:**
- Consumes: tüm önceki task'lar.
- Produces: çalışan, native-doğrulanmış özellik + birincil rezidüel riskin (ffmpeg subprocess TCC) sonucu.

- [ ] **Step 1: Tüm Go testleri + vet son kez yeşil**

Run: `make mcp-server && go vet ./... && go test ./...`
Expected: tüm testler PASS, vet temiz.

- [ ] **Step 2: Native build**

Run: `make build`
Expected: imzalı `.app` üretilir (embed için `mcp-server-bin` mevcut).

- [ ] **Step 3: İlk çalıştırma — mikrofon izni**

Uygulamayı aç, bir terminal panelinde mikrofon butonuna **bas-tut** ve konuş.
Doğrula: macOS mikrofon izin istemi **Agent Chat adına** çıkıyor; System Settings → Privacy & Security → Microphone altında "Agent Chat" listeleniyor. (⚠️ Birincil rezidüel risk: ffmpeg subprocess'in TCC izninin app'e atfedilmesi — burada doğrulanır. İzin app yerine ffmpeg adına/hiç çıkmazsa: spec'in "Kapsam dışı/yedek plan" notuna dön.)

- [ ] **Step 4: Türkçe transkript + doğru panele enjeksiyon**

4 panelli grid'de **A panelinde** bas-konuş ("merhaba dünya bu bir test") → bırak.
Doğrula: transcript **yalnızca A panelinin** input satırına yazıldı (B/C/D'ye değil); otomatik Enter **gönderilmedi** (kullanıcı Enter'layana dek satırda bekliyor); Türkçe doğruluk makul.

- [ ] **Step 5: Ayarlar + ffmpeg-yok + kilit yolları**

Doğrula: ⚙️ Ayarlar'dan key girilip kaydediliyor, `~/.agent-chat/voice.json` `0600` oluşuyor; key boşken mikrofon → "OpenAI API anahtarı yok" hatası; iki panelde aynı anda bas → ikinci panel "Zaten kayıt sürüyor" alıyor.

- [ ] **Step 6: (Bulgu varsa) düzelt ve commit**

Native testte bir sorun çıkarsa (TCC, avfoundation device index, vb.) düzelt ve commit'le. Aksi halde özellik tamamdır.

---

## Self-Review

**Spec coverage:** Faz 0 → Task 1 · `internal/voice` (config/transcriber/whisper/capture) → Task 2-4 · app orkestrasyon (Start/Stop, global kilit, InjectText autosubmit=false, events) → Task 5 · Settings bindings (maskeli) → Task 6 · frontend types/bindings → Task 7 · TerminalPane push-to-talk → Task 8 · SettingsModal → Task 9 · CSS → Task 10 · native E2E + rezidüel risk → Task 11. Tüm spec bölümleri kapsanıyor.

**Placeholder:** Yok — her implementasyon adımı tam kod içerir.

**Type consistency:** `voice.Recorder` (Start/Stop), `voice.Transcriber` (Transcribe), `VoiceStatus{HasKey/KeyHint/FFmpegFound}` Go ile `VoiceStatus{hasKey/keyHint/ffmpegFound}` TS json tag'leriyle uyuşuyor; seam isimleri (`newVoiceRecorder`/`voiceTranscribe`/`voiceInject`/`voiceEmit`) struct ve testlerde tutarlı; event adları `voice:state:`/`voice:transcript:` backend ve frontend'de aynı.
