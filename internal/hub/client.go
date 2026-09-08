package hub

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"sync/atomic"
	"time"

	"desktop/internal/types"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
	maxMsgSize = 1 << 20 // 1MB
)

// Client represents a single WebSocket connection to the hub.
type Client struct {
	hub        *Hub
	conn       *websocket.Conn
	send       chan []byte
	rooms      map[string]bool // subscribed rooms
	clientType string          // "mcp" or "desktop"
	// desktopAuthed is true only when client_type=desktop is validated with hub auth token.
	desktopAuthed bool
	agentName     string
	joinedRoom    string
	// closeCause is a low-cardinality classification of WHY the connection
	// ended, set by readPump before it unregisters. Without it the structured
	// stream cannot tell an orderly leave from a transport failure — which is
	// the whole question #98 asks. Written by readPump, read by the client
	// manager after readPump has returned, so no lock is needed.
	closeCause string
	// livenessKey is the room+agent this connection has claimed as live, or ""
	// if none. Holding it on the connection makes the claim per-SOCKET: joining
	// twice on one socket (which clear_room makes possible, since it empties the
	// roster without touching connections) must not leave a claim that can never
	// be released.
	livenessKey string
	// isObserver is the connection-bound observer flag (#17): a connection that
	// joined as an observer cannot send_message, independently of the roster or
	// the allow-list.
	//
	// ATOMIC because it is written across goroutines: the desktop promoting a
	// live observer to manager clears it from the desktop's own request
	// goroutine, while the observer's goroutine may be reading it inside
	// send_message at that moment.
	isObserver atomic.Bool
}

func newClient(hub *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:   hub,
		conn:  conn,
		send:  make(chan []byte, 256),
		rooms: make(map[string]bool),
	}
}

// Close causes. Deliberately few and fixed: this value is counted and grouped
// by, so it must stay low-cardinality (never a formatted error string).
const (
	closeCauseUnknown   = "unknown"
	closeCauseNormal    = "normal_close"
	closeCauseGoingAway = "going_away"
	closeCauseAbnormal  = "abnormal_close"
	closeCauseTimeout   = "read_timeout"
	closeCauseShutdown  = "hub_shutdown"
	closeCauseReadErr   = "read_error"
)

// classifyCloseError maps a read failure onto one of the fixed causes. An
// abnormal close is the signature of a process that vanished without a
// handshake — the dominant pattern in the plain-text log (#98).
func classifyCloseError(err error) string {
	switch {
	case websocket.IsCloseError(err, websocket.CloseNormalClosure):
		return closeCauseNormal
	case websocket.IsCloseError(err, websocket.CloseGoingAway):
		return closeCauseGoingAway
	case websocket.IsCloseError(err, websocket.CloseAbnormalClosure):
		return closeCauseAbnormal
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return closeCauseTimeout
	}
	return closeCauseReadErr
}

// readPump reads messages from the WebSocket connection.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.closeCause = closeCauseUnknown

	c.conn.SetReadLimit(maxMsgSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			c.closeCause = classifyCloseError(err)
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				c.hub.logger.Printf("WebSocket read error: %v", err)
			}
			return
		}

		var req types.Request
		if err := json.Unmarshal(message, &req); err != nil {
			c.hub.logger.Printf("Invalid request JSON: %v", err)
			c.sendError("", "", "invalid JSON")
			continue
		}

		// Gate handling so a graceful shutdown can wait for in-flight requests
		// (and the archive writes they trigger) to finish. Once shutdown closes
		// request handling, stop reading — the connection is being torn down.
		if !c.hub.beginRequest() {
			c.closeCause = closeCauseShutdown
			return
		}
		// endRequest via defer so the inflight count is always balanced, even if
		// a handler panics or a future edit adds an early return — otherwise
		// Shutdown's inflightRequests.Wait could hang.
		func() {
			defer c.hub.endRequest()
			c.hub.handleRequest(c, req)
		}()
	}
}

// writePump writes messages to the WebSocket connection.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.writeWebSocketMessage(websocket.CloseMessage, []byte{})
				return
			}

			if c.writeTextMessage(message) != nil || c.drainQueuedTextMessages() != nil {
				return
			}

		case <-ticker.C:
			if c.writeWebSocketMessage(websocket.PingMessage, nil) != nil {
				return
			}
		}
	}
}

func (c *Client) writeTextMessage(message []byte) error {
	return c.writeWebSocketMessage(websocket.TextMessage, message)
}

func (c *Client) writeWebSocketMessage(messageType int, payload []byte) error {
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return c.conn.WriteMessage(messageType, payload)
}

func (c *Client) drainQueuedTextMessages() error {
	// Drain queued messages — each as its own WebSocket frame. As the sole consumer
	// this reads at most the len() snapshot, and a closed buffered channel still yields
	// its buffered messages first — so nil never surfaces here in practice. The ok
	// check is defense-in-depth: if send is closed mid-drain, stop instead of writing
	// zero-value frames.
	n := len(c.send)
	for i := 0; i < n; i++ {
		msg, ok := <-c.send
		if !ok {
			break
		}
		if err := c.writeTextMessage(msg); err != nil {
			return err
		}
	}
	return nil
}

// sendJSON sends a JSON-encoded message to this client.
// sendJSON queues a response, reporting whether it was accepted for delivery.
// A full buffer drops the response, and a caller that records side effects
// (read progress) must not record them for a response the client never got.
func (c *Client) sendJSON(v any) bool {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("sendJSON marshal error: %v", err)
		return false
	}
	select {
	case c.send <- data:
		return true
	default:
		// Client buffer full, drop
		c.hub.logger.Printf("Client send buffer full, dropping message for %s", c.agentName)
		return false
	}
}

// sendSuccess sends a successful response with an optional JSON payload.
func (c *Client) sendSuccess(id, reqType string, payload any) bool {
	resp := types.Response{ID: id, RequestType: reqType, Success: true}
	if payload != nil {
		resp.Data, _ = json.Marshal(payload)
	}
	return c.sendJSON(resp)
}

// sendOK sends a standard ok=true success response.
func (c *Client) sendOK(id, reqType string) {
	c.sendSuccess(id, reqType, map[string]bool{"ok": true})
}

// sendText sends a text-only success response.
func (c *Client) sendText(id, reqType, text string) bool {
	return c.sendSuccess(id, reqType, map[string]string{"text": text})
}

// sendError sends an error response.
func (c *Client) sendError(id, reqType, errMsg string) {
	c.sendJSON(types.Response{
		ID:          id,
		RequestType: reqType,
		Success:     false,
		Error:       errMsg,
	})
}
