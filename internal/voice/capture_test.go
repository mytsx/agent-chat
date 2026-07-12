package voice

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFFmpegArgsAudioOnly16kMono(t *testing.T) {
	joined := strings.Join(ffmpegArgs(":0", "/tmp/out.wav"), " ")
	for _, want := range []string{
		"-f avfoundation", "-i :0", "-ac 1", "-ar 16000",
		"-acodec pcm_s16le", "-map_metadata -1", "-y /tmp/out.wav",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q; got: %s", want, joined)
		}
	}
}

func TestResolveFFmpegProbesHomebrewPaths(t *testing.T) {
	// The Finder-launch fix depends on probing these absolute locations.
	for _, want := range []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg"} {
		found := false
		for _, p := range commonFFmpegPaths {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("commonFFmpegPaths missing %q", want)
		}
	}
}

func TestResolveFFmpegReturnsExistingExecutable(t *testing.T) {
	p := resolveFFmpeg()
	if p == "" {
		t.Skip("ffmpeg not installed in this environment")
	}
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		t.Errorf("resolveFFmpeg returned %q which is not a file: %v", p, err)
	}
	if !FFmpegAvailable() {
		t.Error("FFmpegAvailable should be true when resolveFFmpeg finds a binary")
	}
}

func TestResolveFFmpegSkipsNonExecutableFallback(t *testing.T) {
	dir := t.TempDir()
	nonExecutable := filepath.Join(dir, "not-executable-ffmpeg")
	executable := filepath.Join(dir, "executable-ffmpeg")
	if err := os.WriteFile(nonExecutable, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write non-executable fallback: %v", err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write executable fallback: %v", err)
	}

	oldPaths := commonFFmpegPaths
	commonFFmpegPaths = []string{nonExecutable, executable}
	t.Cleanup(func() { commonFFmpegPaths = oldPaths })
	t.Setenv("PATH", dir)

	if got := resolveFFmpeg(); got != executable {
		t.Fatalf("resolveFFmpeg() = %q, want executable fallback %q", got, executable)
	}
}

func TestErrFFmpegNotFoundHasInstallHint(t *testing.T) {
	if !strings.Contains(ErrFFmpegNotFound.Error(), "brew install ffmpeg") {
		t.Errorf("error should hint install: %v", ErrFFmpegNotFound)
	}
}

func TestFFmpegRecorderStopIsSingleUseAndCleansTempWAV(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake is for Unix-like platforms")
	}

	dir := t.TempDir()
	fakeFFmpeg := filepath.Join(dir, "fake-ffmpeg.sh")
	readyPath := filepath.Join(dir, "ready")
	outPath := filepath.Join(dir, "capture.wav")
	script := `#!/bin/sh
ready=""
out=""
prev=""
for arg do
  if [ "$prev" = "-i" ]; then
    ready="$arg"
  fi
  prev="$arg"
  out="$arg"
done
: > "$ready"
trap 'printf "RIFF12345678901234567890123456789012345678901234567890" > "$out"; exit 0' INT TERM
while :; do
  sleep 1
done
`
	if err := os.WriteFile(fakeFFmpeg, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	rec := &ffmpegRecorder{bin: fakeFFmpeg, deviceSpec: readyPath, outPath: outPath}
	if err := rec.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFile(t, readyPath)

	wav, err := rec.Stop()
	if err != nil {
		t.Fatalf("first Stop() error = %v", err)
	}
	if len(wav) <= 44 {
		t.Fatalf("first Stop() returned %d bytes, want more than WAV header", len(wav))
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("temp WAV should be removed after Stop(); stat error = %v", err)
	}

	if _, err := rec.Stop(); err == nil || !strings.Contains(err.Error(), "zaten durduruldu") {
		t.Fatalf("second Stop() error = %v, want already-stopped error", err)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("second Stop() should not recreate temp WAV; stat error = %v", err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat ready file: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
