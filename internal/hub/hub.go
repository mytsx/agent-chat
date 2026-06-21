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
	// archiveClosed (guarded by archiveMu) stops new jobs entering archiveCh once
	// the writer has been told to drain. enqueueArchive performs its channel-send
	// decision under archiveMu so it cannot race Shutdown setting archiveClosed
	// and then draining — a late job either reaches the channel before the drain
	// or is written synchronously, never orphaned.
	archiveClosed bool
	archiveMu     sync.Mutex

	// sessionMu serializes session-snapshot writes (saveSession) and guards
	// sessionLastID. Like archiveMu it may be held across disk I/O — the session
	// path is low-frequency (termination hooks / manual save), not the hot message
	// path. sessionLastID maps room → highest message ID captured by its last
	// snapshot, so an unchanged room (no new messages, hence no roster change since
	// join/leave append system messages) is skipped rather than re-snapshotted.
	sessionMu     sync.Mutex
	sessionLastID map[string]int

	// Graceful request shutdown (mirrors http.Server.Shutdown for our hijacked
	// WebSocket message loop). requestsClosed, set once under requestMu, makes
	// readPump stop handling new requests; inflightRequests counts handlers in
	// progress. Every truncate/clear archive write originates inside a request
	// handler, so waiting on inflightRequests guarantees all such writes finish
	// before Shutdown drains and persists — nothing is left mid-write at exit.
	requestMu        sync.Mutex
	requestsClosed   bool
	inflightRequests sync.WaitGroup

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
		sessionLastID:    make(map[string]int),
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

	// Stop accepting new connections, then stop handling new requests and wait
	// for in-flight handlers to finish. Because every truncate/clear archive
	// write happens inside a request handler, this guarantees no archive write
	// is still in progress (and no new one can start) once Wait returns —
	// closing both the "untracked synchronous write" and the
	// "enqueue-after-drain" shutdown races.
	if h.listener != nil {
		h.listener.Close()
	}
	h.requestMu.Lock()
	h.requestsClosed = true
	h.requestMu.Unlock()
	h.inflightRequests.Wait()

	// Stop any remaining (non-request) enqueue from entering the channel: from
	// here on, enqueueArchive writes synchronously. Set under archiveMu so it
	// orders correctly with enqueueArchive's send decision.
	h.archiveMu.Lock()
	h.archiveClosed = true
	h.archiveMu.Unlock()

	// Wait for the writer to fully drain, including a batch it has already
	// dequeued and is mid-write on (which drainArchiveBacklog could not flush —
	// it only sees jobs still in the channel). With request handling quiesced the
	// remaining work is bounded (<= archiveBufferSize small batches), so this
	// completes in milliseconds on any working disk. We intentionally do NOT cap
	// this with a timeout: abandoning the drain would let the process exit and
	// kill that in-flight write. The desktop parent already bounds a pathological
	// hang (it SIGTERMs the hub, then SIGKILLs after a grace period).
	h.mu.RLock()
	archiveStarted := h.archiveStarted
	h.mu.RUnlock()
	if archiveStarted {
		<-h.archiveDone
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

// beginRequest registers a request handler as in-flight, unless the hub has
// begun shutting down request handling. Returns false when shutting down, in
// which case the caller must NOT handle the request and must NOT call
// endRequest. The requestsClosed check and the Add happen under the same lock
// that Shutdown takes before waiting, so no Add can race the WaitGroup's Wait.
func (h *Hub) beginRequest() bool {
	h.requestMu.Lock()
	defer h.requestMu.Unlock()
	if h.requestsClosed {
		return false
	}
	h.inflightRequests.Add(1)
	return true
}

// endRequest marks an in-flight request handler as finished.
func (h *Hub) endRequest() {
	h.inflightRequests.Done()
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
