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

	"desktop/internal/eventlog"
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
	// roomObservers is the desktop-authorized observer set per room (#17). join_room
	// with role "observer" is rejected unless the agent is in this set, mirroring the
	// manager gate — so a self-asserted observer can't gain read-all transcript access.
	roomObservers map[string]map[string]bool
	// deletedRooms tombstones rooms removed via delete_room. The periodic persist loop
	// skips tombstoned names so an in-flight write cannot resurrect a just-deleted state
	// file; getOrCreateRoom clears the tombstone when a same-named room is legitimately
	// (re)created. Guarded by h.mu.
	deletedRooms map[string]bool
	defaultRoom  string
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
	// sessionLastSig. Like archiveMu it may be held across disk I/O — the session
	// path is low-frequency (termination hooks / manual save), not the hot message
	// path. sessionLastSig maps room → the signature (max message ID + sorted agent
	// roster) captured by its last snapshot, so a room that is unchanged in BOTH
	// messages and roster is skipped rather than re-snapshotted — while a
	// roster-only change (e.g. stale-agent cleanup, which mutates agents without a
	// message) still differs and triggers a fresh snapshot.
	sessionMu      sync.Mutex
	sessionLastSig map[string]string

	// Graceful request shutdown (mirrors http.Server.Shutdown for our hijacked
	// WebSocket message loop). requestsClosed, set once under requestMu, makes
	// readPump stop handling new requests; inflightRequests counts handlers in
	// progress. Every truncate/clear archive write originates inside a request
	// handler, so waiting on inflightRequests guarantees all such writes finish
	// before Shutdown drains and persists — nothing is left mid-write at exit.
	requestMu        sync.Mutex
	requestsClosed   bool
	inflightRequests sync.WaitGroup

	// connMu guards connectedAgents and is deliberately NOT h.mu: liveness is
	// read from inside cleanupStaleLocked, which runs under the ROOM lock, and
	// persistRoom already takes h.mu then the room lock. Reusing h.mu here would
	// invert that order and can deadlock.
	connMu sync.RWMutex
	// connectedAgents counts live connections per room+agent. A count, not a
	// bool: a reconnecting client registers its new connection before the old
	// one unregisters, and the agent must not flicker to "gone" in between.
	connectedAgents map[string]int
	// departGen numbers each disconnect so a superseded grace timer can tell it
	// no longer owns the window.
	departGen map[string]uint64
	// departUntil is when each pending grace window closes, so stale cleanup can
	// leave those entries alone.
	departUntil map[string]time.Time
	// graceWindow is how long a departure waits before it is written down. A
	// client that reconnects inside it leaves no trace; see releaseAgent.
	graceWindow time.Duration

	listener net.Listener

	// events is the structured event stream (#101): the measurement layer for
	// why agents drop out of rooms and miss each other's messages. Never nil —
	// a hub without a data dir, or one whose stream cannot be opened, gets a
	// no-op logger so logging can never be the reason the hub fails.
	events *eventlog.Logger
}

// New creates a new Hub.
func New(dataDir, defaultRoom string, logger *log.Logger) *Hub {
	desktopAuthToken := strings.TrimSpace(os.Getenv("AGENT_CHAT_HUB_TOKEN"))

	// A dataDir-less hub (unit tests, in-process use) logs nowhere; otherwise a
	// failure to open the stream is reported and downgraded, never fatal.
	events := eventlog.NopLogger()
	if dataDir != "" {
		var err error
		// OnError routes sink failures into the plain-text log: the desktop
		// starts the hub with its stderr unset, so a bare stderr write would
		// vanish exactly when the disk is failing.
		opts := eventlog.Options{Dir: dataDir, OnError: func(err error) {
			logger.Printf("Olay logu yazma hatası: %v", err)
		}}
		if events, err = eventlog.New(opts); err != nil {
			logger.Printf("Olay logu açılamadı, olay kaydı devre dışı: %v", err)
		}
	}

	return &Hub{
		rooms:            make(map[string]*RoomState),
		clients:          make(map[*Client]bool),
		subs:             make(map[string]map[*Client]bool),
		roomManager:      make(map[string]string),
		roomObservers:    make(map[string]map[string]bool),
		deletedRooms:     make(map[string]bool),
		defaultRoom:      defaultRoom,
		desktopAuthToken: desktopAuthToken,
		register:         make(chan *Client),
		unregister:       make(chan *Client),
		dataDir:          dataDir,
		logger:           logger,
		done:             make(chan struct{}),
		archiveCh:        make(chan archiveJob, archiveBufferSize),
		archiveDone:      make(chan struct{}),
		sessionLastSig:   make(map[string]string),
		connectedAgents:  make(map[string]int),
		departGen:        make(map[string]uint64),
		departUntil:      make(map[string]time.Time),
		graceWindow:      defaultGraceWindow,
		events:           events,
	}
}

// Run starts the WebSocket server. port=0 lets the OS assign a port.
// The actual port is written to ~/.agent-chat/hub.port.
func (h *Hub) Run(port int) error {
	h.loadPersistedState()
	h.seedSessionTracking()

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

	h.events.Log(eventlog.EventHubStarted,
		eventlog.Int(eventlog.AttrServerPort, actualPort),
		eventlog.Int(eventlog.AttrPID, os.Getpid()),
	)

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
		// The hub is unreachable from here on, but the stopped record is only
		// written after draining and persistence — potentially seconds later.
		// Connections refused in between belong to the outage, so mark its real
		// start now. Written synchronously: a crash mid-shutdown must not lose
		// the boundary, and there may be no later event to carry it.
		h.events.LogSync(eventlog.EventHubUnavailable)
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

	// Persist all state. Whether it succeeded is continuity evidence: a failed
	// persist leaves the next process loading an older snapshot, free to reuse
	// message IDs exactly as a crash does.
	persisted := h.persistAll()

	// Close all client connections
	h.mu.Lock()
	for client := range h.clients {
		close(client.send)
		client.conn.Close()
	}
	h.mu.Unlock()

	// Remove port file
	os.Remove(filepath.Join(h.dataDir, "hub.port"))

	// Drain BEFORE snapshotting the count: a queued event that fails to write
	// increments it, and once this record is out there is no later event for the
	// writer to hang a durable marker on. Written synchronously because it
	// reports the loss and so must not itself be droppable.
	h.events.Drain()
	h.events.LogSync(eventlog.EventHubStopped,
		eventlog.Uint64(eventlog.AttrEventsDropped, h.events.Dropped()),
		eventlog.Bool(eventlog.AttrPersistOK, persisted),
	)
	if err := h.events.Close(); err != nil {
		h.logger.Printf("Olay logu kapatılamadı: %v", err)
	}

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
			// No connect event here: clientType is not assigned until identify
			// runs, so this point can only ever record a blank one. The typed
			// event is emitted from handleIdentify instead.
			h.logger.Printf("Client connected (total: %d)", len(h.clients))

		case client := <-h.unregister:
			var joinedRoom, agentName, clientType string
			// readPump has returned by the time it sends on unregister, so this
			// is safe to read without a lock.
			closeCause := client.closeCause
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
				clientType = client.clientType
			}
			h.mu.Unlock()

			// If this client had joined as an agent, remove it immediately
			// so name re-use and manager lock cleanup do not wait for stale timeout.
			h.releaseAgentForClient(client, joinedRoom, agentName)

			h.events.Log(eventlog.EventClientDisconnected,
				eventlog.String(eventlog.AttrAgentName, agentName),
				eventlog.String(eventlog.AttrConversationID, joinedRoom),
				eventlog.String(eventlog.AttrClientType, clientType),
				// Answers "did it leave or did it fall over" — the distinction
				// the whole stream exists to make (#98).
				eventlog.String(eventlog.AttrErrorType, closeCause),
			)
			h.logger.Printf("Client disconnected (total: %d)", len(h.clients))
		}
	}
}

// defaultGraceWindow defers a departure long enough for a reconnect to land.
//
// The room's system messages are read BY the other agents, so writing
// "X ayrıldı" and then "X katıldı" for every blip is not just log noise: it
// tells the team someone left and a new one arrived. The client's reconnect
// backoff starts well under a second, so a few seconds covers a real blip while
// still clearing a genuinely departed agent promptly.
const defaultGraceWindow = 5 * time.Second

// connKey identifies one agent's presence in one room.
func connKey(room, agentName string) string { return room + "\x00" + agentName }

// claimLiveness records that this connection vouches for an agent, at most once
// per connection. A second join on the same socket is a no-op rather than a
// second claim that nothing will ever release.
func (h *Hub) claimLiveness(c *Client, room, agentName string) {
	if c == nil || room == "" || agentName == "" {
		return
	}
	key := connKey(room, agentName)
	if c.livenessKey == key {
		return // already claimed by this connection
	}
	if c.livenessKey != "" {
		h.releaseLivenessKey(c.livenessKey)
	}
	c.livenessKey = key
	h.connMu.Lock()
	h.connectedAgents[key]++
	// Retire any pending grace timer: the agent is back.
	h.departGen[key]++
	delete(h.departUntil, key)
	h.connMu.Unlock()
}

// agentConnected registers a live connection for an agent in a room. Prefer
// claimLiveness when a *Client is available; this exists for the paths that
// only know the names.
func (h *Hub) agentConnected(room, agentName string) {
	if room == "" || agentName == "" {
		return
	}
	h.connMu.Lock()
	h.connectedAgents[connKey(room, agentName)]++
	h.connMu.Unlock()
}

// releaseLivenessKey drops one claim on an already-built key.
func (h *Hub) releaseLivenessKey(key string) {
	h.connMu.Lock()
	if n := h.connectedAgents[key] - 1; n > 0 {
		h.connectedAgents[key] = n
	} else {
		delete(h.connectedAgents, key)
	}
	h.connMu.Unlock()
}

// agentDisconnected releases one, removing the entry at zero.
func (h *Hub) agentDisconnected(room, agentName string) {
	if room == "" || agentName == "" {
		return
	}
	h.releaseLivenessKey(connKey(room, agentName))
}

// releaseClientLiveness drops whatever claim this connection holds, if any.
func (h *Hub) releaseClientLiveness(c *Client) {
	if c == nil || c.livenessKey == "" {
		return
	}
	h.releaseLivenessKey(c.livenessKey)
	c.livenessKey = ""
}

// isAgentConnected reports whether any live socket holds this agent in this
// room. Safe to call under the room lock — see connMu.
func (h *Hub) isAgentConnected(room, agentName string) bool {
	h.connMu.RLock()
	defer h.connMu.RUnlock()
	return h.connectedAgents[connKey(room, agentName)] > 0
}

// connectedFnFor builds the per-room liveness predicate.
//
// Liveness must come from the connection, not from a timestamp only RPC calls
// refresh: an agent can work for well past staleTimeout without calling a tool
// while its socket answers every hub ping. Evicting it then is #98's third
// mechanism.
func (h *Hub) connectedFnFor(room string) func(string) bool {
	return func(agentName string) bool { return h.isAgentConnected(room, agentName) }
}

// isAgentProtected reports whether an agent must survive stale cleanup: it is
// connected, or its grace window has not closed yet.
func (h *Hub) isAgentProtected(room, agentName string) bool {
	key := connKey(room, agentName)
	h.connMu.RLock()
	defer h.connMu.RUnlock()
	if h.connectedAgents[key] > 0 {
		return true
	}
	until, ok := h.departUntil[key]
	return ok && time.Now().Before(until)
}

// protectedFnFor builds the per-room stale-cleanup shield.
func (h *Hub) protectedFnFor(room string) func(string) bool {
	return func(agentName string) bool { return h.isAgentProtected(room, agentName) }
}

// releaseAgent gives up one connection's claim on an agent and, if that was the
// last one, schedules the departure after the grace window.
//
// The departure is deferred rather than immediate because a reconnect that
// lands inside the window should be invisible: previously every blip wrote a
// leave and a join into the room, which the other agents read as a teammate
// leaving and a stranger arriving.
// releaseAgentForClient releases the claim this connection holds and then runs
// the shared departure path.
func (h *Hub) releaseAgentForClient(c *Client, room, agentName string) {
	if room == "" || agentName == "" {
		return
	}
	h.releaseClientLiveness(c)
	h.scheduleDeparture(room, agentName)
}

// releaseAgent releases one claim by name. Kept for callers without a *Client.
func (h *Hub) releaseAgent(room, agentName string) {
	if room == "" || agentName == "" {
		return
	}
	h.agentDisconnected(room, agentName)
	h.scheduleDeparture(room, agentName)
}

// scheduleDeparture defers the removal so a reconnect inside the window is
// invisible.
func (h *Hub) scheduleDeparture(room, agentName string) {
	if h.isAgentConnected(room, agentName) {
		return // another connection still holds this agent
	}

	// Each disconnect gets its own generation. A reconnect (claimLiveness) bumps
	// it, retiring the timer this call is about to arm — otherwise an agent that
	// flaps would be removed on the FIRST disconnect's old deadline instead of
	// getting a fresh window from the latest one.
	key := connKey(room, agentName)
	grace := h.graceWindow
	h.connMu.Lock()
	h.departGen[key]++
	gen := h.departGen[key]
	if grace > 0 {
		h.departUntil[key] = time.Now().Add(grace)
	}
	h.connMu.Unlock()

	if grace <= 0 {
		h.finalizeDeparture(room, agentName, gen)
		return
	}
	time.AfterFunc(grace, func() {
		select {
		case <-h.done:
			return // shutting down; the roster is being torn down anyway
		default:
		}
		h.finalizeDeparture(room, agentName, gen)
	})
}

// finalizeDeparture removes an agent that did not come back, unless a newer
// disconnect has superseded this one.
func (h *Hub) finalizeDeparture(room, agentName string, gen uint64) {
	key := connKey(room, agentName)
	h.connMu.RLock()
	current := h.departGen[key]
	h.connMu.RUnlock()
	if current != gen {
		return // a later disconnect (or a reconnect) owns the window now
	}
	h.connMu.Lock()
	delete(h.departUntil, key)
	h.connMu.Unlock()

	roomState := h.getRoom(room)
	if roomState == nil {
		return
	}
	// The liveness check happens INSIDE the room lock: checking here and leaving
	// afterwards let a reconnect land in between and be removed anyway.
	sysMsg, found := roomState.LeaveIfDisconnected(agentName)
	if !found {
		return
	}
	agents := roomState.GetAgents()
	h.broadcastEvent(room, "message_new", map[string]any{"message": sysMsg})
	h.broadcastEvent(room, "agent_left", map[string]any{"agent_name": agentName, "agents": agents})
	// Reason "disconnect" is what separates #98's connection-loss mechanism
	// from an explicit leave.
	h.events.Log(eventlog.EventAgentLeft,
		eventlog.String(eventlog.AttrConversationID, room),
		eventlog.String(eventlog.AttrAgentName, agentName),
		eventlog.String(eventlog.AttrLeaveReason, eventlog.LeaveReasonDisconnect),
	)
}

// getOrCreateRoom returns the room state, creating it if it doesn't exist.
func (h *Hub) getOrCreateRoom(room string) *RoomState {
	h.mu.Lock()
	defer h.mu.Unlock()

	if r, ok := h.rooms[room]; ok {
		return r
	}
	delete(h.deletedRooms, room) // legitimate (re)creation lifts any tombstone
	r := NewRoomState()
	r.SetArchiveFn(h.archiveFnFor(room))
	r.SetEvictFn(h.evictFnFor(room))
	r.SetResetFn(h.resetFnFor(room))
	r.SetConnectedFn(h.connectedFnFor(room))
	r.SetProtectedFn(h.protectedFnFor(room))
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

// evictFnFor builds the per-room callback that records a stale-timeout eviction.
// Safe to run under the room lock: eventlog.Log is a non-blocking channel send,
// unlike archiveFn which may reach disk.
func (h *Hub) evictFnFor(room string) func(string, float64) {
	return func(agentName string, idleSeconds float64) {
		h.events.Log(eventlog.EventAgentEvicted,
			eventlog.String(eventlog.AttrConversationID, room),
			eventlog.String(eventlog.AttrAgentName, agentName),
			eventlog.Float64(eventlog.AttrIdleSeconds, idleSeconds),
		)
	}
}

// resetFnFor builds the per-room callback that records a clear as a generation
// boundary. Safe under the room lock for the same reason evictFn is: an event-log
// append is a non-blocking channel send.
func (h *Hub) resetFnFor(room string) func(int, int) {
	return func(maxID, generation int) {
		h.events.Log(eventlog.EventRoomReset,
			eventlog.String(eventlog.AttrConversationID, room),
			eventlog.String(eventlog.AttrRoomLifecycle, eventlog.RoomLifecycleCleared),
			eventlog.Int(eventlog.AttrRoomResetMaxID, maxID),
			// The generation this clear ENDED, stamped under the room lock. The
			// analyzer would otherwise reconstruct it by counting boundaries,
			// which drifts once rotation discards an older clear or a rollback
			// reloads an earlier generation.
			eventlog.Int(eventlog.AttrRoomGeneration, generation),
		)
	}
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

// setConfiguredObservers replaces the desktop-authorized observer set for a room
// (#17). An empty list clears the set. Names are stored verbatim; membership is
// matched case-insensitively by isConfiguredObserver.
func (h *Hub) setConfiguredObservers(room string, observers []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(observers) == 0 {
		delete(h.roomObservers, room)
		return
	}
	set := make(map[string]bool, len(observers))
	for _, name := range observers {
		if n := strings.TrimSpace(name); n != "" {
			set[n] = true
		}
	}
	if len(set) == 0 {
		delete(h.roomObservers, room)
		return
	}
	h.roomObservers[room] = set
}

// isConfiguredObserver reports whether agentName is a desktop-authorized observer
// for the room, compared case-insensitively (sameAgentName) like the manager gate.
func (h *Hub) isConfiguredObserver(room, agentName string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for name := range h.roomObservers[room] {
		if sameAgentName(name, agentName) {
			return true
		}
	}
	return false
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
