package hub

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"desktop/internal/types"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Hub is the central WebSocket server that manages rooms and clients.
type Hub struct {
	mu          sync.RWMutex
	rooms       map[string]*RoomState
	clients     map[*Client]bool
	subs        map[string]map[*Client]bool // room → subscribed clients
	defaultRoom string

	register   chan *Client
	unregister chan *Client

	dataDir string
	logger  *log.Logger
	done    chan struct{}

	listener net.Listener
}

// New creates a new Hub.
func New(dataDir, defaultRoom string, logger *log.Logger) *Hub {
	return &Hub{
		rooms:       make(map[string]*RoomState),
		clients:     make(map[*Client]bool),
		subs:        make(map[string]map[*Client]bool),
		defaultRoom: defaultRoom,
		register:    make(chan *Client),
		unregister:  make(chan *Client),
		dataDir:     dataDir,
		logger:      logger,
		done:        make(chan struct{}),
	}
}

// Run starts the WebSocket server. port=0 lets the OS assign a port.
// The actual port is written to ~/.agent-chat/hub.port.
func (h *Hub) Run(port int) error {
	h.loadPersistedState()

	ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return fmt.Errorf("hub listen: %w", err)
	}
	h.listener = ln

	actualPort := ln.Addr().(*net.TCPAddr).Port
	h.logger.Printf("Hub server listening on localhost:%d", actualPort)

	// Write port file
	portPath := filepath.Join(h.dataDir, "hub.port")
	if err := os.WriteFile(portPath, []byte(fmt.Sprintf("%d", actualPort)), 0644); err != nil {
		h.logger.Printf("Failed to write hub.port: %v", err)
	}

	// Start client manager
	go h.runClientManager()

	// Start persistence loop
	go h.persistLoop()

	// HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.handleWS)

	server := &http.Server{Handler: mux}
	return server.Serve(ln)
}

// Port returns the port the hub is listening on, or 0 if not running.
func (h *Hub) Port() int {
	if h.listener == nil {
		return 0
	}
	return h.listener.Addr().(*net.TCPAddr).Port
}

// Shutdown stops the hub gracefully.
func (h *Hub) Shutdown() {
	close(h.done)

	// Persist all state
	h.persistAll()

	// Close listener
	if h.listener != nil {
		h.listener.Close()
	}

	// Close all client connections
	h.mu.Lock()
	for client := range h.clients {
		close(client.send)
		client.conn.Close()
	}
	h.mu.Unlock()

	// Remove port file
	os.Remove(filepath.Join(h.dataDir, "hub.port"))

	h.logger.Println("Hub shut down")
}

func (h *Hub) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := newClient(h, conn)
	h.register <- client

	go client.writePump()
	go client.readPump()
}

func (h *Hub) runClientManager() {
	for {
		select {
		case <-h.done:
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			h.logger.Printf("Client connected (total: %d)", len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				// Remove from room subscriptions
				for room := range client.rooms {
					if subs, ok := h.subs[room]; ok {
						delete(subs, client)
					}
				}
			}
			h.mu.Unlock()
			h.logger.Printf("Client disconnected (total: %d)", len(h.clients))
		}
	}
}

// getOrCreateRoom returns the room state, creating it if it doesn't exist.
func (h *Hub) getOrCreateRoom(room string) *RoomState {
	h.mu.Lock()
	defer h.mu.Unlock()

	if r, ok := h.rooms[room]; ok {
		return r
	}
	r := NewRoomState()
	h.rooms[room] = r
	return r
}

// resolveRoom returns the room name, using defaultRoom if empty.
func (h *Hub) resolveRoom(room string) string {
	if room == "" {
		return h.defaultRoom
	}
	return room
}

// broadcastEvent sends an event to all subscribers of a room.
func (h *Hub) broadcastEvent(room, eventName string, data map[string]any) {
	eventData, _ := json.Marshal(data)
	event := types.Event{
		Type:  "event",
		Event: eventName,
		Room:  room,
		Data:  eventData,
	}

	h.mu.RLock()
	subs := h.subs[room]
	h.mu.RUnlock()

	for client := range subs {
		client.sendJSON(event)
	}
}
