package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// geminiAdapter ingests Gemini CLI's monolithic JSON chat record:
// ~/.gemini/tmp/{sha256(cwd)}/chats/session-*.json — a single object
// {messages:[{type,timestamp,content:[{text}]}]}. type=="user" is a human
// message; content[].text is concatenated. Because it is one JSON object (not
// JSONL) the whole file is re-parsed each tick; Cursor.Count tracks how many user
// messages were already emitted so a re-parse only yields new ones (#65).
type geminiAdapter struct{}

type geminiFile struct {
	Messages []struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Content   []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
}

func (geminiAdapter) ParseNewUserMessages(path string, cur Cursor) ([]UserMessage, Cursor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cur, nil
		}
		return nil, cur, err
	}
	var gf geminiFile
	if json.Unmarshal(data, &gf) != nil {
		// Partial write mid-append — skip this tick, keep the cursor.
		return nil, cur, nil
	}
	var all []UserMessage
	for _, m := range gf.Messages {
		if m.Type != "user" {
			continue
		}
		var text string
		for _, c := range m.Content {
			text += c.Text
		}
		if text == "" {
			continue
		}
		all = append(all, UserMessage{Content: text, Timestamp: m.Timestamp})
	}
	if cur.Count >= len(all) {
		return nil, cur, nil
	}
	fresh := all[cur.Count:]
	return fresh, Cursor{Count: len(all)}, nil
}

func (geminiAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(cwd))
	dir := filepath.Join(home, ".gemini", "tmp", hex.EncodeToString(sum[:]), "chats")
	return newestJSONLAfter(dir, "session-*.json", spawnedAtUnixNano)
}
