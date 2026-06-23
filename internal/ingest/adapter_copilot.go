package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
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

func (copilotAdapter) ParseNewUserMessages(path string, cur Cursor) ([]ParsedMessage, Cursor, error) {
	lines, next, err := readCompleteJSONLines(path, cur.Offset)
	var out []ParsedMessage
	for _, line := range lines {
		var cl copilotLine
		if json.Unmarshal(line.Data, &cl) != nil {
			continue
		}
		if cl.Type != "user.message" || cl.Data.Content == "" {
			continue
		}
		out = append(out, ParsedMessage{Content: cl.Data.Content, Timestamp: cl.Timestamp, After: Cursor{Offset: line.OffsetAfter}})
	}
	return out, Cursor{Offset: next}, err
}

func (copilotAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64, claimed func(string) bool) (string, error) {
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
	// Each session is a {uuid}/ dir (NOT cwd-derived); pick the newest events.jsonl
	// at/after spawn whose workspace.yaml cwd matches THIS terminal — otherwise a
	// concurrent Copilot session in another dir could be ingested under the wrong
	// agent (#65 / Codex P2).
	cutoff := time.Unix(0, spawnedAtUnixNano).Add(-discoverSkew)
	var best string
	var bestMod time.Time
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(base, d.Name())
		if copilotWorkspaceCwd(dir) != cwd {
			continue
		}
		ev := filepath.Join(dir, "events.jsonl")
		info, serr := os.Stat(ev)
		if serr != nil || info.ModTime().Before(cutoff) {
			continue
		}
		if claimed != nil && claimed(ev) {
			continue // another terminal's watcher already locked this file (#65)
		}
		if best == "" || info.ModTime().After(bestMod) {
			best, bestMod = ev, info.ModTime()
		}
	}
	return best, nil
}

// copilotWorkspaceCwd returns the cwd recorded in a session dir's workspace.yaml
// (`cwd: <path>`), or "" if it can't be read. A minimal line parse avoids a YAML
// dependency.
func copilotWorkspaceCwd(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "workspace.yaml"))
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if rest, ok := strings.CutPrefix(ln, "cwd:"); ok {
			return strings.Trim(strings.TrimSpace(rest), `"'`)
		}
	}
	return ""
}
