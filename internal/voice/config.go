// Package voice implements microphone capture and speech-to-text for the voice
// prompt feature (#16): an ffmpeg-based Recorder, an OpenAI Whisper Transcriber,
// and flat-file config for the API key. Pure Go — no CGO.
package voice

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	data, err := os.ReadFile(configPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// SaveConfig writes voice.json atomically (temp + rename), mirroring
// team.Store.save. The key is a secret, so the file is mode 0600.
func SaveConfig(dataDir string, c Config) error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	fp := configPath(dataDir)
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, fp); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
