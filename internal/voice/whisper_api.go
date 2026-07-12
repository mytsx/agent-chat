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
)

// whisperEndpoint is the OpenAI transcription endpoint. A var (not const) so tests
// can point it at an httptest server.
var whisperEndpoint = "https://api.openai.com/v1/audio/transcriptions"

const maxWhisperErrorBodyBytes = 4096

// WhisperClient calls OpenAI's /v1/audio/transcriptions with a fixed Turkish
// language hint. Implements Transcriber.
type WhisperClient struct {
	apiKey string
	http   *http.Client
}

// NewWhisperClient builds a client bound to an API key.
func NewWhisperClient(apiKey string) *WhisperClient {
	return &WhisperClient{apiKey: strings.TrimSpace(apiKey), http: &http.Client{}}
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

func readWhisperResponseBody(resp *http.Response) ([]byte, error) {
	if resp.StatusCode == http.StatusOK {
		return io.ReadAll(resp.Body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWhisperErrorBodyBytes+1))
	if len(data) > maxWhisperErrorBodyBytes {
		data = append(data[:maxWhisperErrorBodyBytes], []byte("…")...)
	}
	return data, err
}
