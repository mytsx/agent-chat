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

func TestHandleSendMessage_ManagerBypass(t *testing.T) {
	h, manager := newTestHubClient()
	h.setConfiguredManager("r1", "manager")

	// Manager joins
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

	// Manager sends a message directly to alice — should NOT be intercepted
	h.handleRequest(manager, types.Request{
		ID:   "msg-1",
		Type: "send_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"from":    "manager",
			"to":      "alice",
			"content": "hello alice, please do X",
		}),
	})
	resp := readResponse(t, manager, "send_message")
	if !resp.Success {
		t.Fatalf("expected manager send success, got error=%s", resp.Error)
	}

	roomState := h.getOrCreateRoom("r1")
	messages := roomState.GetMessages()
	// Find the non-system message
	var found bool
	for _, msg := range messages {
		if msg.From == "manager" && msg.Type != "system" {
			if msg.To != "alice" {
				t.Fatalf("expected manager message to go directly to alice, got to=%q", msg.To)
			}
			if msg.RoutedByManager {
				t.Fatalf("manager's own message should NOT have routed_by_manager=true")
			}
			if msg.OriginalTo != "" {
				t.Fatalf("manager's own message should NOT have original_to, got %q", msg.OriginalTo)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected to find manager's message in room")
	}
}

func TestHandleSendMessage_ManagerInterception(t *testing.T) {
	h, manager := newTestHubClient()
	h.setConfiguredManager("r1", "manager")
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

func TestHandleIdentify_DesktopRequiresToken(t *testing.T) {
	h, c := newTestHubClient()
	h.desktopAuthToken = "desktop-secret"

	h.handleRequest(c, types.Request{
		ID:   "id-1",
		Type: "identify",
		Data: mustRawJSON(t, map[string]any{
			"client_type": "desktop",
		}),
	})
	resp := readResponse(t, c, "identify")
	if resp.Success {
		t.Fatalf("expected desktop identify without token to fail")
	}

	h.handleRequest(c, types.Request{
		ID:   "id-2",
		Type: "identify",
		Data: mustRawJSON(t, map[string]any{
			"client_type": "desktop",
			"auth_token":  "wrong",
		}),
	})
	resp = readResponse(t, c, "identify")
	if resp.Success {
		t.Fatalf("expected desktop identify with wrong token to fail")
	}

	h.handleRequest(c, types.Request{
		ID:   "id-3",
		Type: "identify",
		Data: mustRawJSON(t, map[string]any{
			"client_type": "desktop",
			"auth_token":  "desktop-secret",
		}),
	})
	resp = readResponse(t, c, "identify")
	if !resp.Success {
		t.Fatalf("expected desktop identify with valid token to succeed: %s", resp.Error)
	}
}

func TestHandleJoinRoom_ManagerRequiresConfiguredManager(t *testing.T) {
	h, manager := newTestHubClient()

	h.handleRequest(manager, types.Request{
		ID:   "join-mgr-deny",
		Type: "join_room",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"agent_name": "manager",
			"role":       "manager",
		}),
	})
	resp := readResponse(t, manager, "join_room")
	if resp.Success {
		t.Fatalf("expected manager join to fail when manager is not configured")
	}

	h.setConfiguredManager("r1", "manager")
	h.handleRequest(manager, types.Request{
		ID:   "join-mgr-ok",
		Type: "join_room",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"agent_name": "manager",
			"role":       "manager",
		}),
	})
	resp = readResponse(t, manager, "join_room")
	if !resp.Success {
		t.Fatalf("expected configured manager to join successfully: %s", resp.Error)
	}
}

func TestHandleGetAllMessages_RequiresActiveManagerForAgents(t *testing.T) {
	h, alice := newTestHubClient()

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
		ID:   "all-1",
		Type: "get_all_messages",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"since_id": 0,
			"limit":    10,
		}),
	})
	resp := readResponse(t, alice, "get_all_messages")
	if resp.Success {
		t.Fatalf("expected non-manager agent to be denied get_all_messages")
	}
}

func TestHandleClearRoom_RequiresDesktopOrActiveManager(t *testing.T) {
	h, alice := newTestHubClient()
	h.setConfiguredManager("r1", "manager")

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
		ID:   "clear-alice",
		Type: "clear_room",
		Room: "r1",
	})
	resp := readResponse(t, alice, "clear_room")
	if resp.Success {
		t.Fatalf("expected non-manager agent clear_room to fail")
	}

	manager := &Client{
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

	h.handleRequest(manager, types.Request{
		ID:   "clear-mgr",
		Type: "clear_room",
		Room: "r1",
	})
	resp = readResponse(t, manager, "clear_room")
	if !resp.Success {
		t.Fatalf("expected active manager clear_room to succeed: %s", resp.Error)
	}
}

func TestHandleGetRawEndpoints_RequireDesktopAuth(t *testing.T) {
	h, desktop := newTestHubClient()
	h.desktopAuthToken = "desktop-secret"

	// Unauthenticated client cannot access raw endpoints.
	guest := &Client{
		hub:   h,
		send:  make(chan []byte, 64),
		rooms: make(map[string]bool),
	}
	h.handleRequest(guest, types.Request{
		ID:   "raw-1",
		Type: "get_messages_raw",
		Room: "r1",
	})
	resp := readResponse(t, guest, "get_messages_raw")
	if resp.Success {
		t.Fatalf("expected unauthenticated get_messages_raw to fail")
	}

	h.handleRequest(guest, types.Request{
		ID:   "raw-2",
		Type: "get_agents",
		Room: "r1",
	})
	resp = readResponse(t, guest, "get_agents")
	if resp.Success {
		t.Fatalf("expected unauthenticated get_agents to fail")
	}

	h.handleRequest(desktop, types.Request{
		ID:   "id-desktop",
		Type: "identify",
		Data: mustRawJSON(t, map[string]any{
			"client_type": "desktop",
			"auth_token":  "desktop-secret",
		}),
	})
	resp = readResponse(t, desktop, "identify")
	if !resp.Success {
		t.Fatalf("expected desktop identify to succeed: %s", resp.Error)
	}

	h.handleRequest(desktop, types.Request{
		ID:   "raw-3",
		Type: "get_messages_raw",
		Room: "r1",
	})
	resp = readResponse(t, desktop, "get_messages_raw")
	if !resp.Success {
		t.Fatalf("expected authenticated desktop get_messages_raw to succeed: %s", resp.Error)
	}

	h.handleRequest(desktop, types.Request{
		ID:   "raw-4",
		Type: "get_agents",
		Room: "r1",
	})
	resp = readResponse(t, desktop, "get_agents")
	if !resp.Success {
		t.Fatalf("expected authenticated desktop get_agents to succeed: %s", resp.Error)
	}
}

func TestHandleSetManager_RequiresDesktopAuth(t *testing.T) {
	h, desktop := newTestHubClient()
	h.desktopAuthToken = "desktop-secret"

	guest := &Client{
		hub:   h,
		send:  make(chan []byte, 64),
		rooms: make(map[string]bool),
	}

	h.handleRequest(guest, types.Request{
		ID:   "set-1",
		Type: "set_manager",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"manager_agent": "manager",
		}),
	})
	resp := readResponse(t, guest, "set_manager")
	if resp.Success {
		t.Fatalf("expected unauthenticated set_manager to fail")
	}

	h.handleRequest(desktop, types.Request{
		ID:   "id-desktop",
		Type: "identify",
		Data: mustRawJSON(t, map[string]any{
			"client_type": "desktop",
			"auth_token":  "desktop-secret",
		}),
	})
	resp = readResponse(t, desktop, "identify")
	if !resp.Success {
		t.Fatalf("expected desktop identify to succeed: %s", resp.Error)
	}

	h.handleRequest(desktop, types.Request{
		ID:   "set-2",
		Type: "set_manager",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"manager_agent": "manager",
		}),
	})
	resp = readResponse(t, desktop, "set_manager")
	if !resp.Success {
		t.Fatalf("expected authenticated set_manager to succeed: %s", resp.Error)
	}
	if got := h.getConfiguredManager("r1"); got != "manager" {
		t.Fatalf("expected configured manager to be manager, got %q", got)
	}
}
