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

func txMsgT(id int, content, ts string) types.Message {
	return types.Message{ID: id, From: "alice", To: "bob", Content: content, Timestamp: ts, Type: "direct"}
}

func txContents(msgs []types.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
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

// clear_room resets a room's message IDs back to 1, so the archive (pre-clear
// IDs) and a later snapshot (post-clear IDs) can carry the SAME id for DISTINCT
// messages. Dedup must not conflate them (that would silently drop history); it
// keys on (ID, Timestamp) and orders chronologically (#29 Codex review).
func TestReadFullTranscriptSurvivesClearIDReset(t *testing.T) {
	dataDir := t.TempDir()
	room := "cleared"
	// Pre-clear conversation, archived (IDs 1..3, earlier timestamps).
	txWriteArchive(t, dataDir, room, []types.Message{
		txMsgT(1, "eski-1", "2026-06-22T10:00:00.000000"),
		txMsgT(2, "eski-2", "2026-06-22T10:00:01.000000"),
		txMsgT(3, "eski-3", "2026-06-22T10:00:02.000000"),
	})
	// After clear, IDs restart at 1; the new session snapshot reuses ids 1..2 with
	// LATER timestamps.
	txWriteSnapshot(t, dataDir, room, "1700000100", []types.Message{
		txMsgT(1, "yeni-1", "2026-06-22T11:00:00.000000"),
		txMsgT(2, "yeni-2", "2026-06-22T11:00:01.000000"),
	})

	got, err := ReadFullTranscript(dataDir, room, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"eski-1", "eski-2", "eski-3", "yeni-1", "yeni-2"}
	if g := txContents(got); len(g) != len(want) {
		t.Fatalf("got %d messages, want %d (clear-reset ids must not conflate): %v", len(g), len(want), g)
	}
	for i, w := range want {
		if got[i].Content != w {
			t.Fatalf("order[%d] = %q, want %q (full: %v)", i, got[i].Content, w, txContents(got))
		}
	}
}

// Two DISTINCT messages can share the same id AND the same formatted-microsecond
// timestamp after a clear_room (ID reset) when both happen to fall in the same
// microsecond — a (id, ts)-only dedup key would silently conflate them and drop
// one. The key also includes from+content, so distinct messages survive while a
// genuine duplicate (all fields equal) still collapses (#58).
func TestReadFullTranscriptKeepsDistinctMessagesSameIDAndTimestamp(t *testing.T) {
	dataDir := t.TempDir()
	room := "collide"
	const ts = "2026-06-22T10:00:00.000000"
	// Pre-clear message archived as id=1, and a post-clear DISTINCT message that
	// reused id=1 within the same microsecond — different content (and sender).
	txWriteArchive(t, dataDir, room, []types.Message{
		{ID: 1, From: "alice", To: "bob", Content: "pre-clear", Timestamp: ts, Type: "direct"},
	})
	txWriteSnapshot(t, dataDir, room, "1700000100", []types.Message{
		{ID: 1, From: "carol", To: "bob", Content: "post-clear", Timestamp: ts, Type: "direct"},
	})

	got, err := ReadFullTranscript(dataDir, room, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("distinct same-id/same-ts messages conflated: got %d, want 2: %v", len(got), txContents(got))
	}
}

// A true duplicate (same message present in BOTH archive and snapshot) still
// dedupes — identical id AND timestamp.
func TestReadFullTranscriptDedupsTrueDuplicate(t *testing.T) {
	dataDir := t.TempDir()
	room := "dup"
	txWriteArchive(t, dataDir, room, []types.Message{txMsgT(7, "x", "2026-06-22T10:00:00.000000")})
	txWriteSnapshot(t, dataDir, room, "1700000100", []types.Message{txMsgT(7, "x", "2026-06-22T10:00:00.000000")})

	got, err := ReadFullTranscript(dataDir, room, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("true duplicate not deduped: got %d, want 1: %v", len(got), txContents(got))
	}
}

// One corrupt/partial JSONL line in the rolling archive must be skipped, not fail
// the whole transcript read — otherwise a single damaged append blocks all future
// summary generation for the room (mirrors the snapshot skip; #29 Codex review).
func TestReadFullTranscriptSkipsMalformedArchiveLine(t *testing.T) {
	dataDir := t.TempDir()
	room := "corruptarch"
	dir := filepath.Join(dataDir, "hub-state", "archive")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	good1, _ := json.Marshal(txMsg(1, "a"))
	good2, _ := json.Marshal(txMsg(2, "b"))
	content := string(good1) + "\n{bozuk json line\n" + string(good2) + "\n"
	if err := os.WriteFile(filepath.Join(dir, room+".jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadFullTranscript(dataDir, room, 0, 0)
	if err != nil {
		t.Fatalf("one corrupt archive line must not fail the whole read: %v", err)
	}
	if want := []int{1, 2}; !eqInts(txIDs(got), want) {
		t.Fatalf("ids = %v, want %v (corrupt line skipped, valid kept)", txIDs(got), want)
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

// A corrupt (non-JSON) snapshot file must be skipped (graceful degradation), not
// abort the whole transcript read — otherwise one damaged snapshot loses every
// other snapshot in the room. Uses a corrupt-content sentinel (deterministic,
// cross-platform) rather than chmod, which is platform-specific (no-op on Windows
// / as root).
func TestReadFullTranscriptSkipsCorruptSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	room := "mixed"
	txWriteSnapshot(t, dataDir, room, "1700000005", []types.Message{txMsg(2, "b")})

	// A snapshot whose contents are not valid PersistedRoom JSON.
	dir := filepath.Join(dataDir, "hub-state", "sessions", room)
	if err := os.WriteFile(filepath.Join(dir, "1700000000.json"), []byte("{bozuk snapshot"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadFullTranscript(dataDir, room, 0, 0)
	if err != nil {
		t.Fatalf("a corrupt snapshot must not fail the whole read: %v", err)
	}
	if want := []int{2}; !eqInts(txIDs(got), want) {
		t.Fatalf("ids = %v, want %v (corrupt snapshot skipped, valid kept)", txIDs(got), want)
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
