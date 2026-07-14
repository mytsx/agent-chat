package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// whisperEndpoint is the OpenAI transcription endpoint. A var (not const) so tests
// can point it at an httptest server.
var whisperEndpoint = "https://api.openai.com/v1/audio/transcriptions"

// Bound how much of a Whisper response we read into memory so a huge/streamed body
// can't OOM the app: a success body holds a short transcript, an error body a short
// JSON message.
const (
	maxWhisperErrorBodyBytes   = 4096
	maxWhisperSuccessBodyBytes = 1 << 20
)

// WhisperClient calls OpenAI's /v1/audio/transcriptions with a fixed Turkish
// language hint. Implements Transcriber.
type WhisperClient struct {
	apiKey string
	http   *http.Client
}

// defaultWhisperHTTPClient is shared across all WhisperClients. The API key lives on
// the WhisperClient (rebuilt per request so a Settings change takes effect immediately),
// but the underlying http.Client/Transport is reused so TCP keep-alive and TLS sessions
// survive across transcriptions instead of leaking a fresh connection pool each call.
// The bounded timeout stops a hung/slow OpenAI response from blocking forever (the
// per-request context still applies on top, whichever fires first).
var defaultWhisperHTTPClient = &http.Client{Timeout: 30 * time.Second}

// NewWhisperClient builds a client bound to an API key, reusing the shared HTTP client.
func NewWhisperClient(apiKey string) *WhisperClient {
	return &WhisperClient{apiKey: strings.TrimSpace(apiKey), http: defaultWhisperHTTPClient}
}

// Transcribe uploads wav as multipart/form-data (model=whisper-1, language=tr)
// and returns the transcribed text.
func (c *WhisperClient) Transcribe(ctx context.Context, wav []byte) (string, error) {
	apiKey, err := normalizeOpenAIAPIKey(c.apiKey)
	if err != nil {
		return "", fmt.Errorf("⚠️ OpenAI API anahtarı geçersiz: %w", err)
	}
	if apiKey == "" {
		return "", fmt.Errorf("⚠️ OpenAI API anahtarı yok — Ayarlar'dan girin")
	}

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
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := readWhisperResponseBody(resp)
	if err != nil {
		return "", fmt.Errorf("⚠️ Whisper yanıtı okunamadı: %w", err)
	}
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

// readWhisperResponseBody reads a bounded amount of the response body. A success
// body is limited to maxWhisperSuccessBodyBytes (and rejected if it exceeds that so a
// truncated transcript isn't silently parsed); an error body is capped at
// maxWhisperErrorBodyBytes and elided, since it's only surfaced in a diagnostic.
func readWhisperResponseBody(resp *http.Response) ([]byte, error) {
	if resp.StatusCode == http.StatusOK {
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxWhisperSuccessBodyBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxWhisperSuccessBodyBytes {
			return nil, fmt.Errorf("Whisper response exceeded %d bytes", maxWhisperSuccessBodyBytes)
		}
		return data, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWhisperErrorBodyBytes+1))
	if len(data) > maxWhisperErrorBodyBytes {
		data = append(data[:maxWhisperErrorBodyBytes], []byte("…")...)
	}
	return data, err
}
