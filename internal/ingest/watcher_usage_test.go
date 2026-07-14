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
