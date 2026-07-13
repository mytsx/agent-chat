package hub

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"desktop/internal/types"
)

// A traversal room name reaching log_message / get_last_message_id would be
// materialized in memory by getOrCreateRoom and then written to disk by
// persistRoom (hub-state/{room}.json). Both handlers must reject it up front,
// matching clear_room/archive_room/delete_room.
func TestRoomNameValidation_RejectsTraversal(t *testing.T) {
	t.Run("log_message rejects traversal room", func(t *testing.T) {
		h, c := authedDesktop(t)
		h.handleRequest(c, types.Request{
			ID: "1", Type: "log_message", Room: "../evil",
			Data: mustRawJSON(t, map[string]any{"content": "hi"}),
		})
		if readResponse(t, c, "log_message").Success {
			t.Fatalf("path-traversal oda adı reddedilmeli")
		}
		if h.getRoom("../evil") != nil {
			t.Fatalf("geçersiz oda in-memory'de materialize EDİLMEMELİ")
		}
	})

	t.Run("get_last_message_id rejects traversal room", func(t *testing.T) {
		h, c := authedDesktop(t)
		h.handleRequest(c, types.Request{ID: "1", Type: "get_last_message_id", Room: "../evil"})
		if readResponse(t, c, "get_last_message_id").Success {
			t.Fatalf("path-traversal oda adı reddedilmeli")
		}
		if h.getRoom("../evil") != nil {
			t.Fatalf("geçersiz oda in-memory'de materialize EDİLMEMELİ")
		}
	})
}

// persistRoom is the last write path that could turn an unvalidated in-memory
// room into a write outside hub-state. Its guard must skip a traversal name even
// when called directly (defense-in-depth beyond the handler checks).
func TestPersistRoom_RejectsTraversalName(t *testing.T) {
	h := New(t.TempDir(), "default", log.New(io.Discard, "", 0))
	rs := h.getOrCreateRoom("legit")
	if _, err := rs.SendMessage("a", "all", "hi", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// "../escape" would resolve to dataDir/escape.json — outside hub-state.
	h.persistRoom("../escape", rs)

	if _, err := os.Stat(filepath.Join(h.dataDir, "escape.json")); !os.IsNotExist(err) {
		t.Fatalf("traversal oda adı hub-state DIŞINA yazılmamalıydı")
	}
	// A legitimate name still persists normally.
	h.persistRoom("legit", rs)
	if _, err := os.Stat(filepath.Join(h.dataDir, "hub-state", "legit.json")); err != nil {
		t.Fatalf("geçerli oda normal şekilde persist edilmeliydi: %v", err)
	}
}
