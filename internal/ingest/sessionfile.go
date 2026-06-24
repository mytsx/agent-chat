package ingest

import (
	"os"
	"path/filepath"
	"time"
)

// SessionFilePath returns the on-disk transcript path for a captured session, used
// to enrich the session-history list with message count + snippet (#40 Faz-2).
// Claude/Copilot paths are derivable from cwd+id; Codex's filename embeds a
// timestamp before the uuid, so it is found by globbing for the uuid suffix. Only
// resume-captured CLIs (claude/copilot/codex) are supported — others return false.
// startUnix (the session's recorded start) narrows the Codex glob to that day ±1
// instead of scanning the whole sessions tree — ListAgentSessions calls this in a
// loop, so a whole-tree glob per session is a real I/O cost (Gemini). startUnix<=0
// falls back to the full-tree glob.
func SessionFilePath(cliType, cwd, sessionID string, startUnix float64) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	switch cliType {
	case "claude":
		return filepath.Join(home, ".claude", "projects", claudeSlug(cwd), sessionID+".jsonl"), true
	case "copilot":
		return filepath.Join(home, ".copilot", "session-state", sessionID, "events.jsonl"), true
	case "codex":
		base := filepath.Join(home, ".codex", "sessions")
		glob := "rollout-*-" + sessionID + ".jsonl"
		if startUnix > 0 {
			// The rollout's day-dir ≈ the session's start; check it plus adjacent days
			// for a near-midnight boundary (start captured ~1s after the rollout opened).
			day := time.Unix(int64(startUnix), 0)
			for _, d := range []time.Time{day, day.AddDate(0, 0, -1), day.AddDate(0, 0, 1)} {
				dir := filepath.Join(base, d.Format("2006"), d.Format("01"), d.Format("02"))
				if m, _ := filepath.Glob(filepath.Join(dir, glob)); len(m) > 0 {
					return m[0], true
				}
			}
			return "", false
		}
		if m, _ := filepath.Glob(filepath.Join(base, "*", "*", "*", glob)); len(m) > 0 {
			return m[0], true
		}
		return "", false
	default:
		return "", false
	}
}

// SessionStats returns the human user-message count and a short first-message
// snippet (≤80 runes) for a session file, reusing the CLI adapter's parser. An
// unknown CLI or unreadable file yields (0, "").
func SessionStats(cliType, path string) (int, string) {
	ad := AdapterFor(cliType)
	if ad == nil {
		return 0, ""
	}
	msgs, _, _ := ad.ParseNewUserMessages(path, Cursor{})
	// Drop the app-injected startup/bootstrap prompt (sendStartupPrompt / Copilot -i).
	// The live ingester suppresses it via RecordInjection, but this raw reparse can't —
	// and it is always the FIRST user message of an app-created agent session, so the
	// picker would otherwise show the join/bootstrap prompt as the "first message" and
	// inflate the count by one. Dropping msgs[0] yields the user's real first message;
	// a session with only the bootstrap reports 0 / "" (Codex P2). Rare caveat: a prompt
	// typed before the ~3s startup injection is dropped instead.
	if len(msgs) > 0 {
		msgs = msgs[1:]
	}
	if len(msgs) == 0 {
		return 0, ""
	}
	snippet := msgs[0].Content
	if r := []rune(snippet); len(r) > 80 {
		snippet = string(r[:80])
	}
	return len(msgs), snippet
}
