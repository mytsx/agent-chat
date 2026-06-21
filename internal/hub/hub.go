package hub

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	roomManager map[string]string           // room → configured manager agent name
	defaultRoom string
	// desktopAuthToken is a shared secret set by the desktop app when spawning the hub.
	// It is required to identify as client_type=desktop.
	desktopAuthToken string

	register   chan *Client
	unregister chan *Client

	dataDir string
	logger  *log.Logger
	done    chan struct{}

	// archiveCh feeds dropped/cleared messages to the async archive writer.
	// archiveDone is closed by the writer once it has drained on shutdown.
	// archiveStarted guards the shutdown drain wait (writer only runs after Run).
	// archiveMu serializes appendArchive so the writer goroutine and synchronous
	// archive_room / shutdown writes never interleave on the same file.
	archiveCh      chan archiveJob
	archiveDone    chan struct{}
	archiveStarted bool
	archiveMu      sync.Mutex
	// archiveProducers counts in-flight enqueueArchive callers. archiveDraining,
	// set once under archiveLifecycleMu during Shutdown, flips every later enqueue
	// to a synchronous write. Together they let Shutdown wait for all async
	// producers to finish their hand-off before the final drain, so no job is
	// orphaned in archiveCh after the writer and drain have both stopped.
	archiveLifecycleMu sync.Mutex
	archiveDraining    bool
	archiveProducers   sync.WaitGroup

	listener net.Listener
}

// New creates a new Hub.
func New(dataDir, defaultRoom string, logger *log.Logger) *Hub {
	desktopAuthToken := strings.TrimSpace(os.Getenv("AGENT_CHAT_HUB_TOKEN"))
	return &Hub{
		rooms:            make(map[string]*RoomState),
		clients:          make(map[*Client]bool),
		subs:             make(map[string]map[*Client]bool),
		roomManager:      make(map[string]string),
		defaultRoom:      defaultRoom,
		desktopAuthToken: desktopAuthToken,
		register:         make(chan *Client),
		unregister:       make(chan *Client),
		dataDir:          dataDir,
		logger:           logger,
		done:             make(chan struct{}),
		archiveCh:        make(chan archiveJob, archiveBufferSize),
		archiveDone:      make(chan struct{}),
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

	// Start the async archive writer.
	h.mu.Lock()
	h.archiveStarted = true
	h.mu.Unlock()
	go h.runArchiveWriter()

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

	// Stop accepting new connections first, so the inflow of archive jobs winds
	// down before we drain.
	if h.listener != nil {
		h.listener.Close()
	}

	// Quiesce async archive producers: after archiveDraining is set every new
	// enqueue writes synchronously, and Wait blocks until producers already
	// registered have finished handing their job to the channel (or written it
	// directly). The channel then holds a fixed, fully drainable set.
	h.archiveLifecycleMu.Lock()
	h.archiveDraining = true
	h.archiveLifecycleMu.Unlock()
	h.archiveProducers.Wait()

	// Wait for the writer to flush, then sweep up anything it left behind.
	// Producers are already quiesced, so the channel holds a bounded set
	// (<= archiveBufferSize) of small batches that flush well under the timeout
	// in any realistic case. The timeout is only a safety valve against a hung
	// disk; if it ever fires, drainArchiveBacklog below still flushes whatever
	// remains in the channel synchronously.
	h.mu.RLock()
	archiveStarted := h.archiveStarted
	h.mu.RUnlock()
	if archiveStarted {
		select {
		case <-h.archiveDone:
		case <-time.After(2 * time.Second):
			h.logger.Println("Archive writer drain timed out; flushing remaining backlog synchronously")
		}
	}
	h.drainArchiveBacklog()

	// Persist all state
	h.persistAll()

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
			var joinedRoom, agentName string
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
				joinedRoom = client.joinedRoom
				agentName = client.agentName
			}
			h.mu.Unlock()

			// If this client had joined as an agent, remove it immediately
			// so name re-use and manager lock cleanup do not wait for stale timeout.
			if joinedRoom != "" && agentName != "" {
				roomState := h.getOrCreateRoom(joinedRoom)
				if sysMsg, found := roomState.Leave(agentName); found {
					agents := roomState.GetAgents()
					h.broadcastEvent(joinedRoom, "message_new", map[string]any{"message": sysMsg})
					h.broadcastEvent(joinedRoom, "agent_left", map[string]any{"agent_name": agentName, "agents": agents})
				}
			}

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
	r.SetArchiveFn(h.archiveFnFor(room))
	h.rooms[room] = r
	return r
}

// getRoom returns the existing room state, or nil if the room has never been
// created. Unlike getOrCreateRoom it does not materialize (and later persist) a
// phantom empty room for a name that was never used.
func (h *Hub) getRoom(room string) *RoomState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.rooms[room]
}

// archiveFnFor builds the per-room callback that captures messages leaving the
// room and forwards them to the async archive writer.
func (h *Hub) archiveFnFor(room string) func([]types.Message) {
	return func(msgs []types.Message) {
		h.enqueueArchive(room, msgs)
	}
}

// resolveRoom returns the room name, using defaultRoom if empty.
func (h *Hub) resolveRoom(room string) string {
	if room == "" {
		return h.defaultRoom
	}
	return room
}

func (h *Hub) setConfiguredManager(room, managerAgent string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if strings.TrimSpace(managerAgent) == "" {
		delete(h.roomManager, room)
		return
	}
	h.roomManager[room] = managerAgent
}

func (h *Hub) getConfiguredManager(room string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.roomManager[room]
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
