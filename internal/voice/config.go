// Package voice implements microphone capture and speech-to-text for the voice
// prompt feature (#16): an ffmpeg-based Recorder, an OpenAI Whisper Transcriber,
// and flat-file config for the API key. Pure Go — no CGO.
package voice

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Config holds voice/STT settings persisted to ~/.agent-chat/voice.json.
type Config struct {
	OpenAIAPIKey string `json:"openai_api_key"`
}

func configPath(dataDir string) string {
	return filepath.Join(dataDir, "voice.json")
}

// LoadConfig reads voice.json from dataDir. A missing file yields a zero Config
// (no key set yet) and no error — first run is not a failure.
func LoadConfig(dataDir string) (Config, error) {
	fp := configPath(dataDir)
	data, err := os.ReadFile(fp)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read voice config %s: %w", fp, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse voice config %s: %w", fp, err)
	}
	key, err := normalizeOpenAIAPIKey(c.OpenAIAPIKey)
	if err != nil {
		return Config{}, fmt.Errorf("parse voice config %s: %w", fp, err)
	}
	c.OpenAIAPIKey = key
	return c, nil
}

// SaveConfig writes voice.json atomically (temp + rename), mirroring
// team.Store.save. The key is a secret, so the file is mode 0600.
func SaveConfig(dataDir string, c Config) error {
	key, err := normalizeOpenAIAPIKey(c.OpenAIAPIKey)
	if err != nil {
		return err
	}
	c.OpenAIAPIKey = key

	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("create voice config dir %s: %w", dataDir, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal voice config: %w", err)
	}
	fp := configPath(dataDir)
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write voice config temp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, fp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace voice config %s: %w", fp, err)
	}
	return nil
}

func normalizeOpenAIAPIKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key != "" && strings.ContainsFunc(key, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return "", fmt.Errorf("invalid voice OpenAI API key: whitespace/control characters are not allowed")
	}
	return key, nil
}
