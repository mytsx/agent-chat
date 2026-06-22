package summary

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteAndLatestRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	room := "team-alpha"

	// No summary yet: Latest reports absence without error.
	if _, ok, err := Latest(dataDir, room); err != nil || ok {
		t.Fatalf("Latest on empty room = (ok=%v, err=%v), want (false, nil)", ok, err)
	}

	doc, err := Write(dataDir, room, "ilk özet ✅")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if doc.Epoch == "" || doc.CreatedAt == "" {
		t.Fatalf("Write returned incomplete doc: %+v", doc)
	}

	got, ok, err := Latest(dataDir, room)
	if err != nil || !ok {
		t.Fatalf("Latest = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if got.Text != "ilk özet ✅" {
		t.Fatalf("Latest text = %q, want %q", got.Text, "ilk özet ✅")
	}
	if got.Room != room {
		t.Fatalf("Latest room = %q, want %q", got.Room, room)
	}
}

func TestLatestReturnsNewest(t *testing.T) {
	dataDir := t.TempDir()
	room := "default"

	if _, err := Write(dataDir, room, "eski"); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	d2, err := Write(dataDir, room, "yeni")
	if err != nil {
		t.Fatalf("Write 2: %v", err)
	}

	got, ok, err := Latest(dataDir, room)
	if err != nil || !ok {
		t.Fatalf("Latest = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if got.Text != "yeni" {
		t.Fatalf("Latest text = %q, want %q (devam = en son)", got.Text, "yeni")
	}
	if got.Epoch != d2.Epoch {
		t.Fatalf("Latest epoch = %q, want newest %q", got.Epoch, d2.Epoch)
	}
}

func TestListNewestFirst(t *testing.T) {
	dataDir := t.TempDir()
	room := "room1"

	d1, err := Write(dataDir, room, "bir")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dataDir, room, "iki"); err != nil {
		t.Fatal(err)
	}
	d3, err := Write(dataDir, room, "üç")
	if err != nil {
		t.Fatal(err)
	}

	docs, err := List(dataDir, room)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("List len = %d, want 3", len(docs))
	}
	if docs[0].Epoch != d3.Epoch {
		t.Fatalf("List[0] epoch = %q, want newest %q", docs[0].Epoch, d3.Epoch)
	}
	if docs[2].Epoch != d1.Epoch {
		t.Fatalf("List[2] epoch = %q, want oldest %q", docs[2].Epoch, d1.Epoch)
	}
}

// Concurrent writes to the same room must each get a distinct epoch and survive:
// none may overwrite another. Guards the epoch-collision TOCTOU (os.Stat-then-write
// is not atomic; os.Rename overwrites on POSIX) — without serialization, racing
// writes collide on the same epoch and silently lose summaries.
func TestWriteConcurrentDistinctEpochs(t *testing.T) {
	dataDir := t.TempDir()
	room := "busy"
	const n = 24

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = Write(dataDir, room, fmt.Sprintf("özet-%d", i))
		}(i)
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent Write %d failed: %v", i, e)
		}
	}
	docs, err := List(dataDir, room)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != n {
		t.Fatalf("concurrent writes lost data: got %d distinct summaries, want %d", len(docs), n)
	}
	seen := make(map[string]bool, n)
	for _, d := range docs {
		if seen[d.Epoch] {
			t.Fatalf("duplicate epoch %s — a write overwrote another", d.Epoch)
		}
		seen[d.Epoch] = true
	}
}

func TestRejectsUnsafeRoom(t *testing.T) {
	dataDir := t.TempDir()
	for _, bad := range []string{"../evil", "a/b", ".hidden", "x..y", `back\slash`} {
		if _, err := Write(dataDir, bad, "x"); err == nil {
			t.Errorf("Write(room=%q) = nil error, want rejection", bad)
		}
		if _, _, err := Latest(dataDir, bad); err == nil {
			t.Errorf("Latest(room=%q) = nil error, want rejection", bad)
		}
	}
}

// An unreadable summary file must be skipped (graceful degradation), not fail the
// whole List/Latest — mirrors the transcript snapshot/archive skip (#29 review).
func TestListSkipsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 0000 does not deny root")
	}
	dataDir := t.TempDir()
	room := "r"
	if _, err := Write(dataDir, room, "eski-okunur"); err != nil {
		t.Fatal(err)
	}
	newest, err := Write(dataDir, room, "yeni-bozuk")
	if err != nil {
		t.Fatal(err)
	}
	// Make the NEWEST file unreadable.
	bad := filepath.Join(dataDir, "hub-state", "summaries", room, newest.Epoch+".md")
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	got, ok, err := Latest(dataDir, room)
	if err != nil {
		t.Fatalf("an unreadable summary must not fail Latest: %v", err)
	}
	if !ok || got.Text != "eski-okunur" {
		t.Fatalf("Latest = (ok=%v, text=%q), want (true, eski-okunur) — unreadable newest skipped", ok, got.Text)
	}
}

func TestListEmptyRoom(t *testing.T) {
	dataDir := t.TempDir()
	docs, err := List(dataDir, "never-used")
	if err != nil {
		t.Fatalf("List on empty room: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("List on empty room len = %d, want 0", len(docs))
	}
}

func TestNoDataDirIsNoOp(t *testing.T) {
	// An empty dataDir (unit hubs) must never touch the CWD.
	if _, err := Write("", "room", "x"); err == nil {
		t.Errorf("Write with empty dataDir = nil error, want rejection")
	}
	doc, ok, err := Latest("", "room")
	if err != nil || ok {
		t.Errorf("Latest with empty dataDir = (%+v, ok=%v, err=%v), want (zero, false, nil)", doc, ok, err)
	}
}

// Defense-in-depth: even if ValidateName ever loosened, the cleaned-prefix
// containment check must keep writes inside the summaries dir.
func TestSummaryDirContainment(t *testing.T) {
	dataDir := t.TempDir()
	dir, err := summaryDir(dataDir, "ok-room")
	if err != nil {
		t.Fatalf("summaryDir(ok-room): %v", err)
	}
	base := filepath.Join(dataDir, "hub-state", "summaries")
	if !strings.HasPrefix(dir, base) {
		t.Fatalf("summaryDir escaped base: %q not under %q", dir, base)
	}
}
