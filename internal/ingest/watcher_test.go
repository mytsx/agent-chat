package ingest

import (
	"sync"
	"testing"
	"time"
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

// The graceful-exit command the app injects into a terminal's PTY on close
// (/exit, /quit) must never be emitted as a room prompt: a CLI that records it in
// its transcript would otherwise pollute the room history and session summaries
// with a shutdown keystroke (Codex PR #76 P2). It carries no self-injection
// fingerprint, so pollOnce recognizes it by exact content instead.
func TestPollOnce_SuppressesGracefulExitCommand(t *testing.T) {
	ad := &fakeAdapter{batches: [][]ParsedMessage{{
		pm("gerçek kullanıcı mesajı", 1),
		pm("/exit", 2), // app-injected graceful-exit command on Close()/CloseAll()
	}}}
	fp := newFingerprintStore()

	var mu sync.Mutex
	var emitted []string
	cur := pollOnce(ad, "fake", Cursor{}, fp, truthyEmit(&emitted, &mu))

	if len(emitted) != 1 || emitted[0] != "gerçek kullanıcı mesajı" {
		t.Fatalf("emitted = %v, want only the real user message (exit command suppressed)", emitted)
	}
	if cur.Offset != 2 {
		t.Fatalf("cursor = %d, want 2 (advanced past the suppressed exit command)", cur.Offset)
	}
}

func TestIsCLIExitCommand(t *testing.T) {
	tests := []struct {
		content string
		want    bool
	}{
		{"/exit", true},        // claude/copilot/codex
		{"/quit", true},        // gemini
		{"exit", false},        // shell exit isn't ingested (no adapter); a real AI "exit" prompt must pass through (Codex PR #77)
		{"  /exit  ", true},    // surrounding whitespace trimmed
		{"/exit şimdi", false}, // more than the bare command
		{"ne yapar /exit?", false},
		{"/exithch", false},
		{"", false},
		{"gerçek kullanıcı mesajı", false},
	}
	for _, tt := range tests {
		if got := isCLIExitCommand(tt.content); got != tt.want {
			t.Errorf("isCLIExitCommand(%q) = %v, want %v", tt.content, got, tt.want)
		}
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

func TestStartSession_FiresOnSessionID(t *testing.T) {
	m := New()
	ad := &fakeAdapter{sessID: "fake-id"}
	got := make(chan string, 1)
	m.StartSession("s1", ad, "cwd", 0, nil, nil,
		func(string, string) bool { return true },
		func(id string) {
			select {
			case got <- id:
			default:
			}
		}, nil, nil)
	defer m.StopSession("s1")

	select {
	case id := <-got:
		if id != "fake-id" {
			t.Fatalf("onSessionID = %q, want fake-id", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onSessionID not fired within 2s")
	}
}

// offsetAdapter parses by byte offset like the real JSONL adapters: it returns
// only messages whose After.Offset is past the current cursor, so a ResumeSeed
// cursor actually gates what gets emitted (the fakeAdapter ignores the cursor).
type offsetAdapter struct {
	path string
	msgs []ParsedMessage // After.Offset strictly increasing
}

func (a *offsetAdapter) DiscoverFile(string, int64, func(string) bool) (string, error) {
	return a.path, nil
}
func (a *offsetAdapter) SessionID(string) string { return "" }
func (a *offsetAdapter) ParseNewUserMessages(_ string, cur Cursor) ([]ParsedMessage, Cursor, error) {
	final := cur
	var out []ParsedMessage
	for _, m := range a.msgs {
		if m.After.Offset > cur.Offset {
			out = append(out, m)
		}
		if m.After.Offset > final.Offset {
			final = m.After
		}
	}
	return out, final, nil
}

func waitForEmits(t *testing.T, got *[]string, mu *sync.Mutex, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		c := len(*got)
		mu.Unlock()
		if c >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// On RESUME the watcher must skip content already in the SAME transcript the CLI
// appends to (Copilot): a ResumeSeed whose Path matches the discovered file starts
// ingestion past the snapshotted offset, so the prior conversation is not re-logged
// — only messages appended after the seed are emitted (#40, Codex P1).
func TestStartSession_ResumeSeed_SkipsExistingOnSameFile(t *testing.T) {
	m := New()
	ad := &offsetAdapter{path: "events.jsonl", msgs: []ParsedMessage{
		pm("old-1", 1), pm("old-2", 2), pm("new-after-resume", 3),
	}}
	var mu sync.Mutex
	var got []string
	// Seed past offset 2 (old-1, old-2) on the SAME discovered path.
	m.StartSession("s1", ad, "cwd", 0, nil, nil, truthyEmit(&got, &mu), nil, nil,
		&ResumeSeed{Path: "events.jsonl", Cur: Cursor{Offset: 2}})
	defer m.StopSession("s1")

	waitForEmits(t, &got, &mu, 1)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "new-after-resume" {
		t.Fatalf("resume seed: emitted %v, want only [new-after-resume] (pre-resume transcript must be skipped)", got)
	}
}

// A ResumeSeed for a DIFFERENT path than the one discovered must be ignored: a CLI
// that resumes into a NEW file (Claude/Codex) has a fresh transcript with nothing
// to skip, so every message is ingested from offset 0 (#40, Codex P2-round2).
func TestStartSession_ResumeSeed_IgnoredOnDifferentFile(t *testing.T) {
	m := New()
	ad := &offsetAdapter{path: "new-file.jsonl", msgs: []ParsedMessage{
		pm("m1", 1), pm("m2", 2), pm("m3", 3),
	}}
	var mu sync.Mutex
	var got []string
	// Seed references the OLD file; discovery returns a different (new) file.
	m.StartSession("s1", ad, "cwd", 0, nil, nil, truthyEmit(&got, &mu), nil, nil,
		&ResumeSeed{Path: "old-file.jsonl", Cur: Cursor{Offset: 2}})
	defer m.StopSession("s1")

	waitForEmits(t, &got, &mu, 3)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("resume seed on different file: emitted %v, want all 3 (new file has nothing to skip)", got)
	}
}

// StopAndWait must stop the watcher, wait for its final drain, and release its
// file claim before returning — so a same-file resume can safely reopen the file
// (#40). After it returns, another session can claim the same path.
func TestStopAndWait_StopsAndReleasesClaim(t *testing.T) {
	m := New()
	ad := &fakeAdapter{}
	m.StartSession("s1", ad, "cwd", 0, nil, nil, func(string, string) bool { return true }, nil, nil, nil)

	// Wait for the watcher to discover + claim "fake".
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !m.isClaimedByOther("other", "fake") {
		time.Sleep(20 * time.Millisecond)
	}
	if !m.isClaimedByOther("other", "fake") {
		t.Fatal("s1 should have claimed 'fake' before StopAndWait")
	}

	m.StopAndWait("s1", 2*time.Second)

	if m.isClaimedByOther("other", "fake") {
		t.Fatal("StopAndWait must release s1's claim so the file is reclaimable")
	}
}

// StopAndWait on an unknown/already-finished session returns immediately and never
// blocks on a missing done channel (#40).
func TestStopAndWait_UnknownSessionNoOp(t *testing.T) {
	m := New()
	done := make(chan struct{})
	go func() { m.StopAndWait("ghost", time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("StopAndWait on unknown session must return immediately")
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
