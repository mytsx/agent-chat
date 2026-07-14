package usage

import (
	"regexp"
)

// ansiSeq matches CSI/OSC escape sequences so a rate-limit phrase split by TUI
// color codes still matches after stripping.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// rateLimitPhrases are the narrow patterns that indicate a CLI actually hit its
// usage/rate limit. Deliberately tight so ordinary output ("limit parameter",
// "high rate", a bare "429" in a benchmark) does NOT match. Codex stays the
// authoritative source; this is a best-effort reactive fallback for the
// token-only CLIs whose files don't expose a denominator.
var rateLimitPhrases = regexp.MustCompile(`(?i)(rate limit(ed| exceeded| reached)?|usage limit reached|reached your [a-z ]*limit|429 too many|too many requests|quota (exceeded|exhausted))`)

// ScanRateLimitHit reports whether a PTY output chunk signals a hit rate/usage
// limit. It strips ANSI escapes first, then applies the narrow phrase set.
func ScanRateLimitHit(chunk string) bool {
	if chunk == "" {
		return false
	}
	clean := ansiSeq.ReplaceAllString(chunk, "")
	return rateLimitPhrases.MatchString(clean)
}
