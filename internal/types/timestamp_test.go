package types

import (
	"testing"
	"time"
)

// Ingested CLI timestamps are RFC3339/UTC (e.g. Claude "2026-06-23T10:00:00.000Z"),
// but the room transcript sorts message Timestamp strings lexicographically and
// every hub/app message uses the local-time, no-Z, microsecond Timestamp() layout.
// NormalizeTimestamp must convert a CLI timestamp into that exact layout (local
// time) so an ingested message sorts correctly against hub messages — otherwise a
// UTC+3 user's typed message lands ~3h early in the transcript (#65).
func TestNormalizeTimestamp_ConvertsRFC3339ToLocalLayout(t *testing.T) {
	const in = "2026-06-23T10:00:00.000Z"
	got := NormalizeTimestamp(in)

	// Expected: the SAME instant rendered in the canonical local layout.
	want := time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC).Local().Format("2006-01-02T15:04:05.000000")
	if got != want {
		t.Fatalf("NormalizeTimestamp(%q) = %q, want %q (local, canonical layout)", in, got, want)
	}
	// And it must be in the same lexical shape as Timestamp() (no 'Z', 6 frac digits).
	if len(got) != len("2006-01-02T15:04:05.000000") {
		t.Fatalf("normalized %q has wrong width for the canonical layout", got)
	}
}

func TestNormalizeTimestamp_FallsBackWhenUnparseable(t *testing.T) {
	got := NormalizeTimestamp("not-a-timestamp")
	// Fallback is a freshly-stamped canonical timestamp (consistent format, near
	// real time) — never the raw unparseable string.
	if got == "not-a-timestamp" || len(got) != len("2006-01-02T15:04:05.000000") {
		t.Fatalf("unparseable input must fall back to a canonical stamp, got %q", got)
	}
}

func TestNormalizeTimestamp_EmptyFallsBack(t *testing.T) {
	if got := NormalizeTimestamp(""); len(got) != len("2006-01-02T15:04:05.000000") {
		t.Fatalf("empty input must fall back to a canonical stamp, got %q", got)
	}
}
