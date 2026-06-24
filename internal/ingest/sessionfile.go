package ingest

import (
	"os"
	"path/filepath"
)

// SessionFilePath returns the on-disk transcript path for a captured session, used
// to enrich the session-history list with message count + snippet (#40 Faz-2).
// Claude/Copilot paths are derivable from cwd+id; Codex's filename embeds a
// timestamp before the uuid, so it is found by globbing for the uuid suffix. Only
// resume-captured CLIs (claude/copilot/codex) are supported — others return false.
func SessionFilePath(cliType, cwd, sessionID string) (string, bool) {
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
		matches, _ := filepath.Glob(filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*-"+sessionID+".jsonl"))
		if len(matches) > 0 {
			return matches[0], true
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
	if len(msgs) == 0 {
		return 0, ""
	}
	snippet := msgs[0].Content
	if r := []rune(snippet); len(r) > 80 {
		snippet = string(r[:80])
	}
	return len(msgs), snippet
}
