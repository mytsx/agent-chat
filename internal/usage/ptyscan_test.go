package usage

import "testing"

func TestScanRateLimitHit(t *testing.T) {
	hits := []string{
		"Error: you have hit your rate limit, try again later",
		"\x1b[31mUsage limit reached\x1b[0m for this model",
		"HTTP 429 Too Many Requests",
		"You've reached your usage limit",
	}
	for _, s := range hits {
		if !ScanRateLimitHit(s) {
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
		if ScanRateLimitHit(s) {
			t.Errorf("yanlış-pozitif: %q", s)
		}
	}
}
