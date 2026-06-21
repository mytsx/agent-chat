package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"desktop/internal/types"
)

// TestSaveSession_RoundTrip verifies a session snapshot captures the room's full
// current state (messages + agent roster) as an immutable per-epoch file that
// round-trips back into a PersistedRoom.
func TestSaveSession_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)

	room := h.getOrCreateRoom("proj")
	if _, err := room.SendMessage("a", "all", "hello", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if _, _, err := room.Join("bob", "developer"); err != nil {
		t.Fatalf("join: %v", err)
	}

	path, count, skipped, err := h.saveSession("proj")
	if err != nil {
		t.Fatalf("saveSession: %v", err)
	}
	if skipped {
		t.Fatal("saveSession must not skip a non-empty, changed room")
	}
	if count != 2 { // 1 send + 1 join system message
		t.Fatalf("count = %d, want 2", count)
	}

	wantDir := filepath.Join(dir, "hub-state", "sessions", "proj")
	if got := filepath.Dir(path); got != wantDir {
		t.Fatalf("session dir = %s, want %s", got, wantDir)
	}
	if !strings.HasSuffix(path, ".json") {
		t.Fatalf("session file should end with .json: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file %s: %v", path, err)
	}
	var pr PersistedRoom
	if err := json.Unmarshal(data, &pr); err != nil {
		t.Fatalf("unmarshal session snapshot: %v", err)
	}
	if len(pr.Messages) != 2 {
		t.Fatalf("snapshot messages = %d, want 2", len(pr.Messages))
	}
	if _, ok := pr.Agents["bob"]; !ok {
		t.Fatalf("snapshot missing agent bob: %+v", pr.Agents)
	}
}

// TestSaveSession_SkipsEmptyRoom verifies a room with no messages is not
// snapshotted: an empty session is noise, so saveSession reports skipped and
// writes no file.
func TestSaveSession_SkipsEmptyRoom(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	h.getOrCreateRoom("empty") // created but never messaged

	path, count, skipped, err := h.saveSession("empty")
	if err != nil {
		t.Fatalf("saveSession: %v", err)
	}
	if !skipped {
		t.Fatal("saveSession must skip an empty room")
	}
	if path != "" || count != 0 {
		t.Fatalf("empty skip should return no path/count, got path=%q count=%d", path, count)
	}
	if files := readSessionFiles(t, dir, "empty"); len(files) != 0 {
		t.Fatalf("empty room wrote %d session files, want 0", len(files))
	}
}

// TestSaveSession_UnknownRoomNoPhantom verifies saving a room that was never
// created writes nothing and does not materialize a phantom room.
func TestSaveSession_UnknownRoomNoPhantom(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)

	path, count, skipped, err := h.saveSession("ghost")
	if err != nil {
		t.Fatalf("saveSession on unknown room should not error: %v", err)
	}
	if !skipped || path != "" || count != 0 {
		t.Fatalf("unknown room should skip with no path/count, got skipped=%v path=%q count=%d", skipped, path, count)
	}
	if h.getRoom("ghost") != nil {
		t.Fatal("saving an unknown room created a phantom room")
	}
	if files := readSessionFiles(t, dir, "ghost"); len(files) != 0 {
		t.Fatalf("unknown room wrote %d session files, want 0", len(files))
	}
}

// TestSaveSession_RejectsTraversalRoom verifies a path-traversal room name is
// refused and writes nothing — even when such a room exists in memory, the file
// keyed by its name must never escape the sessions directory.
func TestSaveSession_RejectsTraversalRoom(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)

	room := h.getOrCreateRoom("../evil")
	if _, err := room.SendMessage("a", "all", "x", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	path, _, _, err := h.saveSession("../evil")
	if err == nil {
		t.Fatal("saveSession must reject a path-traversal room name")
	}
	if path != "" {
		t.Fatalf("rejected save should return no path, got %q", path)
	}
	// No file may be written under the data dir for the traversal.
	if _, statErr := os.Stat(filepath.Join(dir, "hub-state", "evil")); !os.IsNotExist(statErr) {
		t.Fatalf("traversal escaped sessions dir: err=%v", statErr)
	}
}

// TestSaveSession_SkipsUnchangedRoom verifies a room that has not changed since
// its last snapshot is skipped — re-saving the same state is wasteful. (Roster
// changes count as changes: join/leave append a system message, bumping the ID.)
func TestSaveSession_SkipsUnchangedRoom(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	room := h.getOrCreateRoom("proj")
	if _, err := room.SendMessage("a", "all", "hi", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, _, skipped, err := h.saveSession("proj"); err != nil || skipped {
		t.Fatalf("first save should write (skipped=%v, err=%v)", skipped, err)
	}

	path, count, skipped, err := h.saveSession("proj")
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if !skipped {
		t.Fatal("unchanged room should be skipped on re-save")
	}
	if path != "" || count != 0 {
		t.Fatalf("skipped save should return no path/count, got path=%q count=%d", path, count)
	}
	if files := readSessionFiles(t, dir, "proj"); len(files) != 1 {
		t.Fatalf("unchanged re-save wrote %d files, want 1", len(files))
	}
}

// TestSaveSession_DistinctImmutableFilesPerSave verifies each changed save lands
// in its own file and never overwrites an earlier one — even two saves in the
// same wall-clock second (where epoch collides) must produce two distinct,
// independently-preserved snapshots.
func TestSaveSession_DistinctImmutableFilesPerSave(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	room := h.getOrCreateRoom("proj")
	if _, err := room.SendMessage("a", "all", "first", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed 1: %v", err)
	}

	path1, _, _, err := h.saveSession("proj")
	if err != nil || path1 == "" {
		t.Fatalf("first save: path=%q err=%v", path1, err)
	}

	if _, err := room.SendMessage("a", "all", "second", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	path2, _, skipped, err := h.saveSession("proj")
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if skipped {
		t.Fatal("a changed room must not be skipped")
	}
	if path1 == path2 {
		t.Fatalf("two saves overwrote the same file: %s", path1)
	}
	if files := readSessionFiles(t, dir, "proj"); len(files) != 2 {
		t.Fatalf("expected 2 immutable session files, got %d", len(files))
	}

	// First snapshot is untouched: still exactly the first message.
	pr1 := loadSession(t, path1)
	if len(pr1.Messages) != 1 || pr1.Messages[0].Content != "first" {
		t.Fatalf("first snapshot mutated: %+v", pr1.Messages)
	}
	// Second snapshot has both messages.
	pr2 := loadSession(t, path2)
	if len(pr2.Messages) != 2 {
		t.Fatalf("second snapshot messages = %d, want 2", len(pr2.Messages))
	}
}

// TestSaveSession_EmptyDataDirNoop verifies saveSession is a silent no-op when no
// data dir is configured, so unit hubs built with New("", ...) never write to the
// current working directory.
func TestSaveSession_EmptyDataDirNoop(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("hub-state") }) // belt-and-suspenders if it leaks
	h := newArchiveHub("")
	room := h.getOrCreateRoom("proj")
	if _, err := room.SendMessage("a", "all", "x", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	path, count, skipped, err := h.saveSession("proj")
	if err != nil {
		t.Fatalf("saveSession with empty data dir should not error: %v", err)
	}
	if !skipped || path != "" || count != 0 {
		t.Fatalf("empty data dir should no-op, got skipped=%v path=%q count=%d", skipped, path, count)
	}
	if _, statErr := os.Stat(filepath.Join("hub-state", "sessions", "proj")); !os.IsNotExist(statErr) {
		t.Fatal("empty data dir wrote relative to the working directory")
	}
}

// TestSaveSession_NoStrayTempFile guards the atomic-write invariant: a successful
// save leaves only the final {epoch}.json, never a half-written .tmp.
func TestSaveSession_NoStrayTempFile(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	room := h.getOrCreateRoom("proj")
	if _, err := room.SendMessage("a", "all", "x", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, _, err := h.saveSession("proj"); err != nil {
		t.Fatalf("saveSession: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "hub-state", "sessions", "proj"))
	if err != nil {
		t.Fatalf("read sessions dir: %v", err)
	}
	jsonCount := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("stray temp file left behind: %s", e.Name())
		}
		if strings.HasSuffix(e.Name(), ".json") {
			jsonCount++
		}
	}
	if jsonCount != 1 {
		t.Fatalf("expected exactly 1 .json snapshot, got %d", jsonCount)
	}
}

// TestHandleSaveSession_DesktopWritesSnapshot verifies the save_session RPC: it is
// desktop-authorized only and synchronously writes the room's full snapshot,
// reporting the message count.
func TestHandleSaveSession_DesktopWritesSnapshot(t *testing.T) {
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

	// Unauthorized client cannot save a session.
	guest := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleRequest(guest, types.Request{ID: "1", Type: "save_session", Room: "proj"})
	if resp := readResponse(t, guest, "save_session"); resp.Success {
		t.Fatal("expected unauthorized save_session to fail")
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

	h.handleRequest(desktop, types.Request{ID: "2", Type: "save_session", Room: "proj"})
	resp := readResponse(t, desktop, "save_session")
	if !resp.Success {
		t.Fatalf("expected desktop save_session to succeed: %s", resp.Error)
	}

	var body struct {
		Saved bool `json:"saved"`
		Count int  `json:"count"`
	}
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("decode save_session response: %v", err)
	}
	if !body.Saved || body.Count != 2 {
		t.Fatalf("response saved=%v count=%d, want saved=true count=2", body.Saved, body.Count)
	}

	files := readSessionFiles(t, dir, "proj")
	if len(files) != 1 {
		t.Fatalf("save_session wrote %d files, want 1", len(files))
	}
	if pr := loadSession(t, files[0]); len(pr.Messages) != 2 {
		t.Fatalf("snapshot messages = %d, want 2", len(pr.Messages))
	}
}

// TestHandleSaveSession_UnknownRoomNoPhantom verifies save_session on a
// never-created room succeeds with saved=false, writes nothing, and does not
// materialize a phantom room.
func TestHandleSaveSession_UnknownRoomNoPhantom(t *testing.T) {
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

	h.handleRequest(desktop, types.Request{ID: "1", Type: "save_session", Room: "ghost"})
	resp := readResponse(t, desktop, "save_session")
	if !resp.Success {
		t.Fatalf("save_session on unknown room should still succeed: %s", resp.Error)
	}
	var body struct {
		Saved bool `json:"saved"`
	}
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Saved {
		t.Fatal("unknown room should report saved=false")
	}
	if h.getRoom("ghost") != nil {
		t.Fatal("save_session created a phantom room")
	}
	if files := readSessionFiles(t, dir, "ghost"); len(files) != 0 {
		t.Fatalf("unknown room wrote %d files, want 0", len(files))
	}
}

// TestSaveSession_ConcurrentNoCorruption exercises saveSession from many
// goroutines racing on the same room (each adds a message, then snapshots) and
// asserts every written file is valid JSON — no torn/interleaved writes, no
// race, no panic. A -race exercise for the sessionMu serialization invariant.
func TestSaveSession_ConcurrentNoCorruption(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	room := h.getOrCreateRoom("proj")

	const goroutines = 8
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := room.SendMessage("a", "all", fmt.Sprintf("m%d", n), false, "", SendOptions{}); err != nil {
				t.Errorf("send: %v", err)
				return
			}
			if _, _, _, err := h.saveSession("proj"); err != nil {
				t.Errorf("saveSession: %v", err)
			}
		}(g)
	}
	wg.Wait()

	files := readSessionFiles(t, dir, "proj")
	if len(files) == 0 {
		t.Fatal("concurrent saves wrote no files")
	}
	for _, p := range files {
		_ = loadSession(t, p) // unparseable (torn) file fails the test
	}
}

// TestSaveSession_ClearRoomResetsUnchangedTracking verifies the unchanged-skip
// optimization does not misfire after a clear_room. clear_room wipes the room and
// new message IDs restart at 1, so a fresh conversation can reach the same max ID
// the pre-clear snapshot had. The clear must reset the per-room ID tracking so
// that coincidental match doesn't wrongly skip the new session's snapshot.
func TestSaveSession_ClearRoomResetsUnchangedTracking(t *testing.T) {
	dir := t.TempDir()
	h := newArchiveHub(dir)
	h.desktopAuthToken = "secret"

	room := h.getOrCreateRoom("proj")
	if _, err := room.SendMessage("a", "all", "one", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// First snapshot writes; tracker records max ID = 1.
	if _, _, skipped, err := h.saveSession("proj"); err != nil || skipped {
		t.Fatalf("first save should write (skipped=%v err=%v)", skipped, err)
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
		t.Fatalf("clear_room should succeed: %s", r.Error)
	}

	// New conversation after the clear restarts at ID 1 — same max ID as before.
	if _, err := room.SendMessage("a", "all", "fresh", false, "", SendOptions{}); err != nil {
		t.Fatalf("post-clear send: %v", err)
	}
	_, count, skipped, err := h.saveSession("proj")
	if err != nil {
		t.Fatalf("save after clear: %v", err)
	}
	if skipped {
		t.Fatal("save after clear must not skip despite the ID coinciding with the pre-clear max")
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

// TestSessionsDir_ConfinedToBase verifies the path-containment barrier: a normal
// room resolves under the sessions base, while any segment that would escape it
// is refused — defense-in-depth beyond ValidateName for the path-injection sink.
func TestSessionsDir_ConfinedToBase(t *testing.T) {
	h := newArchiveHub("/data")

	dir, err := h.sessionsDir("proj")
	if err != nil {
		t.Fatalf("valid room: %v", err)
	}
	if want := filepath.Join("/data", "hub-state", "sessions", "proj"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}

	for _, bad := range []string{"../evil", "a/../../etc", "..", ""} {
		if _, err := h.sessionsDir(bad); err == nil {
			t.Fatalf("sessionsDir(%q) should error (escapes sessions base)", bad)
		}
	}
}

// TestSaveSession_SeededFromDiskSkipsUnchangedAfterRestart verifies the
// unchanged-skip survives a hub restart: loadPersistedState seeds the per-room
// tracker from disk, so opening and quitting the app with no activity does not
// write a duplicate snapshot of an unchanged room.
func TestSaveSession_SeededFromDiskSkipsUnchangedAfterRestart(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "hub-state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	persisted := PersistedRoom{
		Messages: []types.Message{
			{ID: 1, From: "a", To: "all", Content: "one", Type: "broadcast"},
			{ID: 2, From: "b", To: "all", Content: "two", Type: "broadcast"},
		},
		Agents: map[string]types.Agent{"a": {Role: "dev"}},
	}
	data, _ := json.MarshalIndent(persisted, "", "  ")
	if err := os.WriteFile(filepath.Join(stateDir, "proj.json"), data, 0644); err != nil {
		t.Fatalf("write persisted: %v", err)
	}

	// Fresh hub (as after a restart) loads the persisted state.
	h := newArchiveHub(dir)
	h.loadPersistedState()

	// Idle quit: no new messages → must skip (tracker seeded from the loaded max ID).
	path, count, skipped, err := h.saveSession("proj")
	if err != nil {
		t.Fatalf("saveSession: %v", err)
	}
	if !skipped {
		t.Fatal("a restart with no activity must skip (seeded from disk), not write a duplicate")
	}
	if path != "" || count != 0 {
		t.Fatalf("seeded skip should return no path/count, got path=%q count=%d", path, count)
	}
	if files := readSessionFiles(t, dir, "proj"); len(files) != 0 {
		t.Fatalf("restart idle quit wrote %d session files, want 0", len(files))
	}

	// A NEW message after the restart advances the tracker and writes.
	room := h.getRoom("proj")
	if _, err := room.SendMessage("a", "all", "three", false, "", SendOptions{}); err != nil {
		t.Fatalf("post-restart send: %v", err)
	}
	if _, _, skipped, err := h.saveSession("proj"); err != nil || skipped {
		t.Fatalf("a changed room after restart must write (skipped=%v err=%v)", skipped, err)
	}
}

// TestSaveSession_StatErrorSurfacesNotSpin verifies the epoch-collision loop
// surfaces an unexpected stat error instead of treating it as a collision and
// spinning forever. An unsearchable (mode 000) sessions dir makes os.Stat on a
// child return EACCES (not IsNotExist).
func TestSaveSession_StatErrorSurfacesNotSpin(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits are not enforced")
	}
	dir := t.TempDir()
	h := newArchiveHub(dir)
	room := h.getOrCreateRoom("proj")
	if _, err := room.SendMessage("a", "all", "x", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sdir := filepath.Join(dir, "hub-state", "sessions", "proj")
	if err := os.MkdirAll(sdir, 0700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.Chmod(sdir, 0000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(sdir, 0700) }) // restore so TempDir cleanup can remove it

	done := make(chan struct{})
	var serr error
	go func() {
		_, _, _, serr = h.saveSession("proj")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("saveSession spun instead of returning on a non-IsNotExist stat error")
	}
	if serr == nil {
		t.Fatal("saveSession should surface the stat error, not loop or succeed")
	}
}

// readSessionFiles lists a room's session snapshot files (epoch.json), sorted by a room's session snapshot files (epoch.json), sorted by
// name (epoch order), and returns their full paths.
func readSessionFiles(t *testing.T, dataDir, room string) []string {
	t.Helper()
	dir := filepath.Join(dataDir, "hub-state", "sessions", room)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read sessions dir %s: %v", dir, err)
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	return paths
}

// loadSession reads and unmarshals a session snapshot file.
func loadSession(t *testing.T, path string) PersistedRoom {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session %s: %v", path, err)
	}
	var pr PersistedRoom
	if err := json.Unmarshal(data, &pr); err != nil {
		t.Fatalf("unmarshal session %s: %v", path, err)
	}
	return pr
}
