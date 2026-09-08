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

	joined    bool
	joinRoom  string
	joinAgent string
	joinRole  string

	subs []string
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
	pending map[string]chan *types.Response
	onEvent func(types.Event)
	hubAddr string
	logger  *log.Logger
	done    chan struct{}
	closed  bool

	minBackoff time.Duration
	maxBackoff time.Duration
	// resolveAddr re-resolves the hub address before each dial. The hub listens
	// on an OS-assigned port (Run(0)) and rewrites hub.port on every start, so a
	// restarted hub is at a DIFFERENT address — a supervisor that redialled the
	// remembered one would retry a dead port forever and never notice.
	resolveAddr func() (string, error)
	// bootstrap establishes the session on the FIRST successful connection.
	// Later reconnects replay what it recorded rather than running it again.
	bootstrap   func(*HubClient)
	sess        session
	supervising bool
}

// New creates a new HubClient.
func New(hubAddr string, logger *log.Logger) *HubClient {
	return &HubClient{
		pending:    make(map[string]chan *types.Response),
		hubAddr:    hubAddr,
		logger:     logger,
		done:       make(chan struct{}),
		minBackoff: defaultMinBackoff,
		maxBackoff: defaultMaxBackoff,
	}
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
func (c *HubClient) SetBootstrap(fn func(*HubClient)) {
	c.mu.Lock()
	c.bootstrap = fn
	c.mu.Unlock()
}

// hasSession reports whether anything has been recorded to replay.
func (c *HubClient) hasSession() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess.identified || c.sess.joined || len(c.sess.subs) > 0
}

// runBootstrap performs first-connection setup if nothing has been recorded yet.
func (c *HubClient) runBootstrap() {
	if c.hasSession() {
		return
	}
	c.mu.Lock()
	fn := c.bootstrap
	c.mu.Unlock()
	if fn != nil {
		fn(c)
	}
}

// StartBackgroundConnect connects without blocking the caller, retrying until
// it succeeds or the client is closed.
//
// Startup must never wait on the hub: an MCP server that does not answer its
// host's initialize handshake in time is marked failed and never retried, and
// the desktop may not have written hub.port yet. Serve first, connect when the
// hub shows up.
func (c *HubClient) StartBackgroundConnect() {
	go func() {
		if err := c.Connect(); err != nil {
			c.superviseReconnect() // keeps trying, then bootstraps
			return
		}
		c.runBootstrap()
	}()
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
	if !closed {
		c.conn = conn
	}
	c.mu.Unlock()
	if closed {
		conn.Close()
		return fmt.Errorf("hub client closed")
	}

	go c.readLoop(conn)

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

// superviseReconnect redials until it succeeds or the client is closed, then
// restores the session. Runs at most once at a time.
func (c *HubClient) superviseReconnect() {
	c.mu.Lock()
	if c.closed || c.supervising {
		c.mu.Unlock()
		return
	}
	c.supervising = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.supervising = false
		c.mu.Unlock()
	}()

	var backoff time.Duration
	for {
		backoff = c.nextBackoff(backoff)
		select {
		case <-time.After(backoff):
		case <-c.done:
			return
		}

		if err := c.Connect(); err != nil {
			c.logger.Printf("Hub reconnect failed, retrying: %v", err)
			continue
		}
		if err := c.restoreSession(); err != nil {
			// The socket is up but the hub would not have us back. Drop it and
			// let the loop try again rather than pretending we are joined.
			c.logger.Printf("Hub session restore failed, retrying: %v", err)
			c.dropConn()
			continue
		}
		// Nothing recorded yet means this is the first connection the client
		// ever made (the hub was down at startup), so the session still has to
		// be established.
		c.runBootstrap()

		// The socket can die WHILE the session is being replayed. Its read loop
		// calls superviseReconnect, which this still-running supervisor would
		// swallow as a duplicate — so returning blindly here can leave the
		// client disconnected with nobody scheduled to redial. Only exit if the
		// connection actually survived.
		c.mu.Lock()
		live := c.conn != nil
		c.mu.Unlock()
		if !live {
			c.logger.Printf("Hub connection lost during restore, retrying")
			continue
		}

		c.logger.Printf("Hub connection restored")
		return
	}
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
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
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
	c.pending[req.ID] = ch
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
			return nil, fmt.Errorf("hub client closed while waiting for response")
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
func (c *HubClient) readLoop(conn *websocket.Conn) {
	defer func() {
		c.mu.Lock()
		// Only disown the socket if it is still the current one; a newer dial
		// may already have replaced it.
		if c.conn == conn {
			c.conn = nil
		}
		closed := c.closed
		c.mu.Unlock()
		// A read error used to end the client's life. Hand off to the supervisor
		// instead — unless the teardown was deliberate.
		if !closed {
			go c.superviseReconnect()
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
		if ch, ok := c.pending[resp.ID]; ok {
			delete(c.pending, resp.ID)
			c.mu.Unlock()
			ch <- &resp
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

	// Remember the intent when the request never reached the hub, not only when
	// it succeeded. With a background connect the agent can call join_room
	// before the first dial lands; dropping that intent would leave it outside
	// the room until the model happened to retry. A hub that REJECTED the join
	// is different — replaying that would just fail the same way forever.
	if err != nil || (resp != nil && resp.Success) {
		c.mu.Lock()
		c.sess.joined = true
		c.sess.joinRoom = room
		c.sess.joinAgent = agentName
		c.sess.joinRole = role
		c.mu.Unlock()
	}
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
	resp, err := c.Send(types.Request{Type: "leave_room", Room: room, Data: data})
	if err != nil || resp == nil || !resp.Success {
		return resp, err
	}

	// A deliberate departure must not be undone by the next reconnect.
	c.mu.Lock()
	if c.sess.joinRoom == room {
		c.sess.joined = false
	}
	c.mu.Unlock()
	return resp, nil
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
