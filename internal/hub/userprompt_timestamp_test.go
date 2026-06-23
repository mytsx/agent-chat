package hub

import (
	"testing"

	"desktop/internal/types"
)

// The delivery-moment timestamp is produced app-side (when the prompt is written
// to the agent's PTY) and threaded through log_message → LogUserPrompt, instead
// of the hub stamping it whenever the fire-and-forget RPC happens to arrive.
// Otherwise a delayed log goroutine could stamp the prompt AFTER the agent's
// reply, and the timestamp-ordered transcript would show the answer before the
// instruction (#58).
func TestLogUserPrompt_UsesSuppliedTimestamp(t *testing.T) {
	r := NewRoomState()
	const ts = "2026-06-22T09:00:00.000000"
	msg := r.LogUserPrompt(types.UserPromptFrom, "alice", "şu görevi yap", ts)
	if msg.Timestamp != ts {
		t.Fatalf("Timestamp = %q, want supplied %q", msg.Timestamp, ts)
	}
}

// An empty supplied timestamp falls back to a freshly stamped one, so callers
// that don't (yet) supply a delivery timestamp still get a usable ordering key.
func TestLogUserPrompt_FallsBackToNowWhenEmpty(t *testing.T) {
	r := NewRoomState()
	msg := r.LogUserPrompt(types.UserPromptFrom, "alice", "merhaba", "")
	if msg.Timestamp == "" {
		t.Fatal("empty supplied timestamp must fall back to a stamped one, got empty")
	}
}

// handleLogMessage threads the app-supplied delivery timestamp from the request
// payload into the logged message rather than stamping at processing time (#58).
func TestHandleLogMessage_HonorsSuppliedTimestamp(t *testing.T) {
	h, _ := newTestHubClient()
	c := desktopClient(h)
	const ts = "2026-06-22T09:30:00.000000"

	req := types.Request{
		ID:   "1",
		Type: "log_message",
		Room: "r1",
		Data: mustRawJSON(t, map[string]any{"to": "backend", "content": "düzelt", "timestamp": ts}),
	}
	h.handleRequest(c, req)
	resp := readResponse(t, c, "log_message")
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	msgs := h.getRoom("r1").GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("want 1 logged message, got %d", len(msgs))
	}
	if msgs[0].Timestamp != ts {
		t.Fatalf("Timestamp = %q, want app-supplied %q", msgs[0].Timestamp, ts)
	}
}
