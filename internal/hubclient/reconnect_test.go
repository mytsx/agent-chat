package hubclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"desktop/internal/types"

	"github.com/gorilla/websocket"
)

// fakeHub is a minimal stand-in for the real hub: it accepts connections,
// records the requests each one made, and can drop a connection on demand so a
// test can observe what the client does about it.
type fakeHub struct {
	server   *httptest.Server
	upgrader websocket.Upgrader

	mu sync.Mutex
	// requests holds every request type received, in order, across ALL
	// connections — which is how a test sees whether state was replayed.
	requests []types.Request
	conns    []*websocket.Conn
	accepted int
	// rejectJoin makes the hub refuse join_room at the protocol level (success
	// false, no transport error) — the shape a manager authorization failure has.
	rejectJoin bool
	// rejectLeave does the same for leave_room — the shape an identity mismatch
	// has, where the agent stays in the room.
	rejectLeave bool
	// dropOn severs the connection as soon as a request of this type arrives,
	// WITHOUT answering — the "hub applied it but the response was lost" shape.
	dropOn string
	// onRequest runs before the response is written, so a test can change client
	// state at a point the client cannot have observed yet.
	onRequest func(types.Request)
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{}
	h.server = httptest.NewServer(http.HandlerFunc(h.handle))
	t.Cleanup(h.server.Close)
	return h
}

func (h *fakeHub) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.mu.Lock()
	h.conns = append(h.conns, conn)
	h.accepted++
	h.mu.Unlock()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req types.Request
		if json.Unmarshal(data, &req) != nil {
			continue
		}
		h.mu.Lock()
		h.requests = append(h.requests, req)
		h.mu.Unlock()

		h.mu.Lock()
		reject := (h.rejectJoin && req.Type == "join_room") || (h.rejectLeave && req.Type == "leave_room")
		drop := h.dropOn != "" && req.Type == h.dropOn
		hook := h.onRequest
		h.mu.Unlock()
		if hook != nil {
			hook(req)
		}
		if drop {
			conn.Close()
			return
		}

		resp := types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: json.RawMessage(`{"ok":true}`)}
		if reject {
			resp = types.Response{ID: req.ID, RequestType: req.Type, Success: false, Error: "istek reddedildi"}
		}
		payload, _ := json.Marshal(resp)
		if conn.WriteMessage(websocket.TextMessage, payload) != nil {
			return
		}
	}
}

// url is the ws:// address clients dial.
func (h *fakeHub) url() string {
	return "ws" + strings.TrimPrefix(h.server.URL, "http") + "/ws"
}

// dropAll severs every live connection without a close handshake — the abnormal
// closure that dominates the real logs.
func (h *fakeHub) dropAll() {
	h.mu.Lock()
	conns := append([]*websocket.Conn(nil), h.conns...)
	h.conns = nil
	h.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (h *fakeHub) acceptedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.accepted
}

// requestTypes returns the types received so far, oldest first.
func (h *fakeHub) requestTypes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.requests))
	for _, r := range h.requests {
		out = append(out, r.Type)
	}
	return out
}

func newTestClient(t *testing.T, h *fakeHub) *HubClient {
	t.Helper()
	c := New(h.url(), log.New(io.Discard, "", 0))
	// Keep the test fast: the production backoff starts at half a second.
	c.minBackoff = 5 * time.Millisecond
	c.maxBackoff = 20 * time.Millisecond
	t.Cleanup(c.Close)
	return c
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("zaman aşımı: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The core of #98: a dropped connection was permanent. readLoop exited, conn
// went nil, and every later Send failed forever — the agent was out of the room
// until its whole MCP process restarted.
func TestClientReconnectsAfterDrop(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitFor(t, "ilk bağlantı", func() bool { return h.acceptedCount() == 1 })

	h.dropAll()
	waitFor(t, "yeniden bağlanma", func() bool { return h.acceptedCount() >= 2 })

	// And the client is usable again, not just connected.
	if _, err := c.ListRooms(); err != nil {
		t.Errorf("yeniden bağlandıktan sonra istek başarısız: %v", err)
	}
}

// Reconnecting is only half the fix: the hub drops an agent from the roster the
// moment its socket dies, so the client must re-establish its identity, its
// room membership and its subscriptions without anyone asking.
func TestClientReplaysSessionStateOnReconnect(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := c.Identify("mcp", "alice", "r1", ""); err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if _, err := c.JoinRoom("r1", "alice", "manager"); err != nil {
		t.Fatalf("JoinRoom: %v", err)
	}
	if err := c.Subscribe([]string{"r1", "r2"}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	before := len(h.requestTypes())
	h.dropAll()
	waitFor(t, "durumun yeniden kurulması", func() bool {
		var identify, join, subscribe bool
		for _, typ := range h.requestTypes()[before:] {
			switch typ {
			case "identify":
				identify = true
			case "join_room":
				join = true
			case "subscribe":
				subscribe = true
			}
		}
		return identify && join && subscribe
	})
}

// A client that left the room deliberately must not be dragged back in by a
// later reconnect.
func TestClientDoesNotRejoinAfterLeave(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := c.JoinRoom("r1", "alice", ""); err != nil {
		t.Fatalf("JoinRoom: %v", err)
	}
	if _, err := c.LeaveRoom("r1", "alice"); err != nil {
		t.Fatalf("LeaveRoom: %v", err)
	}

	before := len(h.requestTypes())
	h.dropAll()
	waitFor(t, "yeniden bağlanma", func() bool { return h.acceptedCount() >= 2 })
	// Give any (incorrect) rejoin time to appear.
	time.Sleep(100 * time.Millisecond)

	for _, typ := range h.requestTypes()[before:] {
		if typ == "join_room" {
			t.Fatal("bilinçli ayrılıştan sonra odaya geri sokuldu")
		}
	}
}

// Close must stop the supervisor: a client torn down on purpose has no business
// dialling the hub again.
func TestCloseStopsReconnecting(t *testing.T) {
	h := newFakeHub(t)
	c := New(h.url(), log.New(io.Discard, "", 0))
	c.minBackoff = 5 * time.Millisecond
	c.maxBackoff = 20 * time.Millisecond

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitFor(t, "ilk bağlantı", func() bool { return h.acceptedCount() == 1 })

	c.Close()
	h.dropAll()
	time.Sleep(150 * time.Millisecond)

	if got := h.acceptedCount(); got != 1 {
		t.Errorf("kapatıldıktan sonra %d bağlantı denendi, want 1 (hiç)", got)
	}
}

// Backoff must not be a fixed ladder: identical delays make every agent
// reconnect in lockstep after a hub restart and hammer it as one wave.
func TestBackoffIsBoundedAndJittered(t *testing.T) {
	c := New("ws://example.invalid/ws", log.New(io.Discard, "", 0))
	c.minBackoff = 100 * time.Millisecond
	c.maxBackoff = 800 * time.Millisecond

	seen := map[time.Duration]bool{}
	d := time.Duration(0)
	for range 20 {
		d = c.nextBackoff(d)
		seen[d] = true
		if d > c.maxBackoff {
			t.Fatalf("backoff %v üst sınırı (%v) aştı", d, c.maxBackoff)
		}
		if d <= 0 {
			t.Fatalf("backoff %v, pozitif olmalı", d)
		}
	}
	if len(seen) < 5 {
		t.Errorf("farklı gecikme sayısı = %d; jitter yok gibi görünüyor", len(seen))
	}
}

// The hub listens on an OS-assigned port (Run(0)) and rewrites hub.port on
// every start, so a restarted hub is at a DIFFERENT address. A supervisor that
// redials the remembered one would retry a dead port forever — the crash
// recovery would look like it worked and never actually reconnect.
func TestClientRedialsResolvedAddressAfterHubMoves(t *testing.T) {
	oldHub := newFakeHub(t)
	newHub := newFakeHub(t)

	addr := oldHub.url()
	var addrMu sync.Mutex
	c := New(addr, log.New(io.Discard, "", 0))
	c.minBackoff = 5 * time.Millisecond
	c.maxBackoff = 20 * time.Millisecond
	c.SetAddrResolver(func() (string, error) {
		addrMu.Lock()
		defer addrMu.Unlock()
		return addr, nil
	})
	t.Cleanup(c.Close)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitFor(t, "ilk bağlantı", func() bool { return oldHub.acceptedCount() == 1 })

	// The hub restarts somewhere else, exactly as Run(0) produces.
	addrMu.Lock()
	addr = newHub.url()
	addrMu.Unlock()
	oldHub.server.Close()
	oldHub.dropAll()

	waitFor(t, "yeni adrese bağlanma", func() bool { return newHub.acceptedCount() >= 1 })
}

// A client that could not reach the hub at startup must keep trying rather than
// giving up: the desktop may not have written hub.port yet.
func TestClientRecoversWhenHubAppearsLater(t *testing.T) {
	h := newFakeHub(t)
	// Point the client at nothing until the hub "appears".
	var addrMu sync.Mutex
	addr := "ws://127.0.0.1:1/ws"

	c := New(addr, log.New(io.Discard, "", 0))
	c.minBackoff = 5 * time.Millisecond
	c.maxBackoff = 20 * time.Millisecond
	c.SetAddrResolver(func() (string, error) {
		addrMu.Lock()
		defer addrMu.Unlock()
		return addr, nil
	})
	t.Cleanup(c.Close)

	c.StartBackgroundConnect()

	addrMu.Lock()
	addr = h.url()
	addrMu.Unlock()

	waitFor(t, "hub sonradan açılınca bağlanma", func() bool { return h.acceptedCount() >= 1 })
}

// The bootstrap hook exists because a background connect gives the caller no
// inline moment to identify/join. It must run once the hub appears — and only
// once, since reconnects replay what it recorded.
func TestBootstrapRunsOnceWhenHubAppears(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	var calls int
	var mu sync.Mutex
	c.SetBootstrap(func(cl Bootstrap) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return cl.Identify("mcp", "alice", "r1", "")
	})

	c.StartBackgroundConnect()
	waitFor(t, "bootstrap", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls == 1
	})

	// A reconnect replays the identify; it must not run bootstrap again.
	h.dropAll()
	waitFor(t, "yeniden bağlanma", func() bool { return h.acceptedCount() >= 2 })
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("bootstrap %d kez çalıştı, want 1", calls)
	}
}

// Codex review, PR #107: with a background connect the agent can call join_room
// before the first dial lands. Dropping that intent would leave it outside the
// room until the model happened to retry on its own.
func TestJoinBeforeConnectIsReplayedOnceHubAppears(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	// No connection has been made at all yet, so this fails at the transport —
	// exactly the window a background connect opens for the agent. Ordering the
	// join before the dial (rather than racing the two) keeps the test about the
	// behaviour instead of about scheduling.
	if _, err := c.JoinRoom("r1", "alice", ""); err == nil {
		t.Fatal("bağlantı yokken join başarılı görünmemeli")
	}

	c.StartBackgroundConnect()

	waitFor(t, "bağlantı kurulunca join'in replay edilmesi", func() bool {
		for _, typ := range h.requestTypes() {
			if typ == "join_room" {
				return true
			}
		}
		return false
	})
}

// A join the hub REJECTED must not be replayed: it would fail the same way on
// every reconnect forever.
func TestRejectedJoinIsNotReplayed(t *testing.T) {
	h := newFakeHub(t)
	h.rejectJoin = true
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	resp, err := c.JoinRoom("r1", "alice", "manager")
	if err != nil {
		t.Fatalf("JoinRoom transport error: %v", err)
	}
	if resp.Success {
		t.Fatal("kurulum hatası: sahte hub join'i reddetmeliydi")
	}

	before := len(h.requestTypes())
	h.dropAll()
	waitFor(t, "yeniden bağlanma", func() bool { return h.acceptedCount() >= 2 })
	time.Sleep(100 * time.Millisecond)

	for _, typ := range h.requestTypes()[before:] {
		if typ == "join_room" {
			t.Fatal("hub'ın reddettiği join replay edildi; her turda aynı şekilde başarısız olurdu")
		}
	}
}

// Copilot review, PR #107: the fake hub always answered Success, so no test
// drove restoreSession to failure — which is how the "rejected replay counts as
// restored" bug survived. Here the hub refuses the REPLAYED join, so the
// supervisor must keep retrying instead of declaring victory.
func TestSupervisorRetriesWhenReplayIsRejected(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := c.JoinRoom("r1", "alice", "manager"); err != nil {
		t.Fatalf("JoinRoom: %v", err)
	}

	// From now on the hub refuses joins, as it would while the desktop has not
	// restored a manager's authorization yet.
	h.mu.Lock()
	h.rejectJoin = true
	h.mu.Unlock()

	h.dropAll()
	// The supervisor must not settle: each attempt is refused, so it keeps
	// dialling rather than leaving the client connected-but-unjoined.
	waitFor(t, "reddedilen replay sonrası yeniden deneme", func() bool { return h.acceptedCount() >= 3 })

	// Once the hub relents, the session is restored for real.
	h.mu.Lock()
	h.rejectJoin = false
	h.mu.Unlock()

	waitFor(t, "izin verilince katılım", func() bool {
		var joins int
		for _, typ := range h.requestTypes() {
			if typ == "join_room" {
				joins++
			}
		}
		return joins >= 2
	})
}

// Copilot review round 2, PR #107: reconnection must be level-triggered. With
// the earlier edge-triggered relaunch, a socket dying between the supervisor's
// last check and its return had its wake-up swallowed as a duplicate, leaving
// the client disconnected with nobody scheduled to redial. Repeated rapid drops
// land signals exactly in that window.
func TestRepeatedRapidDropsAlwaysEndConnected(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := c.Identify("mcp", "alice", "r1", ""); err != nil {
		t.Fatalf("Identify: %v", err)
	}

	for range 15 {
		h.dropAll()
		time.Sleep(3 * time.Millisecond) // sinyali süpervizörün iş ortasına düşür
	}

	waitFor(t, "arka arkaya kopuşlardan sonra bağlantının geri gelmesi", func() bool {
		_, err := c.ListRooms()
		return err == nil
	})
}

// A disconnect signalled while the supervisor is already working must still be
// honoured once it finishes — the buffered channel is what guarantees that.
func TestDisconnectSignalIsNotLostWhileSupervisorBusy(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	// Raise the signal twice before anything is connected: the second must not
	// be swallowed in a way that leaves the client idle.
	c.notifyDisconnected()
	c.notifyDisconnected()

	waitFor(t, "sinyalin işlenmesi", func() bool { return h.acceptedCount() >= 1 })

	// And a signal raised after that connection still reconnects.
	h.dropAll()
	waitFor(t, "sonraki sinyalin işlenmesi", func() bool { return h.acceptedCount() >= 2 })
}

// Codex review round 3, PR #107: the leave intent must be cleared BEFORE the
// RPC. If the hub applies the leave but the response is lost, a still-set
// intent silently rejoins the agent the caller just took out.
func TestLeaveIntentClearedEvenWhenResponseIsLost(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := c.JoinRoom("r1", "alice", ""); err != nil {
		t.Fatalf("JoinRoom: %v", err)
	}

	// The hub applies the leave and then the socket dies before the response
	// lands — modelled by dropping every connection as the call goes out.
	h.mu.Lock()
	h.dropOn = "leave_room"
	h.mu.Unlock()
	_, _ = c.LeaveRoom("r1", "alice")

	waitFor(t, "yeniden bağlanma", func() bool { return h.acceptedCount() >= 2 })
	time.Sleep(150 * time.Millisecond)

	c.mu.Lock()
	joined := c.sess.joined
	c.mu.Unlock()
	if joined {
		t.Error("yanıt kaybolunca ayrılma niyeti korunmuş; reconnect agent'ı odaya geri sokardı")
	}
}

// Codex review round 3: a failed join for another room must not overwrite an
// established membership — later operations still target the original room and
// would be rejected as wrong-room.
func TestFailedJoinDoesNotOverwriteEstablishedRoom(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := c.JoinRoom("A", "alice", ""); err != nil {
		t.Fatalf("JoinRoom A: %v", err)
	}

	// A transient disconnect, then a join for a different room that never
	// reaches the hub.
	c.dropConn()
	if _, err := c.JoinRoom("B", "alice", ""); err == nil {
		t.Fatal("bağlantı yokken join başarılı görünmemeli")
	}

	c.mu.Lock()
	room := c.sess.joinRoom
	c.mu.Unlock()
	if room != "A" {
		t.Errorf("kayıtlı oda = %q, want A (başarısız join kurulu üyeliği ezmemeli)", room)
	}
}

// Found while testing the leave-intent fix: an RPC in flight when the socket
// died waited out the full 15s request timeout, because nothing woke the
// pending caller. For an agent that is the same as hanging — the symptom #98
// exists to remove.
func TestInFlightRequestFailsFastWhenConnectionDrops(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	// The hub swallows this request and severs the connection instead of
	// answering it.
	h.mu.Lock()
	h.dropOn = "list_rooms"
	h.mu.Unlock()

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	start := time.Now()
	if _, err := c.ListRooms(); err == nil {
		t.Fatal("kopan bağlantıda istek başarılı görünmemeli")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("istek %v bekledi; bağlantı koptuğunda hemen dönmeliydi (istek zaman aşımı %v)", elapsed, defaultTimeout)
	}
}

// Codex review round 4, PR #107: a read loop finishing after a replacement
// socket was installed must not fail requests written on the new one.
func TestFailPendingIsScopedToItsOwnSocket(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// A request on the CURRENT socket, then an old epoch's failure.
	c.mu.Lock()
	epoch := c.connEpoch
	c.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := c.ListRooms()
		done <- err
	}()

	// Simulate a stale read loop from a previous socket finishing now.
	c.failPending(epoch - 1)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("güncel sokete yazılmış istek, eski okuyucunun çıkışıyla düşürüldü: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("istek yanıtlanmadı")
	}
}

// Codex review round 4: during a prolonged startup outage a corrected
// pre-connect join must replace the earlier pending one — but a join the hub
// actually granted must not be overwritten by a later failed attempt.
func TestPendingJoinIntentCanBeCorrected(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	// Nothing connected yet: both fail at the transport.
	if _, err := c.JoinRoom("A", "alice", ""); err == nil {
		t.Fatal("bağlantı yokken join başarılı görünmemeli")
	}
	if _, err := c.JoinRoom("B", "alice", ""); err == nil {
		t.Fatal("bağlantı yokken join başarılı görünmemeli")
	}

	c.mu.Lock()
	room := c.sess.joinRoom
	c.mu.Unlock()
	if room != "B" {
		t.Errorf("kayıtlı oda = %q, want B (düzeltilen bekleyen niyet öncekinin yerine geçmeli)", room)
	}

	// Once established, a later failed attempt must not move it.
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := c.JoinRoom("B", "alice", ""); err != nil {
		t.Fatalf("JoinRoom B: %v", err)
	}
	c.dropConn()
	if _, err := c.JoinRoom("C", "alice", ""); err == nil {
		t.Fatal("bağlantı yokken join başarılı görünmemeli")
	}

	c.mu.Lock()
	room = c.sess.joinRoom
	c.mu.Unlock()
	if room != "B" {
		t.Errorf("kayıtlı oda = %q, want B (kurulu üyelik ezilmemeli)", room)
	}
}

// Codex review round 5, PR #107: a Subscribe issued while the supervisor is
// between sockets was lost. CreateTeam only logs that error, so the desktop
// stayed connected but received no events for the new team until a restart.
func TestSubscribeIntentSurvivesTransportFailure(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	// Nothing connected yet: the subscribe fails at the transport.
	if err := c.Subscribe([]string{"r1", "r2"}); err == nil {
		t.Fatal("bağlantı yokken subscribe başarılı görünmemeli")
	}

	c.StartBackgroundConnect()

	waitFor(t, "bağlantı kurulunca subscribe'ın replay edilmesi", func() bool {
		for _, typ := range h.requestTypes() {
			if typ == "subscribe" {
				return true
			}
		}
		return false
	})
}

// Codex review round 6, PR #107: a join already in flight — notably one the
// supervisor is replaying — must not resurrect an intent the caller cleared
// while it ran, or the next disconnect silently rejoins the agent.
func TestConcurrentLeaveBeatsInFlightJoin(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := c.JoinRoom("r1", "alice", ""); err != nil {
		t.Fatalf("JoinRoom: %v", err)
	}

	// Model the replay's window: capture the pre-leave generation, let the leave
	// land, then complete the join.
	if _, err := c.LeaveRoom("r1", "alice"); err != nil {
		t.Fatalf("LeaveRoom: %v", err)
	}
	if _, err := c.JoinRoom("r1", "alice", ""); err != nil {
		t.Fatalf("JoinRoom (replay): %v", err)
	}

	// The replayed join was a fresh call, so it legitimately re-establishes
	// membership; what must NOT happen is a stale in-flight join reviving it.
	// Simulate that directly: a join whose generation predates the leave.
	c.mu.Lock()
	c.sess.joined = false
	c.sess.joinEstablished = false
	stale := c.sess.gen
	rev := c.sess.rev
	c.sess.gen++ // araya giren bir leave
	c.mu.Unlock()

	c.recordJoinIfCurrent(stale, rev, "r1", "alice", "", true, true)

	c.mu.Lock()
	joined := c.sess.joined
	c.mu.Unlock()
	if joined {
		t.Error("bayat bir join, araya giren leave'in temizlediği niyeti geri diriltti")
	}
}

// #108/1: AGENT_CHAT_HUB_PORT cannot change while the process runs and outranks
// hub.port, so a malformed value is permanent — the MCP server would serve stdio
// and retry the same bad port forever. A missing hub.port is the opposite and
// must stay transient, or startup would go back to exiting on a hub that simply
// had not written the file yet.
func TestDiscoverHubAddrSeparatesPermanentConfigFromTransient(t *testing.T) {
	t.Run("invalid env override is permanent", func(t *testing.T) {
		t.Setenv("AGENT_CHAT_HUB_PORT", "70000")

		_, err := DiscoverHubAddr(t.TempDir())
		if !errors.Is(err, ErrInvalidHubPortConfig) {
			t.Fatalf("DiscoverHubAddr() error = %v, want ErrInvalidHubPortConfig", err)
		}
	})

	t.Run("missing hub.port is transient", func(t *testing.T) {
		t.Setenv("AGENT_CHAT_HUB_PORT", "")

		_, err := DiscoverHubAddr(t.TempDir())
		if err == nil {
			t.Fatal("DiscoverHubAddr() error = nil, want hub.port not found")
		}
		if errors.Is(err, ErrInvalidHubPortConfig) {
			t.Fatalf("DiscoverHubAddr() error = %v, must NOT be permanent: the desktop writes hub.port moments later", err)
		}
	})

	t.Run("malformed hub.port file is transient", func(t *testing.T) {
		t.Setenv("AGENT_CHAT_HUB_PORT", "")
		dataDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dataDir, "hub.port"), []byte("abc"), 0644); err != nil {
			t.Fatal(err)
		}

		_, err := DiscoverHubAddr(dataDir)
		if err == nil {
			t.Fatal("DiscoverHubAddr() error = nil, want invalid port")
		}
		// The file is rewritten on every hub start, so a torn read is worth
		// retrying — unlike the env var.
		if errors.Is(err, ErrInvalidHubPortConfig) {
			t.Fatalf("DiscoverHubAddr() error = %v, must NOT be permanent for a file source", err)
		}
	})
}

// #108/2: a write can see a half-open connection before the read loop does. The
// write path used to return the error and leave the dead socket installed, so
// isConnected() stayed true, the supervisor stayed asleep, and every later RPC
// went to the same unusable connection until the 90s read deadline expired.
//
// The socket is installed WITHOUT a read loop on purpose: that isolates the
// write path, so a pass cannot be the read loop's cleanup doing the work.
func TestWriteFailureDropsSocketAndTriggersReconnect(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	conn, _, err := websocket.DefaultDialer.Dial(h.url(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.mu.Lock()
	c.conn = conn
	c.connEpoch++
	c.mu.Unlock()

	// Kill the transport under gorilla so the next write fails while nothing is
	// reading — the half-open shape.
	if err := conn.UnderlyingConn().Close(); err != nil {
		t.Fatalf("underlying close: %v", err)
	}

	if _, err := c.ListRooms(); err == nil {
		t.Fatal("ListRooms() error = nil, want write failure")
	}
	if c.isConnected() {
		t.Fatal("yazma hatasından sonra soket hâlâ kurulu; süpervizör uyanmaz")
	}
	waitFor(t, "yazma hatasından sonra yeniden bağlanma", func() bool { return h.acceptedCount() >= 2 })
}

// #108/3: the raw socket used to become visible before identify/join/subscribe
// replayed onto it, so an ordinary tool call racing the reconnect landed on an
// unidentified connection and got a protocol rejection instead of its result.
func TestOrdinarySendsWaitForSessionRestore(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	release := make(chan struct{})
	started := make(chan struct{})
	c.SetBootstrap(func(cl Bootstrap) error {
		close(started)
		<-release
		// Session-building traffic passes the gate: it is what opens it.
		return cl.Identify("mcp", "alice", "r1", "")
	})

	c.StartBackgroundConnect()
	<-started

	if _, err := c.ListRooms(); err == nil || !strings.Contains(err.Error(), "geri yükleniyor") {
		t.Fatalf("ListRooms() during restore = %v, want gated", err)
	}

	close(release)
	waitFor(t, "oturum hazır", func() bool {
		_, err := c.ListRooms()
		return err == nil
	})

	// The gate must not have swallowed the bootstrap's own request.
	if !slices.Contains(h.requestTypes(), "identify") {
		t.Errorf("hub istekleri = %v, identify bekleniyordu", h.requestTypes())
	}
}

// Codex review, PR #113: a join turned away by the restore gate still records
// its intent. If the replay had already taken its snapshot, that intent was not
// carried — restore reported success, the supervisor stopped, and the client sat
// connected but outside the room until the next disconnect. In the startup race
// that is the whole session.
func TestJoinRecordedDuringRestoreIsStillReplayed(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	release := make(chan struct{})
	started := make(chan struct{})
	c.SetBootstrap(func(cl Bootstrap) error {
		close(started)
		<-release
		return cl.Identify("mcp", "alice", "r1", "")
	})

	c.StartBackgroundConnect()
	<-started

	// The agent's own join lands while the session is being restored: refused
	// here, but recorded as intent.
	if _, err := c.JoinRoom("r1", "alice", ""); err == nil {
		t.Fatal("JoinRoom() during restore = nil, want gated")
	}
	close(release)

	// No second disconnect: the same restore has to notice and replay it.
	waitFor(t, "kapının reddettiği join'in replay edilmesi", func() bool {
		return slices.Contains(h.requestTypes(), "join_room")
	})
	if got := h.acceptedCount(); got != 1 {
		t.Errorf("kabul edilen bağlantı = %d, want 1 (replay yeni bağlantı gerektirmemeli)", got)
	}
}

// Codex review, PR #113: the gate used to be armed only after Connect returned,
// so a request scheduled between the socket being published and the replay
// starting saw a live socket and an open gate — and reached the hub before
// identify replayed, collecting the protocol rejection the gate exists to
// prevent. The window is a few instructions wide, so it is pinned at its source:
// the address resolver runs inside Connect, before the dial.
func TestGateIsArmedBeforeTheDial(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	var mu sync.Mutex
	var gatedAtDial []bool
	c.SetAddrResolver(func() (string, error) {
		mu.Lock()
		gatedAtDial = append(gatedAtDial, c.isRestoring())
		mu.Unlock()
		return h.url(), nil
	})

	c.StartBackgroundConnect()
	waitFor(t, "bağlantı", func() bool { return h.acceptedCount() >= 1 })

	mu.Lock()
	defer mu.Unlock()
	if len(gatedAtDial) == 0 {
		t.Fatal("çözümleyici hiç çağrılmadı")
	}
	for i, gated := range gatedAtDial {
		if !gated {
			t.Fatalf("dial #%d kapı açıkken yapıldı; soket replay'den önce trafiğe açılır", i+1)
		}
	}
}

// Codex review round 2, PR #113: a replayed join for room A can land after the
// caller corrected itself to room B behind the gate. Writing A back would make
// the next pass replay A and lose B for good.
func TestStaleReplaySuccessDoesNotOverwriteNewerJoinIntent(t *testing.T) {
	c := newTestClient(t, newFakeHub(t))

	c.mu.Lock()
	startGen, startRev := c.sess.gen, c.sess.rev
	c.mu.Unlock()

	// The corrected join lands while the replay is in flight.
	c.recordJoinIfCurrent(startGen, startRev, "B", "alice", "", false, true, true)

	// The in-flight replay of A now succeeds, carrying the older revision.
	c.recordJoinIfCurrent(startGen, startRev, "A", "alice", "", true, false)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.joinRoom != "B" {
		t.Errorf("kayıtlı oda = %q, want B (bayat replay yeni niyeti ezmemeli)", c.sess.joinRoom)
	}
}

// Codex review round 2, PR #113: a leave the gate turned away never reached the
// hub, while the replay running at that moment may have just put the agent back
// in the room. Nothing would rejoin — but nothing would take it out either.
func TestLeaveRefusedByGateIsPerformedAfterRestore(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	release := make(chan struct{})
	started := make(chan struct{})
	c.SetBootstrap(func(cl Bootstrap) error {
		close(started)
		<-release
		if err := cl.Identify("mcp", "alice", "r1", ""); err != nil {
			return err
		}
		_, err := cl.JoinRoom("r1", "alice", "")
		return err
	})

	c.StartBackgroundConnect()
	<-started

	if _, err := c.LeaveRoom("r1", "alice"); err == nil {
		t.Fatal("LeaveRoom() during restore = nil, want gated")
	}
	close(release)

	waitFor(t, "kapının reddettiği leave'in tamamlanması", func() bool {
		return slices.Contains(h.requestTypes(), "leave_room")
	})
	if got := h.acceptedCount(); got != 1 {
		t.Errorf("kabul edilen bağlantı = %d, want 1 (telafi yeni bağlantı gerektirmemeli)", got)
	}
}

// Codex review round 2, PR #113: if the session keeps changing, the last pass
// used to fall through and report success — stopping the supervisor with work
// still unreplayed and no reconnect scheduled.
func TestRestoreThatNeverSettlesFailsInsteadOfReportingSuccess(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	// The hub bumps the revision as each replayed identify arrives — before its
	// response, so the client cannot yet have finished the pass. Every pass
	// therefore ends with the session changed underneath it.
	h.mu.Lock()
	h.onRequest = func(req types.Request) {
		if req.Type != "identify" {
			return
		}
		c.mu.Lock()
		c.sess.rev++
		c.mu.Unlock()
	}
	h.mu.Unlock()

	c.mu.Lock()
	c.sess.identified = true
	c.sess.clientType = "mcp"
	c.mu.Unlock()

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	err := c.restoreOnto()
	if err == nil {
		t.Fatal("restoreOnto() = nil, want failure: oturum kararlı hâle gelmedi")
	}
	if !strings.Contains(err.Error(), "kararlı") {
		t.Errorf("hata = %v, want kararsız oturum teşhisi", err)
	}
	if c.isRestoring() {
		t.Error("başarısız restore sonrası kapı açık kaldı")
	}
}

// Codex review round 3, PR #113: a caller's newer join must cancel a departure
// that never reached the hub. Flushing it after the join would take the agent
// straight back out while sess.joined still said it was in.
func TestNewerJoinCancelsQueuedLeave(t *testing.T) {
	c := newTestClient(t, newFakeHub(t))

	c.mu.Lock()
	c.sess.pendingLeave = &pendingLeave{room: "r1", agent: "alice"}
	startGen, startJoinRev := c.sess.gen, c.sess.joinRev
	c.mu.Unlock()

	c.recordJoinIfCurrent(startGen, startJoinRev, "r1", "alice", "", true, true)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.pendingLeave != nil {
		t.Error("yeni join kuyruktaki leave'i iptal etmedi; replay sonrası agent odadan çıkarılırdı")
	}
}

// The mirror of it: a REPLAY is putting back a membership that predates the
// leave, which is exactly what the queued departure exists to undo.
func TestReplayedJoinDoesNotCancelQueuedLeave(t *testing.T) {
	c := newTestClient(t, newFakeHub(t))

	c.mu.Lock()
	c.sess.pendingLeave = &pendingLeave{room: "r1", agent: "alice"}
	startGen, startJoinRev := c.sess.gen, c.sess.joinRev
	c.mu.Unlock()

	c.recordJoinIfCurrent(startGen, startJoinRev, "r1", "alice", "", true, false)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.pendingLeave == nil {
		t.Error("replay kuyruktaki leave'i iptal etti; agent odada kalırdı")
	}
}

// Codex review round 3, PR #113: the stale-replay guard must key on membership,
// not on every session revision. An unrelated Subscribe completing concurrently
// would otherwise discard a good join result, leaving the socket in the room
// with nothing recorded to replay.
func TestConcurrentSubscribeDoesNotDiscardJoinResult(t *testing.T) {
	c := newTestClient(t, newFakeHub(t))

	c.mu.Lock()
	startGen, startJoinRev := c.sess.gen, c.sess.joinRev
	c.mu.Unlock()

	// Something unrelated advances the session while the join is in flight.
	c.rememberSubscriptions([]string{"other"}, true)

	c.recordJoinIfCurrent(startGen, startJoinRev, "r1", "alice", "", true, true)

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sess.joined || c.sess.joinRoom != "r1" {
		t.Errorf("join kaydı = (joined:%v room:%q), want kaydedilmiş r1", c.sess.joined, c.sess.joinRoom)
	}
}

// Codex review round 3, PR #113: a compensating leave the hub REFUSES leaves
// the agent in the room. Clearing the queue there would end restoration out of
// step with the hub, with nothing left to repair it.
func TestRefusedCompensatingLeaveStaysQueued(t *testing.T) {
	h := newFakeHub(t)
	h.mu.Lock()
	h.rejectLeave = true
	h.mu.Unlock()
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	c.mu.Lock()
	c.sess.pendingLeave = &pendingLeave{room: "r1", agent: "alice"}
	c.mu.Unlock()

	if err := c.flushPendingLeave(); err == nil {
		t.Fatal("flushPendingLeave() = nil, want refusal")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.pendingLeave == nil {
		t.Error("reddedilen telafi leave kuyruktan düştü")
	}
}

// Codex review round 3, PR #113: on a FAILED restore the gate must stay armed
// until the caller has dropped the socket. Clearing it as restoreOnto returns
// exposes a half-restored connection to ordinary traffic for as long as the
// caller takes to disown it.
func TestGateStaysArmedWhenRestoreFails(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	c.SetBootstrap(func(cl Bootstrap) error { return fmt.Errorf("kasıtlı hata") })

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// The supervisor arms the gate before dialling; mirror that here.
	c.setRestoring(true)
	if err := c.restoreOnto(); err == nil {
		t.Fatal("restoreOnto() = nil, want failure")
	}
	if !c.isRestoring() {
		t.Error("başarısız restore kapıyı açtı; soket düşürülene kadar kapalı kalmalı")
	}
	if _, err := c.ListRooms(); !errors.Is(err, errRestoreGate) {
		t.Errorf("ListRooms() = %v, want gated", err)
	}
}

// Codex review round 4, PR #113: a leave that pauses before its send can be
// overtaken by a newer join. Queueing it unconditionally afterwards would flush
// a stale departure right after the replay put the agent in.
func TestQueuedLeaveLosesToANewerJoin(t *testing.T) {
	c := newTestClient(t, newFakeHub(t))

	c.mu.Lock()
	atJoinRev := c.sess.joinRev
	c.sess.joinRev++ // araya giren yeni bir üyelik kaydı
	c.mu.Unlock()

	c.queueLeaveIfCurrent("r1", "alice", atJoinRev)

	c.mu.Lock()
	queued := c.sess.pendingLeave
	c.mu.Unlock()
	if queued != nil {
		t.Fatal("bayat leave kuyruğa alındı; replay agent'ı odaya koyduktan sonra çıkarırdı")
	}

	// Nothing newer: it must still queue.
	c.mu.Lock()
	current := c.sess.joinRev
	c.mu.Unlock()
	c.queueLeaveIfCurrent("r1", "alice", current)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.pendingLeave == nil {
		t.Error("güncel leave kuyruğa alınmadı")
	}
}

// Codex review round 4, PR #113: the hub holds the desktop's per-room
// configuration in memory only, and the desktop re-sends it just when the hub
// PROCESS restarts. A socket-level reconnect must put it back, or a room
// silently loses its manager gateway for the rest of the session.
func TestDesktopConfigurationIsReplayedOnReconnect(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := c.Identify("desktop", "", "", "tok"); err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if err := c.SetManager("r1", "yonetici"); err != nil {
		t.Fatalf("SetManager: %v", err)
	}
	if err := c.SetObservers("r1", []string{"gozcu"}); err != nil {
		t.Fatalf("SetObservers: %v", err)
	}

	h.dropAll()
	waitFor(t, "yeniden bağlanma", func() bool { return h.acceptedCount() >= 2 })
	waitFor(t, "yapılandırmanın replay edilmesi", func() bool {
		var manager, observers int
		for _, ty := range h.requestTypes() {
			switch ty {
			case "set_manager":
				manager++
			case "set_observers":
				observers++
			}
		}
		return manager >= 2 && observers >= 2
	})
}

// And a configuration the gate turned away is worth replaying for the same
// reason: the hub never saw it, while the desktop reported it as applied.
func TestConfigurationRefusedByGateIsReplayed(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h)

	release := make(chan struct{})
	started := make(chan struct{})
	c.SetBootstrap(func(cl Bootstrap) error {
		close(started)
		<-release
		return cl.Identify("desktop", "", "", "tok")
	})

	c.StartBackgroundConnect()
	<-started

	if err := c.SetManager("r1", "yonetici"); err == nil {
		t.Fatal("SetManager() during restore = nil, want gated")
	}
	close(release)

	waitFor(t, "kapının reddettiği yapılandırmanın replay edilmesi", func() bool {
		return slices.Contains(h.requestTypes(), "set_manager")
	})
	if got := h.acceptedCount(); got != 1 {
		t.Errorf("kabul edilen bağlantı = %d, want 1", got)
	}
}
