package ingest

import (
	"encoding/json"
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

func (claudeAdapter) ParseNewUserMessages(path string, cur Cursor) ([]ParsedMessage, Cursor, error) {
	lines, next, err := readCompleteJSONLines(path, cur.Offset)
	var out []ParsedMessage
	for _, line := range lines {
		var cl claudeLine
		if json.Unmarshal(line.Data, &cl) != nil {
			continue // skip a corrupt line, keep the rest
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
		out = append(out, ParsedMessage{Content: content, Timestamp: cl.Timestamp, After: Cursor{Offset: line.OffsetAfter}})
	}
	return out, Cursor{Offset: next}, err
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

func (claudeAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64, claimed func(string) bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".claude", "projects", claudeSlug(cwd))
	return newestJSONLAfter(dir, "*.jsonl", spawnedAtUnixNano, claimed)
}
