package hub

import (
	"encoding/json"
	"io"
	"log"
	"testing"
	"time"

	"desktop/internal/types"
)

func newTestHubClient() (*Hub, *Client) {
	h := New("", "default", log.New(io.Discard, "", 0))
	c := &Client{
		hub:   h,
		send:  make(chan []byte, 64),
		rooms: make(map[string]bool),
	}
	return h, c
}

func mustRawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	return b
}

func readResponse(t *testing.T, c *Client, reqType string) types.Response {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case payload := <-c.send:
			var resp types.Response
			if err := json.Unmarshal(payload, &resp); err == nil && resp.RequestType != "" {
				if reqType == "" || resp.RequestType == reqType {
					return resp
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting response for %s", reqType)
		}
	}
}

func TestHandleSendMessage_BeforeJoinRejected(t *testing.T) {
	h, c := newTestHubClient()
	req := types.Request{
		ID:   "1",
		Type: "send_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"from":    "alice",
			"to":      "bob",
			"content": "hello",
		}),
	}

	h.handleRequest(c, req)
	resp := readResponse(t, c, "send_message")
	if resp.Success {
		t.Fatalf("expected join-before-send rejection")
	}
}

func TestHandleSendMessage_FromMismatchRejected(t *testing.T) {
	h, c := newTestHubClient()

	joinReq := types.Request{
		ID:   "join-1",
		Type: "join_room",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"agent_name": "alice",
			"role":       "developer",
		}),
	}
	h.handleRequest(c, joinReq)
	_ = readResponse(t, c, "join_room")

	sendReq := types.Request{
		ID:   "msg-1",
		Type: "send_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"from":    "bob",
			"to":      "alice",
			"content": "spoof",
		}),
	}
	h.handleRequest(c, sendReq)
	resp := readResponse(t, c, "send_message")
	if resp.Success {
		t.Fatalf("expected from mismatch rejection")
	}
}

func TestHandleSendMessage_ManagerInterception(t *testing.T) {
	h, manager := newTestHubClient()
	alice := &Client{
		hub:   h,
		send:  make(chan []byte, 64),
		rooms: make(map[string]bool),
	}

	h.handleRequest(manager, types.Request{
		ID:   "join-mgr",
		Type: "join_room",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"agent_name": "manager",
			"role":       "manager",
		}),
	})
	_ = readResponse(t, manager, "join_room")

	h.handleRequest(alice, types.Request{
		ID:   "join-alice",
		Type: "join_room",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"agent_name": "alice",
			"role":       "developer",
		}),
	})
	_ = readResponse(t, alice, "join_room")

	h.handleRequest(alice, types.Request{
		ID:   "msg-1",
		Type: "send_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"from":    "alice",
			"to":      "bob",
			"content": "hello bob",
		}),
	})
	resp := readResponse(t, alice, "send_message")
	if !resp.Success {
		t.Fatalf("expected intercepted send success, got error=%s", resp.Error)
	}

	roomState := h.getOrCreateRoom("r1")
	messages := roomState.GetMessages()
	if len(messages) == 0 {
		t.Fatalf("expected at least one message")
	}
	last := messages[len(messages)-1]
	if last.To != "manager" {
		t.Fatalf("expected intercepted target manager, got %q", last.To)
	}
	if last.OriginalTo != "bob" {
		t.Fatalf("expected original_to=bob, got %q", last.OriginalTo)
	}
	if !last.RoutedByManager {
		t.Fatalf("expected routed_by_manager=true")
	}
}
