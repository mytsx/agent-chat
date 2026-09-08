package hub

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"desktop/internal/eventlog"
	"desktop/internal/types"
)

// newTestHubDir builds a hub over a temp data dir and ties the event logger's
// lifetime to the test.
//
// A hub owns an async event writer; only Shutdown closes it. A test that
// abandons the hub leaves that goroutine running, and its next write recreates
// events.jsonl underneath t.TempDir()'s cleanup — which then fails with
// "directory not empty". Production is unaffected (Shutdown closes it), so the
// fix belongs here.
func newTestHubDir(t *testing.T) (*Hub, string) {
	t.Helper()
	dir := t.TempDir()
	h := New(dir, "default", log.New(io.Discard, "", 0))
	t.Cleanup(func() { _ = h.events.Close() })
	return h, dir
}

// newEventHub builds a hub whose event stream lands in a temp dir, plus one
// unattached client to drive handlers with.
func newEventHub(t *testing.T) (*Hub, *Client, string) {
	t.Helper()
	h, dir := newTestHubDir(t)
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

// Copilot review, PR #103: when the manager gateway intercepts a message it is
// stored for the manager, not the addressee. The event must record both, or the
// "never read" report blames an agent that was never a delivery target.
func TestEventLogRecordsDeliveryTargetOnReroute(t *testing.T) {
	h, alice, dir := newEventHub(t)
	mgrClient := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.setConfiguredManager("r1", "yonetici")

	h.handleJoinRoom(mgrClient, types.Request{
		ID: "join-mgr", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "yonetici", "role": "manager"}),
	})
	if resp := readResponse(t, mgrClient, "join_room"); !resp.Success {
		t.Fatalf("manager join başarısız: %s", resp.Error)
	}
	joinAgent(t, h, alice, "r1", "alice")

	h.handleSendMessage(alice, types.Request{
		ID: "send", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "bob", "content": "merhaba"}),
	})
	if resp := readResponse(t, alice, "send_message"); !resp.Success {
		t.Fatalf("send başarısız: %s", resp.Error)
	}

	events := loggedEvents(t, h, dir)
	sent := onlyEvent(t, events, eventlog.EventMessageSent)
	// Addressee stays what the sender typed — report 2 (#99) depends on it.
	if sent[eventlog.AttrRecipientName] != "bob" {
		t.Errorf("%s = %v, want bob", eventlog.AttrRecipientName, sent[eventlog.AttrRecipientName])
	}
	// Delivery target is where it actually went — report 3 depends on it.
	if sent[eventlog.AttrDeliveryTarget] != "yonetici" {
		t.Errorf("%s = %v, want yonetici", eventlog.AttrDeliveryTarget, sent[eventlog.AttrDeliveryTarget])
	}
	rerouted := onlyEvent(t, events, eventlog.EventMessageRerouted)
	if rerouted[eventlog.AttrRerouteTarget] != "yonetici" {
		t.Errorf("%s = %v, want yonetici", eventlog.AttrRerouteTarget, rerouted[eventlog.AttrRerouteTarget])
	}
}

// Without a manager the delivery target is simply the addressee.
func TestEventLogDeliveryTargetIsAddresseeWithoutManager(t *testing.T) {
	h, alice, dir := newEventHub(t)
	bob := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	joinAgent(t, h, alice, "r1", "alice")
	joinAgent(t, h, bob, "r1", "bob")

	h.handleSendMessage(alice, types.Request{
		ID: "send", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "bob", "content": "merhaba"}),
	})
	readResponse(t, alice, "send_message")

	sent := onlyEvent(t, loggedEvents(t, h, dir), eventlog.EventMessageSent)
	if sent[eventlog.AttrDeliveryTarget] != "bob" {
		t.Errorf("%s = %v, want bob", eventlog.AttrDeliveryTarget, sent[eventlog.AttrDeliveryTarget])
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

// Codex review, PR #103: a read is capped by its limit and returns only the
// newest matching tail, so the record must carry the exact IDs it returned.
func TestEventLogRecordsExactReadIDs(t *testing.T) {
	h, alice, dir := newEventHub(t)
	bob := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	joinAgent(t, h, alice, "r1", "alice")
	joinAgent(t, h, bob, "r1", "bob")

	for i := range 3 {
		h.handleSendMessage(alice, types.Request{
			ID: "send", Type: "send_message", Room: "r1",
			Data: mustRawJSON(t, map[string]any{
				"from": "alice", "to": "bob", "content": fmt.Sprintf("mesaj %d", i),
			}),
		})
		readResponse(t, alice, "send_message")
	}

	// A limit smaller than the backlog: only the newest tail comes back.
	h.handleGetMessages(bob, types.Request{
		ID: "read", Type: "get_messages", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"agent_name": "bob", "limit": 2}),
	})
	readResponse(t, bob, "get_messages")

	e := onlyEvent(t, loggedEvents(t, h, dir), eventlog.EventMessagesRead)
	ids, ok := e[eventlog.AttrReadMessageIDs].([]any)
	if !ok {
		t.Fatalf("%s alanı yok veya liste değil: %v", eventlog.AttrReadMessageIDs, e[eventlog.AttrReadMessageIDs])
	}
	if len(ids) != 2 {
		t.Errorf("kaydedilen id sayısı = %d, want 2 (limit kadar)", len(ids))
	}
	if got := e[eventlog.AttrReadReturned]; got != float64(len(ids)) {
		t.Errorf("returned = %v, id listesi uzunluğu = %d", got, len(ids))
	}
}

// Codex review, PR #103: managers are told to poll read_all_messages, so that
// path must record progress too — otherwise everything a manager reads through
// its normal tool is reported as never read.
func TestEventLogRecordsReadAllProgress(t *testing.T) {
	h, alice, dir := newEventHub(t)
	mgr := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.setConfiguredManager("r1", "yonetici")
	h.handleJoinRoom(mgr, types.Request{
		ID: "join-mgr", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "yonetici", "role": "manager"}),
	})
	if resp := readResponse(t, mgr, "join_room"); !resp.Success {
		t.Fatalf("manager join başarısız: %s", resp.Error)
	}
	joinAgent(t, h, alice, "r1", "alice")

	h.handleSendMessage(alice, types.Request{
		ID: "send", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "bob", "content": "merhaba"}),
	})
	readResponse(t, alice, "send_message")

	h.handleGetAllMessages(mgr, types.Request{
		ID: "readall", Type: "get_all_messages", Room: "r1",
		Data: mustRawJSON(t, map[string]any{}),
	})
	readResponse(t, mgr, "get_all_messages")

	e := onlyEvent(t, loggedEvents(t, h, dir), eventlog.EventMessagesRead)
	if e[eventlog.AttrAgentName] != "yonetici" {
		t.Errorf("%s = %v, want yonetici", eventlog.AttrAgentName, e[eventlog.AttrAgentName])
	}
	if got, ok := e[eventlog.AttrReadMessageIDs].([]any); !ok || len(got) == 0 {
		t.Errorf("read_all okuma ilerlemesi kaydetmedi: %v", e[eventlog.AttrReadMessageIDs])
	}
}

// Codex review, PR #103: clientType is only assigned by identify, so the connect
// event must be emitted there or it can never distinguish MCP from desktop.
func TestEventLogClientConnectedCarriesType(t *testing.T) {
	h, c, dir := newEventHub(t)

	h.handleIdentify(c, types.Request{
		ID: "id-1", Type: "identify",
		Data: mustRawJSON(t, map[string]string{"client_type": "mcp", "agent_name": "alice"}),
	})
	if resp := readResponse(t, c, "identify"); !resp.Success {
		t.Fatalf("identify başarısız: %s", resp.Error)
	}

	e := onlyEvent(t, loggedEvents(t, h, dir), eventlog.EventClientConnected)
	if e[eventlog.AttrClientType] != "mcp" {
		t.Errorf("%s = %v, want mcp", eventlog.AttrClientType, e[eventlog.AttrClientType])
	}
}

// Codex review round 2, PR #103: a full client buffer drops the response, so
// recording those IDs as read would erase from the report exactly the messages
// the agent never received.
func TestEventLogSkipsReadWhenResponseIsDropped(t *testing.T) {
	h, alice, dir := newEventHub(t)
	// A client whose send buffer is already full cannot receive the response.
	bob := &Client{hub: h, send: make(chan []byte, 1), rooms: make(map[string]bool)}
	joinAgent(t, h, alice, "r1", "alice")

	h.handleJoinRoom(bob, types.Request{
		ID: "join-bob", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "bob"}),
	})
	// Leave the join response sitting in the buffer so it stays full.

	h.handleSendMessage(alice, types.Request{
		ID: "send", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "bob", "content": "merhaba"}),
	})
	readResponse(t, alice, "send_message")

	h.handleGetMessages(bob, types.Request{
		ID: "read", Type: "get_messages", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"agent_name": "bob"}),
	})

	for _, e := range eventsNamed(loggedEvents(t, h, dir), eventlog.EventMessagesRead) {
		if ids, ok := e[eventlog.AttrReadMessageIDs].([]any); ok && len(ids) > 0 {
			t.Errorf("yanıt kuyruğa girmediği hâlde okuma kaydedilmiş: %v", ids)
		}
	}
}

// Codex review round 2: presence must be captured under the same lock as the
// store, or a concurrent join/leave changes the answer after the fact.
func TestSendMessageWithPresenceReportsRosterAtStoreTime(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("bob", ""); err != nil {
		t.Fatal(err)
	}

	if _, present, err := r.SendMessageWithPresence("alice", "bob", "m", false, "", SendOptions{}, "bob"); err != nil || !present {
		t.Errorf("odadaki alıcı için present=%v err=%v, want true/nil", present, err)
	}
	if _, present, err := r.SendMessageWithPresence("alice", "bob", "m", false, "", SendOptions{}, "hayalet"); err != nil || present {
		t.Errorf("odada olmayan alıcı için present=%v err=%v, want false/nil", present, err)
	}
	// The plain SendMessage wrapper must stay behaviour-compatible.
	if _, err := r.SendMessage("alice", "bob", "m", false, "", SendOptions{}); err != nil {
		t.Errorf("SendMessage: %v", err)
	}
}
