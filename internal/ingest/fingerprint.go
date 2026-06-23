package ingest

import (
	"strings"
	"sync"

	"desktop/internal/sanitize"
)

// fingerprintStore remembers texts the app itself injected into a PTY (startup
// prompt, broadcast, prompt-send) so the watcher can suppress the copy the CLI
// records in its session file — otherwise the app's own bootstrap/broadcast
// would be logged as if the user typed it (#65). Each injection is consumable
// once: if the user later genuinely retypes the same text, the second occurrence
// is NOT suppressed. Safe for concurrent use (app records, watcher consumes).
type fingerprintStore struct {
	mu     sync.Mutex
	counts map[string]int
}

func newFingerprintStore() *fingerprintStore {
	return &fingerprintStore{counts: make(map[string]int)}
}

// Add records one self-injected text.
func (f *fingerprintStore) Add(text string) {
	key := normalizeFingerprint(text)
	if key == "" {
		return
	}
	f.mu.Lock()
	f.counts[key]++
	f.mu.Unlock()
}

// Consume reports whether text matches a still-unconsumed injection, decrementing
// its count when it does.
func (f *fingerprintStore) Consume(text string) bool {
	key := normalizeFingerprint(text)
	if key == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts[key] > 0 {
		f.counts[key]--
		if f.counts[key] == 0 {
			delete(f.counts, key)
		}
		return true
	}
	return false
}

// normalizeFingerprint canonicalizes text so a self-injection matches the copy the
// CLI records in its session file, despite the sanitization InjectText applies and
// the CLI's reformatting. It removes bracketed-paste markers, strips the runes
// InjectText also drops before writing to the PTY (invisible-format chars and
// non-whitespace control bytes — so a fingerprint of the ORIGINAL text still
// matches the CLI's SANITIZED copy, #65 / Codex round-5), and collapses all
// whitespace runs to single spaces.
func normalizeFingerprint(s string) string {
	s = strings.ReplaceAll(s, "\x1b[200~", "")
	s = strings.ReplaceAll(s, "\x1b[201~", "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == ' ' {
			return r // whitespace is kept here and collapsed by Fields below
		}
		if sanitize.IsControl(r) || sanitize.IsInvisibleFormat(r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
