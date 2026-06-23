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
// hung process. Start does not bind ctx so a cancel can't truncate the file
// mid-finalize; Stop is the deliberate end.
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
