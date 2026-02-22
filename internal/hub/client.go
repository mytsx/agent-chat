package hub

import (
	"encoding/json"
	"log"
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
	agentName  string
	joinedRoom string
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

		c.hub.handleRequest(c, req)
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
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

			// Drain queued messages — each as its own WebSocket frame
			n := len(c.send)
			for i := 0; i < n; i++ {
				if err := c.conn.WriteMessage(websocket.TextMessage, <-c.send); err != nil {
					return
				}
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// sendJSON sends a JSON-encoded message to this client.
func (c *Client) sendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("sendJSON marshal error: %v", err)
		return
	}
	select {
	case c.send <- data:
	default:
		// Client buffer full, drop
		c.hub.logger.Printf("Client send buffer full, dropping message for %s", c.agentName)
	}
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
