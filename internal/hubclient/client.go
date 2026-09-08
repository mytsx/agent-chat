package hubclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"desktop/internal/types"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	defaultTimeout = 15 * time.Second
	maxReconnect   = 10 * time.Second
	// Reconnect pacing. Jittered so a hub restart does not bring every agent
	// back in lockstep as one wave; capped so a long outage still gets a retry
	// every half minute rather than backing off into never.
	defaultMinBackoff = 500 * time.Millisecond
	defaultMaxBackoff = 30 * time.Second
	// clientReadWait bounds a half-open socket. The hub pings every ~54s
	// (pingPeriod = pongWait*9/10, pongWait 60s), so silence past this means the
	// link is dead even when the OS has not noticed — without it readLoop can
	// block forever on a socket that will never deliver another byte, and the
	// agent goes quiet with no error anywhere.
	clientReadWait = 90 * time.Second
	// closeWriteTimeout bounds the websocket close-handshake write so Close() can't
	// block indefinitely on a wedged connection — which would leak goroutines for
	// callers that run Close asynchronously (monitorHub, shutdown).
	closeWriteTimeout = 2 * time.Second
	// maxRestorePasses bounds how many times a reconnect replays the session
	// because it changed while the previous pass ran. Two passes cover the real
	// case (one intent recorded behind the gate); the extra is slack, and the
	// bound is what keeps a caller that records on every attempt from spinning.
	maxRestorePasses = 3
	// maxPendingLeaveAttempts bounds how often a refused compensating leave may
	// fail a restore before it is abandoned.
	maxPendingLeaveAttempts = 3
)

// session is what the hub forgets when a socket dies and the client must put
// back without being asked: who this connection is, which room it joined, and
// which rooms it subscribed to. The hub removes an agent from the roster the
// instant its connection drops, so reconnecting alone would leave the agent
// silently outside the room (#98).
type session struct {
	identified bool
	clientType string
	identAgent string
	identRoom  string
	authToken  string

	joined bool
	// joinEstablished distinguishes a membership the hub actually granted from
	// one merely intended before any connection existed. Without it, a second
	// pre-connect join (say the caller corrected the room) was discarded because
	// the first pending intent already set joined.
	joinEstablished bool
	// gen advances on every deliberate membership change (a leave). A join that
	// was already in flight — notably one the supervisor is replaying — must not
	// resurrect an intent the caller cleared while it ran.
	gen       uint64
	joinRoom  string
	joinAgent string
	joinRole  string
	// managers and observers are the desktop's per-room configuration. The hub
	// holds it in memory only, and the desktop re-sends it just when the hub
	// PROCESS restarts — so a socket-level reconnect has to put it back or the
	// room silently loses its manager gateway.
	managers  map[string]string
	observers map[string][]string
	// configRev orders overlapping configuration calls for the SAME room. Two
	// SetManager calls can reach the hub as A then B while B's goroutine resumes
	// first; without a reservation A would then win the replay cache and the next
	// reconnect would silently revert the newer choice.
	configRev map[string]uint64
	// pendingLeave is a departure the gate refused; restoration performs it.
	pendingLeave *pendingLeave
	// joinRev counts membership changes only (join intent recorded, leave). The
	// stale-replay guard compares this rather than rev: an unrelated Subscribe
	// completing concurrently would otherwise discard a perfectly good join
	// result, leaving the socket in the room with nothing recorded to replay.
	joinRev uint64
	// rev counts every recorded change to this session. A replay compares it
	// across its own pass to notice intent that landed while it ran — which is
	// reachable precisely because the gate turns those calls away AFTER they
	// record.
	rev uint64

	subs []string
}

// pendingRequest is an in-flight RPC and the socket generation it was written
// on.
type pendingRequest struct {
	ch    chan *types.Response
	epoch uint64
}

// HubClient is a WebSocket client that connects to the Hub server.
//
// A dropped connection is recovered automatically: readLoop hands off to a
// supervisor that redials with jittered backoff until Close, then replays the
// session above. Before that, one read error took the client out permanently —
// conn went nil and every later Send failed for the life of the process.
type HubClient struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	pending map[string]pendingRequest
	// connEpoch numbers each socket, so a request can be tied to the connection
	// that carried it. A read loop finishing after a replacement was installed
	// must not fail requests written on the new socket.
	connEpoch uint64
	onEvent   func(types.Event)
	hubAddr   string
	logger    *log.Logger
	done      chan struct{}
	closed    bool

	minBackoff time.Duration
	maxBackoff time.Duration
	// resolveAddr re-resolves the hub address before each dial. The hub listens
	// on an OS-assigned port (Run(0)) and rewrites hub.port on every start, so a
	// restarted hub is at a DIFFERENT address — a supervisor that redialled the
	// remembered one would retry a dead port forever and never notice.
	resolveAddr func() (string, error)
	// bootstrap establishes the session on the FIRST successful connection.
	// Later reconnects replay what it recorded rather than running it again.
	bootstrap func(Bootstrap) error
	// restoring gates ordinary traffic while a freshly dialled socket is being
	// brought back to the session it left. Without it the raw socket became
	// visible before identify/join/subscribe replayed, and a concurrent tool
	// call raced onto an unidentified connection and got a protocol rejection
	// instead of its result.
	restoring bool
	// bootstrapped records that it has actually run to completion. Gating on
	// "is there any session state" instead was wrong: a join recorded before the
	// first connect (the background-connect window) counted as state, so the
	// bootstrap — the MCP server's only Identify call — was skipped and the
	// client stayed unidentified for the life of the process.
	bootstrapped bool
	sess         session
	// reconnectCh carries "the connection is gone" to the single long-lived
	// supervisor. Buffered by one and written non-blockingly, so a signal raised
	// while the supervisor is mid-work is never lost — unlike an edge-triggered
	// relaunch guarded by a flag, where a socket dying between the supervisor's
	// last check and its return left nobody scheduled to redial.
	reconnectCh chan struct{}
}

// New creates a new HubClient.
func New(hubAddr string, logger *log.Logger) *HubClient {
	c := &HubClient{
		pending:     make(map[string]pendingRequest),
		hubAddr:     hubAddr,
		logger:      logger,
		done:        make(chan struct{}),
		minBackoff:  defaultMinBackoff,
		maxBackoff:  defaultMaxBackoff,
		reconnectCh: make(chan struct{}, 1),
	}
	// One supervisor for the client's whole life. It idles until something
	// signals a lost connection, so constructing a client that never connects
	// costs nothing.
	go c.runSupervisor()
	return c
}

// errRestoreGate is returned to an ordinary caller whose request was held back
// while a reconnect replayed the session. It means the request was NOT written.
var errRestoreGate = errors.New("hub oturumu geri yükleniyor")

// pendingLeave is a leave_room the gate refused, to be performed once the
// session is up again.
type pendingLeave struct {
	room  string
	agent string
	// attempts counts refusals. A refusal that never stops being a refusal — a
	// name the hub will not accept, say — would otherwise fail every restore
	// forever, and the client would reconnect in a loop instead of working.
	attempts int
}

// ErrInvalidHubPortConfig marks a discovery failure that cannot resolve itself.
// AGENT_CHAT_HUB_PORT is fixed for the lifetime of the process and takes
// priority over hub.port, so retrying a malformed value is an infinite loop by
// construction: the MCP server would serve stdio while every tool call reports
// "connecting to hub". A missing or momentarily empty hub.port is the opposite —
// the desktop writes it moments later — and stays transient.
var ErrInvalidHubPortConfig = errors.New("invalid hub port configuration")

// DiscoverHubAddr reads the hub port from the data directory.
func DiscoverHubAddr(dataDir string) (string, error) {
	// Check env var override first
	if port := os.Getenv("AGENT_CHAT_HUB_PORT"); port != "" {
		if err := validateHubPort("AGENT_CHAT_HUB_PORT", port); err != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalidHubPortConfig, err)
		}
		return fmt.Sprintf("ws://localhost:%s/ws", port), nil
	}

	portPath := filepath.Join(dataDir, "hub.port")
	data, err := os.ReadFile(portPath)
	if err != nil {
		return "", fmt.Errorf("hub.port not found: %w", err)
	}

	port := strings.TrimSpace(string(data))
	if err := validateHubPort(portPath, port); err != nil {
		return "", err
	}
	return fmt.Sprintf("ws://localhost:%s/ws", port), nil
}

func validateHubPort(source, port string) error {
	// Trim here (not just at the file-read site) so the AGENT_CHAT_HUB_PORT env var —
	// which reaches this function untrimmed — tolerates stray surrounding whitespace.
	port = strings.TrimSpace(port)
	if port == "" {
		return fmt.Errorf("%s is empty", source)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid hub port from %s: %q (must be 1-65535)", source, port)
	}
	return nil
}

// SetAddrResolver installs a function consulted before every dial, so the
// client follows the hub across restarts instead of pinning the address it was
// constructed with. Nil (the default) keeps using that address.
func (c *HubClient) SetAddrResolver(fn func() (string, error)) {
	c.mu.Lock()
	c.resolveAddr = fn
	c.mu.Unlock()
}

// Bootstrap is the restricted client view handed to the bootstrap callback.
//
// It exists so the callback can build the session while ordinary traffic is
// still held back: its calls are part of bringing the connection up, so they
// pass the reconnect gate that everything else waits behind. Taking the client
// itself would have made that distinction impossible — there is no way to tell
// a bootstrap's Identify from an unrelated caller's.
type Bootstrap struct{ c *HubClient }

// Identify identifies the connection being established.
func (b Bootstrap) Identify(clientType, agentName, room, authToken string) error {
	return b.c.identify(clientType, agentName, room, authToken, true)
}

// JoinRoom joins a room on the connection being established.
func (b Bootstrap) JoinRoom(room, agentName, role string) (*types.Response, error) {
	return b.c.joinRoom(room, agentName, role, true)
}

// Subscribe subscribes to room events on the connection being established.
func (b Bootstrap) Subscribe(rooms []string) error {
	return b.c.subscribe(rooms, true)
}

// SetBootstrap installs a callback run on the first successful connection, to
// establish the session (identify, join, subscribe). It is NOT run again on
// reconnect: by then the calls it made are recorded as session state and
// replayed verbatim, so running it twice would just repeat them.
//
// It exists because a background connect gives the caller no inline moment to
// perform that setup.
func (c *HubClient) SetBootstrap(fn func(Bootstrap) error) {
	c.mu.Lock()
	c.bootstrap = fn
	c.mu.Unlock()
}

// runBootstrap performs first-connection setup, once. A failure is returned so
// the caller can treat it like any other incomplete session and retry on the
// next connection rather than leaving the client half-established.
func (c *HubClient) runBootstrap() error {
	c.mu.Lock()
	fn, done := c.bootstrap, c.bootstrapped
	c.mu.Unlock()
	if fn == nil || done {
		return nil
	}
	if err := fn(Bootstrap{c: c}); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	c.mu.Lock()
	c.bootstrapped = true
	c.mu.Unlock()
	return nil
}

// StartBackgroundConnect connects without blocking the caller, retrying until
// it succeeds or the client is closed.
//
// Startup must never wait on the hub: an MCP server that does not answer its
// host's initialize handshake in time is marked failed and never retried, and
// the desktop may not have written hub.port yet. Serve first, connect when the
// hub shows up.
func (c *HubClient) StartBackgroundConnect() {
	c.notifyDisconnected()
}

// Connect establishes the WebSocket connection to the hub.
func (c *HubClient) Connect() error {
	c.mu.Lock()
	addr, resolve := c.hubAddr, c.resolveAddr
	c.mu.Unlock()
	if resolve != nil {
		resolved, err := resolve()
		if err != nil {
			return fmt.Errorf("hub adresi çözülemedi: %w", err)
		}
		addr = resolved
		c.mu.Lock()
		c.hubAddr = addr
		c.mu.Unlock()
	}

	conn, _, err := websocket.DefaultDialer.Dial(addr, nil)
	if err != nil {
		return fmt.Errorf("hub connect: %w", err)
	}

	// Treat prolonged silence as a dead link. The hub pings on a schedule, so a
	// healthy connection always refreshes this deadline well before it expires.
	_ = conn.SetReadDeadline(time.Now().Add(clientReadWait))
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(clientReadWait))
		// Keep gorilla's default behaviour of answering the ping.
		err := conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(closeWriteTimeout))
		if err == websocket.ErrCloseSent {
			return nil
		}
		return err
	})

	c.mu.Lock()
	closed := c.closed
	epoch := c.connEpoch + 1
	if !closed {
		c.conn = conn
		c.connEpoch = epoch
	}
	c.mu.Unlock()
	if closed {
		conn.Close()
		return fmt.Errorf("hub client closed")
	}

	go c.readLoop(conn, epoch)

	c.logger.Printf("Connected to hub at %s", addr)
	return nil
}

// nextBackoff returns the delay to wait before the next redial: exponential
// from minBackoff, capped at maxBackoff, with +/-50% jitter so agents that all
// lost the same hub do not come back as one synchronized wave.
func (c *HubClient) nextBackoff(prev time.Duration) time.Duration {
	base := prev * 2
	if base < c.minBackoff {
		base = c.minBackoff
	}
	if base > c.maxBackoff {
		base = c.maxBackoff
	}
	jittered := time.Duration(float64(base) * (0.5 + rand.Float64()))
	if jittered > c.maxBackoff {
		jittered = c.maxBackoff
	}
	if jittered <= 0 {
		jittered = time.Millisecond
	}
	return jittered
}

// notifyDisconnected asks the supervisor to (re)establish the connection.
// Non-blocking: the channel holds one pending request, which is all that is
// needed — "reconnect" is not a queue, it is a level.
func (c *HubClient) notifyDisconnected() {
	select {
	case c.reconnectCh <- struct{}{}:
	default:
	}
}

// runSupervisor owns reconnection for the client's whole life.
//
// It is level-triggered on purpose. The earlier design relaunched a supervisor
// per disconnect and guarded it with a "supervising" flag, which had a
// check-then-act hole: a socket dying between the supervisor's last check and
// its return had its wake-up swallowed as a duplicate, leaving the client
// disconnected with nobody scheduled to redial — #98's symptom, reintroduced.
// A buffered signal cannot be lost that way.
func (c *HubClient) runSupervisor() {
	for {
		select {
		case <-c.done:
			return
		case <-c.reconnectCh:
		}
		c.reconnectUntilLive()
	}
}

// reconnectUntilLive dials and restores until the client holds a live session
// or is closed. The first attempt is immediate; only failures back off.
func (c *HubClient) reconnectUntilLive() {
	var backoff time.Duration
	for {
		select {
		case <-c.done:
			return
		default:
		}
		if c.isConnected() {
			return // someone else got there first
		}

		// The gate is armed BEFORE the dial, not after it. Arming it once the
		// dial returned left a window where the socket was already installed and
		// the gate still open, so a tool call scheduled in between reached the
		// hub before identify/join replayed — the very rejection this exists to
		// prevent. While the dial is in flight there is no socket anyway, so
		// ordinary callers see the same transient answer either way.
		c.setRestoring(true)
		if err := c.Connect(); err != nil {
			c.setRestoring(false)
			c.logger.Printf("Hub connect failed, retrying: %v", err)
		} else if err := c.restoreOnto(); err != nil {
			// The socket is up but the hub would not have us back. Drop it and
			// try again rather than pretending we are joined. The gate is still
			// armed here on purpose — it comes down only once the half-restored
			// socket is gone.
			c.logger.Printf("Hub session restore failed, retrying: %v", err)
			c.dropConn()
			c.setRestoring(false)
		} else if c.isConnected() {
			c.logger.Printf("Hub connection restored")
			return
		}

		backoff = c.nextBackoff(backoff)
		select {
		case <-time.After(backoff):
		case <-c.done:
			return
		}
	}
}

// isConnected reports whether a socket is currently installed.
func (c *HubClient) isConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// dropConn tears down the current socket without closing the client, so the
// supervisor can try again from a clean state.
func (c *HubClient) dropConn() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// dropConnIf tears down conn, but only if it is still the socket the client
// holds: a replacement dial may already have installed a newer one, and closing
// that would kill a healthy connection over a dead socket's error.
//
// The read loop's teardown does the rest (fail pending, wake the supervisor)
// once the close unblocks it; the extra signal here just means the supervisor
// does not have to wait for that scheduling.
func (c *HubClient) dropConnIf(conn *websocket.Conn) {
	c.mu.Lock()
	current := c.conn == conn
	if current {
		c.conn = nil
	}
	closed := c.closed
	c.mu.Unlock()
	if !current {
		return
	}
	conn.Close()
	if !closed {
		c.notifyDisconnected()
	}
}

// isRestoring reports whether the reconnect gate is armed.
func (c *HubClient) isRestoring() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.restoring
}

func (c *HubClient) setRestoring(v bool) {
	c.mu.Lock()
	c.restoring = v
	c.mu.Unlock()
}

// restoreOnto replays the session onto the socket the dial just installed. The
// caller arms the gate; this always leaves it closed.
//
// It repeats while the session changed underneath it. A JoinRoom or Subscribe
// turned away by the gate still RECORDS its intent before failing, and a replay
// that had already taken its snapshot would not carry it: restore would report
// success, the supervisor would stop, and the client would sit connected
// without that membership until the next disconnect — a startup join landing in
// this window would leave the agent outside the room for the whole session.
// Bounded, so a caller recording on every attempt cannot spin here; whatever the
// last pass missed is replayed by the next reconnect.
//
// Only the supervisor's path is gated. The desktop connects inline
// (ConnectWithRetry then Identify on the same goroutine) and has no concurrent
// traffic to hold back.
// The gate is cleared only on SUCCESS. On failure it stays armed and the caller
// clears it after dropping the socket: the connection is half-restored at that
// moment, so an ordinary request slipping in between would reach the hub on a
// session that was never established.
func (c *HubClient) restoreOnto() error {
	for attempt := 0; attempt < maxRestorePasses; attempt++ {
		c.mu.Lock()
		before := c.sess.rev
		c.mu.Unlock()

		if err := c.afterConnect(); err != nil {
			return err
		}

		c.mu.Lock()
		unchanged := c.sess.rev == before
		c.mu.Unlock()
		if unchanged {
			c.setRestoring(false)
			return nil
		}
	}
	// Still moving after the last pass: something recorded is not on the wire.
	// Reporting success here would stop the supervisor with the gate open and no
	// reconnect scheduled, so the unreplayed join or subscription would wait for
	// a disconnect that may never come. Failing drops the socket and redials.
	return fmt.Errorf("oturum %d turda kararlı hâle gelmedi", maxRestorePasses)
}

// afterConnect brings a freshly dialled socket up to the session the caller
// expects, whichever path got here.
//
// Both paths need BOTH steps: an agent can record a join before any connection
// exists (the background-connect window), so even a first-try success has state
// to replay — running only the bootstrap there silently dropped that join.
func (c *HubClient) afterConnect() error {
	if err := c.restoreSession(); err != nil {
		return err
	}
	// Independent of restoreSession: a client can have a recorded join (made
	// before any connection existed) and still never have identified.
	if err := c.runBootstrap(); err != nil {
		return err
	}
	return c.flushPendingLeave()
}

// queueLeaveIfCurrent queues a departure the gate refused, unless the membership
// moved on while that call was in flight.
//
// A join recorded in that window cannot be cancelled by recordJoinIfCurrent — it
// ran BEFORE this queueing — so queueing unconditionally would flush a stale
// departure right after the replay had put the agent in, while sess.joined still
// said it was there.
func (c *HubClient) queueLeaveIfCurrent(room, agentName string, atJoinRev uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.joinRev != atJoinRev {
		return
	}
	c.sess.pendingLeave = &pendingLeave{room: room, agent: agentName}
	c.sess.rev++
}

// flushPendingLeave performs a departure the gate refused, after the session is
// back up — the replay may have rejoined the agent the caller had just taken
// out, and only an actual leave_room reconciles that.
func (c *HubClient) flushPendingLeave() error {
	c.mu.Lock()
	p := c.sess.pendingLeave
	c.mu.Unlock()
	if p == nil {
		return nil
	}

	data, _ := json.Marshal(map[string]string{"agent_name": p.agent})
	resp, err := c.send(types.Request{Type: "leave_room", Room: p.room, Data: data}, true)
	if err != nil {
		// Transport failure: keep it queued for the next connection.
		return err
	}
	// A protocol refusal (a name that does not match the connection's identity,
	// say) means the agent may still be in the room. Keep it queued and fail the
	// restore rather than reporting a session that is out of step with the hub.
	// An agent the hub no longer holds answers with success, so this branch does
	// not fire for the already-absent case.
	if resp != nil && !resp.Success {
		c.mu.Lock()
		giveUp := false
		if q := c.sess.pendingLeave; q == p {
			p.attempts++
			if p.attempts >= maxPendingLeaveAttempts {
				c.sess.pendingLeave = nil
				giveUp = true
			}
		}
		c.mu.Unlock()
		if giveUp {
			// Stop failing the restore over it: a session that can never come up
			// is worse than one whose departure the hub refused. Loud, because
			// the hub and the client now disagree about the room.
			c.logger.Printf("Kuyruktaki leave_room %d kez reddedildi, vazgeçiliyor (oda=%s agent=%s): %s",
				p.attempts, p.room, p.agent, resp.Error)
			return nil
		}
		return fmt.Errorf("kuyruktaki leave_room reddedildi: %s", resp.Error)
	}
	c.mu.Lock()
	if c.sess.pendingLeave == p {
		c.sess.pendingLeave = nil
	}
	c.mu.Unlock()
	return nil
}

// restoreSession replays identity, membership and subscriptions onto a fresh
// connection. Order matters: the hub binds an agent name at identify/join and
// rejects mismatches afterwards.
func (c *HubClient) restoreSession() error {
	c.mu.Lock()
	s := c.sess
	subs := append([]string(nil), c.sess.subs...)
	c.mu.Unlock()

	if s.identified {
		if err := c.identify(s.clientType, s.identAgent, s.identRoom, s.authToken, true); err != nil {
			return fmt.Errorf("identify: %w", err)
		}
	}
	if s.joined {
		// A protocol-level rejection returns a nil error with Success=false, so
		// checking err alone would report the session restored while the agent
		// sat outside the room — with no further transport failure to trigger
		// another attempt.
		resp, err := c.joinRoom(s.joinRoom, s.joinAgent, s.joinRole, true)
		if err != nil {
			return fmt.Errorf("join_room: %w", err)
		}
		if err := ensureSuccess("join_room", resp); err != nil {
			return err
		}
	}
	if len(subs) > 0 {
		if err := c.subscribe(subs, true); err != nil {
			return fmt.Errorf("subscribe: %w", err)
		}
	}

	// Configuration last: it is keyed by room and independent of membership,
	// but it must not run before identify — the hub authorizes these on the
	// desktop's identity.
	c.mu.Lock()
	managers := maps.Clone(c.sess.managers)
	observers := maps.Clone(c.sess.observers)
	c.mu.Unlock()
	for room, agent := range managers {
		if err := c.setManager(room, agent, true); err != nil {
			return fmt.Errorf("set_manager: %w", err)
		}
	}
	for room, list := range observers {
		if err := c.setObservers(room, list, true); err != nil {
			return fmt.Errorf("set_observers: %w", err)
		}
	}
	return nil
}

// Close closes the connection.
func (c *HubClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	c.closed = true
	close(c.done)

	if c.conn != nil {
		// Bound the close-handshake write: WriteMessage carries no deadline of its own,
		// so a wedged connection (full send buffer) could block Close indefinitely. The
		// deadline makes it fail fast; conn.Close() below tears the socket down regardless
		// of the handshake outcome.
		_ = c.conn.SetWriteDeadline(time.Now().Add(closeWriteTimeout))
		c.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		c.conn.Close()
	}

	// Unblock any pending requests
	for id, p := range c.pending {
		close(p.ch)
		delete(c.pending, id)
	}
}

// failPending releases every in-flight request because the connection carrying
// them is gone. Callers see a prompt "connection lost" instead of waiting out
// the request timeout for a reply that can never arrive.
func (c *HubClient) failPending(epoch uint64) {
	c.mu.Lock()
	var doomed []chan *types.Response
	for id, p := range c.pending {
		if p.epoch != epoch {
			continue // written on a different socket; not this loop's to fail
		}
		doomed = append(doomed, p.ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	for _, ch := range doomed {
		close(ch)
	}
}

// SetEventHandler sets the function called when an event is received.
func (c *HubClient) SetEventHandler(fn func(types.Event)) {
	c.onEvent = fn
}

func (c *HubClient) forgetPending(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func ensureSuccess(operation string, resp *types.Response) error {
	// Send returns (non-nil resp, nil err) on success and (nil, err) on failure, so a
	// nil resp here should be unreachable — guard anyway so a future transport change
	// surfaces a clear error instead of a nil-pointer panic.
	if resp == nil {
		return fmt.Errorf("%s failed: hub'dan boş yanıt", operation)
	}
	if resp.Success {
		return nil
	}
	return fmt.Errorf("%s failed: %s", operation, resp.Error)
}

func decodeSuccessData[T any](operation string, resp *types.Response) (T, error) {
	var data T
	if err := ensureSuccess(operation, resp); err != nil {
		return data, err
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return data, err
	}
	return data, nil
}

func (c *HubClient) sendExpectSuccess(operation string, req types.Request) error {
	return c.sendExpectSuccessGated(operation, req, false)
}

func (c *HubClient) sendExpectSuccessGated(operation string, req types.Request, bypassGate bool) error {
	resp, err := c.send(req, bypassGate)
	if err != nil {
		return err
	}
	return ensureSuccess(operation, resp)
}

// Send sends a request and waits for a response (synchronous RPC).
func (c *HubClient) Send(req types.Request) (*types.Response, error) {
	return c.send(req, false)
}

// send is Send with the reconnect gate optionally bypassed. Only the calls that
// BUILD the session (identify, join, subscribe — replay and bootstrap alike)
// bypass it; everything else waits for the session to be ready, because a fresh
// socket that has not identified or rejoined yet answers ordinary requests with
// a protocol rejection rather than the result the caller expects.
func (c *HubClient) send(req types.Request, bypassGate bool) (*types.Response, error) {
	if req.ID == "" {
		req.ID = uuid.New().String()
	}

	ch := make(chan *types.Response, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("hub client closed")
	}
	if c.restoring && !bypassGate {
		c.mu.Unlock()
		// Same shape as the not-connected answer: transient, and the supervisor
		// is already working on it. Typed, because a caller that must reconcile
		// afterwards (LeaveRoom) has to tell "never written" from an ambiguous
		// transport failure.
		return nil, fmt.Errorf("%w (yeniden bağlanılıyor)", errRestoreGate)
	}
	// Tie the request to the socket it is about to be written on, so only that
	// socket's death fails it.
	c.pending[req.ID] = pendingRequest{ch: ch, epoch: c.connEpoch}
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		c.forgetPending(req.ID)
		// The supervisor is redialling in the background, so this is a
		// transient state rather than the terminal one it used to be.
		return nil, fmt.Errorf("not connected to hub (yeniden bağlanılıyor)")
	}

	data, err := json.Marshal(req)
	if err != nil {
		c.forgetPending(req.ID)
		return nil, err
	}

	c.mu.Lock()
	// Bound the write so a wedged connection can't block here while holding c.mu —
	// that would also stall Close() (it needs the same mutex), defeating Close's own
	// write deadline. On timeout the write errors, the mutex releases, and the RPC
	// fails like any other write error.
	_ = conn.SetWriteDeadline(time.Now().Add(defaultTimeout))
	err = conn.WriteMessage(websocket.TextMessage, data)
	c.mu.Unlock()
	if err != nil {
		c.forgetPending(req.ID)
		// A write can see a half-open connection before the read loop does (the
		// read side only learns of it when the 90s deadline expires). Returning
		// the error alone left the dead socket installed, so isConnected() stayed
		// true, the supervisor stayed asleep, and every following RPC went to the
		// same unusable connection until that deadline. Tear it down here.
		c.dropConnIf(conn)
		return nil, fmt.Errorf("hub write: %w", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			// Closed rather than answered: either the client is shutting down or
			// the connection carrying this request died.
			return nil, fmt.Errorf("hub bağlantısı yanıt beklenirken kesildi")
		}
		return resp, nil
	case <-time.After(defaultTimeout):
		c.forgetPending(req.ID)
		return nil, fmt.Errorf("hub request timeout (id=%s type=%s)", req.ID, req.Type)
	case <-c.done:
		c.forgetPending(req.ID)
		return nil, fmt.Errorf("hub client closed")
	}
}

// readLoop reads from the socket it was started for.
//
// It takes the connection as a parameter rather than reading c.conn: a
// replacement dial can install a new socket while this loop is still winding
// down, and touching the shared field would then read from — or clear — the
// wrong one.
func (c *HubClient) readLoop(conn *websocket.Conn, epoch uint64) {
	defer func() {
		c.mu.Lock()
		// Only disown the socket if it is still the current one; a newer dial
		// may already have replaced it.
		if c.conn == conn {
			c.conn = nil
		}
		closed := c.closed
		c.mu.Unlock()
		// Wake everything waiting on a response from THIS socket. Without it an
		// RPC in flight when the connection dropped sat out the full 15s
		// timeout — the agent simply hung, which is the very symptom this work
		// exists to remove. Scoped by epoch so a late-finishing old loop cannot
		// fail requests already written on the replacement socket.
		c.failPending(epoch)

		// A read error used to end the client's life. Signal the supervisor
		// instead — unless the teardown was deliberate.
		if !closed {
			c.notifyDisconnected()
		}
	}()

	for {
		select {
		case <-c.done:
			return
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				c.logger.Printf("Hub read error: %v", err)
			}
			return
		}

		// Try to determine message type
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(message, &raw); err != nil {
			continue
		}

		// Check if it's an event (has "event" field)
		if _, hasEvent := raw["event"]; hasEvent {
			var event types.Event
			if err := json.Unmarshal(message, &event); err == nil && event.Type == "event" {
				if c.onEvent != nil {
					c.onEvent(event)
				}
				continue
			}
		}

		// Otherwise it's a response
		var resp types.Response
		if err := json.Unmarshal(message, &resp); err != nil {
			continue
		}

		c.mu.Lock()
		if p, ok := c.pending[resp.ID]; ok {
			delete(c.pending, resp.ID)
			c.mu.Unlock()
			p.ch <- &resp
		} else {
			c.mu.Unlock()
		}
	}
}

// --- Convenience methods ---

// Identify sends an identify request.
func (c *HubClient) Identify(clientType, agentName, room, authToken string) error {
	return c.identify(clientType, agentName, room, authToken, false)
}

func (c *HubClient) identify(clientType, agentName, room, authToken string, bypassGate bool) error {
	data, _ := json.Marshal(map[string]string{
		"client_type": clientType,
		"agent_name":  agentName,
		"room":        room,
		"auth_token":  authToken,
	})
	if err := c.sendExpectSuccessGated("identify", types.Request{Type: "identify", Data: data}, bypassGate); err != nil {
		return err
	}
	c.rememberIdentify(clientType, agentName, room, authToken, !bypassGate)
	return nil
}

// rememberIdentify and its siblings record what a reconnect has to put back.
// Only successful calls are remembered: replaying a request the hub rejected
// would just fail the same way on every retry.
// external distinguishes a caller's own call from the supervisor replaying what
// was already recorded. Only the former advances sess.rev: a replay bumping it
// would make every restore look like it had raced something and run its passes
// out for nothing.
func (c *HubClient) rememberIdentify(clientType, agentName, room, authToken string, external bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sess.identified = true
	c.sess.clientType = clientType
	c.sess.identAgent = agentName
	c.sess.identRoom = room
	c.sess.authToken = authToken
	if external {
		c.sess.rev++
	}
}

// Subscribe subscribes to room events.
func (c *HubClient) Subscribe(rooms []string) error {
	return c.subscribe(rooms, false)
}

func (c *HubClient) subscribe(rooms []string, bypassGate bool) error {
	data, _ := json.Marshal(map[string][]string{"rooms": rooms})
	resp, err := c.send(types.Request{Type: "subscribe", Data: data}, bypassGate)

	// Same rule as JoinRoom: a request the hub never saw is worth replaying, a
	// request it rejected is not. Without this, a Subscribe issued while the
	// supervisor is between sockets is lost — CreateTeam only logs that error,
	// so the desktop stays connected but receives no events for the new team
	// until an explicit resubscribe or a restart.
	if err != nil || (resp != nil && resp.Success) {
		c.rememberSubscriptions(rooms, !bypassGate)
	}
	return err
}

// rememberSubscriptions accumulates rooms across calls: the desktop subscribes
// incrementally as teams open, and a reconnect has to restore all of them.
func (c *HubClient) rememberSubscriptions(rooms []string, external bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, room := range rooms {
		if !slices.Contains(c.sess.subs, room) {
			c.sess.subs = append(c.sess.subs, room)
			if external {
				c.sess.rev++
			}
		}
	}
}

// SetManager configures the allowed manager agent for a room.
func (c *HubClient) SetManager(room, managerAgent string) error {
	return c.setManager(room, managerAgent, false)
}

func (c *HubClient) setManager(room, managerAgent string, bypassGate bool) error {
	myRev := c.reserveConfig("manager:"+room, !bypassGate)
	data, _ := json.Marshal(map[string]string{"manager_agent": managerAgent})
	resp, err := c.send(types.Request{Type: "set_manager", Room: room, Data: data}, bypassGate)

	// Same rule as Subscribe, and for the same reason: the hub forgets this
	// configuration when the socket dies, and the desktop only re-sends it when
	// the hub PROCESS restarts. A request the hub never saw — a transport
	// failure, or one the restore gate turned away — is therefore worth
	// replaying; one it rejected is not. Without this a manager set during a
	// reconnect was reported as applied while the hub kept the old routing
	// configuration for the rest of the session.
	// Only a CALLER's own configuration is recorded. A replay is putting back
	// what is already in the map, and a newer value recorded behind the gate
	// while this replay was in flight would be overwritten by the older one —
	// the next pass would then replay the stale value again and settle on it.
	if !bypassGate && (err != nil || (resp != nil && resp.Success)) {
		c.rememberManager(room, managerAgent, myRev)
	}
	if err != nil {
		return err
	}
	return ensureSuccess("set_manager", resp)
}

// reserveConfig claims the next revision for a configuration key, so an outcome
// recorded later can tell whether it is still the newest call. Replays do not
// reserve: they carry no new intent.
func (c *HubClient) reserveConfig(key string, external bool) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.configRev == nil {
		c.sess.configRev = map[string]uint64{}
	}
	if external {
		c.sess.configRev[key]++
	}
	return c.sess.configRev[key]
}

func (c *HubClient) configIsCurrentLocked(key string, myRev uint64) bool {
	return c.sess.configRev[key] == myRev
}

func (c *HubClient) rememberManager(room, managerAgent string, myRev uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.configIsCurrentLocked("manager:"+room, myRev) {
		return
	}
	if c.sess.managers == nil {
		c.sess.managers = map[string]string{}
	}
	c.sess.managers[room] = managerAgent
	c.sess.rev++
}

// SetObservers configures the desktop-authorized observer set for a room (#17).
// The hub rejects join_room with role "observer" for any agent not in this set.
func (c *HubClient) SetObservers(room string, observers []string) error {
	return c.setObservers(room, observers, false)
}

func (c *HubClient) setObservers(room string, observers []string, bypassGate bool) error {
	myRev := c.reserveConfig("observers:"+room, !bypassGate)
	data, _ := json.Marshal(map[string][]string{"observers": observers})
	resp, err := c.send(types.Request{Type: "set_observers", Room: room, Data: data}, bypassGate)
	if !bypassGate && (err != nil || (resp != nil && resp.Success)) {
		c.rememberObservers(room, observers, myRev)
	}
	if err != nil {
		return err
	}
	return ensureSuccess("set_observers", resp)
}

func (c *HubClient) rememberObservers(room string, observers []string, myRev uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.configIsCurrentLocked("observers:"+room, myRev) {
		return
	}
	if c.sess.observers == nil {
		c.sess.observers = map[string][]string{}
	}
	c.sess.observers[room] = append([]string(nil), observers...)
	c.sess.rev++
}

// DeleteRoom removes an orphan room's state from the hub (desktop-authorized).
func (c *HubClient) DeleteRoom(room string) error {
	if err := c.sendExpectSuccess("delete_room", types.Request{Type: "delete_room", Room: room}); err != nil {
		return err
	}
	// Drop every trace of the room from the replay state. The hub creates a room
	// on demand, so a reconnect replaying its manager or observers would
	// resurrect the one this call just deleted — and the deletion would not
	// survive a socket blip.
	c.mu.Lock()
	delete(c.sess.managers, room)
	delete(c.sess.observers, room)
	delete(c.sess.configRev, "manager:"+room)
	delete(c.sess.configRev, "observers:"+room)
	c.sess.subs = slices.DeleteFunc(c.sess.subs, func(s string) bool { return s == room })
	c.sess.rev++
	c.mu.Unlock()
	return nil
}

// JoinRoom joins a room.
func (c *HubClient) JoinRoom(room, agentName, role string) (*types.Response, error) {
	return c.joinRoom(room, agentName, role, false)
}

func (c *HubClient) joinRoom(room, agentName, role string, bypassGate bool) (*types.Response, error) {
	data, _ := json.Marshal(map[string]string{
		"agent_name": agentName,
		"role":       role,
	})
	c.mu.Lock()
	startGen := c.sess.gen
	// A caller's join RESERVES the membership revision before it is sent, rather
	// than advancing it when the outcome is recorded. Otherwise a replay that
	// records first would make this later call look stale and discard it — and
	// because a replay's record does not advance sess.rev, restoration would see
	// no change, open the gate on the old membership, and lose the caller's
	// intent for good.
	if !bypassGate {
		c.sess.joinRev++
	}
	startJoinRev := c.sess.joinRev
	c.mu.Unlock()

	resp, err := c.send(types.Request{Type: "join_room", Room: room, Data: data}, bypassGate)

	succeeded := err == nil && resp != nil && resp.Success
	// Three outcomes, three rules:
	//   success            → this is the session
	//   transport failure  → keep as INITIAL intent only (see below); the hub
	//                        never saw it, so it is worth replaying
	//   protocol rejection → record nothing; replaying it would fail the same
	//                        way on every reconnect, forever
	unreached := err != nil

	c.recordJoinIfCurrent(startGen, startJoinRev, room, agentName, role, succeeded, !bypassGate, unreached)
	return resp, err
}

// recordJoinIfCurrent stores the join outcome unless a deliberate leave landed
// while the request was in flight.
//
// A leave that arrives mid-join wins: recording the intent afterwards would
// silently rejoin the agent on the next reconnect, undoing a departure the
// caller asked for. This is reachable through the supervisor's replay, which
// runs concurrently with user calls.
func (c *HubClient) recordJoinIfCurrent(startGen, startJoinRev uint64, room, agentName, role string, succeeded, external bool, unreached ...bool) {
	transportFailed := len(unreached) > 0 && unreached[0]

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.gen != startGen {
		return
	}
	// Nor may it overwrite an intent recorded while it was in flight. A replay
	// of an unestablished join for room A can land after the caller corrected
	// itself to room B (recorded behind the gate); writing A back would make the
	// next pass replay A and lose B for good.
	if c.sess.joinRev != startJoinRev {
		return
	}
	// A failed join is kept only when nothing is established yet: with a
	// background connect the agent can call join_room before the first dial
	// lands, and dropping that would leave it outside the room. But letting a
	// failed join for room B overwrite an established membership in room A would
	// silently move the client, and its later operations — still aimed at A —
	// would be rejected as wrong-room.
	if succeeded || (transportFailed && !c.sess.joinEstablished) {
		c.sess.joined = true
		c.sess.joinEstablished = succeeded
		c.sess.joinRoom = room
		c.sess.joinAgent = agentName
		c.sess.joinRole = role
		if external {
			c.sess.rev++
		}
		// A newer join from the CALLER supersedes a departure that never reached
		// the hub; flushing it afterwards would take the agent straight back out
		// while sess.joined still said otherwise. A replay must not cancel it:
		// the replay is putting back a membership that predates the leave, which
		// is exactly what the queued departure exists to undo.
		if p := c.sess.pendingLeave; external && p != nil && p.room == room && p.agent == agentName {
			c.sess.pendingLeave = nil
		}
	}
}

// SendMessage sends a message to a room.
func (c *HubClient) SendMessage(room, from, to, content string, expectsReply bool, priority string) (*types.Response, error) {
	data, _ := json.Marshal(map[string]any{
		"from":          from,
		"to":            to,
		"content":       content,
		"expects_reply": expectsReply,
		"priority":      priority,
	})
	return c.Send(types.Request{Type: "send_message", Room: room, Data: data})
}

// GetMessages reads messages from a room.
func (c *HubClient) GetMessages(room, agentName string, sinceID, limit int, unreadOnly bool) (*types.Response, error) {
	data, _ := json.Marshal(map[string]any{
		"agent_name":  agentName,
		"since_id":    sinceID,
		"limit":       limit,
		"unread_only": unreadOnly,
	})
	return c.Send(types.Request{Type: "get_messages", Room: room, Data: data})
}

// GetAllMessages reads all messages from a room.
func (c *HubClient) GetAllMessages(room string, sinceID, limit int) (*types.Response, error) {
	data, _ := json.Marshal(map[string]any{
		"since_id": sinceID,
		"limit":    limit,
	})
	return c.Send(types.Request{Type: "get_all_messages", Room: room, Data: data})
}

// GetSummary reads the newest saved per-session summary for a room (#29).
func (c *HubClient) GetSummary(room string) (*types.Response, error) {
	return c.Send(types.Request{Type: "read_summary", Room: room})
}

// logMessageData builds the log_message RPC payload. The timestamp is the
// delivery-moment timestamp produced app-side; the hub uses it as the message's
// ordering key instead of stamping at RPC-arrival time (#58).
func logMessageData(to, content, timestamp string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"to": to, "content": content, "timestamp": timestamp})
	return data
}

// LogMessage records an out-of-band human→agent prompt in the room transcript
// (#29). The hub stamps the sender identity (the user sentinel); the caller
// supplies the recipient ("all" or an agent name), the prompt content, and the
// delivery-moment timestamp (#58 — pass "" to let the hub stamp it).
func (c *HubClient) LogMessage(room, to, content, timestamp string) error {
	return c.sendExpectSuccess("log_message", types.Request{Type: "log_message", Room: room, Data: logMessageData(to, content, timestamp)})
}

// ListAgents lists agents in a room.
func (c *HubClient) ListAgents(room, agentName string) (*types.Response, error) {
	data, _ := json.Marshal(map[string]string{"agent_name": agentName})
	return c.Send(types.Request{Type: "list_agents", Room: room, Data: data})
}

// LeaveRoom leaves a room.
func (c *HubClient) LeaveRoom(room, agentName string) (*types.Response, error) {
	data, _ := json.Marshal(map[string]string{"agent_name": agentName})

	// Clear the replay intent BEFORE sending. If the hub applies the leave but
	// the response is lost — or a socket failure starts restoration before the
	// response is processed — a still-set intent would silently rejoin the agent
	// the caller just took out of the room.
	c.mu.Lock()
	hadJoin := c.sess.joined && c.sess.joinRoom == room
	if hadJoin {
		c.sess.joined = false
		c.sess.joinEstablished = false
	}
	// Advance regardless: a join racing this leave must lose even if the intent
	// was not set when we looked.
	c.sess.gen++
	c.sess.joinRev++
	myJoinRev := c.sess.joinRev
	c.mu.Unlock()

	resp, err := c.send(types.Request{Type: "leave_room", Room: room, Data: data}, false)

	// A leave the GATE turned away never reached the hub, and the replay running
	// at that moment may already have put the agent back in the room. The intent
	// is cleared, so nothing would rejoin — but nothing would take the agent out
	// either. Queue it: restoration performs it once the session is up.
	if errors.Is(err, errRestoreGate) {
		c.queueLeaveIfCurrent(room, agentName, myJoinRev)
	}

	// Only an explicit protocol rejection means the agent is still in the room,
	// so only then is the intent put back. A transport error leaves it cleared:
	// the hub may well have applied the leave.
	if hadJoin && err == nil && resp != nil && !resp.Success {
		c.mu.Lock()
		c.sess.joined = true
		c.sess.joinEstablished = true
		c.sess.joinRoom = room
		c.mu.Unlock()
	}
	return resp, err
}

// ClearRoom clears a room.
func (c *HubClient) ClearRoom(room string) (*types.Response, error) {
	return c.Send(types.Request{Type: "clear_room", Room: room})
}

// ArchiveRoom flushes a room's current messages to its append-only archive,
// synchronously on the hub. Returns once the hub has written them (buffered to
// the OS, consistent with the hub's other persistence — not fsync'd) or on error.
func (c *HubClient) ArchiveRoom(room string) error {
	return c.sendExpectSuccess("archive_room", types.Request{Type: "archive_room", Room: room})
}

// SaveSession writes an immutable per-session snapshot of a room's full state
// (messages + agent roster) to hub-state/sessions/{room}/{epoch}.json,
// synchronously on the hub. Returns the snapshotted message count and whether a
// file was actually written (saved=false when the room was empty or unchanged
// since its last snapshot). Mirrors ArchiveRoom but, unlike the rolling
// append-only archive, each call produces a distinct, never-overwritten file.
func (c *HubClient) SaveSession(room string) (count int, saved bool, err error) {
	resp, err := c.Send(types.Request{Type: "save_session", Room: room})
	if err != nil {
		return 0, false, err
	}
	if err := ensureSuccess("save_session", resp); err != nil {
		return 0, false, err
	}
	var body struct {
		Saved bool `json:"saved"`
		Count int  `json:"count"`
	}
	if len(resp.Data) > 0 {
		// Surface a decode failure rather than silently reporting {saved:false,
		// count:0}, which would mask a protocol drift as a benign "no new content".
		if err := json.Unmarshal(resp.Data, &body); err != nil {
			return 0, false, fmt.Errorf("decode save_session response: %w", err)
		}
	}
	return body.Count, body.Saved, nil
}

// GetLastMessageID gets the last message ID.
func (c *HubClient) GetLastMessageID(room, agentName string) (*types.Response, error) {
	data, _ := json.Marshal(map[string]string{"agent_name": agentName})
	return c.Send(types.Request{Type: "get_last_message_id", Room: room, Data: data})
}

// ListRooms lists all rooms.
func (c *HubClient) ListRooms() (*types.Response, error) {
	return c.Send(types.Request{Type: "list_rooms"})
}

// ListRoomsDetailed returns structured summaries for all rooms (desktop only).
func (c *HubClient) ListRoomsDetailed() ([]types.RoomSummary, error) {
	resp, err := c.Send(types.Request{Type: "list_rooms_detailed"})
	if err != nil {
		return nil, err
	}
	data, err := decodeSuccessData[struct {
		Rooms []types.RoomSummary `json:"rooms"`
	}]("list_rooms_detailed", resp)
	if err != nil {
		return nil, err
	}
	return data.Rooms, nil
}

// GetAgentsRaw returns raw agent data for a room.
func (c *HubClient) GetAgentsRaw(room string) (map[string]types.Agent, error) {
	resp, err := c.Send(types.Request{Type: "get_agents", Room: room})
	if err != nil {
		return nil, err
	}
	data, err := decodeSuccessData[struct {
		Agents map[string]types.Agent `json:"agents"`
	}]("get_agents", resp)
	if err != nil {
		return nil, err
	}
	return data.Agents, nil
}

// GetMessagesRaw returns raw message data for a room.
func (c *HubClient) GetMessagesRaw(room string) ([]types.Message, error) {
	resp, err := c.Send(types.Request{Type: "get_messages_raw", Room: room})
	if err != nil {
		return nil, err
	}
	data, err := decodeSuccessData[struct {
		Messages []types.Message `json:"messages"`
	}]("get_messages_raw", resp)
	if err != nil {
		return nil, err
	}
	return data.Messages, nil
}

// ConnectWithRetry tries to connect with exponential backoff.
func (c *HubClient) ConnectWithRetry(maxAttempts int) error {
	backoff := 500 * time.Millisecond
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		err := c.Connect()
		if err == nil {
			return nil
		}
		lastErr = err
		c.logger.Printf("Hub connect attempt %d/%d failed: %v (retrying in %v)", i+1, maxAttempts, err, backoff)

		select {
		case <-time.After(backoff):
		case <-c.done:
			return fmt.Errorf("hub client closed")
		}

		backoff *= 2
		if backoff > maxReconnect {
			backoff = maxReconnect
		}
	}
	if lastErr != nil {
		return fmt.Errorf("failed to connect to hub after %d attempts; last error: %w", maxAttempts, lastErr)
	}
	return fmt.Errorf("failed to connect to hub after %d attempts", maxAttempts)
}
