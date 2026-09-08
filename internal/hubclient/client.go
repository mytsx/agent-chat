package hubclient

import (
	"encoding/json"
	"fmt"
	"log"
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
	joinRoom        string
	joinAgent       string
	joinRole        string

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
	bootstrap func(*HubClient) error
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

// DiscoverHubAddr reads the hub port from the data directory.
func DiscoverHubAddr(dataDir string) (string, error) {
	// Check env var override first
	if port := os.Getenv("AGENT_CHAT_HUB_PORT"); port != "" {
		if err := validateHubPort("AGENT_CHAT_HUB_PORT", port); err != nil {
			return "", err
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

// SetBootstrap installs a callback run on the first successful connection, to
// establish the session (identify, join, subscribe). It is NOT run again on
// reconnect: by then the calls it made are recorded as session state and
// replayed verbatim, so running it twice would just repeat them.
//
// It exists because a background connect gives the caller no inline moment to
// perform that setup.
func (c *HubClient) SetBootstrap(fn func(*HubClient) error) {
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
	if err := fn(c); err != nil {
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

		if err := c.Connect(); err != nil {
			c.logger.Printf("Hub connect failed, retrying: %v", err)
		} else if err := c.afterConnect(); err != nil {
			// The socket is up but the hub would not have us back. Drop it and
			// try again rather than pretending we are joined.
			c.logger.Printf("Hub session restore failed, retrying: %v", err)
			c.dropConn()
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
	return c.runBootstrap()
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
		if err := c.Identify(s.clientType, s.identAgent, s.identRoom, s.authToken); err != nil {
			return fmt.Errorf("identify: %w", err)
		}
	}
	if s.joined {
		// A protocol-level rejection returns a nil error with Success=false, so
		// checking err alone would report the session restored while the agent
		// sat outside the room — with no further transport failure to trigger
		// another attempt.
		resp, err := c.JoinRoom(s.joinRoom, s.joinAgent, s.joinRole)
		if err != nil {
			return fmt.Errorf("join_room: %w", err)
		}
		if err := ensureSuccess("join_room", resp); err != nil {
			return err
		}
	}
	if len(subs) > 0 {
		if err := c.Subscribe(subs); err != nil {
			return fmt.Errorf("subscribe: %w", err)
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
	resp, err := c.Send(req)
	if err != nil {
		return err
	}
	return ensureSuccess(operation, resp)
}

// Send sends a request and waits for a response (synchronous RPC).
func (c *HubClient) Send(req types.Request) (*types.Response, error) {
	if req.ID == "" {
		req.ID = uuid.New().String()
	}

	ch := make(chan *types.Response, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("hub client closed")
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
	data, _ := json.Marshal(map[string]string{
		"client_type": clientType,
		"agent_name":  agentName,
		"room":        room,
		"auth_token":  authToken,
	})
	if err := c.sendExpectSuccess("identify", types.Request{Type: "identify", Data: data}); err != nil {
		return err
	}
	c.rememberIdentify(clientType, agentName, room, authToken)
	return nil
}

// rememberIdentify and its siblings record what a reconnect has to put back.
// Only successful calls are remembered: replaying a request the hub rejected
// would just fail the same way on every retry.
func (c *HubClient) rememberIdentify(clientType, agentName, room, authToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sess.identified = true
	c.sess.clientType = clientType
	c.sess.identAgent = agentName
	c.sess.identRoom = room
	c.sess.authToken = authToken
}

// Subscribe subscribes to room events.
func (c *HubClient) Subscribe(rooms []string) error {
	data, _ := json.Marshal(map[string][]string{"rooms": rooms})
	if _, err := c.Send(types.Request{Type: "subscribe", Data: data}); err != nil {
		return err
	}
	c.rememberSubscriptions(rooms)
	return nil
}

// rememberSubscriptions accumulates rooms across calls: the desktop subscribes
// incrementally as teams open, and a reconnect has to restore all of them.
func (c *HubClient) rememberSubscriptions(rooms []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, room := range rooms {
		if !slices.Contains(c.sess.subs, room) {
			c.sess.subs = append(c.sess.subs, room)
		}
	}
}

// SetManager configures the allowed manager agent for a room.
func (c *HubClient) SetManager(room, managerAgent string) error {
	data, _ := json.Marshal(map[string]string{"manager_agent": managerAgent})
	return c.sendExpectSuccess("set_manager", types.Request{Type: "set_manager", Room: room, Data: data})
}

// SetObservers configures the desktop-authorized observer set for a room (#17).
// The hub rejects join_room with role "observer" for any agent not in this set.
func (c *HubClient) SetObservers(room string, observers []string) error {
	data, _ := json.Marshal(map[string][]string{"observers": observers})
	return c.sendExpectSuccess("set_observers", types.Request{Type: "set_observers", Room: room, Data: data})
}

// DeleteRoom removes an orphan room's state from the hub (desktop-authorized).
func (c *HubClient) DeleteRoom(room string) error {
	return c.sendExpectSuccess("delete_room", types.Request{Type: "delete_room", Room: room})
}

// JoinRoom joins a room.
func (c *HubClient) JoinRoom(room, agentName, role string) (*types.Response, error) {
	data, _ := json.Marshal(map[string]string{
		"agent_name": agentName,
		"role":       role,
	})
	resp, err := c.Send(types.Request{Type: "join_room", Room: room, Data: data})

	succeeded := err == nil && resp != nil && resp.Success
	// Three outcomes, three rules:
	//   success            → this is the session
	//   transport failure  → keep as INITIAL intent only (see below); the hub
	//                        never saw it, so it is worth replaying
	//   protocol rejection → record nothing; replaying it would fail the same
	//                        way on every reconnect, forever
	unreached := err != nil

	c.mu.Lock()
	// A failed join is kept only when nothing is established yet: with a
	// background connect the agent can call join_room before the first dial
	// lands, and dropping that would leave it outside the room. But letting a
	// failed join for room B overwrite an established membership in room A would
	// silently move the client, and its later operations — still aimed at A —
	// would be rejected as wrong-room.
	if succeeded || (unreached && !c.sess.joinEstablished) {
		c.sess.joined = true
		c.sess.joinEstablished = succeeded
		c.sess.joinRoom = room
		c.sess.joinAgent = agentName
		c.sess.joinRole = role
	}
	c.mu.Unlock()
	return resp, err
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
	c.mu.Unlock()

	resp, err := c.Send(types.Request{Type: "leave_room", Room: room, Data: data})

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
