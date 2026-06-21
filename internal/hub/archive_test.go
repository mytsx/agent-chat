package hub

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
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

// TestHandleClearRoom_AbortsOnArchiveFailure verifies the durable destructive
// clear: when the history cannot be archived, clear_room fails and the room is
// NOT wiped, so a clear never silently loses history.
func TestHandleClearRoom_AbortsOnArchiveFailure(t *testing.T) {
	tmp := t.TempDir()
	badDataDir := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(badDataDir, []byte("x"), 0600); err != nil {
		t.Fatalf("seed bad data dir: %v", err)
	}
	h := newArchiveHub(badDataDir)
	h.desktopAuthToken = "secret"

	room := h.getOrCreateRoom("proj")
	for i := 0; i < 3; i++ {
		if _, err := room.SendMessage("a", "all", "hi", false, "", SendOptions{}); err != nil {
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

	h.handleRequest(desktop, types.Request{ID: "1", Type: "clear_room", Room: "proj"})
	if resp := readResponse(t, desktop, "clear_room"); resp.Success {
		t.Fatalf("expected clear_room to abort when the archive cannot be written")
	}
	if n := len(room.GetMessages()); n != 3 {
		t.Fatalf("room must NOT be cleared when archive fails, but %d/3 messages remain", n)
	}
}

// TestJoinDoesNotTruncate verifies a join into a full room does NOT truncate:
// the manager's own join must not drop history out from under its first read
// (read_all_messages). Only SendMessage truncates; join/leave append in place.
func TestJoinDoesNotTruncate(t *testing.T) {
	r := NewRoomState()
	var archived []types.Message
	r.SetArchiveFn(func(msgs []types.Message) { archived = append(archived, msgs...) })

	// Fill to exactly the cap.
	for i := 0; i < maxMessagesInRoom; i++ {
		if _, err := r.SendMessage("a", "all", "m", false, "", SendOptions{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	// A manager joins the full room: its system message appends without trimming.
	if _, _, err := r.Join("manager", "manager"); err != nil {
		t.Fatalf("join: %v", err)
	}
	if n := len(r.GetMessages()); n != maxMessagesInRoom+1 {
		t.Fatalf("join must not truncate: room has %d, want %d", n, maxMessagesInRoom+1)
	}
	if len(archived) != 0 {
		t.Fatalf("join must not archive (no truncation), archived=%d", len(archived))
	}
}

// TestNonManagerJoinTruncates verifies a non-manager join goes through the cap,
// so connect/disconnect churn can't grow the room unbounded.
func TestNonManagerJoinTruncates(t *testing.T) {
	r := NewRoomState()
	var archived []types.Message
	r.SetArchiveFn(func(msgs []types.Message) { archived = append(archived, msgs...) })

	for i := 0; i < maxMessagesInRoom; i++ {
		if _, err := r.SendMessage("a", "all", "m", false, "", SendOptions{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if _, _, err := r.Join("bob", "developer"); err != nil {
		t.Fatalf("join: %v", err)
	}
	if n := len(r.GetMessages()); n != truncateToMessages {
		t.Fatalf("non-manager join must truncate: room has %d, want %d", n, truncateToMessages)
	}
	if want := maxMessagesInRoom + 1 - truncateToMessages; len(archived) != want {
		t.Fatalf("non-manager join archived %d, want %d", len(archived), want)
	}
}

// TestLeaveTruncates verifies a leave goes through the cap (churn bound).
func TestLeaveTruncates(t *testing.T) {
	r := NewRoomState()
	var archived []types.Message
	r.SetArchiveFn(func(msgs []types.Message) { archived = append(archived, msgs...) })

	if _, _, err := r.Join("bob", "developer"); err != nil { // 1 message
		t.Fatalf("join: %v", err)
	}
	for i := 0; i < maxMessagesInRoom-1; i++ { // up to the cap
		if _, err := r.SendMessage("a", "all", "m", false, "", SendOptions{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if len(archived) != 0 {
		t.Fatalf("no truncation expected at the cap, archived=%d", len(archived))
	}
	if _, ok := r.Leave("bob"); !ok { // pushes past the cap -> truncate
		t.Fatal("leave should succeed")
	}
	if n := len(r.GetMessages()); n != truncateToMessages {
		t.Fatalf("leave must truncate: room has %d, want %d", n, truncateToMessages)
	}
	if len(archived) == 0 {
		t.Fatal("leave truncation must archive the dropped messages")
	}
}

// TestClearArchivedZeroKeepsRacingMessage verifies the empty-snapshot clear case:
// when the archived snapshot was empty (maxID=0) but a message raced in
// afterwards, ClearArchived must keep it rather than full-wiping it.
func TestClearArchivedZeroKeepsRacingMessage(t *testing.T) {
	r := NewRoomState()
	// A message that arrived after an empty snapshot (its ID is 1 > 0).
	if _, err := r.SendMessage("a", "all", "raced-in", false, "", SendOptions{}); err != nil {
		t.Fatalf("send: %v", err)
	}

	r.ClearArchived(0) // empty snapshot: nothing was archived, so wipe nothing

	got := r.GetMessages()
	if len(got) != 1 || got[0].Content != "raced-in" {
		t.Fatalf("ClearArchived(0) must keep the racing message, got %+v", got)
	}
}

// TestArchiveFnNilNoPanic locks in backward compatibility: with no archiveFn
// installed, truncation behaves exactly as before (no panic, room truncates to
// the retained tail) and a full clear empties it.
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

	msgs := r.GetMessages()
	r.ClearArchived(msgs[len(msgs)-1].ID) // wipe everything up to the last ID
	if got := len(r.GetMessages()); got != 0 {
		t.Fatalf("after clear kept = %d, want 0", got)
	}
}

// TestClearArchivedKeepsNewerMessages verifies ClearArchived preserves messages
// that arrived after the archived snapshot (ID > maxID) — the clear_room race fix.
func TestClearArchivedKeepsNewerMessages(t *testing.T) {
	r := NewRoomState()
	for i := 0; i < 5; i++ {
		if _, err := r.SendMessage("a", "all", "m", false, "", SendOptions{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	// Archived snapshot covered IDs 1..3; messages 4 and 5 raced in afterwards.
	r.ClearArchived(3)
	got := r.GetMessages()
	if len(got) != 2 {
		t.Fatalf("ClearArchived(3) kept %d messages, want 2 (IDs 4,5)", len(got))
	}
	if got[0].ID != 4 || got[1].ID != 5 {
		t.Fatalf("kept wrong messages: %+v", got)
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

// TestEnqueueArchiveAfterDoneWritesSynchronously verifies the shutdown done
// check: a non-request enqueue arriving after the hub has begun shutting down
// (e.g. runClientManager's Leave) is written synchronously instead of being
// orphaned in a channel the writer may have already stopped draining. Looped so
// a regression (dropping the done check) fails with near-certainty.
func TestEnqueueArchiveAfterDoneWritesSynchronously(t *testing.T) {
	const iterations = 30
	for i := 0; i < iterations; i++ {
		dir := t.TempDir()
		h := newArchiveHub(dir)
		close(h.done) // simulate shutdown; no writer running

		h.enqueueArchive("room1", []types.Message{{ID: 1, From: "a", To: "all", Content: "x"}})

		got := readArchiveLines(t, dir, "room1")
		if len(got) != 1 {
			t.Fatalf("iter %d: enqueue after done wrote %d messages, want 1 (lost to buffer)", i, len(got))
		}
	}
}

// TestBeginRequestGatedAfterShutdown verifies the request gate: once shutdown
// has closed request handling, beginRequest refuses new handlers (so no new
// truncate/clear archive write can start), while it admits them beforehand.
func TestBeginRequestGatedAfterShutdown(t *testing.T) {
	h := newArchiveHub(t.TempDir())

	if !h.beginRequest() {
		t.Fatal("beginRequest should admit handlers before shutdown")
	}
	h.endRequest()

	h.requestMu.Lock()
	h.requestsClosed = true
	h.requestMu.Unlock()

	if h.beginRequest() {
		t.Fatal("beginRequest should refuse handlers after request shutdown")
	}
}

// TestShutdownDrainsBufferedArchive verifies the end-to-end no-loss guarantee:
// jobs buffered in archiveCh are flushed to disk by a full Shutdown sequence.
func TestShutdownDrainsBufferedArchive(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	h.mu.Lock()
	h.archiveStarted = true
	h.mu.Unlock()
	go h.runArchiveWriter()

	for i := 0; i < 3; i++ {
		h.enqueueArchive("room1", []types.Message{{ID: i + 1, From: "a", To: "all", Content: "x"}})
	}

	// Full graceful shutdown: quiesce producers, drain writer, sweep backlog.
	h.Shutdown()

	got := readArchiveLines(t, dir, "room1")
	if len(got) != 3 {
		t.Fatalf("shutdown flushed %d messages, want 3", len(got))
	}
}

// TestAppendArchiveConcurrentNoLoss exercises appendArchive from many goroutines
// at once (mirroring the writer goroutine racing a synchronous archive_room RPC)
// and asserts every message lands exactly once — no loss, no interleaved/corrupt
// lines. Guards the serialization invariant; also a -race exercise.
func TestAppendArchiveConcurrentNoLoss(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)

	const goroutines = 8
	const perG = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				id := base*perG + i + 1
				h.appendArchive("room1", []types.Message{{ID: id, From: "a", To: "all", Content: "x"}})
			}
		}(g)
	}
	wg.Wait()

	got := readArchiveLines(t, dir, "room1")
	if len(got) != goroutines*perG {
		t.Fatalf("concurrent append: %d lines, want %d (loss or corruption)", len(got), goroutines*perG)
	}
	seen := make(map[int]int)
	for _, m := range got {
		seen[m.ID]++
	}
	for id := 1; id <= goroutines*perG; id++ {
		if seen[id] != 1 {
			t.Fatalf("ID %d appeared %d times, want exactly 1", id, seen[id])
		}
	}
}

// TestHandleArchiveRoom_UnknownRoomNoPhantom verifies archiving a never-used
// room writes nothing and does not materialize a phantom empty room.
func TestHandleArchiveRoom_UnknownRoomNoPhantom(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	h.desktopAuthToken = "secret"

	desktop := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleRequest(desktop, types.Request{
		ID:   "id",
		Type: "identify",
		Data: mustRawJSON(t, map[string]any{"client_type": "desktop", "auth_token": "secret"}),
	})
	if r := readResponse(t, desktop, "identify"); !r.Success {
		t.Fatalf("desktop identify should succeed: %s", r.Error)
	}

	h.handleRequest(desktop, types.Request{ID: "1", Type: "archive_room", Room: "ghost"})
	if r := readResponse(t, desktop, "archive_room"); !r.Success {
		t.Fatalf("archive_room on unknown room should still succeed: %s", r.Error)
	}

	if h.getRoom("ghost") != nil {
		t.Fatalf("archiving an unknown room created a phantom room")
	}
	if _, err := os.Stat(filepath.Join(dir, "hub-state", "archive", "ghost.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("archiving an unknown room wrote a file: err=%v", err)
	}
}

// TestHandleArchiveRoom_ReportsWriteFailure verifies the synchronous archive_room
// path surfaces a write failure to the caller instead of falsely reporting
// success (so DeleteTeam can't silently delete a team whose archive failed).
func TestHandleArchiveRoom_ReportsWriteFailure(t *testing.T) {
	// Point dataDir at a regular file so creating hub-state/archive under it fails.
	tmp := t.TempDir()
	badDataDir := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(badDataDir, []byte("x"), 0600); err != nil {
		t.Fatalf("seed bad data dir: %v", err)
	}
	h := newArchiveHub(badDataDir)
	h.desktopAuthToken = "secret"

	room := h.getOrCreateRoom("proj")
	if _, err := room.SendMessage("a", "all", "hi", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed message: %v", err)
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

	h.handleRequest(desktop, types.Request{ID: "1", Type: "archive_room", Room: "proj"})
	resp := readResponse(t, desktop, "archive_room")
	if resp.Success {
		t.Fatalf("expected archive_room to fail when the archive cannot be written")
	}
}

// TestHandleJoinRoom_RejectsInvalidRoomName verifies the hub refuses a room name
// that cannot be safely used as a filename, keeping room acceptance consistent
// with snapshot persistence and archiving (both key files by room name).
func TestHandleJoinRoom_RejectsInvalidRoomName(t *testing.T) {
	h, c := newTestHubClient()
	h.handleRequest(c, types.Request{
		ID:   "1",
		Type: "join_room",
		Room: "foo/bar",
		Data: mustRawJSON(t, map[string]any{"agent_name": "alice", "role": "developer"}),
	})
	if resp := readResponse(t, c, "join_room"); resp.Success {
		t.Fatalf("expected join_room with an invalid room name to fail")
	}
}

// TestHandleSubscribe_RejectsInvalidRoomName verifies subscribe also refuses an
// invalid room name rather than creating an unpersistable subscription entry.
func TestHandleSubscribe_RejectsInvalidRoomName(t *testing.T) {
	h, c := newTestHubClient()
	h.handleRequest(c, types.Request{
		ID:   "1",
		Type: "subscribe",
		Data: mustRawJSON(t, map[string][]string{"rooms": {"ok-room", "bad/room"}}),
	})
	if resp := readResponse(t, c, "subscribe"); resp.Success {
		t.Fatalf("expected subscribe with an invalid room name to fail")
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
