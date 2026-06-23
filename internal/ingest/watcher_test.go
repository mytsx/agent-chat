package ingest

import (
	"sync"
	"testing"
)

// fakeAdapter returns a preset batch of ParsedMessages once per ParseNewUserMessages
// call (successive calls return successive batches, then nothing).
type fakeAdapter struct {
	batches [][]ParsedMessage
	calls   int
	sessID  string
}

func (f *fakeAdapter) DiscoverFile(string, int64, func(string) bool) (string, error) {
	return "fake", nil
}
func (f *fakeAdapter) SessionID(string) string { return f.sessID }
func (f *fakeAdapter) ParseNewUserMessages(_ string, cur Cursor) ([]ParsedMessage, Cursor, error) {
	i := f.calls
	f.calls++
	if i < len(f.batches) && len(f.batches[i]) > 0 {
		b := f.batches[i]
		return b, b[len(b)-1].After, nil
	}
	return nil, cur, nil
}

func pm(content string, off int64) ParsedMessage {
	return ParsedMessage{Content: content, Timestamp: "t", After: Cursor{Offset: off}}
}

// truthyEmit records content and always reports delivered.
func truthyEmit(rec *[]string, mu *sync.Mutex) EmitFunc {
	return func(content, _ string) bool {
		mu.Lock()
		*rec = append(*rec, content)
		mu.Unlock()
		return true
	}
}

func TestPollOnce_EmitsUnsuppressedSuppressesInjections(t *testing.T) {
	ad := &fakeAdapter{batches: [][]ParsedMessage{{
		pm("startup prompt metni", 1),
		pm("kullanıcı elle yazdı", 2),
	}}}
	fp := newFingerprintStore()
	fp.Add("startup prompt metni") // app injected this — must be suppressed

	var mu sync.Mutex
	var emitted []string
	cur := pollOnce(ad, "fake", Cursor{}, fp, truthyEmit(&emitted, &mu))

	if len(emitted) != 1 || emitted[0] != "kullanıcı elle yazdı" {
		t.Fatalf("emitted = %v, want only the directly-typed message", emitted)
	}
	if cur.Offset != 2 {
		t.Fatalf("cursor = %d, want 2 (advanced past both handled messages)", cur.Offset)
	}
}

func TestPollOnce_AdvancesCursor(t *testing.T) {
	ad := &fakeAdapter{batches: [][]ParsedMessage{
		{pm("bir", 1)},
		{pm("iki", 2)},
	}}
	fp := newFingerprintStore()
	var mu sync.Mutex
	var got []string
	emit := truthyEmit(&got, &mu)

	cur := pollOnce(ad, "fake", Cursor{}, fp, emit)
	cur = pollOnce(ad, "fake", cur, fp, emit)

	if len(got) != 2 || got[0] != "bir" || got[1] != "iki" {
		t.Fatalf("got %v, want [bir iki] across two polls (cursor advanced)", got)
	}
}

// When an emit FAILS (hub unavailable mid-RPC), the cursor must NOT advance past
// the failed message — it is retried next tick rather than silently lost (#65).
func TestPollOnce_EmitFailureKeepsCursorBeforeMessage(t *testing.T) {
	ad := &fakeAdapter{batches: [][]ParsedMessage{{
		pm("teslim edilen", 1),
		pm("teslim edilemeyen", 2),
	}}}
	fp := newFingerprintStore()

	var emitted []string
	failingEmit := func(content, _ string) bool {
		if content == "teslim edilemeyen" {
			return false // hub down for this one
		}
		emitted = append(emitted, content)
		return true
	}

	cur := pollOnce(ad, "fake", Cursor{}, fp, failingEmit)

	if len(emitted) != 1 || emitted[0] != "teslim edilen" {
		t.Fatalf("emitted = %v, want only the delivered one", emitted)
	}
	if cur.Offset != 1 {
		t.Fatalf("cursor = %d, want 1 (must stay before the un-delivered message for retry)", cur.Offset)
	}
}

// Two watchers in the same cwd must not both lock onto the same discovered file:
// tryClaim is exclusive, and isClaimedByOther reports the lock to siblings (#65).
func TestManager_ClaimsAreExclusive(t *testing.T) {
	m := New()
	if !m.tryClaim("sessA", "/path/file.jsonl") {
		t.Fatal("first claim must succeed")
	}
	if m.tryClaim("sessB", "/path/file.jsonl") {
		t.Fatal("a second session must NOT claim a file already owned by another")
	}
	if !m.isClaimedByOther("sessB", "/path/file.jsonl") {
		t.Fatal("sessB must see the file as claimed-by-other")
	}
	if m.isClaimedByOther("sessA", "/path/file.jsonl") {
		t.Fatal("the owner must NOT see its own claim as claimed-by-other")
	}
	// Releasing sessA's claims frees the file for sessB.
	m.mu.Lock()
	m.releaseClaims("sessA")
	m.mu.Unlock()
	if !m.tryClaim("sessB", "/path/file.jsonl") {
		t.Fatal("after release, sessB must be able to claim the file")
	}
}
