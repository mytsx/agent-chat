package ingest

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// claudeAdapter ingests Claude Code's JSONL session file:
// ~/.claude/projects/{slug(cwd)}/{uuid}.jsonl. A human message is a line with
// type=="user" whose message.content is a plain STRING; assistant output uses a
// content array, and tool results arrive as type=="user" lines whose content is
// an ARRAY of blocks — both are skipped (#65).
type claudeAdapter struct{}

type claudeLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func (claudeAdapter) ParseNewUserMessages(path string, cur Cursor) ([]UserMessage, Cursor, error) {
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
		consumed += int64(len(line)) + 1 // +1 for the newline the scanner stripped
		if len(line) == 0 {
			continue
		}
		var cl claudeLine
		if json.Unmarshal(line, &cl) != nil {
			continue // skip a corrupt/partial line, keep the rest
		}
		if cl.Type != "user" {
			continue
		}
		var content string
		// Only a JSON string content is a human-typed message; an array is a
		// tool_result envelope, not human text.
		if json.Unmarshal(cl.Message.Content, &content) != nil {
			continue
		}
		if content == "" {
			continue
		}
		out = append(out, UserMessage{Content: content, Timestamp: cl.Timestamp})
	}
	if err := sc.Err(); err != nil {
		return out, Cursor{Offset: consumed}, err
	}
	return out, Cursor{Offset: consumed}, nil
}

// claudeSlug turns a cwd into Claude's project folder name: every non-alphanumeric
// character becomes '-' (so a leading '/' yields a leading '-', and dots become
// dashes too). Matches Claude Code's path-slugify scheme.
func claudeSlug(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func (claudeAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".claude", "projects", claudeSlug(cwd))
	return newestJSONLAfter(dir, "*.jsonl", spawnedAtUnixNano)
}
