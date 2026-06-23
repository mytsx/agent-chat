package voice

import (
	"os"
	"strings"
	"testing"
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

func TestErrFFmpegNotFoundHasInstallHint(t *testing.T) {
	if !strings.Contains(ErrFFmpegNotFound.Error(), "brew install ffmpeg") {
		t.Errorf("error should hint install: %v", ErrFFmpegNotFound)
	}
}
