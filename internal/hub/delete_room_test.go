package hub

import (
	"io"
	"log"
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
