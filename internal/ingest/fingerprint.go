package ingest

import (
	"strings"
	"sync"
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

// normalizeFingerprint canonicalizes text so an injection matches the CLI's
// recorded copy despite reformatting: bracketed-paste markers are removed and all
// runs of whitespace collapse to single spaces, trimmed.
func normalizeFingerprint(s string) string {
	s = strings.ReplaceAll(s, "\x1b[200~", "")
	s = strings.ReplaceAll(s, "\x1b[201~", "")
	return strings.Join(strings.Fields(s), " ")
}
