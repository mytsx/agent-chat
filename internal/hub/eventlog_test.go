package hub

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"desktop/internal/eventlog"
	"desktop/internal/types"
)

// newEventHub builds a hub whose event stream lands in a temp dir, plus one
// unattached client to drive handlers with.
func newEventHub(t *testing.T) (*Hub, *Client, string) {
	t.Helper()
	dir := t.TempDir()
	h := New(dir, "default", log.New(io.Discard, "", 0))
	t.Cleanup(func() { _ = h.events.Close() })
	c := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	return h, c, dir
}

// loggedEvents flushes the stream and returns every record written so far.
func loggedEvents(t *testing.T, h *Hub, dir string) []map[string]any {
	t.Helper()
	h.events.Flush()
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("olay akışı okunamadı: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("satır JSON değil: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// eventsNamed returns the records carrying the given event.name.
func eventsNamed(events []map[string]any, name string) []map[string]any {
	var out []map[string]any
	for _, e := range events {
		if e[eventlog.AttrEventName] == name {
			out = append(out, e)
		}
	}
	return out
}

func onlyEvent(t *testing.T, events []map[string]any, name string) map[string]any {
	t.Helper()
	got := eventsNamed(events, name)
	if len(got) != 1 {
		t.Fatalf("%q olayı %d kez yazıldı, want 1", name, len(got))
	}
	return got[0]
}

func joinAgent(t *testing.T, h *Hub, c *Client, room, agent string) {
	t.Helper()
	h.handleJoinRoom(c, types.Request{
		ID: "join-" + agent, Type: "join_room", Room: room,
		Data: mustRawJSON(t, map[string]string{"agent_name": agent}),
	})
	if resp := readResponse(t, c, "join_room"); !resp.Success {
		t.Fatalf("join %s başarısız: %s", agent, resp.Error)
	}
}

func TestEventLogRecordsJoin(t *testing.T) {
	h, c, dir := newEventHub(t)
	joinAgent(t, h, c, "r1", "alice")

	e := onlyEvent(t, loggedEvents(t, h, dir), eventlog.EventAgentJoined)
	if e[eventlog.AttrAgentName] != "alice" {
		t.Errorf("%s = %v, want alice", eventlog.AttrAgentName, e[eventlog.AttrAgentName])
	}
	if e[eventlog.AttrConversationID] != "r1" {
		t.Errorf("%s = %v, want r1", eventlog.AttrConversationID, e[eventlog.AttrConversationID])
	}
}

// The recipient-presence flag is what makes #99 measurable: a message addressed
// to someone who is not in the room must be visible as such in the log.
func TestEventLogRecordsRecipientPresence(t *testing.T) {
	h, alice, dir := newEventHub(t)
	bob := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	joinAgent(t, h, alice, "r1", "alice")
	joinAgent(t, h, bob, "r1", "bob")

	send := func(to, content string) {
		h.handleSendMessage(alice, types.Request{
			ID: "send", Type: "send_message", Room: "r1",
			Data: mustRawJSON(t, map[string]any{"from": "alice", "to": to, "content": content}),
		})
		if resp := readResponse(t, alice, "send_message"); !resp.Success {
			t.Fatalf("send to %s başarısız: %s", to, resp.Error)
		}
	}
	send("bob", "odadaki alıcı")
	send("hayalet", "odada olmayan alıcı")

	sent := eventsNamed(loggedEvents(t, h, dir), eventlog.EventMessageSent)
	if len(sent) != 2 {
		t.Fatalf("message.sent sayısı = %d, want 2", len(sent))
	}
	if got := sent[0][eventlog.AttrRecipientInRoom]; got != true {
		t.Errorf("odadaki alıcı için in_room = %v, want true", got)
	}
	if got := sent[1][eventlog.AttrRecipientInRoom]; got != false {
		t.Errorf("odada olmayan alıcı için in_room = %v, want false", got)
	}
	if got := sent[1][eventlog.AttrRecipientName]; got != "hayalet" {
		t.Errorf("%s = %v, want hayalet", eventlog.AttrRecipientName, got)
	}
	if got := sent[0][eventlog.AttrInputMessages]; got != "odadaki alıcı" {
		t.Errorf("içerik kaydedilmemiş: %v", got)
	}
	if _, ok := sent[0][eventlog.AttrMessageID]; !ok {
		t.Errorf("%s alanı yok", eventlog.AttrMessageID)
	}
}

// read.max_id is the high-water mark the "sent but never read" report is built
// on, so a read must record how far the agent got.
func TestEventLogRecordsReadProgress(t *testing.T) {
	h, alice, dir := newEventHub(t)
	bob := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	joinAgent(t, h, alice, "r1", "alice")
	joinAgent(t, h, bob, "r1", "bob")

	h.handleSendMessage(alice, types.Request{
		ID: "send", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "bob", "content": "merhaba"}),
	})
	readResponse(t, alice, "send_message")

	h.handleGetMessages(bob, types.Request{
		ID: "read", Type: "get_messages", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"agent_name": "bob"}),
	})
	readResponse(t, bob, "get_messages")

	e := onlyEvent(t, loggedEvents(t, h, dir), eventlog.EventMessagesRead)
	if e[eventlog.AttrAgentName] != "bob" {
		t.Errorf("%s = %v, want bob", eventlog.AttrAgentName, e[eventlog.AttrAgentName])
	}
	if got, ok := e[eventlog.AttrReadReturned].(float64); !ok || got < 1 {
		t.Errorf("%s = %v, want >= 1", eventlog.AttrReadReturned, e[eventlog.AttrReadReturned])
	}
	// The high-water mark must be the newest message the read actually saw.
	msgs := h.getOrCreateRoom("r1").GetMessages()
	wantMax := float64(msgs[len(msgs)-1].ID)
	if got := e[eventlog.AttrReadMaxID]; got != wantMax {
		t.Errorf("%s = %v, want %v", eventlog.AttrReadMaxID, got, wantMax)
	}
}

// #98's two drop mechanisms must be distinguishable in the log: an explicit
// leave, a disconnect, and a stale eviction are three different records.
func TestEventLogDistinguishesLeaveReasons(t *testing.T) {
	t.Run("explicit leave", func(t *testing.T) {
		h, c, dir := newEventHub(t)
		joinAgent(t, h, c, "r1", "alice")

		h.handleLeaveRoom(c, types.Request{
			ID: "leave", Type: "leave_room", Room: "r1",
			Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
		})
		readResponse(t, c, "leave_room")

		e := onlyEvent(t, loggedEvents(t, h, dir), eventlog.EventAgentLeft)
		if e[eventlog.AttrLeaveReason] != eventlog.LeaveReasonExplicit {
			t.Errorf("%s = %v, want %q", eventlog.AttrLeaveReason, e[eventlog.AttrLeaveReason], eventlog.LeaveReasonExplicit)
		}
	})

	t.Run("stale eviction", func(t *testing.T) {
		h, c, dir := newEventHub(t)
		joinAgent(t, h, c, "r1", "alice")

		roomState := h.getOrCreateRoom("r1")
		roomState.mu.Lock()
		a := roomState.agents["alice"]
		a.LastSeen = types.Now() - float64(staleTimeout) - 1
		roomState.agents["alice"] = a
		roomState.mu.Unlock()

		roomState.ListAgents("") // stale temizliğini tetikler

		e := onlyEvent(t, loggedEvents(t, h, dir), eventlog.EventAgentEvicted)
		if e[eventlog.AttrAgentName] != "alice" {
			t.Errorf("%s = %v, want alice", eventlog.AttrAgentName, e[eventlog.AttrAgentName])
		}
		if idle, ok := e[eventlog.AttrIdleSeconds].(float64); !ok || idle < float64(staleTimeout) {
			t.Errorf("%s = %v, want >= %d", eventlog.AttrIdleSeconds, e[eventlog.AttrIdleSeconds], staleTimeout)
		}
		// Eviction is its own event; it must not also masquerade as a leave.
		if got := eventsNamed(loggedEvents(t, h, dir), eventlog.EventAgentLeft); len(got) != 0 {
			t.Errorf("eviction ayrıca agent.left yazmış: %v", got)
		}
	})
}

// A hub without a data dir (unit hubs, in-process tests) must still work: the
// event logger falls back to a no-op rather than the hub refusing to run.
func TestHubWithoutDataDirUsesNopEventLogger(t *testing.T) {
	h, c := newTestHubClient()
	joinAgent(t, h, c, "r1", "alice") // panik etmemeli
	h.events.Flush()
	if got := h.events.Dropped(); got != 0 {
		t.Errorf("Dropped() = %d, want 0", got)
	}
}
