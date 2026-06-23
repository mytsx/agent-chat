package ingest

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// copilotAdapter ingests GitHub Copilot CLI's JSONL transcript:
// ~/.copilot/session-state/{uuid}/events.jsonl. A human message is a line with
// type=="user.message"; data.content is the verbatim typed text (data.
// transformedContent is the model-facing augmented version — NOT used) (#65).
type copilotAdapter struct{}

type copilotLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Data      struct {
		Content string `json:"content"`
	} `json:"data"`
}

func (copilotAdapter) ParseNewUserMessages(path string, cur Cursor) ([]UserMessage, Cursor, error) {
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
		var cl copilotLine
		if json.Unmarshal(line, &cl) != nil {
			continue
		}
		if cl.Type != "user.message" || cl.Data.Content == "" {
			continue
		}
		out = append(out, UserMessage{Content: cl.Data.Content, Timestamp: cl.Timestamp})
	}
	if err := sc.Err(); err != nil {
		return out, Cursor{Offset: consumed}, err
	}
	return out, Cursor{Offset: consumed}, nil
}

func (copilotAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".copilot", "session-state")
	dirs, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	// Each session is a {uuid}/ dir; pick the newest events.jsonl at/after spawn.
	var best string
	var bestMod int64
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		p, derr := newestJSONLAfter(filepath.Join(base, d.Name()), "events.jsonl", spawnedAtUnixNano)
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
