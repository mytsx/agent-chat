package hub

import (
	"encoding/json"
	"log"
	"sync"
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
	sendMu     sync.Mutex
	sendClosed bool
	rooms      map[string]bool // subscribed rooms
	clientType string          // "mcp" or "desktop"
	// desktopAuthed is true only when client_type=desktop is validated with hub auth token.
	desktopAuthed bool
	agentName     string
	joinedRoom    string
	// isObserver is set once at a gated observer join (#17). It is connection-bound,
	// so an observer can never send_message for the life of this connection even if
	// the desktop later removes it from the allow-list or clear_room wipes the roster
	// — it would have to reconnect (and not as an observer) to send.
	isObserver bool
}

func newClient(hub *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:   hub,
		conn:  conn,
		send:  make(chan []byte, 256),
		rooms: make(map[string]bool),
	}
}

// readPump reads messages from the WebSocket connection.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMsgSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
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
	// Drain queued messages — each as its own WebSocket frame.
	n := len(c.send)
	for i := 0; i < n; i++ {
		if err := c.writeTextMessage(<-c.send); err != nil {
			return err
		}
	}
	return nil
}

// closeSend closes the outbound queue exactly once. Request handlers and
// broadcasts can call sendJSON while unregister/shutdown tears the client down,
// so sends and closes must be serialized to avoid send-on-closed-channel panics.
func (c *Client) closeSend() {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.sendClosed {
		return
	}
	c.sendClosed = true
	close(c.send)
}

// sendJSON sends a JSON-encoded message to this client.
func (c *Client) sendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("sendJSON marshal error: %v", err)
		return
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.sendClosed {
		return
	}
	select {
	case c.send <- data:
	default:
		// Client buffer full, drop
		c.hub.logger.Printf("Client send buffer full, dropping message for %s", c.agentName)
	}
}

// sendSuccess sends a successful response with an optional JSON payload.
func (c *Client) sendSuccess(id, reqType string, payload any) {
	resp := types.Response{ID: id, RequestType: reqType, Success: true}
	if payload != nil {
		resp.Data, _ = json.Marshal(payload)
	}
	c.sendJSON(resp)
}

// sendOK sends a standard ok=true success response.
func (c *Client) sendOK(id, reqType string) {
	c.sendSuccess(id, reqType, map[string]bool{"ok": true})
}

// sendText sends a text-only success response.
func (c *Client) sendText(id, reqType, text string) {
	c.sendSuccess(id, reqType, map[string]string{"text": text})
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
