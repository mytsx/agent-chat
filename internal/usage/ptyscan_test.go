package usage

import "testing"

func TestScanRateLimitHit(t *testing.T) {
	hits := []string{
		"Error: you have hit your rate limit, try again later",
		"\x1b[31mUsage limit reached\x1b[0m for this model",
		"HTTP 429 Too Many Requests",
		"You've reached your usage limit",
		// Mixed-case hit: proves containsFoldASCII + the (?i) regex handle arbitrary
		// case. A fixed set of lowercase/uppercase variants would miss "RaTe LiMiTeD",
		// which is exactly why we fold case instead of listing variants.
		"RaTe LiMiTeD now",
		// Real Gemini 429 wording: "quota" comes AFTER "exceeded your ... quota".
		"You exceeded your current quota",
		// Real Gemini 429 wording: gRPC RESOURCE_EXHAUSTED style.
		"Resource has been exhausted (e.g. check quota)",
		// The three canonical Google/gRPC "resource exhausted" forms WITHOUT the word
		// "quota" — so they exercise the cheap fast-path gate (which now includes
		// "exhaust") as well as the regex. These prove the pattern
		// `resource( has been)?[ _]?exhausted` matches all three spellings:
		//   "resource has been exhausted" → resource + " has been" + " " + exhausted
		//   "resource exhausted"          → resource + ""          + " " + exhausted
		//   "resource_exhausted"          → resource + ""          + "_" + exhausted
		// Before this fix the gate returned false (no quota/limit/429/request word) and
		// the regex missed "resource exhausted" (single space, no "has been").
		"Resource has been exhausted",
		"Resource exhausted",
		"RESOURCE_EXHAUSTED",
	}
	for _, s := range hits {
		if !ScanRateLimitHit([]byte(s)) {
			t.Errorf("beklenen hit yakalanmadı: %q", s)
		}
	}
	misses := []string{
		"",
		"Running tests at a high rate, all passed",
		"The limit parameter defaults to 15",
		"rate of change is 429 units/sec in this benchmark",
		"go test ./... limit reached? no, just checking",
		"the rate limit parameter defaults to 100",
	}
	for _, s := range misses {
		if ScanRateLimitHit([]byte(s)) {
			t.Errorf("yanlış-pozitif: %q", s)
		}
	}
}
