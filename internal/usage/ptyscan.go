package usage

import (
	"regexp"
	"strings"
)

// ansiSeq matches CSI/OSC escape sequences so a rate-limit phrase split by TUI
// color codes still matches after stripping.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// rateLimitPhrases are the narrow patterns that indicate a CLI actually hit its
// usage/rate limit. Deliberately tight so ordinary output ("limit parameter",
// "high rate", a bare "429" in a benchmark, a bare "rate limit" doc mention)
// does NOT match — the suffix is required, so an actual hit/reached/exceeded
// phrasing must be present. Codex stays the authoritative source; this is a
// best-effort reactive fallback for the token-only CLIs whose files don't
// expose a denominator.
var rateLimitPhrases = regexp.MustCompile(`(?i)(rate limited|rate limits? (exceeded|reached|hit)|hit (your )?rate limit|reached your [a-z ]*limit|usage limit reached|429 too many|too many requests|quota (exceeded|exhausted))`)

// ScanRateLimitHit reports whether a PTY output chunk signals a hit rate/usage
// limit. PTY output is a hot path, so a cheap lowercase substring gate runs
// BEFORE the ANSI-strip + regex — every regex alternative contains one of
// "limit"/"429"/"request"/"quota". (A rare "limit" split mid-word by an ANSI
// code would be missed by the gate — acceptable, this signal is best-effort
// with Codex authoritative.)
func ScanRateLimitHit(chunk string) bool {
	if chunk == "" {
		return false
	}
	lower := strings.ToLower(chunk)
	if !strings.Contains(lower, "limit") && !strings.Contains(lower, "429") &&
		!strings.Contains(lower, "request") && !strings.Contains(lower, "quota") {
		return false
	}
	clean := ansiSeq.ReplaceAllString(chunk, "")
	return rateLimitPhrases.MatchString(clean)
}
