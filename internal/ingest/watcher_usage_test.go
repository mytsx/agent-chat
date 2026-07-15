package ingest

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"desktop/internal/usage"
)

func TestWatcherEmitsUsage(t *testing.T) {
	// Fake adapter: discovers a fixed path, no messages, ParseUsage returns a codex-like snapshot.
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-x.jsonl")
	os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":50,"window_minutes":10080,"resets_at":1},"plan_type":"pro"}}}`+"\n"), 0600)

	ad := &fakeUsageAdapter{path: path}
	m := New()
	var mu sync.Mutex
	var got *usage.Snapshot
	m.StartSession("s1", ad, dir, time.Now().UnixNano(), nil, nil,
		func(content, ts string) bool { return true },
		nil,
		func(snap *usage.Snapshot) { mu.Lock(); got = snap; mu.Unlock() },
		nil)
	defer m.StopSession("s1")

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		g := got
		mu.Unlock()
		if g != nil {
			if g.SessionID != "s1" || g.Primary == nil || g.Primary.UsedPercent != 50 {
				t.Fatalf("beklenmeyen snapshot: %+v", g)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("onUsage 4s içinde çağrılmadı")
}

// TestWatcherSkipsUsageWhenMuted verifies a muted (observer) watcher never
// parses/emits usage, even though it still claims its file. Mute is applied
// synchronously right after StartSession — Mute only sets an atomic bool, so
// it lands well before the first poll tick (pollInterval=700ms), making the
// muted state observed on that very first tick deterministically (#10/#17).
func TestWatcherSkipsUsageWhenMuted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-x.jsonl")
	os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":50,"window_minutes":10080,"resets_at":1},"plan_type":"pro"}}}`+"\n"), 0600)

	ad := &fakeUsageAdapter{path: path}
	m := New()
	var mu sync.Mutex
	var got *usage.Snapshot
	m.StartSession("s1", ad, dir, time.Now().UnixNano(), nil, nil,
		func(content, ts string) bool { return true },
		nil,
		func(snap *usage.Snapshot) { mu.Lock(); got = snap; mu.Unlock() },
		nil)
	m.Mute("s1")
	defer m.StopSession("s1")

	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		g := got
		mu.Unlock()
		if g != nil {
			t.Fatalf("muted watcher onUsage çağırmamalı, ama çağrıldı: %+v", g)
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if got != nil {
		t.Fatalf("muted watcher onUsage çağırmamalı, ama çağrıldı: %+v", got)
	}
}

// TestWatcherFinalDrainForcesUsage proves the ONE-AND-ONLY final drain (cancel →
// discoverAndPoll) bypasses the usageParseInterval (2s) throttle: a CLI that writes
// its last usage at shutdown (Copilot's session.shutdown) within 2s of the previous
// parse must still have it parsed. Without the forceUsage path the final drain would
// skip ParseUsage while throttled and the final totals would never emit (#10 follow-up).
func TestWatcherFinalDrainForcesUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-x.jsonl")
	os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":50,"window_minutes":10080,"resets_at":1},"plan_type":"pro"}}}`+"\n"), 0600)

	ad := &fakeUsageAdapter{path: path}
	m := New()
	var mu sync.Mutex
	var count int
	m.StartSession("s1", ad, dir, time.Now().UnixNano(), nil, nil,
		func(content, ts string) bool { return true },
		nil,
		func(snap *usage.Snapshot) { mu.Lock(); count++; mu.Unlock() },
		nil)

	// Wait for the first (ticker-driven) usage parse to fire.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		c := count
		mu.Unlock()
		if c >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	first := count
	mu.Unlock()
	if first < 1 {
		t.Fatal("ilk usage 4s içinde çağrılmadı")
	}

	// StopSession now — well within usageParseInterval (2s) of the last parse — closes
	// cancel and triggers the final drain. The forceUsage path must re-parse despite
	// the throttle, so onUsage fires AGAIN.
	m.StopSession("s1")

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		c := count
		mu.Unlock()
		if c > first {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	final := count
	mu.Unlock()
	t.Fatalf("final drain usage'ı throttle penceresinde tekrar emit etmedi: count=%d (first=%d)", final, first)
}

// fakeUsageAdapter implements SessionAdapter + UsageParser.
type fakeUsageAdapter struct{ path string }

func (f *fakeUsageAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64, claimed func(string) bool) (string, error) {
	return f.path, nil
}
func (f *fakeUsageAdapter) ParseNewUserMessages(path string, cur Cursor) ([]ParsedMessage, Cursor, error) {
	return nil, cur, nil
}
func (f *fakeUsageAdapter) SessionID(path string) string { return "cli-id" }
func (f *fakeUsageAdapter) ParseUsage(path string) (*usage.Snapshot, error) {
	return codexAdapter{}.ParseUsage(path)
}
