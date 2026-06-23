package ingest

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// codexAdapter ingests OpenAI Codex CLI's "rollout" JSONL:
// ~/.codex/sessions/Y/M/D/rollout-{ts}-{uuid}.jsonl. Two schemas exist (#65):
//   - new (2026): {timestamp,type,payload}; human msg = type"event_msg" +
//     payload.type"user_message" + payload.message. The same turn also appears as
//     a response_item — ignored to avoid double-counting.
//   - old (2025): {type"message",role"user",content:"..."} (string content).
type codexAdapter struct{}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Role      string          `json:"role"`    // old format
	Content   json.RawMessage `json:"content"` // old format (string)
	Payload   struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"payload"` // new format
}

func (codexAdapter) ParseNewUserMessages(path string, cur Cursor) ([]UserMessage, Cursor, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cur, nil
		}
		return nil, cur, err
	}
	defer f.Close()
	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return nil, cur, err
	}
	var out []UserMessage
	consumed := cur.Offset
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		consumed += int64(len(line)) + 1
		if len(line) == 0 {
			continue
		}
		var cl codexLine
		if json.Unmarshal(line, &cl) != nil {
			continue
		}
		switch {
		case cl.Type == "event_msg" && cl.Payload.Type == "user_message" && cl.Payload.Message != "":
			out = append(out, UserMessage{Content: cl.Payload.Message, Timestamp: cl.Timestamp})
		case cl.Type == "message" && cl.Role == "user":
			var s string
			if json.Unmarshal(cl.Content, &s) == nil && s != "" {
				out = append(out, UserMessage{Content: s, Timestamp: cl.Timestamp})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return out, Cursor{Offset: consumed}, err
	}
	return out, Cursor{Offset: consumed}, nil
}

func (codexAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".codex", "sessions")
	spawn := time.Unix(0, spawnedAtUnixNano)
	// Check the spawn day and the previous day (around-midnight spawns).
	var best string
	var bestMod int64
	for _, day := range []time.Time{spawn, spawn.Add(-24 * time.Hour)} {
		dir := filepath.Join(base, day.Format("2006"), day.Format("01"), day.Format("02"))
		p, derr := newestJSONLAfter(dir, "rollout-*.jsonl", spawnedAtUnixNano)
		if derr != nil || p == "" {
			continue
		}
		info, serr := os.Stat(p)
		if serr != nil {
			continue
		}
		if best == "" || info.ModTime().UnixNano() > bestMod {
			best, bestMod = p, info.ModTime().UnixNano()
		}
	}
	return best, nil
}
