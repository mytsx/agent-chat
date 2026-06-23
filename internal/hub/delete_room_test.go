package hub

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"desktop/internal/types"
)

// authedDesktop builds a hub (with a real temp dataDir) + an authorized desktop client.
func authedDesktop(t *testing.T) (*Hub, *Client) {
	t.Helper()
	h := New(t.TempDir(), "default", log.New(io.Discard, "", 0))
	h.desktopAuthToken = "secret"
	c := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleRequest(c, types.Request{
		ID: "id", Type: "identify",
		Data: mustRawJSON(t, map[string]any{"client_type": "desktop", "auth_token": "secret"}),
	})
	if r := readResponse(t, c, "identify"); !r.Success {
		t.Fatalf("desktop identify failed: %s", r.Error)
	}
	return h, c
}

func TestGetMessagesRaw_DoesNotReviveRoom(t *testing.T) {
	h, c := authedDesktop(t)

	h.handleRequest(c, types.Request{ID: "1", Type: "get_messages_raw", Room: "ghost"})
	if resp := readResponse(t, c, "get_messages_raw"); !resp.Success {
		t.Fatalf("get_messages_raw should succeed with empty result: %s", resp.Error)
	}
	if h.getRoom("ghost") != nil {
		t.Fatalf("ham mesaj okuması var-olmayan odayı materialize ETMEMELİ")
	}

	h.handleRequest(c, types.Request{ID: "2", Type: "get_agents", Room: "ghost2"})
	if resp := readResponse(t, c, "get_agents"); !resp.Success {
		t.Fatalf("get_agents should succeed with empty result: %s", resp.Error)
	}
	if h.getRoom("ghost2") != nil {
		t.Fatalf("ham agent okuması var-olmayan odayı materialize ETMEMELİ")
	}
}

func TestTombstone_PersistSkipsAndRecreateClears(t *testing.T) {
	h := New(t.TempDir(), "default", log.New(io.Discard, "", 0))

	// Dirty bir oda + tombstone → persistDirtyRooms onu YAZMAMALI.
	rs := h.getOrCreateRoom("doomed")
	if _, err := rs.SendMessage("a", "all", "hi", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h.mu.Lock()
	h.deletedRooms["doomed"] = true
	h.mu.Unlock()

	h.persistDirtyRooms()

	if _, err := os.Stat(filepath.Join(h.dataDir, "hub-state", "doomed.json")); !os.IsNotExist(err) {
		t.Fatalf("tombstoned oda persist edilmemeliydi (dosya var)")
	}

	// persistRoom'un KENDİ guard'ı da tombstoned odayı yazmamalı (TOCTOU race fix):
	// doğrudan çağır, dosya oluşmamalı.
	h.persistRoom("doomed", rs)
	if _, err := os.Stat(filepath.Join(h.dataDir, "hub-state", "doomed.json")); !os.IsNotExist(err) {
		t.Fatalf("persistRoom tombstoned odayı yazmamalı")
	}

	// Gerçek delete_room akışını taklit et: oda h.rooms'tan da kalkar. Sonra meşru
	// recreation (getOrCreateRoom create-branch'i) tombstone'u temizlemeli.
	h.mu.Lock()
	delete(h.rooms, "doomed")
	h.mu.Unlock()

	h.getOrCreateRoom("doomed")
	h.mu.RLock()
	stillTomb := h.deletedRooms["doomed"]
	h.mu.RUnlock()
	if stillTomb {
		t.Fatalf("getOrCreateRoom recreation tombstone'u temizlemeliydi")
	}
}

func TestHandleDeleteRoom(t *testing.T) {
	t.Run("requires desktop auth", func(t *testing.T) {
		h := New(t.TempDir(), "default", log.New(io.Discard, "", 0))
		guest := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
		h.handleRequest(guest, types.Request{ID: "1", Type: "delete_room", Room: "x"})
		if readResponse(t, guest, "delete_room").Success {
			t.Fatalf("yetkisiz client silememeli")
		}
	})

	t.Run("rejects default room", func(t *testing.T) {
		h, c := authedDesktop(t)
		h.handleRequest(c, types.Request{ID: "1", Type: "delete_room", Room: "default"})
		if readResponse(t, c, "delete_room").Success {
			t.Fatalf("default oda silinememeli")
		}
	})

	t.Run("rejects path-traversal room name", func(t *testing.T) {
		h, c := authedDesktop(t)
		// A crafted name must never reach os.Remove (would delete files outside hub-state).
		h.handleRequest(c, types.Request{ID: "1", Type: "delete_room", Room: "../evil"})
		if readResponse(t, c, "delete_room").Success {
			t.Fatalf("path-traversal oda adı reddedilmeli")
		}
	})

	t.Run("rejects subscribed room", func(t *testing.T) {
		h, c := authedDesktop(t)
		h.getOrCreateRoom("live")
		sub := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
		h.mu.Lock()
		h.subs["live"] = map[*Client]bool{sub: true}
		h.mu.Unlock()
		h.handleRequest(c, types.Request{ID: "1", Type: "delete_room", Room: "live"})
		if readResponse(t, c, "delete_room").Success {
			t.Fatalf("abonesi olan oda silinememeli")
		}
	})

	t.Run("deletes orphan room, preserves archive", func(t *testing.T) {
		h, c := authedDesktop(t)
		rs := h.getOrCreateRoom("orphan")
		if _, err := rs.SendMessage("a", "all", "hi", false, "", SendOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		h.persistDirtyRooms() // hub-state/orphan.json yaz

		// Stale session signature: silmede temizlenmeli (aynı adla recreate'te ilk
		// snapshot yanlışlıkla atlanmasın).
		h.sessionMu.Lock()
		h.sessionLastSig["orphan"] = "stale-sig"
		h.sessionMu.Unlock()

		statePath := filepath.Join(h.dataDir, "hub-state", "orphan.json")
		if _, err := os.Stat(statePath); err != nil {
			t.Fatalf("state dosyası önce var olmalı: %v", err)
		}
		// Arşiv dosyasını simüle et (silmede korunmalı).
		archiveDir := filepath.Join(h.dataDir, "hub-state", "archive")
		os.MkdirAll(archiveDir, 0700)
		archivePath := filepath.Join(archiveDir, "orphan.jsonl")
		os.WriteFile(archivePath, []byte("{}\n"), 0644)

		h.handleRequest(c, types.Request{ID: "1", Type: "delete_room", Room: "orphan"})
		if resp := readResponse(t, c, "delete_room"); !resp.Success {
			t.Fatalf("orphan silme başarılı olmalı: %s", resp.Error)
		}

		if h.getRoom("orphan") != nil {
			t.Fatalf("oda in-memory'den kaldırılmalı")
		}
		h.mu.RLock()
		tomb := h.deletedRooms["orphan"]
		h.mu.RUnlock()
		if !tomb {
			t.Fatalf("oda tombstone'lanmalı")
		}
		if _, err := os.Stat(statePath); !os.IsNotExist(err) {
			t.Fatalf("state dosyası silinmeliydi")
		}
		if _, err := os.Stat(archivePath); err != nil {
			t.Fatalf("arşiv dosyası KORUNMALIYDI: %v", err)
		}
		h.sessionMu.Lock()
		_, sigStillThere := h.sessionLastSig["orphan"]
		h.sessionMu.Unlock()
		if sigStillThere {
			t.Fatalf("sessionLastSig silmede temizlenmeliydi")
		}
	})
}
