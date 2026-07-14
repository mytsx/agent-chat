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

func (codexAdapter) ParseNewUserMessages(path string, cur Cursor) ([]ParsedMessage, Cursor, error) {
	return parseCompleteJSONLUserMessages(path, cur, func(line []byte) (string, string, bool) {
		var cl codexLine
		if json.Unmarshal(line, &cl) != nil {
			return "", "", false
		}
		switch {
		case cl.Type == "event_msg" && cl.Payload.Type == "user_message" && cl.Payload.Message != "":
			return cl.Payload.Message, cl.Timestamp, true
		case cl.Type == "message" && cl.Role == "user":
			var s string
			if json.Unmarshal(cl.Content, &s) == nil && s != "" {
				return s, cl.Timestamp, true
			}
		}
		return "", "", false
	})
}

func (codexAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64, claimed func(string) bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".codex", "sessions")
	spawn := time.Unix(0, spawnedAtUnixNano)
	cutoff := spawn.Add(-discoverSkew)
	// Check the spawn day plus the adjacent days: the previous day for a just-after-
	// midnight spawn whose file the CLI dated to the prior day, and the NEXT day
	// because spawnedAt is captured before pty.Start, so a just-before-midnight spawn
	// can have its rollout created under tomorrow's dir (#65 / Codex P3). Among the
	// newest-after-spawn rollouts, pick the one whose session_meta.cwd matches THIS
	// terminal and isn't already locked by another watcher (#65 / Codex P2).
	// Collect matching unclaimed candidates across the days, then pick the one
	// nearest to (and preferably after) spawn — so sibling same-cwd terminals each
	// lock onto their own rollout and a quick restart can't grab the old file (#65).
	var cands []fileCandidate
	var readErr error
	for _, day := range []time.Time{spawn, spawn.Add(-24 * time.Hour), spawn.Add(24 * time.Hour)} {
		dir := filepath.Join(base, day.Format("2006"), day.Format("01"), day.Format("02"))
		entries, derr := os.ReadDir(dir)
		if derr != nil {
			// A missing day dir is normal; surface any other read error only if no
			// candidate is found, so a transient FS error isn't silently swallowed.
			if !os.IsNotExist(derr) && readErr == nil {
				readErr = derr
			}
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
			if ierr != nil || !info.Mode().IsRegular() || info.ModTime().Before(cutoff) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if codexFileCwd(p) != cwd {
				continue
			}
			if claimed != nil && claimed(p) {
				continue // another terminal's watcher already locked this file (#65)
			}
			cands = append(cands, fileCandidate{path: p, mod: info.ModTime()})
		}
	}
	if p := pickNearestPostSpawn(cands, spawn); p != "" {
		return p, nil
	}
	return "", readErr
}

// SessionID extracts Codex's session UUID from a rollout's session_meta first
// line (payload.id) (#40). The filename embeds the uuid too, but its leading
// timestamp also contains '-', so reading session_meta is robust where
// filename-splitting is fragile.
func (codexAdapter) SessionID(path string) string {
	return codexFileID(path)
}

// codexFileID returns the session id recorded in a rollout's first line
// (session_meta.payload.id), or "" if it can't be read.
func codexFileID(path string) string {
	return readCodexFileMeta(path).id
}

// codexFileCwd returns the cwd recorded in a rollout's first line
// (session_meta.payload.cwd), or "" if it can't be read.
func codexFileCwd(path string) string {
	return readCodexFileMeta(path).cwd
}

type codexFileMeta struct {
	id  string
	cwd string
}

func readCodexFileMeta(path string) codexFileMeta {
	f, err := os.Open(path)
	if err != nil {
		return codexFileMeta{}
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadBytes('\n')
	var meta struct {
		Payload struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(line, &meta)
	return codexFileMeta{id: meta.Payload.ID, cwd: meta.Payload.Cwd}
}
