package hubclient

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
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
		reject := h.rejectJoin && req.Type == "join_room"
		h.mu.Unlock()

		resp := types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: json.RawMessage(`{"ok":true}`)}
		if reject {
			resp = types.Response{ID: req.ID, RequestType: req.Type, Success: false, Error: "manager rolü atanmadı"}
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
	c.SetBootstrap(func(cl *HubClient) {
		mu.Lock()
		calls++
		mu.Unlock()
		if err := cl.Identify("mcp", "alice", "r1", ""); err != nil {
			t.Errorf("Identify: %v", err)
		}
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

	// The hub is not up yet, so this fails at the transport.
	if _, err := c.JoinRoom("r1", "alice", ""); err == nil {
		t.Fatal("bağlantı yokken join başarılı görünmemeli")
	}

	addrMu.Lock()
	addr = h.url()
	addrMu.Unlock()

	waitFor(t, "hub açılınca join'in replay edilmesi", func() bool {
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
