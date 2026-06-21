package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"desktop/internal/types"
)

func txMsg(id int, content string) types.Message {
	return types.Message{ID: id, From: "alice", To: "bob", Content: content, Timestamp: "2026-06-21T10:00:00Z", Type: "direct"}
}

func txWriteSnapshot(t *testing.T, dataDir, room, epoch string, msgs []types.Message) {
	t.Helper()
	dir := filepath.Join(dataDir, "hub-state", "sessions", room)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	data, err := json.Marshal(PersistedRoom{Messages: msgs, Agents: map[string]types.Agent{}})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, epoch+".json"), data, 0644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

func txWriteArchive(t *testing.T, dataDir, room string, msgs []types.Message) {
	t.Helper()
	dir := filepath.Join(dataDir, "hub-state", "archive")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	var b strings.Builder
	for _, m := range msgs {
		line, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal archive line: %v", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, room+".jsonl"), []byte(b.String()), 0644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
}

func txIDs(msgs []types.Message) []int {
	out := make([]int, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReadFullTranscriptMergesAndDedups(t *testing.T) {
	dataDir := t.TempDir()
	room := "team-alpha"

	// Archive holds the older overflow; snapshot holds the retained tail. ID 3
	// overlaps both — it must appear exactly once.
	txWriteArchive(t, dataDir, room, []types.Message{txMsg(1, "a"), txMsg(2, "b"), txMsg(3, "c")})
	txWriteSnapshot(t, dataDir, room, "1700000000", []types.Message{txMsg(3, "c"), txMsg(4, "d"), txMsg(5, "e")})

	got, err := ReadFullTranscript(dataDir, room, 0, 0)
	if err != nil {
		t.Fatalf("ReadFullTranscript: %v", err)
	}
	if want := []int{1, 2, 3, 4, 5}; !eqInts(txIDs(got), want) {
		t.Fatalf("ids = %v, want %v (merged, deduped, sorted)", txIDs(got), want)
	}
}

func TestReadFullTranscriptSinceIDAndLimit(t *testing.T) {
	dataDir := t.TempDir()
	room := "default"
	txWriteArchive(t, dataDir, room, []types.Message{txMsg(1, "a"), txMsg(2, "b")})
	txWriteSnapshot(t, dataDir, room, "1700000000", []types.Message{txMsg(3, "c"), txMsg(4, "d"), txMsg(5, "e")})

	since, err := ReadFullTranscript(dataDir, room, 2, 0)
	if err != nil {
		t.Fatalf("sinceID: %v", err)
	}
	if want := []int{3, 4, 5}; !eqInts(txIDs(since), want) {
		t.Fatalf("sinceID ids = %v, want %v", txIDs(since), want)
	}

	limited, err := ReadFullTranscript(dataDir, room, 0, 2)
	if err != nil {
		t.Fatalf("limit: %v", err)
	}
	if want := []int{4, 5}; !eqInts(txIDs(limited), want) {
		t.Fatalf("limit ids = %v, want %v (most recent)", txIDs(limited), want)
	}
}

func TestReadFullTranscriptMultipleSnapshots(t *testing.T) {
	dataDir := t.TempDir()
	room := "room-x"
	// Two snapshots from two sessions, overlapping tails.
	txWriteSnapshot(t, dataDir, room, "1700000000", []types.Message{txMsg(1, "a"), txMsg(2, "b")})
	txWriteSnapshot(t, dataDir, room, "1700000005", []types.Message{txMsg(2, "b"), txMsg(3, "c")})

	got, err := ReadFullTranscript(dataDir, room, 0, 0)
	if err != nil {
		t.Fatalf("ReadFullTranscript: %v", err)
	}
	if want := []int{1, 2, 3}; !eqInts(txIDs(got), want) {
		t.Fatalf("ids = %v, want %v", txIDs(got), want)
	}
}

func TestReadFullTranscriptArchiveOnly(t *testing.T) {
	dataDir := t.TempDir()
	room := "archived"
	txWriteArchive(t, dataDir, room, []types.Message{txMsg(1, "a"), txMsg(2, "b")})

	got, err := ReadFullTranscript(dataDir, room, 0, 0)
	if err != nil {
		t.Fatalf("ReadFullTranscript: %v", err)
	}
	if want := []int{1, 2}; !eqInts(txIDs(got), want) {
		t.Fatalf("ids = %v, want %v", txIDs(got), want)
	}
}

func TestReadFullTranscriptMissingRoom(t *testing.T) {
	dataDir := t.TempDir()
	got, err := ReadFullTranscript(dataDir, "never-used", 0, 0)
	if err != nil {
		t.Fatalf("missing room should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing room len = %d, want 0", len(got))
	}
}

// An unreadable snapshot file must be skipped (graceful degradation), not abort
// the whole transcript read — matching the corrupt-JSON skip. Otherwise a single
// permission glitch loses every other snapshot in the room.
func TestReadFullTranscriptSkipsUnreadableSnapshot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 0000 does not deny root; test is meaningless as root")
	}
	dataDir := t.TempDir()
	room := "mixed"
	txWriteSnapshot(t, dataDir, room, "1700000000", []types.Message{txMsg(1, "a")})
	txWriteSnapshot(t, dataDir, room, "1700000005", []types.Message{txMsg(2, "b")})

	bad := filepath.Join(dataDir, "hub-state", "sessions", room, "1700000000.json")
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) }) // let TempDir cleanup remove it

	got, err := ReadFullTranscript(dataDir, room, 0, 0)
	if err != nil {
		t.Fatalf("an unreadable snapshot must not fail the whole read: %v", err)
	}
	if want := []int{2}; !eqInts(txIDs(got), want) {
		t.Fatalf("ids = %v, want %v (unreadable snapshot skipped, readable kept)", txIDs(got), want)
	}
}

func TestReadFullTranscriptRejectsUnsafeRoom(t *testing.T) {
	dataDir := t.TempDir()
	for _, bad := range []string{"../evil", "a/b", ".hidden"} {
		if _, err := ReadFullTranscript(dataDir, bad, 0, 0); err == nil {
			t.Errorf("ReadFullTranscript(room=%q) = nil error, want rejection", bad)
		}
	}
}
