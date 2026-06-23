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
