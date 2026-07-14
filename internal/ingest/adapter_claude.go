package ingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"desktop/internal/usage"
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
	return parseCompleteJSONLUserMessages(path, cur, func(line []byte) (string, string, bool) {
		var cl claudeLine
		if json.Unmarshal(line, &cl) != nil {
			return "", "", false // skip a corrupt line, keep the rest
		}
		if cl.Type != "user" {
			return "", "", false
		}
		var content string
		// Only a JSON string content is a human-typed message; an array is a
		// tool_result envelope, not human text.
		if json.Unmarshal(cl.Message.Content, &content) != nil {
			return "", "", false
		}
		return content, cl.Timestamp, true
	})
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
	return nearestSessionFileAfter(dir, "*.jsonl", spawnedAtUnixNano, claimed)
}

// SessionID extracts Claude's session UUID from a discovered file path
// ({uuid}.jsonl → uuid), or "" for an empty path (#40).
func (claudeAdapter) SessionID(path string) string {
	if path == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

type claudeUsageLine struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseUsage sums per-message token usage across the transcript (no denominator
// exists — Claude's rateLimits field is null in practice) and returns the last
// model. (nil,nil) when no usage line is present.
func (claudeAdapter) ParseUsage(path string) (*usage.Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	snap := usage.Snapshot{CLI: "claude", Kind: usage.KindTokenCount}
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		// Fast-path: only lines carrying a usage block matter; skip unmarshaling the rest.
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"usage"`)) {
			continue
		}
		var cl claudeUsageLine
		if json.Unmarshal(line, &cl) != nil {
			continue
		}
		u := cl.Message.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
			continue
		}
		found = true
		snap.InputTokens += u.InputTokens
		snap.OutputTokens += u.OutputTokens
		snap.CacheTokens += u.CacheReadInputTokens + u.CacheCreationInputTokens
		if cl.Message.Model != "" {
			snap.Model = cl.Message.Model
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &snap, nil
}
