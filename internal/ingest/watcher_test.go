package ingest

import (
	"sync"
	"testing"
)

// fakeAdapter ignores cwd/spawn and parses a preset slice once per call.
type fakeAdapter struct {
	batches [][]UserMessage // returned successively per pollOnce call
	calls   int
}

func (f *fakeAdapter) DiscoverFile(string, int64) (string, error) { return "fake", nil }
func (f *fakeAdapter) ParseNewUserMessages(_ string, cur Cursor) ([]UserMessage, Cursor, error) {
	i := f.calls
	f.calls++
	if i < len(f.batches) {
		return f.batches[i], Cursor{Count: cur.Count + len(f.batches[i])}, nil
	}
	return nil, cur, nil
}

func TestPollOnce_EmitsUnsuppressedSuppressesInjections(t *testing.T) {
	ad := &fakeAdapter{batches: [][]UserMessage{{
		{Content: "startup prompt metni", Timestamp: "t0"},
		{Content: "kullanıcı elle yazdı", Timestamp: "t1"},
	}}}
	fp := newFingerprintStore()
	fp.Add("startup prompt metni") // the app injected this — must be suppressed

	var mu sync.Mutex
	var emitted []string
	emit := func(content, ts string) {
		mu.Lock()
		emitted = append(emitted, content)
		mu.Unlock()
	}

	pollOnce(ad, "fake", Cursor{}, fp, emit)

	if len(emitted) != 1 || emitted[0] != "kullanıcı elle yazdı" {
		t.Fatalf("emitted = %v, want only the directly-typed message", emitted)
	}
}

func TestPollOnce_AdvancesCursor(t *testing.T) {
	ad := &fakeAdapter{batches: [][]UserMessage{
		{{Content: "bir", Timestamp: "t1"}},
		{{Content: "iki", Timestamp: "t2"}},
	}}
	fp := newFingerprintStore()
	var got []string
	emit := func(c, _ string) { got = append(got, c) }

	cur := pollOnce(ad, "fake", Cursor{}, fp, emit)
	cur = pollOnce(ad, "fake", cur, fp, emit)

	if len(got) != 2 || got[0] != "bir" || got[1] != "iki" {
		t.Fatalf("got %v, want [bir iki] across two polls (cursor advanced)", got)
	}
}
