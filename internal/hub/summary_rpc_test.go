package hub

import (
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"

	"desktop/internal/summary"
	"desktop/internal/types"
)

func desktopClient(h *Hub) *Client {
	c := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	c.clientType = "desktop"
	c.desktopAuthed = true
	return c
}

func TestHandleLogMessage_DesktopLogsUserPrompt(t *testing.T) {
	h, _ := newTestHubClient()
	c := desktopClient(h)

	req := types.Request{
		ID:   "1",
		Type: "log_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"to": "backend", "content": "şu dosyayı düzelt"}),
	}
	h.handleRequest(c, req)
	resp := readResponse(t, c, "log_message")
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	rs := h.getRoom("r1")
	if rs == nil {
		t.Fatal("room not created by log_message")
	}
	// Verify via the raw read: user_prompt is intentionally filtered from the
	// agent-facing ReadAllMessages, but lands in the room/transcript.
	msgs := rs.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("want 1 logged message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.Type != "user_prompt" {
		t.Errorf("Type = %q, want user_prompt", m.Type)
	}
	if m.From != "user" {
		t.Errorf("From = %q, want user (server-forced sentinel)", m.From)
	}
	if m.To != "backend" {
		t.Errorf("To = %q, want backend", m.To)
	}
	if m.Content != "şu dosyayı düzelt" {
		t.Errorf("Content = %q", m.Content)
	}
}

func TestHandleLogMessage_NonDesktopRejected(t *testing.T) {
	h, c := newTestHubClient() // clientType "" — not desktop-authorized
	req := types.Request{
		ID:   "1",
		Type: "log_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"to": "backend", "content": "x"}),
	}
	h.handleRequest(c, req)
	resp := readResponse(t, c, "log_message")
	if resp.Success {
		t.Fatal("expected non-desktop client to be rejected")
	}
}

func TestHandleLogMessage_EmptyContentRejected(t *testing.T) {
	h, _ := newTestHubClient()
	c := desktopClient(h)
	req := types.Request{
		ID:   "1",
		Type: "log_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"to": "backend", "content": "   "}),
	}
	h.handleRequest(c, req)
	resp := readResponse(t, c, "log_message")
	if resp.Success {
		t.Fatal("expected empty prompt to be rejected")
	}
}

func TestHandleReadSummary_NoSummary(t *testing.T) {
	h := New(t.TempDir(), "default", log.New(io.Discard, "", 0))
	c := desktopClient(h)
	req := types.Request{ID: "1", Type: "read_summary", Room: "r1"}
	h.handleRequest(c, req)
	resp := readResponse(t, c, "read_summary")
	if !resp.Success {
		t.Fatalf("want success, got error: %s", resp.Error)
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body["text"], "özet yok") {
		t.Fatalf("text = %q, want a no-summary notice", body["text"])
	}
}

func TestHandleReadSummary_ReturnsLatest(t *testing.T) {
	dataDir := t.TempDir()
	h := New(dataDir, "default", log.New(io.Discard, "", 0))
	if _, err := summary.Write(dataDir, "r1", "ESKI"); err != nil {
		t.Fatal(err)
	}
	if _, err := summary.Write(dataDir, "r1", "ÖZET METNİ ✅"); err != nil {
		t.Fatal(err)
	}

	c := desktopClient(h)
	req := types.Request{ID: "1", Type: "read_summary", Room: "r1"}
	h.handleRequest(c, req)
	resp := readResponse(t, c, "read_summary")
	if !resp.Success {
		t.Fatalf("want success, got error: %s", resp.Error)
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body["text"], "ÖZET METNİ ✅") {
		t.Fatalf("text = %q, want latest summary", body["text"])
	}
}

// A continued manager is steered to read_summary instead of read_all_messages, so
// read_summary must refresh the manager heartbeat like the other read/poll
// handlers — otherwise an actively-polling manager goes stale after
// managerTimeoutSec and routing is bypassed (#29 Codex review).
func TestHandleReadSummary_RefreshesManagerHeartbeat(t *testing.T) {
	dataDir := t.TempDir()
	h := New(dataDir, "default", log.New(io.Discard, "", 0))
	if _, err := summary.Write(dataDir, "r1", "özet"); err != nil {
		t.Fatal(err)
	}
	rs := h.getOrCreateRoom("r1")
	if _, _, err := rs.Join("mgr", "manager"); err != nil {
		t.Fatal(err)
	}
	// Age the heartbeat but keep it within managerTimeoutSec (300s) so the manager
	// is still active — read_summary should push it back toward "now".
	rs.mu.Lock()
	rs.managerLastSeen = types.Now() - 100
	rs.mu.Unlock()

	c := &Client{hub: h, send: make(chan []byte, 64), rooms: map[string]bool{}}
	c.agentName = "mgr"
	c.joinedRoom = "r1"
	h.handleReadSummary(c, types.Request{ID: "1", Type: "read_summary", Room: "r1"})
	_ = readResponse(t, c, "read_summary")

	rs.mu.Lock()
	age := types.Now() - rs.managerLastSeen
	rs.mu.Unlock()
	if age > 5 {
		t.Fatalf("read_summary did not refresh the manager heartbeat: age=%.0fs", age)
	}
}

func TestHandleReadSummary_WrongRoomRejected(t *testing.T) {
	h := New(t.TempDir(), "default", log.New(io.Discard, "", 0))
	c := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	c.agentName = "alice"
	c.joinedRoom = "other-room"

	req := types.Request{ID: "1", Type: "read_summary", Room: "r1"}
	h.handleRequest(c, req)
	resp := readResponse(t, c, "read_summary")
	if resp.Success {
		t.Fatal("expected agent in a different room to be rejected")
	}
}

func TestHandleReadSummary_UnidentifiedRejected(t *testing.T) {
	h := New(t.TempDir(), "default", log.New(io.Discard, "", 0))
	c := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	req := types.Request{ID: "1", Type: "read_summary", Room: "r1"}
	h.handleRequest(c, req)
	resp := readResponse(t, c, "read_summary")
	if resp.Success {
		t.Fatal("expected unidentified client to be rejected")
	}
}
