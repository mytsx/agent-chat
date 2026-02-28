package hubclient

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
)

// HubClient is a WebSocket client that connects to the Hub server.
type HubClient struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	pending map[string]chan *types.Response
	onEvent func(types.Event)
	hubAddr string
	logger  *log.Logger
	done    chan struct{}
	closed  bool
}

// New creates a new HubClient.
func New(hubAddr string, logger *log.Logger) *HubClient {
	return &HubClient{
		pending: make(map[string]chan *types.Response),
		hubAddr: hubAddr,
		logger:  logger,
		done:    make(chan struct{}),
	}
}

// DiscoverHubAddr reads the hub port from the data directory.
func DiscoverHubAddr(dataDir string) (string, error) {
	// Check env var override first
	if port := os.Getenv("AGENT_CHAT_HUB_PORT"); port != "" {
		return fmt.Sprintf("ws://localhost:%s/ws", port), nil
	}

	portPath := filepath.Join(dataDir, "hub.port")
	data, err := os.ReadFile(portPath)
	if err != nil {
		return "", fmt.Errorf("hub.port not found: %w", err)
	}

	port := strings.TrimSpace(string(data))
	return fmt.Sprintf("ws://localhost:%s/ws", port), nil
}

// Connect establishes the WebSocket connection to the hub.
func (c *HubClient) Connect() error {
	conn, _, err := websocket.DefaultDialer.Dial(c.hubAddr, nil)
	if err != nil {
		return fmt.Errorf("hub connect: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	go c.readLoop()

	c.logger.Printf("Connected to hub at %s", c.hubAddr)
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
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
		return nil, fmt.Errorf("not connected to hub")
	}

	data, err := json.Marshal(req)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
		return nil, err
	}

	c.mu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, data)
	c.mu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
		return nil, fmt.Errorf("hub write: %w", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("hub client closed while waiting for response")
		}
		return resp, nil
	case <-time.After(defaultTimeout):
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
		return nil, fmt.Errorf("hub request timeout (id=%s type=%s)", req.ID, req.Type)
	case <-c.done:
		return nil, fmt.Errorf("hub client closed")
	}
}

func (c *HubClient) readLoop() {
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	for {
		select {
		case <-c.done:
			return
		default:
		}

		_, message, err := c.conn.ReadMessage()
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
	resp, err := c.Send(types.Request{Type: "identify", Data: data})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("identify failed: %s", resp.Error)
	}
	return nil
}

// Subscribe subscribes to room events.
func (c *HubClient) Subscribe(rooms []string) error {
	data, _ := json.Marshal(map[string][]string{"rooms": rooms})
	_, err := c.Send(types.Request{Type: "subscribe", Data: data})
	return err
}

// SetManager configures the allowed manager agent for a room.
func (c *HubClient) SetManager(room, managerAgent string) error {
	data, _ := json.Marshal(map[string]string{"manager_agent": managerAgent})
	resp, err := c.Send(types.Request{Type: "set_manager", Room: room, Data: data})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("set_manager failed: %s", resp.Error)
	}
	return nil
}

// JoinRoom joins a room.
func (c *HubClient) JoinRoom(room, agentName, role string) (*types.Response, error) {
	data, _ := json.Marshal(map[string]string{
		"agent_name": agentName,
		"role":       role,
	})
	return c.Send(types.Request{Type: "join_room", Room: room, Data: data})
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

// ListAgents lists agents in a room.
func (c *HubClient) ListAgents(room, agentName string) (*types.Response, error) {
	data, _ := json.Marshal(map[string]string{"agent_name": agentName})
	return c.Send(types.Request{Type: "list_agents", Room: room, Data: data})
}

// LeaveRoom leaves a room.
func (c *HubClient) LeaveRoom(room, agentName string) (*types.Response, error) {
	data, _ := json.Marshal(map[string]string{"agent_name": agentName})
	return c.Send(types.Request{Type: "leave_room", Room: room, Data: data})
}

// ClearRoom clears a room.
func (c *HubClient) ClearRoom(room string) (*types.Response, error) {
	return c.Send(types.Request{Type: "clear_room", Room: room})
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

// GetAgentsRaw returns raw agent data for a room.
func (c *HubClient) GetAgentsRaw(room string) (map[string]types.Agent, error) {
	resp, err := c.Send(types.Request{Type: "get_agents", Room: room})
	if err != nil {
		return nil, err
	}
	var data struct {
		Agents map[string]types.Agent `json:"agents"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
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
	var data struct {
		Messages []types.Message `json:"messages"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, err
	}
	return data.Messages, nil
}

// ConnectWithRetry tries to connect with exponential backoff.
func (c *HubClient) ConnectWithRetry(maxAttempts int) error {
	backoff := 500 * time.Millisecond
	for i := 0; i < maxAttempts; i++ {
		err := c.Connect()
		if err == nil {
			return nil
		}
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
	return fmt.Errorf("failed to connect to hub after %d attempts", maxAttempts)
}
