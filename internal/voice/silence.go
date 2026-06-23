package voice

import (
	"encoding/binary"
	"math"
	"strings"
	"unicode"
)

// pcmData returns the bytes of the WAV "data" chunk. It walks the RIFF chunk list
// rather than assuming a fixed 44-byte header, because ffmpeg inserts a LIST/INFO
// metadata chunk before "data" — measuring from byte 44 would feed that metadata
// into the RMS and let a silent recording clear the gate (Codex P2). Returns nil for
// anything that isn't a recognizable PCM WAV.
func pcmData(wav []byte) []byte {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil
	}
	off := 12
	for off+8 <= len(wav) {
		id := string(wav[off : off+4])
		size := int(binary.LittleEndian.Uint32(wav[off+4 : off+8]))
		body := off + 8
		if id == "data" {
			end := body + size
			if size < 0 || end > len(wav) {
				end = len(wav) // tolerate a streamed/oversized declared size
			}
			return wav[body:end]
		}
		off = body + size
		if size%2 == 1 {
			off++ // chunks are word-aligned (padded to even length)
		}
	}
	return nil
}

// rmsDBFS returns the RMS level of a 16-bit PCM mono WAV's data chunk in dBFS
// (0 = full scale, negative = quieter). Empty/too-short/unparseable data returns a
// very low level (treated as silence).
func rmsDBFS(wav []byte) float64 {
	pcm := pcmData(wav)
	n := len(pcm) / 2
	if n == 0 {
		return -120
	}
	var sumSq float64
	for i := 0; i+1 < len(pcm); i += 2 {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[i : i+2])))
		sumSq += s * s
	}
	rms := math.Sqrt(sumSq / float64(n))
	if rms < 1 {
		return -120
	}
	return 20 * math.Log10(rms/32768.0)
}

// silenceDBFS is the RMS level below which a capture is treated as no-speech.
// Measured ambient/no-speech sits near -71 dBFS; speech is above ~-30 dBFS, so
// -50 leaves a wide margin on both sides. Below this we skip transcription, since
// Whisper hallucinates subtitle artifacts (e.g. "Altyazı M.K.") on silent audio.
const silenceDBFS = -50.0

// IsLikelySilent reports whether a capture contains no real speech, plus the
// measured dBFS (returned for logging/tuning).
func IsLikelySilent(wav []byte) (bool, float64) {
	db := rmsDBFS(wav)
	return db < silenceDBFS, db
}

// normalizeText lowercases s and drops everything but letters/digits, so phrase
// matching ignores spacing and punctuation ("Altyazı M.K." → "altyazımk").
func normalizeText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hallucinationPhrases are normalized phrases Whisper emits on silent Turkish audio
// (subtitle-credit artifacts) that are never real user speech. Kept deliberately
// narrow — common real words (e.g. "teşekkürler") are NOT listed.
var hallucinationPhrases = []string{"altyazımk", "altyazimk"}

// IsHallucination reports whether text is only a known no-speech hallucination
// (possibly repeated back-to-back), so it should be dropped rather than injected.
func IsHallucination(text string) bool {
	norm := normalizeText(text)
	if norm == "" {
		return false
	}
	for _, h := range hallucinationPhrases {
		if h != "" && strings.ReplaceAll(norm, h, "") == "" {
			return true
		}
	}
	return false
}
