package voice

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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

// commonFFmpegPaths are absolute locations probed when ffmpeg isn't on PATH. A
// macOS app launched from Finder inherits launchd's minimal PATH
// (/usr/bin:/bin:/usr/sbin:/sbin), which omits Homebrew/MacPorts — so a LookPath
// alone reports ffmpeg missing for normal packaged-app users (Codex P2).
var commonFFmpegPaths = []string{
	"/opt/homebrew/bin/ffmpeg", // Apple-silicon Homebrew
	"/usr/local/bin/ffmpeg",    // Intel Homebrew
	"/opt/local/bin/ffmpeg",    // MacPorts
}

// resolveFFmpeg returns an absolute path to an ffmpeg executable — PATH first, then
// the common install locations — or "" if none is found.
func resolveFFmpeg() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	for _, p := range commonFFmpegPaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode().Perm()&0111 != 0 {
			return p
		}
	}
	return ""
}

// FFmpegAvailable reports whether an ffmpeg executable can be located.
func FFmpegAvailable() bool {
	return resolveFFmpeg() != ""
}

// ffmpegArgs builds the avfoundation capture args: audio-only device
// (":<index>" → empty video slot), mono 16 kHz signed-16 PCM WAV to outPath.
// -map_metadata -1 drops ffmpeg's LIST/INFO chunk so the output is a clean
// canonical WAV. Pure so it can be unit-tested without spawning ffmpeg.
func ffmpegArgs(deviceSpec, outPath string) []string {
	return []string{
		"-f", "avfoundation",
		"-i", deviceSpec, // ":0" = default mic, no video
		"-ac", "1",
		"-ar", "16000",
		"-acodec", "pcm_s16le",
		"-map_metadata", "-1",
		"-y", outPath,
	}
}

type ffmpegRecorder struct {
	mu         sync.Mutex
	bin        string
	deviceSpec string
	outPath    string
	cmd        *exec.Cmd
	stderr     bytes.Buffer
	stopped    bool
}

// NewFFmpegRecorder returns a Recorder writing to a unique temp WAV under dataDir.
// Returns ErrFFmpegNotFound if ffmpeg cannot be located. deviceSpec is the
// avfoundation input (e.g. ":0"). The resolved absolute ffmpeg path is captured so
// a later Finder launch (minimal PATH) still spawns the same binary.
func NewFFmpegRecorder(dataDir, deviceSpec string) (Recorder, error) {
	bin := resolveFFmpeg()
	if bin == "" {
		return nil, ErrFFmpegNotFound
	}
	out := filepath.Join(dataDir, fmt.Sprintf("voice-%d.wav", time.Now().UnixNano()))
	return &ffmpegRecorder{bin: bin, deviceSpec: deviceSpec, outPath: out}, nil
}

func (r *ffmpegRecorder) Start(ctx context.Context) error {
	r.cmd = exec.Command(r.bin, ffmpegArgs(r.deviceSpec, r.outPath)...)
	// Capture ffmpeg's stderr so a failed capture (mic permission denied, bad
	// device index) surfaces a real message instead of vanishing — otherwise an
	// empty/missing WAV only shows up later as an opaque read error.
	r.cmd.Stderr = &r.stderr
	return r.cmd.Start()
}

// Stop signals ffmpeg to finalize the WAV (SIGINT → it writes the trailer and
// exits), waits briefly, then reads and removes the temp file. A SIGKILL guards a
// hung process. Start does not bind ctx so a cancel can't truncate the file
// mid-finalize; Stop is the deliberate end.
func (r *ffmpegRecorder) Stop() ([]byte, error) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil, fmt.Errorf("kayıt zaten durduruldu")
	}
	if r.cmd == nil || r.cmd.Process == nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("kayıt başlatılmadı")
	}
	r.stopped = true
	cmd := r.cmd
	outPath := r.outPath
	r.mu.Unlock()

	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	defer os.Remove(outPath)
	wav, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("WAV okunamadı (%v) — ffmpeg: %s", err, tail(r.stderr.String(), 400))
	}
	if len(wav) <= 44 { // 44 bytes = empty WAV header, no audio captured
		return nil, fmt.Errorf("ses yakalanamadı (boş kayıt) — ffmpeg: %s", tail(r.stderr.String(), 400))
	}
	return wav, nil
}

// tail returns the last n bytes of s (for surfacing ffmpeg's most recent stderr).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
