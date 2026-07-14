package ingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"desktop/internal/usage"
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
	for _, day := range []time.Time{spawn, spawn.Add(-24 * time.Hour), spawn.Add(24 * time.Hour)} {
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
			if claimed != nil && claimed(p) {
				continue // another terminal's watcher already locked this file (#65)
			}
			cands = append(cands, fileCandidate{path: p, mod: info.ModTime()})
		}
	}
	return pickNearestPostSpawn(cands, spawn), nil
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

// codexRateLimits mirrors the rate_limits block inside a token_count event_msg.
type codexRateLimits struct {
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
	PlanType  string       `json:"plan_type"`
}
type codexWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

// codexUsageLine decodes the payloads ParseUsage cares about: token_count (which
// carries rate_limits + total token usage) and thread_settings_applied (model).
type codexUsageLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type string `json:"type"`
		Info struct {
			TotalTokenUsage struct {
				InputTokens       int64 `json:"input_tokens"`
				CachedInputTokens int64 `json:"cached_input_tokens"`
				OutputTokens      int64 `json:"output_tokens"`
			} `json:"total_token_usage"`
		} `json:"info"`
		RateLimits     *codexRateLimits `json:"rate_limits"`
		ThreadSettings struct {
			Model string `json:"model"`
		} `json:"thread_settings"`
	} `json:"payload"`
}

// ParseUsage reads the whole rollout and returns the LAST rate_limits reading
// (authoritative used-percent per window), the last-seen model, and the last
// token totals. Returns (nil, nil) when no rate_limits line exists yet (a fresh
// session before the first API response). A missing file is (nil, nil) too.
func (codexAdapter) ParseUsage(path string) (*usage.Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	snap := usage.Snapshot{CLI: "codex", Kind: usage.KindPercentLimit}
	found := false
	// Reader-based scan (no line-size cap) mirrors readCompleteJSONLines: a
	// bufio.Scanner would abort with ErrTooLong on a record larger than its 8 MiB
	// buffer and lose every later (small) rate_limits line, silently freezing usage
	// tracking for the rest of the session (#10). ReadBytes has no such cap.
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadBytes('\n')
		// Process each complete line, plus the final partial chunk at EOF (a valid
		// last record with no trailing newline). ReadBytes keeps the '\n';
		// json.Unmarshal ignores trailing whitespace, so no trimming is needed.
		// Fast-path: only event_msg lines carry usage; skip unmarshaling the rest.
		if len(line) > 0 && bytes.Contains(line, []byte(`"event_msg"`)) {
			var cl codexUsageLine
			if json.Unmarshal(line, &cl) == nil && cl.Type == "event_msg" {
				switch cl.Payload.Type {
				case "thread_settings_applied":
					if cl.Payload.ThreadSettings.Model != "" {
						snap.Model = cl.Payload.ThreadSettings.Model
					}
				case "token_count":
					tu := cl.Payload.Info.TotalTokenUsage
					if tu.InputTokens != 0 || tu.OutputTokens != 0 {
						snap.InputTokens, snap.OutputTokens, snap.CacheTokens = tu.InputTokens, tu.OutputTokens, tu.CachedInputTokens
					}
					if rl := cl.Payload.RateLimits; rl != nil {
						found = true
						snap.Primary = toUsageWindow(rl.Primary)
						snap.Secondary = toUsageWindow(rl.Secondary)
						snap.PlanType = rl.PlanType
					}
				}
			}
		}
		if rerr != nil {
			// io.EOF: the final chunk (if any) was processed above. A genuine mid-
			// stream read error shouldn't wipe a good earlier snapshot — return what
			// we found; only surface the error when nothing was found.
			if rerr != io.EOF && !found {
				return nil, rerr
			}
			break
		}
	}
	if !found {
		return nil, nil
	}
	return &snap, nil
}

func toUsageWindow(w *codexWindow) *usage.Window {
	if w == nil {
		return nil
	}
	return &usage.Window{UsedPercent: w.UsedPercent, WindowMinutes: w.WindowMinutes, ResetsAt: w.ResetsAt}
}
