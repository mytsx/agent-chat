package hub

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"desktop/internal/types"
)

func newArchiveHub(dataDir string) *Hub {
	return New(dataDir, "default", log.New(io.Discard, "", 0))
}

// TestSendMessageTruncateInvokesArchiveFn verifies that when the room exceeds
// the message cap and truncates, the dropped (oldest) messages are handed to
// archiveFn before they leave memory — and that the room keeps exactly the
// retained tail.
func TestSendMessageTruncateInvokesArchiveFn(t *testing.T) {
	r := NewRoomState()

	var archived []types.Message
	r.SetArchiveFn(func(msgs []types.Message) {
		archived = append(archived, msgs...)
	})

	// Fill past the cap so exactly one truncation fires.
	total := maxMessagesInRoom + 1 // 501 -> drops (501-300)=201, keeps 300
	for i := 0; i < total; i++ {
		if _, err := r.SendMessage("a", "all", "msg", false, "", SendOptions{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	wantDropped := total - truncateToMessages // 201
	if len(archived) != wantDropped {
		t.Fatalf("archived count = %d, want %d", len(archived), wantDropped)
	}

	// Dropped messages must be the oldest, in order: IDs 1..201.
	for i, m := range archived {
		if m.ID != i+1 {
			t.Fatalf("archived[%d].ID = %d, want %d", i, m.ID, i+1)
		}
	}

	// Room retains the most recent `truncateToMessages` (IDs 202..501).
	kept := r.GetMessages()
	if len(kept) != truncateToMessages {
		t.Fatalf("kept count = %d, want %d", len(kept), truncateToMessages)
	}
	if kept[0].ID != wantDropped+1 {
		t.Fatalf("first kept ID = %d, want %d", kept[0].ID, wantDropped+1)
	}
}

// TestClearInvokesArchiveFnBeforeClearing verifies Clear hands the full
// message history to archiveFn before wiping the room.
func TestClearInvokesArchiveFnBeforeClearing(t *testing.T) {
	r := NewRoomState()

	var archived []types.Message
	r.SetArchiveFn(func(msgs []types.Message) {
		archived = append(archived, msgs...)
	})

	const n = 5
	for i := 0; i < n; i++ {
		if _, err := r.SendMessage("a", "all", "msg", false, "", SendOptions{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	r.Clear()

	if len(archived) != n {
		t.Fatalf("archived count = %d, want %d", len(archived), n)
	}
	for i, m := range archived {
		if m.ID != i+1 {
			t.Fatalf("archived[%d].ID = %d, want %d", i, m.ID, i+1)
		}
	}
	if msgs := r.GetMessages(); len(msgs) != 0 {
		t.Fatalf("room not cleared: %d messages remain", len(msgs))
	}
}

// TestArchiveFnNilNoPanic locks in backward compatibility: with no archiveFn
// installed, truncation and Clear behave exactly as before (no panic, room
// truncates to the retained tail, Clear empties it).
func TestArchiveFnNilNoPanic(t *testing.T) {
	r := NewRoomState()

	// Exactly one truncation crossing (501 -> 300).
	for i := 0; i < maxMessagesInRoom+1; i++ {
		if _, err := r.SendMessage("a", "all", "msg", false, "", SendOptions{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if got := len(r.GetMessages()); got != truncateToMessages {
		t.Fatalf("after truncate kept = %d, want %d", got, truncateToMessages)
	}

	r.Clear()
	if got := len(r.GetMessages()); got != 0 {
		t.Fatalf("after clear kept = %d, want 0", got)
	}
}

// TestAppendArchiveWritesJSONL verifies appendArchive appends one JSON line per
// message and that repeated calls accumulate (append-only).
func TestAppendArchiveWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)

	first := []types.Message{
		{ID: 1, From: "a", To: "all", Content: "one", Type: "broadcast"},
		{ID: 2, From: "b", To: "a", Content: "two", Type: "direct"},
	}
	h.appendArchive("room1", first)

	got := readArchiveLines(t, dir, "room1")
	if len(got) != 2 {
		t.Fatalf("after first append: %d lines, want 2", len(got))
	}
	if got[0].ID != 1 || got[0].Content != "one" || got[1].ID != 2 {
		t.Fatalf("unexpected archived messages: %+v", got)
	}

	h.appendArchive("room1", []types.Message{{ID: 3, From: "c", To: "all", Content: "three"}})
	got = readArchiveLines(t, dir, "room1")
	if len(got) != 3 {
		t.Fatalf("after second append: %d lines, want 3 (append-only)", len(got))
	}
	if got[2].ID != 3 || got[2].Content != "three" {
		t.Fatalf("unexpected third message: %+v", got[2])
	}

	// Directory is created restricted (0700).
	info, err := os.Stat(filepath.Join(dir, "hub-state", "archive"))
	if err != nil {
		t.Fatalf("stat archive dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Fatalf("archive dir perm = %o, want 700", perm)
	}
}

// TestAppendArchiveRejectsInvalidRoom verifies a path-traversal room name is
// refused and writes nothing.
func TestAppendArchiveRejectsInvalidRoom(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)

	h.appendArchive("../evil", []types.Message{{ID: 1, Content: "x"}})

	// No file should be created anywhere under the data dir for the traversal.
	if _, err := os.Stat(filepath.Join(dir, "hub-state", "evil.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote a file: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hub-state", "archive", "..", "evil.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote a file under archive parent: err=%v", err)
	}
}

// TestAppendArchiveEmptyInputsNoop verifies empty message slices and an unset
// data dir are silent no-ops (no file, no panic).
func TestAppendArchiveEmptyInputsNoop(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)

	h.appendArchive("room1", nil)
	if _, err := os.Stat(filepath.Join(dir, "hub-state", "archive", "room1.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("empty message slice should not create a file: err=%v", err)
	}

	// No data dir configured: must not write relative to CWD.
	hNoDir := newArchiveHub("")
	hNoDir.appendArchive("room1", []types.Message{{ID: 1, Content: "x"}})
}

// TestEnqueueArchiveWritesViaWriter verifies the async path: enqueued messages
// reach disk once the writer goroutine drains, and shutdown drains the backlog.
func TestEnqueueArchiveWritesViaWriter(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)

	go h.runArchiveWriter()

	h.enqueueArchive("room1", []types.Message{{ID: 1, From: "a", To: "all", Content: "one"}})
	h.enqueueArchive("room1", []types.Message{{ID: 2, From: "b", To: "all", Content: "two"}})

	// Closing done makes the writer drain the backlog then signal completion.
	close(h.done)
	select {
	case <-h.archiveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("archive writer did not drain/stop in time")
	}

	got := readArchiveLines(t, dir, "room1")
	if len(got) != 2 {
		t.Fatalf("archived via writer: %d lines, want 2", len(got))
	}
	if got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("unexpected archived order: %+v", got)
	}
}

// TestEnqueueArchiveEmptyDataDirSkips verifies enqueue is a no-op without a
// data dir (so unit hubs built with New("", ...) never touch the filesystem).
func TestEnqueueArchiveEmptyDataDirSkips(t *testing.T) {
	h := newArchiveHub("")
	go h.runArchiveWriter()
	h.enqueueArchive("room1", []types.Message{{ID: 1, Content: "x"}})
	close(h.done)
	select {
	case <-h.archiveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not stop")
	}
	// Nothing to assert beyond "did not panic / did not block".
}

// TestRoomTruncateArchivesViaHubWiring verifies the full wiring: a room created
// through getOrCreateRoom archives its truncated messages to disk.
func TestRoomTruncateArchivesViaHubWiring(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	go h.runArchiveWriter()

	room := h.getOrCreateRoom("proj")
	total := maxMessagesInRoom + 1
	for i := 0; i < total; i++ {
		if _, err := room.SendMessage("a", "all", "msg", false, "", SendOptions{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	close(h.done)
	select {
	case <-h.archiveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("archive writer did not drain")
	}

	got := readArchiveLines(t, dir, "proj")
	wantDropped := total - truncateToMessages
	if len(got) != wantDropped {
		t.Fatalf("archived via wiring: %d lines, want %d", len(got), wantDropped)
	}
	for i, m := range got {
		if m.ID != i+1 {
			t.Fatalf("archived[%d].ID = %d, want %d", i, m.ID, i+1)
		}
	}
}

// TestHandleArchiveRoom_DesktopFlushesCurrentMessages verifies the archive_room
// RPC: it is desktop-authorized only and synchronously flushes the room's
// current messages to the archive file before responding.
func TestHandleArchiveRoom_DesktopFlushesCurrentMessages(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	h.desktopAuthToken = "secret"

	room := h.getOrCreateRoom("proj")
	if _, err := room.SendMessage("a", "all", "one", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if _, err := room.SendMessage("b", "all", "two", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed 2: %v", err)
	}

	// Unauthorized client cannot archive.
	guest := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleRequest(guest, types.Request{ID: "1", Type: "archive_room", Room: "proj"})
	if resp := readResponse(t, guest, "archive_room"); resp.Success {
		t.Fatalf("expected unauthorized archive_room to fail")
	}

	// Authorize the desktop client.
	desktop := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleRequest(desktop, types.Request{
		ID:   "id",
		Type: "identify",
		Data: mustRawJSON(t, map[string]any{"client_type": "desktop", "auth_token": "secret"}),
	})
	if r := readResponse(t, desktop, "identify"); !r.Success {
		t.Fatalf("desktop identify should succeed: %s", r.Error)
	}

	// Authorized desktop archives synchronously.
	h.handleRequest(desktop, types.Request{ID: "2", Type: "archive_room", Room: "proj"})
	resp := readResponse(t, desktop, "archive_room")
	if !resp.Success {
		t.Fatalf("expected desktop archive_room to succeed: %s", resp.Error)
	}

	// File is present immediately (synchronous flush — no writer goroutine).
	got := readArchiveLines(t, dir, "proj")
	if len(got) != 2 {
		t.Fatalf("archive_room flushed %d messages, want 2", len(got))
	}
	if got[0].Content != "one" || got[1].Content != "two" {
		t.Fatalf("unexpected archived content: %+v", got)
	}
}

// TestClearRoomViaDesktopArchivesToDisk verifies the destructive clear_room RPC
// archives the wiped messages through the full hub path before clearing.
func TestClearRoomViaDesktopArchivesToDisk(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	h.desktopAuthToken = "secret"
	go h.runArchiveWriter()

	room := h.getOrCreateRoom("proj")
	for i := 0; i < 3; i++ {
		if _, err := room.SendMessage("a", "all", "msg", false, "", SendOptions{}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	desktop := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleRequest(desktop, types.Request{
		ID:   "id",
		Type: "identify",
		Data: mustRawJSON(t, map[string]any{"client_type": "desktop", "auth_token": "secret"}),
	})
	if r := readResponse(t, desktop, "identify"); !r.Success {
		t.Fatalf("desktop identify should succeed: %s", r.Error)
	}

	h.handleRequest(desktop, types.Request{ID: "clr", Type: "clear_room", Room: "proj"})
	if r := readResponse(t, desktop, "clear_room"); !r.Success {
		t.Fatalf("desktop clear_room should succeed: %s", r.Error)
	}

	// Drain the writer, then confirm the wiped messages are on disk.
	close(h.done)
	select {
	case <-h.archiveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("archive writer did not drain")
	}

	got := readArchiveLines(t, dir, "proj")
	if len(got) != 3 {
		t.Fatalf("clear archived %d messages, want 3", len(got))
	}
	if n := len(room.GetMessages()); n != 0 {
		t.Fatalf("room should be cleared, but %d messages remain", n)
	}
}

// readArchiveLines parses a room's archive JSONL file into messages.
func readArchiveLines(t *testing.T, dataDir, room string) []types.Message {
	t.Helper()
	path := filepath.Join(dataDir, "hub-state", "archive", room+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive %s: %v", path, err)
	}
	defer f.Close()

	var out []types.Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m types.Message
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("parse archive line %q: %v", line, err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan archive: %v", err)
	}
	return out
}
