package voice

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func le16(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
func le32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }

// buildWAV makes a valid 16-bit/16kHz mono WAV with `samples` samples all set to
// `amp`. When withLIST is true it inserts a LIST/INFO chunk before `data` — exactly
// what ffmpeg emits — so tests can prove the `data` payload (not metadata) is what
// gets measured.
func buildWAV(samples int, amp int16, withLIST bool) []byte {
	var list []byte
	if withLIST {
		body := bytes.NewBufferString("INFO")
		body.WriteString("ISFT")
		body.Write(le32(13))
		body.WriteString("Lavf62.3.100\x00") // 13 bytes incl trailing NUL
		list = append([]byte("LIST"), le32(uint32(body.Len()))...)
		list = append(list, body.Bytes()...)
	}
	data := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(amp))
	}
	var w bytes.Buffer
	w.WriteString("RIFF")
	w.Write(le32(uint32(4 + 24 + len(list) + 8 + len(data)))) // WAVE + fmt(24) + LIST + data hdr+payload
	w.WriteString("WAVE")
	w.WriteString("fmt ")
	w.Write(le32(16))
	w.Write(le16(1))     // PCM
	w.Write(le16(1))     // mono
	w.Write(le32(16000)) // sample rate
	w.Write(le32(32000)) // byte rate
	w.Write(le16(2))     // block align
	w.Write(le16(16))    // bits/sample
	w.Write(list)
	w.WriteString("data")
	w.Write(le32(uint32(len(data))))
	w.Write(data)
	return w.Bytes()
}

func TestIsLikelySilentDetectsSilence(t *testing.T) {
	if silent, db := IsLikelySilent(buildWAV(8000, 0, false)); !silent {
		t.Errorf("all-zero capture should be silent (dBFS=%.1f)", db)
	}
}

func TestIsLikelySilentPassesSpeechLevel(t *testing.T) {
	// amp 8000 ≈ -12 dBFS, clearly speech-level.
	if silent, db := IsLikelySilent(buildWAV(8000, 8000, false)); silent {
		t.Errorf("loud capture should not be silent (dBFS=%.1f)", db)
	}
}

// Regression for Codex P2: a silent capture carrying a LIST/INFO metadata chunk
// (ffmpeg's default) must still read as silent. The old wav[44:] slice measured the
// metadata bytes and could wrongly clear the gate, re-opening the hallucination path.
func TestIsLikelySilentIgnoresLISTChunk(t *testing.T) {
	if silent, db := IsLikelySilent(buildWAV(8000, 0, true)); !silent {
		t.Errorf("silent capture with LIST chunk should be silent (dBFS=%.1f)", db)
	}
}

func TestIsLikelySilentShortBufferIsSilent(t *testing.T) {
	if silent, _ := IsLikelySilent([]byte("RIFFfake")); !silent {
		t.Error("a too-short/invalid buffer should count as silent")
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
