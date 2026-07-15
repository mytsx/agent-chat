package usage

import "regexp"

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
//
// `exceeded your [a-z ]*quota` and `resource( has been)?[ _]?exhausted` cover the
// real Gemini 429 wording where "quota" comes AFTER ("You exceeded your current
// quota") or the gRPC RESOURCE_EXHAUSTED phrasing — neither of which the
// `quota (exceeded|exhausted)` order matched (#10). The `resource...exhausted`
// alternative matches all three canonical Google/gRPC spellings: "Resource
// exhausted", "Resource has been exhausted", and "RESOURCE_EXHAUSTED"
// (the optional `[ _]?` separator covers the single-space and underscore forms
// that a plain " has been " join missed).
var rateLimitPhrases = regexp.MustCompile(`(?i)(rate limited|rate limits? (exceeded|reached|hit)|hit (your )?rate limit|reached your [a-z ]*limit|usage limit reached|429 too many|too many requests|quota (exceeded|exhausted)|exceeded your [a-z ]*quota|resource( has been)?[ _]?exhausted)`)

// containsFoldASCII reports whether b contains substr using ASCII case-insensitive
// matching, with no allocation (unlike strings.ToLower + Contains). substr must be
// lowercase ASCII. O(len(b)*len(substr)) but substr is a short keyword.
func containsFoldASCII(b []byte, substr string) bool {
	n := len(substr)
	if n == 0 {
		return true
	}
	for i := 0; i+n <= len(b); i++ {
		ok := true
		for j := 0; j < n; j++ {
			c := b[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != substr[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// ScanRateLimitHit reports whether a PTY output chunk signals a hit rate/usage
// limit. PTY output is a hot path, so a cheap allocation-free keyword check runs
// BEFORE the ANSI-strip + regex — every regex alternative contains one of
// "limit"/"429"/"request"/"quota"/"exhaust" ("exhaust" is a substring of
// "exhausted"/"exhaustion", so the resource-exhausted alternative passes the gate
// even when no "quota" accompanies it). Taking []byte and folding case in place
// avoids the string(data) + strings.ToLower allocations on the ~99.9% of chunks
// with no trigger word. (A rare "limit" split mid-word by an ANSI code would be
// missed by the gate — acceptable, this signal is best-effort with Codex
// authoritative.)
func ScanRateLimitHit(chunk []byte) bool {
	if len(chunk) == 0 {
		return false
	}
	if !containsFoldASCII(chunk, "limit") && !containsFoldASCII(chunk, "429") &&
		!containsFoldASCII(chunk, "request") && !containsFoldASCII(chunk, "quota") &&
		!containsFoldASCII(chunk, "exhaust") {
		return false
	}
	clean := ansiSeq.ReplaceAll(chunk, nil)
	return rateLimitPhrases.Match(clean)
}
