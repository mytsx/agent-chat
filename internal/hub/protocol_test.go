package hub

import (
	"encoding/json"
	"io"
	"log"
	"strings"
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

func TestRequireValidNonEmptyNames(t *testing.T) {
	t.Parallel()

	_, c := newTestHubClient()
	req := types.Request{ID: "req-1", Type: "set_observers"}
	if !c.requireValidNonEmptyNames(req, []string{"", "  ", "Alice"}) {
		t.Fatalf("blank and valid names should be accepted")
	}
	select {
	case payload := <-c.send:
		t.Fatalf("expected no response for valid names, got %s", string(payload))
	default:
	}

	if c.requireValidNonEmptyNames(req, []string{"ok", "../bad"}) {
		t.Fatalf("invalid non-empty name should be rejected")
	}
	resp := readResponse(t, c, "set_observers")
	if resp.Success || !strings.Contains(resp.Error, "invalid name \"../bad\"") {
		t.Fatalf("unexpected invalid-name response: success=%v error=%q", resp.Success, resp.Error)
	}
}

func TestRequireValidRoomNames(t *testing.T) {
	t.Parallel()

	_, c := newTestHubClient()
	req := types.Request{ID: "req-1", Type: "subscribe"}
	if !c.requireValidRoomNames(req, []string{"", "room one"}) {
		t.Fatalf("blank default and valid room names should be accepted")
	}
	select {
	case payload := <-c.send:
		t.Fatalf("expected no response for valid rooms, got %s", string(payload))
	default:
	}

	if c.requireValidRoomNames(req, []string{"room", ".hidden"}) {
		t.Fatalf("invalid room name should be rejected")
	}
	resp := readResponse(t, c, "subscribe")
	want := "geçersiz oda adı \".hidden\": invalid name \".hidden\": leading dot not allowed"
	if resp.Success || resp.Error != want {
		t.Fatalf("response success=%v error=%q, want error %q", resp.Success, resp.Error, want)
	}
}

func TestFormatAgentMessages(t *testing.T) {
	t.Parallel()

	messages := []types.Message{{
		ID:        7,
		Timestamp: "2026-07-11T08:09:10.000000",
		From:      "alice",
		To:        "bob",
		Content:   "selam",
	}}

	got := formatAgentMessages(messages, 3, 1)
	want := "📬 Son 1 mesaj (toplam 3):\n\n[08:09:10] alice → bob: selam\n  (ID: 7)\n\n"
	if got != want {
		t.Fatalf("formatAgentMessages() = %q, want %q", got, want)
	}
}

func TestFormatAllMessages(t *testing.T) {
	t.Parallel()

	messages := []types.Message{{
		ID:        7,
		Timestamp: "2026-07-11T08:09:10.000000",
		From:      "alice",
		To:        "bob",
		Content:   "selam",
	}}

	tests := []struct {
		name       string
		totalCount int
		limit      int
		want       string
	}{
		{
			name:       "all messages header",
			totalCount: 1,
			limit:      15,
			want:       "📬 1 mesaj (tümü):\n\n[08:09:10] #7 alice → bob: selam\n\n",
		},
		{
			name:       "limited header omits all suffix",
			totalCount: 3,
			limit:      1,
			want:       "📬 Son 1 mesaj (toplam 3):\n\n[08:09:10] #7 alice → bob: selam\n\n",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := formatAllMessages(messages, tt.totalCount, tt.limit)
			if got != tt.want {
				t.Fatalf("formatAllMessages() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClientRequireJoinedRoom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		agentName   string
		joinedRoom  string
		room        string
		wantAllowed bool
		wantError   string
	}{
		{
			name:      "not joined",
			room:      "r1",
			wantError: "önce join_room çağırmalısınız",
		},
		{
			name:       "wrong room",
			agentName:  "alice",
			joinedRoom: "r1",
			room:       "r2",
			wantError:  "yalnızca katıldığınız odadan mesaj okuyabilirsiniz: r1",
		},
		{
			name:        "joined room",
			agentName:   "alice",
			joinedRoom:  "r1",
			room:        "r1",
			wantAllowed: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, c := newTestHubClient()
			c.agentName = tt.agentName
			c.joinedRoom = tt.joinedRoom
			req := types.Request{ID: "req-1", Type: "get_messages"}

			allowed := c.requireJoinedRoom(req, tt.room, "önce join_room çağırmalısınız", "yalnızca katıldığınız odadan mesaj okuyabilirsiniz: %s")
			if allowed != tt.wantAllowed {
				t.Fatalf("requireJoinedRoom() = %v, want %v", allowed, tt.wantAllowed)
			}
			if tt.wantAllowed {
				select {
				case payload := <-c.send:
					t.Fatalf("expected no response, got %s", string(payload))
				default:
				}
				return
			}

			resp := readResponse(t, c, "get_messages")
			if resp.Success || resp.Error != tt.wantError {
				t.Fatalf("response success=%v error=%q, want error %q", resp.Success, resp.Error, tt.wantError)
			}
		})
	}
}

func TestClientRequireJoinedRoomOrDesktop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		agentName   string
		joinedRoom  string
		desktop     bool
		room        string
		wantAllowed bool
		wantError   string
	}{
		{
			name:      "anonymous non desktop rejected",
			room:      "r1",
			wantError: "önce join_room veya yetkili desktop identify çağırmalısınız",
		},
		{
			name:        "authorized desktop allowed",
			desktop:     true,
			room:        "r1",
			wantAllowed: true,
		},
		{
			name:      "identified without joined room rejected",
			agentName: "alice",
			room:      "r1",
			wantError: "yalnızca katıldığınız odanın özetini okuyabilirsiniz: ",
		},
		{
			name:       "joined wrong room rejected",
			agentName:  "alice",
			joinedRoom: "r1",
			room:       "r2",
			wantError:  "yalnızca katıldığınız odanın özetini okuyabilirsiniz: r1",
		},
		{
			name:        "joined room allowed",
			agentName:   "alice",
			joinedRoom:  "r1",
			room:        "r1",
			wantAllowed: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, c := newTestHubClient()
			c.agentName = tt.agentName
			c.joinedRoom = tt.joinedRoom
			if tt.desktop {
				c.clientType = "desktop"
				c.desktopAuthed = true
			}
			req := types.Request{ID: "req-1", Type: "read_summary"}

			allowed := c.requireJoinedRoomOrDesktop(req, tt.room, "önce join_room veya yetkili desktop identify çağırmalısınız", "yalnızca katıldığınız odanın özetini okuyabilirsiniz: %s")
			if allowed != tt.wantAllowed {
				t.Fatalf("requireJoinedRoomOrDesktop() = %v, want %v", allowed, tt.wantAllowed)
			}
			if tt.wantAllowed {
				select {
				case payload := <-c.send:
					t.Fatalf("expected no response, got %s", string(payload))
				default:
				}
				return
			}

			resp := readResponse(t, c, "read_summary")
			if resp.Success || resp.Error != tt.wantError {
				t.Fatalf("response success=%v error=%q, want error %q", resp.Success, resp.Error, tt.wantError)
			}
		})
	}
}

func TestAuthorizeReadAllMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(h *Hub, c *Client, roomState *RoomState)
		wantAllowed bool
		wantError   string
	}{
		{
			name:      "anonymous non desktop rejected",
			wantError: "önce yetkili desktop identify veya join_room çağırmalısınız",
		},
		{
			name: "authorized desktop allowed",
			setup: func(_ *Hub, c *Client, _ *RoomState) {
				c.clientType = "desktop"
				c.desktopAuthed = true
			},
			wantAllowed: true,
		},
		{
			name: "joined wrong room rejected",
			setup: func(_ *Hub, c *Client, _ *RoomState) {
				c.agentName = "alice"
				c.joinedRoom = "other"
			},
			wantError: "yalnızca katıldığınız odadan mesaj okuyabilirsiniz: other",
		},
		{
			name: "joined non manager non observer rejected",
			setup: func(_ *Hub, c *Client, _ *RoomState) {
				c.agentName = "alice"
				c.joinedRoom = "r1"
			},
			wantError: "yalnızca aktif manager veya observer tüm mesajları okuyabilir",
		},
		{
			name: "active manager allowed",
			setup: func(_ *Hub, c *Client, roomState *RoomState) {
				c.agentName = "manager"
				c.joinedRoom = "r1"
				if _, _, err := roomState.Join("manager", "manager"); err != nil {
					panic(err)
				}
			},
			wantAllowed: true,
		},
		{
			name: "configured observer allowed",
			setup: func(h *Hub, c *Client, _ *RoomState) {
				c.agentName = "observer"
				c.joinedRoom = "r1"
				h.setConfiguredObservers("r1", []string{"observer"})
			},
			wantAllowed: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, c := newTestHubClient()
			roomState := h.getOrCreateRoom("r1")
			if tt.setup != nil {
				tt.setup(h, c, roomState)
			}

			req := types.Request{ID: "req-1", Type: "get_all_messages"}
			allowed := h.authorizeReadAllMessages(c, req, "r1", roomState)
			if allowed != tt.wantAllowed {
				t.Fatalf("authorizeReadAllMessages() = %v, want %v", allowed, tt.wantAllowed)
			}
			if tt.wantAllowed {
				select {
				case payload := <-c.send:
					t.Fatalf("expected no response, got %s", string(payload))
				default:
				}
				return
			}

			resp := readResponse(t, c, "get_all_messages")
			if resp.Success || resp.Error != tt.wantError {
				t.Fatalf("response success=%v error=%q, want error %q", resp.Success, resp.Error, tt.wantError)
			}
		})
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

func TestHandleManagerCasing_JoinAndInterception(t *testing.T) {
	h, manager := newTestHubClient()
	// The team config stores the manager name lowercase ("pilot"); the agent
	// joins with a different casing ("Pilot"). Manager identity must be
	// casing-independent, so the join must be accepted and routing must work.
	h.setConfiguredManager("r1", "pilot")

	h.handleRequest(manager, types.Request{
		ID:   "join-mgr",
		Type: "join_room",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"agent_name": "Pilot",
			"role":       "manager",
		}),
	})
	resp := readResponse(t, manager, "join_room")
	if !resp.Success {
		t.Fatalf("expected manager to join despite casing difference, got error=%s", resp.Error)
	}

	alice := &Client{
		hub:   h,
		send:  make(chan []byte, 64),
		rooms: make(map[string]bool),
	}
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
	resp = readResponse(t, alice, "send_message")
	if !resp.Success {
		t.Fatalf("expected intercepted send success, got error=%s", resp.Error)
	}

	roomState := h.getOrCreateRoom("r1")
	messages := roomState.GetMessages()
	last := messages[len(messages)-1]
	if last.To != "Pilot" {
		t.Fatalf("expected message intercepted to manager Pilot, got to=%q", last.To)
	}
	if last.OriginalTo != "bob" || !last.RoutedByManager {
		t.Fatalf("expected routed-by-manager metadata, got original_to=%q routed=%v", last.OriginalTo, last.RoutedByManager)
	}
}

func TestHandleSetManager_CaseVariantKeepsActiveLock(t *testing.T) {
	h, desktop := newTestHubClient()
	h.desktopAuthToken = "desktop-secret"
	h.setConfiguredManager("r1", "pilot")

	// Manager joins as "Pilot" (different casing than the configured "pilot").
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
			"agent_name": "Pilot",
			"role":       "manager",
		}),
	})
	if resp := readResponse(t, manager, "join_room"); !resp.Success {
		t.Fatalf("expected manager join to succeed: %s", resp.Error)
	}

	// Desktop re-affirms the same manager using the configured (lowercase) name.
	h.handleRequest(desktop, types.Request{
		ID:   "id-desktop",
		Type: "identify",
		Data: mustRawJSON(t, map[string]any{
			"client_type": "desktop",
			"auth_token":  "desktop-secret",
		}),
	})
	_ = readResponse(t, desktop, "identify")

	h.handleRequest(desktop, types.Request{
		ID:   "set-mgr",
		Type: "set_manager",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{
			"manager_agent": "pilot",
		}),
	})
	if resp := readResponse(t, desktop, "set_manager"); !resp.Success {
		t.Fatalf("expected set_manager to succeed: %s", resp.Error)
	}

	// The active manager lock must survive — same identity, different casing.
	if got := h.getOrCreateRoom("r1").GetActiveManager(); got != "Pilot" {
		t.Fatalf("expected active manager Pilot to survive case-variant re-affirm, got %q", got)
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

func TestWriteAgentMessageLineFormatsExistingCases(t *testing.T) {
	tests := []struct {
		name string
		msg  types.Message
		want string
	}{
		{
			name: "system",
			msg: types.Message{
				Timestamp: "2026-07-11T01:02:03.000000",
				Type:      "system",
				Content:   "joined",
			},
			want: "[01:02:03] joined\n",
		},
		{
			name: "broadcast",
			msg: types.Message{
				Timestamp: "2026-07-11T01:02:03.000000",
				From:      "alice",
				To:        "all",
				Content:   "hello",
			},
			want: "[01:02:03] alice → HERKESE: hello\n",
		},
		{
			name: "routed",
			msg: types.Message{
				Timestamp:  "2026-07-11T01:02:03.000000",
				From:       "alice",
				To:         "manager",
				OriginalTo: "bob",
				Content:    "please check",
			},
			want: "[01:02:03] alice → manager (orijinal: bob): please check\n",
		},
		{
			name: "direct",
			msg: types.Message{
				Timestamp: "2026-07-11T01:02:03.000000",
				From:      "alice",
				To:        "bob",
				Content:   "hello",
			},
			want: "[01:02:03] alice → bob: hello\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb strings.Builder
			writeAgentMessageLine(&sb, tt.msg)
			if got := sb.String(); got != tt.want {
				t.Fatalf("formatted line mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestWriteAllMessagesLineFormatsExistingCases(t *testing.T) {
	longContent := strings.Repeat("x", 101)
	tests := []struct {
		name string
		msg  types.Message
		want string
	}{
		{
			name: "system",
			msg: types.Message{
				Timestamp: "2026-07-11T01:02:03.000000",
				Type:      "system",
				Content:   "joined",
			},
			want: "[01:02:03] SYSTEM: joined\n",
		},
		{
			name: "routed",
			msg: types.Message{
				ID:         42,
				Timestamp:  "2026-07-11T01:02:03.000000",
				From:       "alice",
				To:         "manager",
				OriginalTo: "bob",
				Content:    longContent,
			},
			want: "[01:02:03] #42 alice → manager (orijinal: bob): " + strings.Repeat("x", 100) + "\n",
		},
		{
			name: "broadcast",
			msg: types.Message{
				ID:        8,
				Timestamp: "2026-07-11T01:02:03.000000",
				From:      "alice",
				To:        "all",
				Content:   "hello all",
			},
			want: "[01:02:03] #8 alice → all: hello all\n",
		},
		{
			name: "direct",
			msg: types.Message{
				ID:        7,
				Timestamp: "2026-07-11T01:02:03.000000",
				From:      "alice",
				To:        "bob",
				Content:   "hello",
			},
			want: "[01:02:03] #7 alice → bob: hello\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb strings.Builder
			writeAllMessagesLine(&sb, tt.msg)
			if got := sb.String(); got != tt.want {
				t.Fatalf("formatted line mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}
