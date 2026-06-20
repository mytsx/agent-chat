package hub

import (
	"encoding/json"
	"testing"

	"desktop/internal/types"
)

func TestHandleListRoomsDetailed_RequiresDesktopAuth(t *testing.T) {
	h, desktop := newTestHubClient()
	h.desktopAuthToken = "desktop-secret"

	// Seed a room with one message so the summary is non-trivial.
	if _, err := h.getOrCreateRoom("proj").SendMessage("a", "all", "hi", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed send: %v", err)
	}

	// Unauthenticated client cannot list detailed rooms.
	guest := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleRequest(guest, types.Request{ID: "1", Type: "list_rooms_detailed"})
	if resp := readResponse(t, guest, "list_rooms_detailed"); resp.Success {
		t.Fatalf("expected unauthenticated list_rooms_detailed to fail")
	}

	// Authorize the desktop client.
	h.handleRequest(desktop, types.Request{
		ID:   "id",
		Type: "identify",
		Data: mustRawJSON(t, map[string]any{"client_type": "desktop", "auth_token": "desktop-secret"}),
	})
	if r := readResponse(t, desktop, "identify"); !r.Success {
		t.Fatalf("desktop identify should succeed: %s", r.Error)
	}

	// Authorized desktop gets structured summaries including the seeded room.
	h.handleRequest(desktop, types.Request{ID: "2", Type: "list_rooms_detailed"})
	resp := readResponse(t, desktop, "list_rooms_detailed")
	if !resp.Success {
		t.Fatalf("expected desktop list_rooms_detailed to succeed: %s", resp.Error)
	}

	var data struct {
		Rooms []types.RoomSummary `json:"rooms"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	var found *types.RoomSummary
	for i := range data.Rooms {
		if data.Rooms[i].Name == "proj" {
			found = &data.Rooms[i]
		}
	}
	if found == nil {
		t.Fatalf("expected 'proj' room in response, got %+v", data.Rooms)
	}
	if found.MessageCount != 1 {
		t.Fatalf("proj message count = %d, want 1", found.MessageCount)
	}
}
