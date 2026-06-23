package voice

import "testing"

// pcmWAV builds a minimal 16-bit mono WAV: a 44-byte (zeroed) header followed by
// `samples` little-endian int16 values, all set to `amp`.
func pcmWAV(samples int, amp int16) []byte {
	b := make([]byte, 44+samples*2)
	for i := 0; i < samples; i++ {
		v := uint16(amp)
		b[44+i*2] = byte(v)
		b[44+i*2+1] = byte(v >> 8)
	}
	return b
}

func TestIsLikelySilentDetectsSilence(t *testing.T) {
	silent, db := IsLikelySilent(pcmWAV(8000, 0))
	if !silent {
		t.Errorf("all-zero capture should be silent (dBFS=%.1f)", db)
	}
}

func TestIsLikelySilentPassesSpeechLevel(t *testing.T) {
	// amp 8000 ≈ -12 dBFS, clearly speech-level.
	silent, db := IsLikelySilent(pcmWAV(8000, 8000))
	if silent {
		t.Errorf("loud capture should not be silent (dBFS=%.1f)", db)
	}
}

func TestIsLikelySilentShortBufferIsSilent(t *testing.T) {
	if silent, _ := IsLikelySilent([]byte("RIFFfake")); !silent {
		t.Error("a too-short buffer should count as silent")
	}
}

func TestIsHallucination(t *testing.T) {
	cases := map[string]bool{
		"Altyazı M.K.":             true,
		"Altyazı M.K.Altyazı M.K.": true,
		"altyazimk":                true,
		"deneme 1 2 3":             false,
		"merhaba dünya":            false,
		"teşekkürler":              false,
		"":                         false,
		"   ":                      false,
	}
	for in, want := range cases {
		if got := IsHallucination(in); got != want {
			t.Errorf("IsHallucination(%q) = %v, want %v", in, got, want)
		}
	}
}
