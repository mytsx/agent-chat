package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

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
		// Liveness now comes from the connection, so an eviction can only
		// happen once that connection is gone.
		h.agentDisconnected("r1", "alice")

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
	ids := decodeReadIDs(t, e)
	if len(ids) != 2 {
		t.Errorf("kaydedilen id sayısı = %d, want 2 (limit kadar)", len(ids))
	}
	if got := e[eventlog.AttrReadReturned]; got != float64(len(ids)) {
		t.Errorf("returned = %v, id sayısı = %d", got, len(ids))
	}
}

// decodeReadIDs expands the range-encoded IDs a read event carries.
func decodeReadIDs(t *testing.T, e map[string]any) []int {
	t.Helper()
	raw, ok := e[eventlog.AttrReadIDRanges].([]any)
	if !ok {
		t.Fatalf("%s alanı yok veya liste değil: %v", eventlog.AttrReadIDRanges, e[eventlog.AttrReadIDRanges])
	}
	pairs := make([]int, 0, len(raw))
	for _, v := range raw {
		f, ok := v.(float64)
		if !ok {
			t.Fatalf("aralık değeri sayı değil: %v", v)
		}
		pairs = append(pairs, int(f))
	}
	return eventlog.DecodeIDRanges(pairs)
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
	if len(decodeReadIDs(t, e)) == 0 {
		t.Error("read_all okuma ilerlemesi kaydetmedi")
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
		if ids, ok := e[eventlog.AttrReadIDRanges].([]any); ok && len(ids) > 0 {
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

	if _, present, _, err := r.SendMessageWithPresence("alice", "bob", "m", false, "", SendOptions{}, "bob"); err != nil || !present {
		t.Errorf("odadaki alıcı için present=%v err=%v, want true/nil", present, err)
	}
	if _, present, _, err := r.SendMessageWithPresence("alice", "bob", "m", false, "", SendOptions{}, "hayalet"); err != nil || present {
		t.Errorf("odada olmayan alıcı için present=%v err=%v, want false/nil", present, err)
	}
	// The plain SendMessage wrapper must stay behaviour-compatible.
	if _, err := r.SendMessage("alice", "bob", "m", false, "", SendOptions{}); err != nil {
		t.Errorf("SendMessage: %v", err)
	}
}

// Codex review round 3, PR #103: clear_room only wipes up to the ID it
// archived. The event must carry that watermark so the analyzer can keep the
// messages that survived the clear.
func TestEventLogRoomResetCarriesClearedMaxID(t *testing.T) {
	h, alice, dir := newEventHub(t)
	// clear_room needs the active manager (or an authorized desktop).
	h.setConfiguredManager("r1", "alice")
	h.handleJoinRoom(alice, types.Request{
		ID: "join", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice", "role": "manager"}),
	})
	if resp := readResponse(t, alice, "join_room"); !resp.Success {
		t.Fatalf("manager join başarısız: %s", resp.Error)
	}

	h.handleSendMessage(alice, types.Request{
		ID: "send", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "all", "content": "merhaba"}),
	})
	readResponse(t, alice, "send_message")

	msgs := h.getOrCreateRoom("r1").GetMessages()
	wantMax := float64(msgs[len(msgs)-1].ID)

	h.handleClearRoom(alice, types.Request{
		ID: "clear", Type: "clear_room", Room: "r1",
		Data: mustRawJSON(t, map[string]any{}),
	})
	readResponse(t, alice, "clear_room")

	e := onlyEvent(t, loggedEvents(t, h, dir), eventlog.EventRoomReset)
	if e[eventlog.AttrRoomLifecycle] != eventlog.RoomLifecycleCleared {
		t.Errorf("%s = %v", eventlog.AttrRoomLifecycle, e[eventlog.AttrRoomLifecycle])
	}
	if got := e[eventlog.AttrRoomResetMaxID]; got != wantMax {
		t.Errorf("%s = %v, want %v (arşivlenen son id)", eventlog.AttrRoomResetMaxID, got, wantMax)
	}
}

// Codex review round 5, PR #103: ClearArchived releases the room lock before
// the boundary was logged, so a still-joined client could store a fresh ID-1
// message in the gap and have it land on the wrong side of the generation
// boundary — a permanent false unread. The boundary is now emitted from inside
// ClearArchived, under the lock.
func TestEventLogRoomResetPrecedesPostClearMessages(t *testing.T) {
	h, alice, dir := newEventHub(t)
	h.setConfiguredManager("r1", "alice")
	h.handleJoinRoom(alice, types.Request{
		ID: "join", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice", "role": "manager"}),
	})
	if resp := readResponse(t, alice, "join_room"); !resp.Success {
		t.Fatalf("manager join başarısız: %s", resp.Error)
	}

	h.handleSendMessage(alice, types.Request{
		ID: "send-1", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "all", "content": "clear öncesi"}),
	})
	readResponse(t, alice, "send_message")

	h.handleClearRoom(alice, types.Request{
		ID: "clear", Type: "clear_room", Room: "r1",
		Data: mustRawJSON(t, map[string]any{}),
	})
	readResponse(t, alice, "clear_room")

	h.handleSendMessage(alice, types.Request{
		ID: "send-2", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "all", "content": "clear sonrası"}),
	})
	readResponse(t, alice, "send_message")

	// The reset must sit between the two sends in the stream, so the post-clear
	// message (which restarts at a low ID) lands in the new generation.
	var order []string
	for _, e := range loggedEvents(t, h, dir) {
		switch e[eventlog.AttrEventName] {
		case eventlog.EventMessageSent, eventlog.EventRoomReset:
			order = append(order, e[eventlog.AttrEventName].(string))
		}
	}
	want := []string{eventlog.EventMessageSent, eventlog.EventRoomReset, eventlog.EventMessageSent}
	if len(order) != len(want) {
		t.Fatalf("olay sırası = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("olay sırası = %v, want %v", order, want)
		}
	}
}

// Codex review round 6, PR #103: the sent event must carry the generation the
// message was STORED in, captured under the room lock — not one inferred from
// where the event lands relative to the boundary record.
func TestEventLogStampsRoomGeneration(t *testing.T) {
	h, alice, dir := newEventHub(t)
	h.setConfiguredManager("r1", "alice")
	h.handleJoinRoom(alice, types.Request{
		ID: "join", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice", "role": "manager"}),
	})
	if resp := readResponse(t, alice, "join_room"); !resp.Success {
		t.Fatalf("manager join başarısız: %s", resp.Error)
	}

	send := func(id string) {
		h.handleSendMessage(alice, types.Request{
			ID: id, Type: "send_message", Room: "r1",
			Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "all", "content": id}),
		})
		readResponse(t, alice, "send_message")
	}
	send("once")
	h.handleClearRoom(alice, types.Request{
		ID: "clear", Type: "clear_room", Room: "r1",
		Data: mustRawJSON(t, map[string]any{}),
	})
	readResponse(t, alice, "clear_room")
	send("sonra")

	sent := eventsNamed(loggedEvents(t, h, dir), eventlog.EventMessageSent)
	if len(sent) != 2 {
		t.Fatalf("message.sent sayısı = %d, want 2", len(sent))
	}
	if got := sent[0][eventlog.AttrRoomGeneration]; got != float64(0) {
		t.Errorf("clear öncesi kuşak = %v, want 0", got)
	}
	if got := sent[1][eventlog.AttrRoomGeneration]; got != float64(1) {
		t.Errorf("clear sonrası kuşak = %v, want 1", got)
	}
}

// Codex review round 6: the delete boundary must be ordered while h.mu still
// excludes recreation, or a room recreated in the gap logs its low-ID messages
// on the wrong side of it.
func TestEventLogDeleteResetOrderedUnderHubLock(t *testing.T) {
	h, c, dir := newEventHub(t)
	c.clientType = "desktop"
	c.desktopAuthed = true
	h.getOrCreateRoom("silinecek")

	h.handleDeleteRoom(c, types.Request{
		ID: "del", Type: "delete_room", Room: "silinecek",
		Data: mustRawJSON(t, map[string]any{}),
	})
	if resp := readResponse(t, c, "delete_room"); !resp.Success {
		t.Fatalf("delete_room başarısız: %s", resp.Error)
	}

	e := onlyEvent(t, loggedEvents(t, h, dir), eventlog.EventRoomReset)
	if e[eventlog.AttrRoomLifecycle] != eventlog.RoomLifecycleDeleted {
		t.Errorf("%s = %v, want deleted", eventlog.AttrRoomLifecycle, e[eventlog.AttrRoomLifecycle])
	}
	if e[eventlog.AttrConversationID] != "silinecek" {
		t.Errorf("%s = %v", eventlog.AttrConversationID, e[eventlog.AttrConversationID])
	}
}

// Codex review round 7, PR #103: the stamped generation must survive a restart,
// or a persisted message and a later read of it land in different generations.
func TestRoomGenerationSurvivesPersistRoundTrip(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("alice", ""); err != nil {
		t.Fatal(err)
	}
	r.ClearArchived(0)
	r.ClearArchived(0)

	snap := r.Snapshot()
	if snap.Generation != 2 {
		t.Fatalf("Snapshot().Generation = %d, want 2", snap.Generation)
	}

	restored := NewRoomState()
	restored.mu.Lock()
	restored.generation = snap.Generation
	restored.mu.Unlock()

	_, _, gen, err := restored.SendMessageWithPresence("alice", "all", "m", false, "", SendOptions{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if gen != 2 {
		t.Errorf("yeniden yüklenen odada kuşak = %d, want 2", gen)
	}
}

// Codex review round 8, PR #103: the disconnect event must say WHY the
// connection ended. Without a cause the stream cannot tell an orderly leave
// from a transport failure — the distinction #98 turns on.
func TestClassifyCloseErrorIsLowCardinality(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"normal kapanış", &websocket.CloseError{Code: websocket.CloseNormalClosure}, closeCauseNormal},
		{"going away", &websocket.CloseError{Code: websocket.CloseGoingAway}, closeCauseGoingAway},
		{"anormal kapanış", &websocket.CloseError{Code: websocket.CloseAbnormalClosure}, closeCauseAbnormal},
		{"okuma zaman aşımı", os.ErrDeadlineExceeded, closeCauseTimeout},
		{"diğer okuma hatası", errors.New("boom"), closeCauseReadErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCloseError(tc.err); got != tc.want {
				t.Errorf("classifyCloseError = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEventLogDisconnectCarriesCloseCause(t *testing.T) {
	h, c, dir := newEventHub(t)
	joinAgent(t, h, c, "r1", "alice")

	// The client manager must be receiving before unregister is sent — that
	// channel is unbuffered.
	go h.runClientManager()
	t.Cleanup(func() { close(h.done) })

	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
	c.closeCause = closeCauseAbnormal
	h.unregister <- c

	// The manager handles the unregister asynchronously; poll (not spin) for it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := eventsNamed(loggedEvents(t, h, dir), eventlog.EventClientDisconnected)
		if len(got) > 0 {
			if cause := got[0][eventlog.AttrErrorType]; cause != closeCauseAbnormal {
				t.Fatalf("%s = %v, want %q", eventlog.AttrErrorType, cause, closeCauseAbnormal)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnect olayı yazılmadı")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// #98 M3: liveness was derived from a timestamp that only RPC calls refreshed,
// so an agent that spent five minutes working — connected the whole time, its
// socket answering the hub's pings — was evicted from the roster as "stale".
func TestConnectedAgentIsNotEvictedAsStale(t *testing.T) {
	h, c, dir := newEventHub(t)
	joinAgent(t, h, c, "r1", "alice") // join, bağlantıyı canlı olarak kaydeder

	roomState := h.getOrCreateRoom("r1")
	roomState.mu.Lock()
	a := roomState.agents["alice"]
	a.LastSeen = types.Now() - float64(staleTimeout) - 1 // uzun süredir tool çağırmadı
	roomState.agents["alice"] = a
	roomState.mu.Unlock()

	roomState.ListAgents("") // stale temizliğini tetikler

	if !roomState.HasAgent("alice") {
		t.Error("bağlantısı açık agent stale sayılıp roster'dan silindi")
	}
	if got := eventsNamed(loggedEvents(t, h, dir), eventlog.EventAgentEvicted); len(got) != 0 {
		t.Errorf("bağlı agent için eviction olayı yazıldı: %+v", got)
	}
}

// An agent with no connection still ages out: the timeout is what clears
// records left behind by a client that never came back.
func TestDisconnectedAgentStillEvicted(t *testing.T) {
	h, c, dir := newEventHub(t)
	joinAgent(t, h, c, "r1", "alice")
	h.agentDisconnected("r1", "alice") // soket gitti; kaydı tutan kimse kalmadı

	roomState := h.getOrCreateRoom("r1")
	roomState.mu.Lock()
	a := roomState.agents["alice"]
	a.LastSeen = types.Now() - float64(staleTimeout) - 1
	roomState.agents["alice"] = a
	roomState.mu.Unlock()

	roomState.ListAgents("")

	if roomState.HasAgent("alice") {
		t.Error("bağlantısı olmayan agent stale temizliğinden kurtuldu")
	}
	if got := eventsNamed(loggedEvents(t, h, dir), eventlog.EventAgentEvicted); len(got) != 1 {
		t.Errorf("eviction olayı sayısı = %d, want 1", len(got))
	}
}

// A reconnecting client registers its new connection before the old one
// unregisters, so liveness is a count rather than a flag — otherwise the agent
// would briefly look gone and could be evicted mid-handover.
func TestLivenessSurvivesReconnectHandover(t *testing.T) {
	h, _, _ := newEventHub(t)

	h.agentConnected("r1", "alice") // eski bağlantı
	h.agentConnected("r1", "alice") // yeni bağlantı önce kaydolur
	h.agentDisconnected("r1", "alice")

	if !h.isAgentConnected("r1", "alice") {
		t.Error("devir sırasında agent bir an 'bağlı değil' göründü")
	}

	h.agentDisconnected("r1", "alice")
	if h.isAgentConnected("r1", "alice") {
		t.Error("son bağlantı da kapandığı hâlde agent bağlı sayılıyor")
	}
}

// A brief blip used to write "X ayrıldı" and then "X katıldı" into the room —
// system messages the other agents READ, so a reconnect looked like a departure
// and a new arrival. With a grace window a client that comes right back leaves
// no trace.
func TestQuickReconnectLeavesNoDepartureNoise(t *testing.T) {
	h, c, dir := newEventHub(t)
	h.graceWindow = 200 * time.Millisecond
	joinAgent(t, h, c, "r1", "alice")

	msgsBefore := len(h.getOrCreateRoom("r1").GetMessages())

	// The socket dies and the client is back before the window closes — through
	// the REAL join path. Calling h.agentConnected directly would bypass
	// RoomState.Join and hide a rejoin the hub actually refuses (Copilot review,
	// PR #107).
	h.releaseAgentForClient(c, "r1", "alice")
	replacement := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleJoinRoom(replacement, types.Request{
		ID: "rejoin", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
	})
	if resp := readResponse(t, replacement, "join_room"); !resp.Success {
		t.Fatalf("pencere içinde yeniden katılım reddedildi: %s", resp.Error)
	}

	time.Sleep(400 * time.Millisecond)

	if !h.getOrCreateRoom("r1").HasAgent("alice") {
		t.Error("pencere içinde dönen agent yine de odadan düşürüldü")
	}
	if got := len(h.getOrCreateRoom("r1").GetMessages()); got != msgsBefore {
		t.Errorf("mesaj sayısı %d → %d; kısa kopuş transcript'e gürültü yazdı", msgsBefore, got)
	}
	if got := eventsNamed(loggedEvents(t, h, dir), eventlog.EventAgentLeft); len(got) != 0 {
		t.Errorf("dönen agent için ayrılma olayı yazıldı: %+v", got)
	}
}

// An agent that does NOT come back must still be removed once the window
// closes — the window defers the departure, it does not cancel it.
func TestAgentGoneAfterGraceWindow(t *testing.T) {
	h, c, dir := newEventHub(t)
	h.graceWindow = 100 * time.Millisecond
	joinAgent(t, h, c, "r1", "alice")

	h.releaseAgentForClient(c, "r1", "alice")
	time.Sleep(300 * time.Millisecond)

	if h.getOrCreateRoom("r1").HasAgent("alice") {
		t.Error("pencere kapandığı hâlde agent roster'da kaldı")
	}
	got := eventsNamed(loggedEvents(t, h, dir), eventlog.EventAgentLeft)
	if len(got) != 1 {
		t.Fatalf("ayrılma olayı sayısı = %d, want 1", len(got))
	}
	if got[0][eventlog.AttrLeaveReason] != eventlog.LeaveReasonDisconnect {
		t.Errorf("%s = %v, want %q", eventlog.AttrLeaveReason, got[0][eventlog.AttrLeaveReason], eventlog.LeaveReasonDisconnect)
	}
}

// Codex review, PR #107: the grace window and the client's session replay were
// on a collision course. The window keeps the roster entry alive for five
// seconds; RoomState.Join rejects a name that is already present. So the very
// reconnect the window exists to smooth over was refused — leaving the agent
// connected but permanently outside the room.
func TestRejoinDuringGraceWindowIsTakeover(t *testing.T) {
	h, c, dir := newEventHub(t)
	joinAgent(t, h, c, "r1", "alice")
	msgsAfterJoin := len(h.getOrCreateRoom("r1").GetMessages())

	// Socket dies; the roster entry is deliberately retained for the window.
	h.releaseAgentForClient(c, "r1", "alice")

	// The replacement connection replays its join immediately.
	replacement := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleJoinRoom(replacement, types.Request{
		ID: "rejoin", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
	})
	resp := readResponse(t, replacement, "join_room")
	if !resp.Success {
		t.Fatalf("pencere içindeki yeniden katılım reddedildi: %s", resp.Error)
	}

	if !h.getOrCreateRoom("r1").HasAgent("alice") {
		t.Error("devralma sonrası agent roster'da yok")
	}
	if !h.isAgentConnected("r1", "alice") {
		t.Error("devralan bağlantı canlılık kaydı bırakmadı")
	}
	// A takeover is not an arrival: it must not announce itself to the room.
	if got := len(h.getOrCreateRoom("r1").GetMessages()); got != msgsAfterJoin {
		t.Errorf("mesaj sayısı %d → %d; devralma odaya katılım mesajı yazdı", msgsAfterJoin, got)
	}
	if got := eventsNamed(loggedEvents(t, h, dir), eventlog.EventAgentJoined); len(got) != 1 {
		t.Errorf("agent.joined olayı %d kez yazıldı, want 1 (devralma yeni katılım değil)", len(got))
	}
}

// Codex review, PR #107: the liveness counter counted successful joins, not
// sockets. clear_room wipes the roster without releasing connection claims, so
// a still-connected agent that joined again raised its own count to two — and a
// single later release could never bring it back to zero. That name would then
// look permanently connected: neither departure cleanup nor stale eviction
// could ever remove it.
func TestLivenessClaimIsPerConnectionNotPerJoin(t *testing.T) {
	h, c, _ := newEventHub(t)
	joinAgent(t, h, c, "r1", "alice")

	// clear_room empties the roster; the socket is untouched and rejoins.
	h.getOrCreateRoom("r1").ClearArchived(0)
	h.handleJoinRoom(c, types.Request{
		ID: "rejoin", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
	})
	if resp := readResponse(t, c, "join_room"); !resp.Success {
		t.Fatalf("clear sonrası yeniden katılım başarısız: %s", resp.Error)
	}

	// One socket, one claim — however many times it joined.
	h.releaseAgentForClient(c, "r1", "alice")
	if h.isAgentConnected("r1", "alice") {
		t.Error("tek soket birden fazla canlılık hakkı bıraktı; isim kalıcı olarak 'bağlı' kalırdı")
	}
}

// Codex review round 3, PR #107: a reconnect landing exactly as the grace timer
// expires must not be removed. Checking liveness and leaving as two steps let
// the join reclaim the entry in between — the client was told its join
// succeeded and then vanished from the roster.
func TestGraceExpiryDoesNotRemoveReclaimedAgent(t *testing.T) {
	h, c, _ := newEventHub(t)
	h.graceWindow = 50 * time.Millisecond
	joinAgent(t, h, c, "r1", "alice")

	h.releaseAgentForClient(c, "r1", "alice")

	// Reclaim right at the boundary, repeatedly, to land inside the window.
	for range 20 {
		replacement := &Client{hub: h, send: make(chan []byte, 8), rooms: make(map[string]bool)}
		h.handleJoinRoom(replacement, types.Request{
			ID: "rejoin", Type: "join_room", Room: "r1",
			Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
		})
		if resp := readResponse(t, replacement, "join_room"); !resp.Success {
			t.Fatalf("yeniden katılım reddedildi: %s", resp.Error)
		}
		time.Sleep(5 * time.Millisecond)
		if !h.getOrCreateRoom("r1").HasAgent("alice") {
			t.Fatal("başarıyla katılmış agent grace timer'ı tarafından silindi")
		}
		h.releaseAgentForClient(replacement, "r1", "alice")
	}
}

// Codex review round 3: an agent that disconnects, reconnects and disconnects
// again must get a FRESH window from the latest disconnect — not be removed on
// the first disconnect's old deadline.
func TestSecondDisconnectGetsFreshGraceWindow(t *testing.T) {
	h, c, _ := newEventHub(t)
	h.graceWindow = 300 * time.Millisecond
	joinAgent(t, h, c, "r1", "alice")

	h.releaseAgentForClient(c, "r1", "alice") // 1. kopuş, saat başlar
	time.Sleep(200 * time.Millisecond)

	replacement := &Client{hub: h, send: make(chan []byte, 8), rooms: make(map[string]bool)}
	h.handleJoinRoom(replacement, types.Request{
		ID: "rejoin", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
	})
	readResponse(t, replacement, "join_room")

	h.releaseAgentForClient(replacement, "r1", "alice") // 2. kopuş
	// The first timer would fire ~100ms from now; the second deserves 300ms.
	time.Sleep(180 * time.Millisecond)

	if !h.getOrCreateRoom("r1").HasAgent("alice") {
		t.Error("eski timer erken tetiklenip agent'ı sildi; ikinci kopuş taze pencere almalıydı")
	}
}

// Copilot review round 4, PR #107: after a hub restart the roster is reloaded
// from persisted state but managerAgent is NOT persisted. A reconnecting
// manager therefore takes the takeover branch, which only refreshed LastSeen —
// leaving the manager in the roster with no routing lock. The gateway silently
// stopped intercepting, and it could not self-heal: while the manager stayed
// connected, later joins were rejected as a duplicate name.
func TestManagerTakeoverRestoresRoutingLock(t *testing.T) {
	h, _, _ := newEventHub(t)
	h.setConfiguredManager("r1", "yonetici")

	// Simulate the post-restart state: the agent is in the reloaded roster, but
	// nothing holds the manager lock and no connection is registered.
	roomState := h.getOrCreateRoom("r1")
	roomState.mu.Lock()
	roomState.agents["yonetici"] = types.Agent{Role: "manager", LastSeen: types.Now()}
	roomState.mu.Unlock()
	if got := roomState.GetActiveManager(); got != "" {
		t.Fatalf("kurulum hatası: manager kilidi = %q, want boş", got)
	}

	mgr := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleJoinRoom(mgr, types.Request{
		ID: "rejoin", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "yonetici", "role": "manager"}),
	})
	if resp := readResponse(t, mgr, "join_room"); !resp.Success {
		t.Fatalf("manager yeniden katılımı reddedildi: %s", resp.Error)
	}

	if got := roomState.GetActiveManager(); got != "yonetici" {
		t.Errorf("devralmadan sonra manager kilidi = %q, want yonetici — gateway sessizce devre dışı kalırdı", got)
	}

	// And the gateway actually intercepts again.
	alice := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	joinAgent(t, h, alice, "r1", "alice")
	h.handleSendMessage(alice, types.Request{
		ID: "send", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "bob", "content": "merhaba"}),
	})
	readResponse(t, alice, "send_message")

	msgs := roomState.GetMessages()
	last := msgs[len(msgs)-1]
	if last.To != "yonetici" {
		t.Errorf("mesaj %q'ya gitti, want yonetici (manager gateway araya girmeli)", last.To)
	}
}

// The takeover must not let a reconnect steal the manager seat from a different,
// still-live manager.
func TestManagerTakeoverDoesNotDisplaceLiveManager(t *testing.T) {
	h, _, _ := newEventHub(t)
	h.setConfiguredManager("r1", "yonetici")

	mgr := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleJoinRoom(mgr, types.Request{
		ID: "join-mgr", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "yonetici", "role": "manager"}),
	})
	if resp := readResponse(t, mgr, "join_room"); !resp.Success {
		t.Fatalf("manager join başarısız: %s", resp.Error)
	}

	// Another name sits in the roster with no connection (post-restart shape),
	// and reconnects claiming the manager role.
	roomState := h.getOrCreateRoom("r1")
	roomState.mu.Lock()
	roomState.agents["sahte"] = types.Agent{Role: "manager", LastSeen: types.Now()}
	roomState.mu.Unlock()

	if _, ok := roomState.Takeover("sahte", "manager", nil, nil); !ok {
		t.Fatal("kurulum hatası: devralma gerçekleşmedi")
	}
	if got := roomState.GetActiveManager(); got != "yonetici" {
		t.Errorf("manager kilidi = %q, want yonetici (canlı manager devrilmemeli)", got)
	}
}

// Codex review round 4, PR #107: two replacement sockets replaying the same
// name can both see "disconnected" outside the room lock. Checking only that
// the roster entry exists let BOTH take over — two live clients sending and
// consuming under one identity.
func TestTakeoverIsExclusive(t *testing.T) {
	h, c, _ := newEventHub(t)
	joinAgent(t, h, c, "r1", "alice")
	h.releaseAgentForClient(c, "r1", "alice")

	first := &Client{hub: h, send: make(chan []byte, 32), rooms: make(map[string]bool)}
	second := &Client{hub: h, send: make(chan []byte, 32), rooms: make(map[string]bool)}

	join := func(cl *Client, id string) bool {
		h.handleJoinRoom(cl, types.Request{
			ID: id, Type: "join_room", Room: "r1",
			Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
		})
		return readResponse(t, cl, "join_room").Success
	}

	if !join(first, "a") {
		t.Fatal("ilk devralma başarısız")
	}
	if join(second, "b") {
		t.Error("ikinci istemci de aynı kimlikle odaya girdi; devralma münhasır olmalı")
	}
}

// Codex review round 4: a long-quiet agent whose socket blips must keep its
// roster entry until the grace window closes. Otherwise any concurrent
// list_agents deletes it first and the reconnect becomes a noisy fresh join —
// the window defeated exactly for the agents it was written for.
func TestStaleCleanupRespectsGraceWindow(t *testing.T) {
	h, c, _ := newEventHub(t)
	h.graceWindow = 2 * time.Second
	joinAgent(t, h, c, "r1", "alice")

	roomState := h.getOrCreateRoom("r1")
	roomState.mu.Lock()
	a := roomState.agents["alice"]
	a.LastSeen = types.Now() - float64(staleTimeout) - 1 // uzun süredir sessiz
	roomState.agents["alice"] = a
	roomState.mu.Unlock()

	h.releaseAgentForClient(c, "r1", "alice") // soket kısa süreliğine gitti

	roomState.ListAgents("") // eşzamanlı list_agents stale temizliğini tetikler

	if !roomState.HasAgent("alice") {
		t.Error("grace penceresi içindeki kayıt stale temizliğiyle silindi; yeniden bağlanma gürültülü join olurdu")
	}
}

// Codex review round 5, PR #107: the normal startup path makes the agent retry
// a join it cannot know already succeeded — join_room before the background
// dial returns a transport error, the supervisor replays it, and the agent
// tries again. Falling through to Join told it its own name was taken.
func TestRepeatJoinByOwningSocketIsIdempotent(t *testing.T) {
	h, c, _ := newEventHub(t)
	joinAgent(t, h, c, "r1", "alice")

	h.handleJoinRoom(c, types.Request{
		ID: "again", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
	})
	resp := readResponse(t, c, "join_room")
	if !resp.Success {
		t.Fatalf("kendi sahibi olduğu odaya tekrar katılım reddedildi: %s", resp.Error)
	}
	if !h.isAgentConnected("r1", "alice") {
		t.Error("yinelenen katılım canlılık kaydını düşürdü")
	}
}

// Codex review round 5: the fresh-join path claimed liveness after releasing the
// room lock, so two sockets racing an unused name could both end up live under
// one identity — the first adds the entry and pauses, the second takes it over.
func TestConcurrentFreshJoinsDoNotShareIdentity(t *testing.T) {
	h, _, _ := newEventHub(t)

	first := &Client{hub: h, send: make(chan []byte, 32), rooms: make(map[string]bool)}
	second := &Client{hub: h, send: make(chan []byte, 32), rooms: make(map[string]bool)}

	join := func(cl *Client, id string) bool {
		h.handleJoinRoom(cl, types.Request{
			ID: id, Type: "join_room", Room: "r1",
			Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
		})
		return readResponse(t, cl, "join_room").Success
	}

	if !join(first, "a") {
		t.Fatal("ilk katılım başarısız")
	}
	if join(second, "b") {
		t.Error("ikinci soket aynı ismi aldı; taze join de kilit altında talep etmeli")
	}
}

// Codex review round 5: a superseded timer must not retire the window a newer
// disconnect just earned.
func TestSupersededTimerDoesNotStealNewGraceWindow(t *testing.T) {
	h, _, _ := newEventHub(t)
	key := connKey("r1", "alice")

	h.agentConnected("r1", "alice")
	h.agentDisconnected("r1", "alice")
	h.connMu.Lock()
	h.departGen[key]++
	stale := h.departGen[key]
	h.departUntil[key] = time.Now().Add(time.Second)
	h.connMu.Unlock()

	// A newer disconnect supersedes it.
	h.connMu.Lock()
	h.departGen[key]++
	h.departUntil[key] = time.Now().Add(5 * time.Second)
	h.connMu.Unlock()

	if h.claimDeparture(key, stale) {
		t.Error("eski timer pencereyi sahiplendi")
	}
	h.connMu.RLock()
	_, stillSet := h.departUntil[key]
	h.connMu.RUnlock()
	if !stillSet {
		t.Error("eski timer yeni kopuşun penceresini sildi")
	}
}

// Symmetry audit after round 5: the idempotent-rejoin shortcut keys on the
// CONNECTION's belief that it is joined. clear_room empties the roster without
// touching connections, so that belief can outlive the entry — and the shortcut
// would report success while leaving the agent out of the room for good.
func TestRepeatJoinAfterClearActuallyRejoins(t *testing.T) {
	h, c, _ := newEventHub(t)
	joinAgent(t, h, c, "r1", "alice")

	h.getOrCreateRoom("r1").ClearArchived(0) // roster boşaldı, bağlantı duruyor

	h.handleJoinRoom(c, types.Request{
		ID: "again", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
	})
	if resp := readResponse(t, c, "join_room"); !resp.Success {
		t.Fatalf("clear sonrası yeniden katılım reddedildi: %s", resp.Error)
	}

	if !h.getOrCreateRoom("r1").HasAgent("alice") {
		t.Error("idempotent kısayol başarı dönüp agent'ı roster'a geri koymadı")
	}
}

// Codex review round 6, PR #107: clear_room empties the roster without touching
// sockets, so a name can be free in the roster while a live socket still
// answers to it. The fresh-join path checked only the roster and handed a
// SECOND client the same identity.
func TestFreshJoinRejectedWhileAnotherSocketHoldsName(t *testing.T) {
	h, owner, _ := newEventHub(t)
	joinAgent(t, h, owner, "r1", "alice")

	h.getOrCreateRoom("r1").ClearArchived(0) // roster boş, owner'ın soketi duruyor

	intruder := &Client{hub: h, send: make(chan []byte, 32), rooms: make(map[string]bool)}
	h.handleJoinRoom(intruder, types.Request{
		ID: "steal", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
	})
	if resp := readResponse(t, intruder, "join_room"); resp.Success {
		t.Error("başka bir soket canlıyken aynı isim ikinci istemciye verildi")
	}

	// The owner itself must still be able to rejoin after the clear.
	h.handleJoinRoom(owner, types.Request{
		ID: "self", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice"}),
	})
	if resp := readResponse(t, owner, "join_room"); !resp.Success {
		t.Errorf("sahibi kendi ismine geri dönemedi: %s", resp.Error)
	}
}

// Codex review round 6: a repeat join that upgrades the role must actually take
// the routing lock, not report a success that changes nothing.
func TestRepeatJoinAppliesRoleUpgrade(t *testing.T) {
	h, c, _ := newEventHub(t)
	h.setConfiguredManager("r1", "alice")
	joinAgent(t, h, c, "r1", "alice") // önce rolsüz katılır

	if got := h.getOrCreateRoom("r1").GetActiveManager(); got != "" {
		t.Fatalf("kurulum hatası: manager kilidi = %q, want boş", got)
	}

	h.handleJoinRoom(c, types.Request{
		ID: "upgrade", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice", "role": "manager"}),
	})
	if resp := readResponse(t, c, "join_room"); !resp.Success {
		t.Fatalf("rol yükseltmesi reddedildi: %s", resp.Error)
	}

	if got := h.getOrCreateRoom("r1").GetActiveManager(); got != "alice" {
		t.Errorf("manager kilidi = %q, want alice — kısayol başarı dönüp rolü uygulamamış", got)
	}
}

// Codex review round 6: releasing the claim and arming the grace window must be
// one locked step. In the gap the agent counts as neither connected nor
// departing, so a concurrent list_agents deletes a long-quiet agent outright.
func TestReleaseAndGraceArePresentedAtomically(t *testing.T) {
	h, c, _ := newEventHub(t)
	h.graceWindow = 2 * time.Second
	joinAgent(t, h, c, "r1", "alice")

	roomState := h.getOrCreateRoom("r1")
	roomState.mu.Lock()
	a := roomState.agents["alice"]
	a.LastSeen = types.Now() - float64(staleTimeout) - 1
	roomState.agents["alice"] = a
	roomState.mu.Unlock()

	// Hammer stale cleanup from another goroutine while the release happens.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				roomState.ListAgents("")
			}
		}
	}()

	h.releaseAgentForClient(c, "r1", "alice")
	time.Sleep(50 * time.Millisecond)
	close(stop)
	<-done

	if !roomState.HasAgent("alice") {
		t.Error("bırakma ile pencere kurulumu arasındaki boşlukta agent silindi")
	}
}

// Codex review round 7, PR #107: the role follow-up was only half done. A
// downgrade left managerAgent set — the room kept routing through an agent that
// had already told the hub it was no longer the manager, and the client had
// recorded the lesser role for its next replay.
func TestTakeoverAppliesRoleDowngrade(t *testing.T) {
	h, c, _ := newEventHub(t)
	h.setConfiguredManager("r1", "alice")
	h.handleJoinRoom(c, types.Request{
		ID: "join", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice", "role": "manager"}),
	})
	if resp := readResponse(t, c, "join_room"); !resp.Success {
		t.Fatalf("manager join başarısız: %s", resp.Error)
	}
	roomState := h.getOrCreateRoom("r1")
	if got := roomState.GetActiveManager(); got != "alice" {
		t.Fatalf("kurulum hatası: manager = %q", got)
	}

	// Same socket rejoins as a plain worker.
	h.handleJoinRoom(c, types.Request{
		ID: "downgrade", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "alice", "role": ""}),
	})
	if resp := readResponse(t, c, "join_room"); !resp.Success {
		t.Fatalf("rol düşürme reddedildi: %s", resp.Error)
	}

	if got := roomState.GetActiveManager(); got != "" {
		t.Errorf("manager kilidi = %q, want boş — düşürülen agent üzerinden routing sürüyor", got)
	}
	if got := roomState.GetAgents()["alice"].Role; got != "" {
		t.Errorf("roster rolü = %q, want boş — roster ile kilit birlikte hareket etmeli", got)
	}
}
