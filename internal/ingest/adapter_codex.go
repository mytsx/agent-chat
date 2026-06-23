package ingest

import (
	"bufio"
	"encoding/json"
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
	lines, next, err := readCompleteJSONLines(path, cur.Offset)
	var out []UserMessage
	for _, line := range lines {
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
	return out, Cursor{Offset: next}, err
}

func (codexAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".codex", "sessions")
	spawn := time.Unix(0, spawnedAtUnixNano)
	cutoff := spawn.Add(-discoverSkew)
	// Check the spawn day and the previous day (around-midnight spawns). Among the
	// newest-after-spawn rollouts, pick the one whose session_meta.cwd matches THIS
	// terminal's cwd — otherwise a concurrent Codex session in another dir, written
	// just after spawn, could be ingested under the wrong agent (#65 / Codex P2).
	var best string
	var bestMod time.Time
	for _, day := range []time.Time{spawn, spawn.Add(-24 * time.Hour)} {
		dir := filepath.Join(base, day.Format("2006"), day.Format("01"), day.Format("02"))
		entries, derr := os.ReadDir(dir)
		if derr != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if ok, _ := filepath.Match("rollout-*.jsonl", e.Name()); !ok {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil || info.ModTime().Before(cutoff) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if codexFileCwd(p) != cwd {
				continue
			}
			if best == "" || info.ModTime().After(bestMod) {
				best, bestMod = p, info.ModTime()
			}
		}
	}
	return best, nil
}

// codexFileCwd returns the cwd recorded in a rollout's first line
// (session_meta.payload.cwd), or "" if it can't be read.
func codexFileCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadBytes('\n')
	var meta struct {
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(line, &meta)
	return meta.Payload.Cwd
}
