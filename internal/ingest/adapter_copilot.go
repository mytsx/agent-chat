package ingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"desktop/internal/usage"
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
	return parseCompleteJSONLUserMessages(path, cur, func(line []byte) (string, string, bool) {
		var cl copilotLine
		if json.Unmarshal(line, &cl) != nil {
			return "", "", false
		}
		if cl.Type != "user.message" || cl.Data.Content == "" {
			return "", "", false
		}
		return cl.Data.Content, cl.Timestamp, true
	})
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
	spawn := time.Unix(0, spawnedAtUnixNano)
	cutoff := spawn.Add(-discoverSkew)
	var cands []fileCandidate
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
		if serr != nil || !info.Mode().IsRegular() || info.ModTime().Before(cutoff) {
			continue
		}
		if claimed != nil && claimed(ev) {
			continue // another terminal's watcher already locked this file (#65)
		}
		cands = append(cands, fileCandidate{path: ev, mod: info.ModTime()})
	}
	// Nearest-to-spawn (post-spawn preferred), so sibling same-cwd terminals each
	// lock onto their own session and a quick restart can't grab the old one (#65).
	return pickNearestPostSpawn(cands, spawn), nil
}

// SessionID extracts Copilot's session UUID from a discovered events.jsonl path
// ({uuid}/events.jsonl → {uuid}), or "" for an empty path (#40).
func (copilotAdapter) SessionID(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(filepath.Dir(path))
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

type copilotUsageLine struct {
	Type string `json:"type"`
	Data struct {
		Usage struct {
			InputTokens     int64 `json:"inputTokens"`
			OutputTokens    int64 `json:"outputTokens"`
			CacheReadTokens int64 `json:"cacheReadTokens"`
		} `json:"usage"`
		CurrentModel string `json:"currentModel"`
	} `json:"data"`
}

// ParseUsage reads the LAST line carrying a usage block (Copilot fills usage on
// session.shutdown, and some turn events) and returns its token totals + model.
// (nil,nil) when no usage is present yet (live session mid-turn).
func (copilotAdapter) ParseUsage(path string) (*usage.Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	snap := usage.Snapshot{CLI: "copilot", Kind: usage.KindTokenCount}
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		// Fast-path: only lines carrying a usage block matter; skip unmarshaling the rest.
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"usage"`)) {
			continue
		}
		var cl copilotUsageLine
		if json.Unmarshal(line, &cl) != nil {
			continue
		}
		u := cl.Data.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 {
			continue
		}
		found = true
		snap.InputTokens, snap.OutputTokens, snap.CacheTokens = u.InputTokens, u.OutputTokens, u.CacheReadTokens
		if cl.Data.CurrentModel != "" {
			snap.Model = cl.Data.CurrentModel
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
