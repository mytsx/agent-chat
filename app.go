package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"desktop/internal/cli"
	"desktop/internal/git"
	"desktop/internal/hub"
	"desktop/internal/hubclient"
	"desktop/internal/ingest"
	"desktop/internal/orchestrator"
	"desktop/internal/prompt"
	ptymgr "desktop/internal/pty"
	"desktop/internal/sanitize"
	"desktop/internal/sessionlog"
	"desktop/internal/summary"
	"desktop/internal/team"
	"desktop/internal/types"
	"desktop/internal/validation"
	"desktop/internal/voice"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed prompts/*.md
var promptsFS embed.FS

//go:embed build/mcp-server-bin
var mcpServerBin []byte

// App struct
type App struct {
	ctx        context.Context
	ptyManager *ptymgr.Manager
	// hubClient is reassigned by monitorHub on hub-crash recovery (Store) while every
	// Wails binding reads it (Load); an atomic.Pointer makes those accesses race-free
	// for the single-writer/many-reader pattern (#56). Always read it once into a local
	// (c := a.hubClient.Load(); if c == nil { ... }) — never call methods on the field
	// directly, to avoid a nil-check-then-deref TOCTOU against a concurrent Store(nil).
	hubClient atomic.Pointer[hubclient.HubClient]
	// hubProcess is written by startHub (startup + monitorHub restart) and read by
	// monitorHub/shutdown across goroutines, so it is atomic for the same reason as
	// hubClient. Load it once into a local before use.
	hubProcess atomic.Pointer[os.Process]
	// shuttingDown is set once at the top of shutdown(); monitorHub checks it so a
	// SIGTERM-induced exit during quit isn't mistaken for a crash and restarted into
	// an orphaned hub process (#60).
	shuttingDown atomic.Bool
	hubAuthToken string
	orchestrator *orchestrator.Orchestrator
	// ingestMgr watches each AI terminal's CLI session file and logs the messages
	// the user typed directly into the terminal as user_prompt (#65). nil-safe:
	// hand-constructed test Apps leave it nil, and the Manager's methods no-op on a
	// nil receiver.
	ingestMgr     *ingest.Manager
	sessionLog    *sessionlog.Store
	promptStore   *prompt.Store
	teamStore     *team.Store
	dataDir       string
	worktreeLocks sync.Map // path → *sync.Mutex — per-path worktree lock
	// promptLogN counts in-flight fire-and-forget user_prompt logging goroutines so
	// a transcript read can drain them first and not miss a just-delivered prompt
	// (#29). An atomic counter (not a WaitGroup) because new logs can start
	// concurrently with a drain — Add racing Wait violates the WaitGroup contract.
	promptLogN atomic.Int64
	// Voice/STT state (#16). voiceMu guards the single active microphone capture —
	// only one panel records at a time (one mic). activeRecorder/activeVoiceSession
	// are non-nil exactly while a capture is in flight; transcription runs after the
	// recorder is detached, so panel B can record while panel A's audio uploads.
	voiceMu            sync.Mutex
	activeRecorder     voice.Recorder
	activeVoiceSession string
	// transcribingSessions is the SET of sessions whose audio is being uploaded/
	// transcribed right now. A per-session map (not a single slot) so a second
	// session transcribing concurrently can't overwrite the first's guard (Codex P2):
	// each entry outlives its activeRecorder (cleared once ffmpeg exits) and blocks a
	// repeat capture for that same session until injection finishes — authoritative,
	// since the pane-local busyRef resets on remount.
	transcribingSessions map[string]bool
	// Injectable seams (orchestrator SendFunc pattern), defaulted in startup(),
	// overridden in tests so the flow runs with no ffmpeg/network/Wails runtime.
	newVoiceRecorder func() (voice.Recorder, error)
	voiceTranscribe  func(ctx context.Context, wav []byte) (string, error)
	voiceInject      func(sessionID, text string, submit bool) error
	voiceEmit        func(event string, payload interface{})
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Data directory
	homeDir, _ := os.UserHomeDir()
	a.dataDir = filepath.Join(homeDir, ".agent-chat")
	os.MkdirAll(a.dataDir, 0700)

	// Initialize PTY manager
	a.ptyManager = ptymgr.NewManager(func(sessionID string, data []byte) {
		runtime.EventsEmit(a.ctx, "pty:output:"+sessionID, string(data))
	})

	// Initialize orchestrator
	a.orchestrator = orchestrator.New(a.ptyManager)

	// Initialize session-file ingestion (#65): logs messages the user types
	// directly into an AI terminal by reading the CLI's own session file.
	a.ingestMgr = ingest.New()
	// #40 Faz-2: persistent per-agent session history for the resume picker.
	if sl, err := sessionlog.New(a.dataDir); err != nil {
		log.Printf("[SESSIONLOG] init failed: %v", err)
	} else {
		a.sessionLog = sl
	}
	// UI fallback: when a notification can't be safely injected (the user kept
	// typing past the deferral cap), surface it in the frontend instead of
	// corrupting the user's input line.
	a.orchestrator.SetDeferredHandler(func(sessionID, agentName, prompt string) {
		runtime.EventsEmit(a.ctx, "notification:deferred", map[string]string{
			"sessionID": sessionID,
			"agentName": agentName,
			"prompt":    prompt,
		})
	})

	// Voice seam defaults (#16). Tests replace these; production uses ffmpeg +
	// Whisper + the real PTY injection and Wails event bus.
	a.newVoiceRecorder = func() (voice.Recorder, error) {
		return voice.NewFFmpegRecorder(a.dataDir, ":0")
	}
	a.voiceTranscribe = func(ctx context.Context, wav []byte) (string, error) {
		cfg, err := voice.LoadConfig(a.dataDir)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
			return "", fmt.Errorf("⚠️ OpenAI API anahtarı yok — Ayarlar'dan girin")
		}
		return voice.NewWhisperClient(cfg.OpenAIAPIKey).Transcribe(ctx, wav)
	}
	a.voiceInject = a.ptyManager.InjectText
	a.voiceEmit = func(event string, payload interface{}) {
		runtime.EventsEmit(a.ctx, event, payload)
	}

	// Initialize stores
	a.promptStore, _ = prompt.NewStore(a.dataDir)
	a.teamStore, _ = team.NewStore(a.dataDir)

	// Seed prompts from existing files
	a.seedPrompts()

	// Setup MCP server binary synchronously
	if err := cli.EnsureMCPServerBinary(mcpServerBin, a.dataDir); err != nil {
		log.Printf("MCP server setup error: %v", err)
	} else {
		for _, ct := range []cli.CLIType{cli.CLIClaude, cli.CLIGemini, cli.CLICopilot, cli.CLICodex} {
			cli.ResetMCPConfig(ct, a.dataDir)
		}
	}

	// Start hub process
	if err := a.startHub(); err != nil {
		log.Printf("Hub start error: %v", err)
		return
	}

	// Connect to hub
	if err := a.connectToHub(); err != nil {
		log.Printf("Hub connect error: %v", err)
		return
	}

	// Subscribe to existing teams
	a.subscribeExistingTeams()

	// Monitor hub process
	a.monitorHub()
}

func newHubAuthToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// startHub spawns the hub process.
func (a *App) startHub() error {
	binPath := cli.GetMCPBinaryPath(a.dataDir)
	if strings.TrimSpace(a.hubAuthToken) == "" {
		token, err := newHubAuthToken()
		if err != nil {
			return fmt.Errorf("hub auth token üretilemedi: %w", err)
		}
		a.hubAuthToken = token
	}

	// Remove stale port file to prevent connecting to old hub
	os.Remove(filepath.Join(a.dataDir, "hub.port"))

	cmd := exec.Command(binPath, "--hub")
	cmd.Env = append(os.Environ(),
		"AGENT_CHAT_DATA_DIR="+a.dataDir,
		"AGENT_CHAT_HUB_TOKEN="+a.hubAuthToken,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("hub start: %w", err)
	}

	a.hubProcess.Store(cmd.Process)
	log.Printf("[STARTUP] Hub process started: pid=%d", cmd.Process.Pid)

	// Wait for hub.port file (max 5s)
	portPath := filepath.Join(a.dataDir, "hub.port")
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(portPath); err == nil {
			data, _ := os.ReadFile(portPath)
			log.Printf("[STARTUP] Hub ready on port %s", strings.TrimSpace(string(data)))
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("hub.port not created within 5s")
}

// connectToHub creates a hub client and connects.
func (a *App) connectToHub() error {
	hubAddr, err := hubclient.DiscoverHubAddr(a.dataDir)
	if err != nil {
		return err
	}

	client := hubclient.New(hubAddr, log.New(os.Stderr, "[HUB-CLIENT] ", log.LstdFlags))
	if err := client.ConnectWithRetry(5); err != nil {
		return err
	}

	// Set event handler
	client.SetEventHandler(func(event types.Event) {
		a.handleHubEvent(event)
	})

	// Identify as desktop client
	if err := client.Identify("desktop", "", "", a.hubAuthToken); err != nil {
		client.Close()
		return err
	}

	a.hubClient.Store(client)
	log.Printf("[STARTUP] Connected to hub")
	return nil
}

// handleHubEvent processes events from the hub.
func (a *App) handleHubEvent(event types.Event) {
	switch event.Event {
	case "message_new":
		// Parse message from event data
		var data struct {
			Message types.Message `json:"message"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			log.Printf("[HUB-EVENT] Failed to parse message_new: %v", err)
			return
		}

		// Emit to frontend
		runtime.EventsEmit(a.ctx, "messages:new", map[string]interface{}{
			"chatDir":  event.Room,
			"messages": []types.Message{data.Message},
		})

		// Process through orchestrator
		a.orchestrator.ProcessMessage(event.Room, data.Message)

	case "agent_joined", "agent_left":
		var data struct {
			AgentName string                 `json:"agent_name"`
			Agents    map[string]types.Agent `json:"agents"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			log.Printf("[HUB-EVENT] Failed to parse %s: %v", event.Event, err)
			return
		}

		runtime.EventsEmit(a.ctx, "agents:updated", map[string]interface{}{
			"chatDir": event.Room,
			"agents":  data.Agents,
		})

	case "room_cleared":
		runtime.EventsEmit(a.ctx, "agents:updated", map[string]interface{}{
			"chatDir": event.Room,
			"agents":  map[string]types.Agent{},
		})
	}
}

// subscribeExistingTeams subscribes to hub events for all saved teams.
func (a *App) subscribeExistingTeams() {
	client := a.hubClient.Load()
	if client == nil {
		return
	}
	teams := a.teamStore.List()
	var rooms []string
	for _, t := range teams {
		teamName := t.Name
		if teamName == "" {
			teamName = "default"
		}
		rooms = append(rooms, teamName)
		a.syncHubManager(teamName, strings.TrimSpace(t.ManagerAgent))
		// Re-authorize observers too (#17): roomObservers is in-memory hub state, so
		// it must be re-synced on startup and after a hub-crash reconnect, exactly
		// like the manager, or a reconnecting observer would be rejected at join.
		a.syncHubObservers(teamName, observerNames(t))
	}
	if len(rooms) > 0 {
		if err := client.Subscribe(rooms); err != nil {
			log.Printf("[HUB] Subscribe failed: %v", err)
		}
	}
}

// roomNameOrDefault maps a team name to its hub room, resolving an empty name to the
// default room — the same convention CreateTerminal uses. The hub sync helpers rely
// on this so a default-room team (empty Name) still targets "default" instead of
// being silently skipped, which would leave its manager/observer state stale (#17).
func roomNameOrDefault(name string) string {
	if strings.TrimSpace(name) == "" {
		return "default"
	}
	return name
}

func (a *App) syncHubManager(room, managerAgent string) {
	client := a.hubClient.Load()
	if client == nil {
		return
	}
	room = roomNameOrDefault(room)
	if err := client.SetManager(room, strings.TrimSpace(managerAgent)); err != nil {
		log.Printf("[HUB] set_manager failed for room=%s manager=%s: %v", room, managerAgent, err)
	}
}

// observerNames returns the names of all agents in a team persisted with the
// observer role (#17), used to sync the hub's desktop-authorized observer set.
func observerNames(t team.Team) []string {
	var names []string
	for _, cfg := range t.Agents {
		if strings.EqualFold(strings.TrimSpace(cfg.Role), team.RoleObserver) {
			names = append(names, cfg.Name)
		}
	}
	return names
}

// syncHubObservers tells the hub which agents are authorized observers for a room
// (#17). The hub rejects a join_room with role "observer" for any agent not in this
// set, so the desktop must sync it before an observer agent connects. The full list
// is sent every time because the hub replaces (not merges) the set.
func (a *App) syncHubObservers(room string, observers []string) {
	client := a.hubClient.Load()
	if client == nil {
		return
	}
	room = roomNameOrDefault(room)
	if err := client.SetObservers(room, observers); err != nil {
		log.Printf("[HUB] set_observers failed for room=%s count=%d: %v", room, len(observers), err)
	}
}

// hubShouldRestart reports whether a hub-process exit warrants a restart. Only a
// non-shutdown, unsuccessful exit qualifies: a shutdown in progress means the exit
// was our own SIGTERM (restarting it would spawn an orphaned hub that outlives the
// app, #60), and a nil state (Wait error) or a clean exit is not a crash.
func hubShouldRestart(shuttingDown bool, state *os.ProcessState) bool {
	if shuttingDown {
		return false
	}
	return state != nil && !state.Success()
}

// monitorHub watches the current hub process and restarts it on crash. It re-arms
// itself after a successful restart so every subsequent crash is also caught.
func (a *App) monitorHub() {
	proc := a.hubProcess.Load()
	if proc == nil {
		return
	}
	go a.watchHubProcess(proc)
}

// watchHubProcess waits for one hub process to exit and, if that exit is a crash
// (not our shutdown), restarts the hub and re-arms monitoring on the new process.
func (a *App) watchHubProcess(proc *os.Process) {
	state, err := proc.Wait()
	if err != nil {
		log.Printf("[HUB-MONITOR] Hub process wait error: %v", err)
	}
	if !hubShouldRestart(a.shuttingDown.Load(), state) {
		return // graceful shutdown, clean exit, or unknown state — do not restart
	}
	log.Printf("[HUB-MONITOR] Hub crashed (exit=%d), restarting...", state.ExitCode())
	// Clear the field first, then close the old client asynchronously. Store(nil) up
	// front makes readers fast-fail ("hub not connected") immediately and means a
	// return on startHub/connectToHub failure below leaves nil, not a dead client.
	// Close runs in a goroutine because a wedged write can block it (it holds the
	// client mutex during conn.WriteMessage) — the same hazard shutdown() bounds — and
	// crash recovery must not stall on cleanup of the dead client.
	if client := a.hubClient.Load(); client != nil {
		a.hubClient.Store(nil)
		go client.Close()
	}
	// Restart
	time.Sleep(500 * time.Millisecond)
	if a.shuttingDown.Load() {
		return // shutdown began during the restart backoff — don't spawn an orphan
	}
	if err := a.startHub(); err != nil {
		log.Printf("[HUB-MONITOR] Hub restart failed: %v", err)
		return
	}
	if err := a.connectToHub(); err != nil {
		log.Printf("[HUB-MONITOR] Hub reconnect failed: %v", err)
		return
	}
	a.subscribeExistingTeams()
	a.monitorHub() // re-arm: watch the freshly started process for the next crash
}

// shutdown is called when the app is closing
func (a *App) shutdown(ctx context.Context) {
	// Mark shutdown FIRST so monitorHub treats the hub's imminent SIGTERM exit as our
	// own teardown, not a crash — otherwise it would restart the hub into an orphaned
	// process that outlives the app and holds the port (#60).
	a.shuttingDown.Store(true)
	// Terminate any in-flight voice capture so a quit mid-recording doesn't orphan
	// ffmpeg or leak its temp WAV (Codex P2).
	a.stopActiveVoice()
	// Close PTYs FIRST, while the hub is still up: killing each CLI lets it flush its
	// final transcript line, and the still-running session-file watchers drain that
	// via their SessionDone path and emit it to the hub — so a prompt typed right
	// before quit lands in the snapshot taken below. THEN stop ingestion (a bounded
	// wait for those final drains to deliver) (#65 / Codex round-5).
	if a.ptyManager != nil {
		// #40 Faz-2: close each live session's history open-window (lastSeen) BEFORE
		// CloseAll — quit bypasses closeTerminalInternal, which is where the per-terminal
		// Touch normally fires, so without this the last run keeps lastSeen==firstSeen
		// and correlation can't see it (Codex P2).
		for _, cid := range a.ptyManager.CapturedSessionIDs() {
			a.sessionLog.Touch(cid)
		}
		a.ptyManager.CloseAll()
	}
	a.ingestMgr.StopAll()
	// Snapshot every live hub room as a session BEFORE closing the hub client, so a
	// quit captures the in-flight conversation (#28). Iterating the hub's room list
	// (not just teamStore) covers orphaned / default / MCP rooms that have no team.
	// Best-effort and skip-aware (the hub skips empty/unchanged rooms). Bounded by a
	// short budget so a wedged-but-alive hub can't stall quit for up to N×(RPC
	// timeout): a healthy hub finishes in milliseconds (local disk writes); if the
	// budget elapses we proceed rather than hang the UI.
	if client := a.hubClient.Load(); client != nil {
		saveDone := make(chan struct{})
		go func() {
			defer close(saveDone)
			// Flush in-flight fire-and-forget prompt logs to the hub BEFORE snapshotting
			// (and before Close cancels their RPC), so a prompt sent right before quit
			// isn't lost from the saved session/summary (#29). Bounded by the outer
			// select's budget below. Uses the captured client (not the reassignable
			// a.hubClient field) inside the goroutine.
			a.drainPromptLogs(2 * time.Second)
			rooms, err := client.ListRoomsDetailed()
			if err != nil {
				log.Printf("[SHUTDOWN] Oda listesi alınamadı, session kaydı atlanıyor: %v", err)
				return
			}
			for _, r := range rooms {
				if _, _, err := client.SaveSession(r.Name); err != nil {
					log.Printf("[SHUTDOWN] Session kaydedilemedi (%s): %v", r.Name, err)
				}
			}
		}()
		select {
		case <-saveDone:
		case <-ctx.Done():
			log.Printf("[SHUTDOWN] Session kaydetme iptal edildi (ctx)")
		case <-time.After(3 * time.Second):
			log.Printf("[SHUTDOWN] Session kaydetme 3s bütçesini aştı, devam ediliyor")
		}
	}

	// Close the hub client, but bound it: a wedged write leaves Send holding the
	// client mutex Close also needs, so an unbounded Close could hang quit forever.
	// If Close doesn't finish in time, proceed to SIGTERM the hub anyway — killing
	// the hub breaks the socket, which unblocks the stuck write and lets Close
	// finish in the background.
	if client := a.hubClient.Load(); client != nil {
		closed := make(chan struct{})
		go func() {
			client.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			log.Printf("[SHUTDOWN] hub client Close 2s'i aştı, hub'a SIGTERM ile devam ediliyor")
		}
	}

	// Stop hub process gracefully. The Wait here races monitorHub's watcher Wait on the
	// same process; concurrent os.Process.Wait is safe (one reaps the status, the other
	// returns a harmless error) and the shuttingDown flag set above keeps the watcher
	// from restarting after its Wait returns.
	if proc := a.hubProcess.Load(); proc != nil {
		proc.Signal(syscall.SIGTERM)
		// Wait up to 3s for hub to persist and shut down
		done := make(chan struct{})
		go func() {
			proc.Wait()
			close(done)
		}()
		select {
		case <-done:
			log.Printf("[SHUTDOWN] Hub process exited gracefully")
		case <-time.After(3 * time.Second):
			log.Printf("[SHUTDOWN] Hub process did not exit in 3s, killing")
			proc.Kill()
		}
	}
	// NOTE: PTYs were already closed at the top of shutdown (before the snapshot) so
	// the ingest watchers could drain each CLI's final flushed prompt into the hub
	// before it was snapshotted (#65 / Codex round-5).
}

func (a *App) seedPrompts() {
	basePrompt := a.readEmbeddedPrompt("prompts/base_prompt.md")
	managerPrompt := a.readEmbeddedPrompt("prompts/manager_prompt.md")

	a.promptStore.Seed(string(basePrompt), string(managerPrompt))

	// #29: the editable "session summary" prompt is seeded idempotently (by name)
	// so it also reaches users with an already-populated library; user edits are
	// preserved on subsequent runs.
	if _, _, err := a.promptStore.SeedIfMissingByName(summaryPromptName, a.summaryPromptContent(), "task", []string{"summary", "session"}); err != nil {
		log.Printf("[PROMPT] özet promptu seed edilemedi: %v", err)
	}
}

// summaryPromptName is the stable display name of the seeded session-summary
// prompt; it doubles as the lookup key for the user-editable version.
const summaryPromptName = "Session Özeti Üretimi"

// summaryMaxRunes caps a saved summary server-side, mirroring the frontend's
// SUMMARY_MAX. The summary is injected into every continued agent's startup
// prompt, so an unbounded save must not bloat continuation or exceed CLI limits.
const summaryMaxRunes = 8000

// summaryPromptContent returns the summary-prompt template the user can edit: the
// stored prompt if one exists (preserving edits), otherwise the embedded default.
func (a *App) summaryPromptContent() string {
	for _, p := range a.promptStore.List() {
		if p.Name == summaryPromptName {
			return p.Content
		}
	}
	return string(a.readEmbeddedPrompt("prompts/summary_prompt.md"))
}

func (a *App) readEmbeddedPrompt(path string) []byte {
	data, err := promptsFS.ReadFile(path)
	if err != nil {
		log.Printf("[PROMPT] %s okunamadı: %v", path, err)
	}
	return data
}

// worktreeMu returns a per-path mutex for serializing worktree operations.
func (a *App) worktreeMu(path string) *sync.Mutex {
	v, _ := a.worktreeLocks.LoadOrStore(path, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// ===================== PTY Bindings =====================

// OpenDirectoryDialog opens a native directory picker and returns the selected path
func (a *App) OpenDirectoryDialog() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select Workspace Directory",
	})
}

func hasPromptTag(tags []string, tag string) bool {
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), tag) {
			return true
		}
	}
	return false
}

func (a *App) isManagerPrompt(promptID string) bool {
	if promptID == "" {
		return false
	}
	p, err := a.promptStore.Get(promptID)
	if err != nil {
		return false
	}
	return hasPromptTag(p.Tags, "manager")
}

// resolveManagerIntent determines whether this terminal should start as manager.
// If persist=true and manager is inferred from prompt tag, team manager is auto-set.
func (a *App) resolveManagerIntent(teamID, agentName, promptID string, persist bool) (bool, error) {
	if agentName == "" {
		return false, nil
	}

	managerFromPrompt := a.isManagerPrompt(promptID)
	if teamID == "" {
		return managerFromPrompt, nil
	}

	t, err := a.teamStore.Get(teamID)
	if err != nil {
		return false, fmt.Errorf("takım bilgisi alınamadı %s: %w", teamID, err)
	}

	managerFromTeam := strings.TrimSpace(t.ManagerAgent)
	if managerFromTeam != "" {
		// Case-insensitive: a manager saved as "Pilot" must still be recognized when
		// reopened as "pilot" (e.g. after a case-only re-create), otherwise the agent
		// would reopen as a normal agent instead of the manager.
		if managerFromPrompt && !t.IsManagerAgent(agentName) {
			return false, fmt.Errorf("team manager already set to '%s'; '%s' cannot use manager prompt", managerFromTeam, agentName)
		}
		return t.IsManagerAgent(agentName), nil
	}

	if managerFromPrompt {
		if persist {
			if _, err := a.teamStore.SetManager(teamID, agentName); err != nil {
				return false, err
			}
		}
		return true, nil
	}

	return false, nil
}

// resolveAgentMode determines a terminal's startup mode (#17): "manager",
// "observer", or "" (a normal agent). It threads a single mode string — never
// parallel bools — through CreateTerminal → composeAgentPrompt → ComposeStartupPrompt
// so the two roles can't both be set. Observer is read from the persisted
// AgentConfig.Role — the same value the hub IsObserver gate and broadcastRoleLookup
// read — set via SetTeamObserver.
//
// The EXPLICIT observer selection is checked FIRST, before resolveManagerIntent's
// prompt-tag auto-promotion (Codex P2): otherwise an observer terminal whose user
// also picked a manager-tagged prompt would auto-set the team manager, clear the
// observer role, and start with manager routing authority instead of read-only.
// SetManager/SetObserver keep the two roles mutually exclusive in storage, so a
// genuine manager never carries the observer role here.
func (a *App) resolveAgentMode(teamID, agentName, promptID string) (string, error) {
	if a.isObserverAgent(teamID, agentName) {
		return "observer", nil
	}
	isManager, err := a.resolveManagerIntent(teamID, agentName, promptID, true)
	if err != nil {
		return "", err
	}
	if isManager {
		return "manager", nil
	}
	return "", nil
}

// isObserverPhantom reports whether a persisted AgentConfig is a half-written
// observer: SetTeamObserver pre-persists {Name, Role:observer} before CreateTerminal
// supplies the CLIType, so a failed create leaves an observer with no CLIType. The
// check is narrow ON PURPOSE — a blank CLIType alone is a legitimate login-shell
// fallback (legacy/manually-configured teams), so only the observer-role+blank-CLI
// combination identifies the phantom; legitimate shells (Role != observer) are kept.
func isObserverPhantom(cfg team.AgentConfig) bool {
	return isObserverRole(cfg.Role) && strings.TrimSpace(cfg.CLIType) == ""
}

// restartWorkDir decides which directory a restarted terminal runs in. A
// worktree-backed agent that is now a main-repo role (manager OR observer) restarts
// in the recorded main repo (wtRepo); any other agent reuses its existing worktree
// (wtDir). When there's no worktree, the captured workDir is kept as-is.
func restartWorkDir(mainRepoRole bool, workDir, wtDir, wtRepo string) string {
	if wtDir == "" {
		return workDir
	}
	if mainRepoRole {
		if wtRepo != "" {
			return wtRepo
		}
		return workDir
	}
	return wtDir
}

// isObserverAgent reports whether the agent is persisted with the observer role in
// its team config. Case-insensitive, matching the rest of the role handling.
func (a *App) isObserverAgent(teamID, agentName string) bool {
	if teamID == "" || agentName == "" {
		return false
	}
	t, err := a.teamStore.Get(teamID)
	if err != nil {
		return false
	}
	for _, cfg := range t.Agents {
		if strings.EqualFold(strings.TrimSpace(cfg.Name), strings.TrimSpace(agentName)) {
			return strings.EqualFold(strings.TrimSpace(cfg.Role), team.RoleObserver)
		}
	}
	return false
}

// dirExists reports whether p is an existing directory (#40 Faz-2: a recorded
// worktree cwd may have been cleaned up since the session ran).
func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// agentConfigured reports whether agentName already exists in the team's saved
// config (case-insensitive). Used on resume to decide whether to persist the
// recorded cwd for a brand-new history-only agent vs preserve an existing config
// (#40 Faz-2).
func (a *App) agentConfigured(teamID, agentName string) bool {
	if teamID == "" {
		return false
	}
	t, err := a.teamStore.Get(teamID)
	if err != nil {
		return false
	}
	for _, ag := range t.Agents {
		if strings.EqualFold(ag.Name, agentName) {
			return true
		}
	}
	return false
}

// CreateTerminal creates a new terminal and returns its session ID. Exported
// signature is unchanged (Wails binding stable); it delegates to createTerminal
// with no resume ID (fresh launch).
func (a *App) CreateTerminal(teamID, agentName, workDir, cliType, promptID string, useWorktree bool, slotIndex int) (string, error) {
	return a.createTerminal(teamID, agentName, workDir, cliType, promptID, useWorktree, slotIndex, "")
}

// createTerminal is the implementation. A non-empty resumeID with a resume-capable
// CLI launches via cli.GetCommandResume (--resume <id>); otherwise a fresh
// cli.GetCommand. Everything else (worktree, MCP config, ingest, startup prompt)
// is identical to a fresh create (#40).
func (a *App) createTerminal(teamID, agentName, workDir, cliType, promptID string, useWorktree bool, slotIndex int, resumeID string) (string, error) {
	if err := validation.ValidateName(agentName); err != nil {
		return "", fmt.Errorf("invalid agent name: %w", err)
	}

	// Get team info for room name
	var teamName string
	if teamID != "" {
		t, err := a.teamStore.Get(teamID)
		if err == nil {
			teamName = t.Name
		}
	}
	if teamName == "" {
		teamName = "default"
	}

	mode, err := a.resolveAgentMode(teamID, agentName, promptID)
	if err != nil {
		return "", err
	}
	isManager := mode == "manager"
	isObserver := mode == "observer"

	// Manager and observer always work in the main repo — neither writes code in an
	// isolated worktree (manager routes; observer only watches). Backend guard.
	if isManager || isObserver {
		useWorktree = false
	}

	// #40 Faz-2: if the recorded cwd is GONE (e.g. an auto worktree cleaned up on a prior
	// close) and won't be recreated at the SAME path below, drop the resume entirely — the
	// transcript may still exist so the picker offered this row, but resuming is cwd-keyed,
	// so launching --resume in any other dir opens the wrong/fresh conversation. A worktree
	// agent whose deterministic path still equals the recorded cwd (no team rename) keeps
	// the resume — the normal worktree path recreates it there (Codex P2).
	if resumeID != "" {
		if rec, ok := a.sessionLog.Get(resumeID); ok && rec.Cwd != "" && !dirExists(rec.Cwd) {
			wouldBeWt := filepath.Join(a.dataDir, "worktrees", git.Slug(teamName), git.Slug(agentName))
			if !(useWorktree && wouldBeWt == rec.Cwd) {
				log.Printf("[TEAM] resume: kayıtlı cwd %s yok, aynı path'e yaratılamıyor — taze başlatılıyor (agent=%s)", rec.Cwd, agentName)
				resumeID = ""
			}
		}
	}

	// #40 Faz-2: resume LAUNCHES in the directory the session was recorded in — a CLI's
	// session file is cwd-keyed, so resuming under the wizard/config workDir (often blank
	// or changed, and after a team rename the worktree slug differs) would start in the
	// wrong dir and fail to find its session (Codex P2). Override to the recorded cwd when
	// it still exists; if that cwd is a managed worktree, reconstruct its WorktreeDir/Repo
	// metadata (WorktreeRepo = the config repo) so Close/Restart can still clean it up.
	// persistWorkDir/persistUseWorktree keep the role-adjusted config values so the
	// transient launch override isn't written back — EXCEPT for a brand-new history-only
	// agent (SetupWizard pick, no existing config), where the recorded cwd IS persisted so
	// future fresh opens find the cwd-keyed session (Codex P2).
	persistWorkDir, persistUseWorktree := workDir, useWorktree
	var resumeWtDir, resumeWtRepo string
	if resumeID != "" {
		if rec, ok := a.sessionLog.Get(resumeID); ok && rec.Cwd != "" && dirExists(rec.Cwd) {
			worktreesRoot := filepath.Join(a.dataDir, "worktrees")
			recIsWorktree := false
			if rel, err := filepath.Rel(worktreesRoot, rec.Cwd); err == nil && !strings.HasPrefix(rel, "..") {
				recIsWorktree = true
			}
			// A manager/observer MUST run in the main repo (role guard above). If the
			// recorded cwd is a managed worktree — e.g. the agent ran as a worker before
			// being promoted — ignore it rather than dragging a main-repo role into a
			// stale isolated worktree (Codex P2). All other resumes take the override.
			if recIsWorktree && (isManager || isObserver) {
				// Refusing the worktree cwd means we run in the main repo — but the recorded
				// transcript is cwd-keyed to that worktree, so passing --resume <id> here
				// would open the wrong/fresh conversation. Drop the resume and start clean
				// (Codex P2).
				log.Printf("[TEAM] resume: %s main-repo rolü, kayıtlı worktree cwd yok sayıldı — taze başlatılıyor", agentName)
				resumeID = ""
			} else {
				configRepo := workDir // before override (= main repo for a worktree agent)
				workDir = rec.Cwd
				useWorktree = false
				if recIsWorktree {
					resumeWtDir = rec.Cwd
					// Derive the OWNING repo from the worktree itself — the close path runs
					// `git worktree remove <repo> <wt>` and configRepo can be blank (a
					// history-only SetupWizard agent) or point at a different repo after a
					// config change, which would leak the worktree (Codex P2). Fall back to
					// configRepo only if git can't resolve it.
					if owner, oerr := git.WorktreeOwnerRepo(rec.Cwd); oerr == nil {
						resumeWtRepo = owner
					} else {
						resumeWtRepo = configRepo
					}
				} else if !a.agentConfigured(teamID, agentName) {
					// New history-only agent in a PLAIN dir → persist the recorded cwd so
					// future fresh opens find the cwd-keyed session. A worktree cwd is NOT
					// persisted (it is transient and would trip the worktree-path skip below,
					// dropping the agent entirely) — leave persistWorkDir as the config value
					// so the agent is still saved (Codex P2).
					persistWorkDir = rec.Cwd
				}
			}
		}
	}

	// Worktree setup
	var wtDir, origWorkDir string
	var wtNewlyCreated bool
	if useWorktree && workDir != "" && git.IsGitRepo(workDir) {
		origWorkDir = workDir
		teamSlug := git.Slug(teamName)
		agentSlug := git.Slug(agentName)
		branchName := fmt.Sprintf("agent/%s/%s", teamSlug, agentSlug)
		wtDir = filepath.Join(a.dataDir, "worktrees", teamSlug, agentSlug)

		mu := a.worktreeMu(wtDir)
		mu.Lock()
		created, err := git.CreateWorktree(workDir, wtDir, branchName)
		mu.Unlock()
		if err != nil {
			return "", fmt.Errorf("worktree oluşturulamadı: %w", err)
		}
		wtNewlyCreated = created
		workDir = wtDir // PTY will run in worktree directory
	}

	managerAgent := ""
	var observers []string
	if teamID != "" {
		if t, err := a.teamStore.Get(teamID); err == nil {
			managerAgent = strings.TrimSpace(t.ManagerAgent)
			observers = observerNames(t)
		}
	}
	if managerAgent == "" && isManager {
		managerAgent = agentName
	}
	a.syncHubManager(teamName, managerAgent)
	// Authorize the room's observers on the hub BEFORE the agent spawns, so an
	// observer's join_room (sent a few seconds after spawn) passes the hub gate (#17).
	a.syncHubObservers(teamName, observers)

	// Subscribe to room events
	if client := a.hubClient.Load(); client != nil {
		if err := client.Subscribe([]string{teamName}); err != nil {
			log.Printf("[HUB] Subscribe failed for room=%s: %v", teamName, err)
		}
	}

	// Ensure MCP server binary is ready and configured for the selected CLI
	ct := cli.CLIType(cliType)
	if ct != cli.CLIShell && cliType != "" {
		if err := cli.EnsureMCPServerBinary(mcpServerBin, a.dataDir); err != nil {
			log.Printf("MCP server setup failed: %v", err)
		}
		if err := cli.EnsureMCPConfig(ct, a.dataDir, teamName); err != nil {
			log.Printf("MCP config setup failed for %s: %v", cliType, err)
		}
	}

	// Get command for CLI type. #40: when resuming (resumeID set + CLI supports it)
	// build the resume invocation instead of a fresh launch. Everything downstream
	// (Copilot -i, startup prompt, ingest) is unchanged so the resumed agent still
	// re-joins the room.
	resuming := resumeID != "" && cli.ResumeSupported(ct)
	var cmdName string
	var cmdArgs []string
	if resuming {
		cmdName, cmdArgs = cli.GetCommandResume(ct, resumeID)
		log.Printf("[RESUME] agent=%s cli=%s id=%s", agentName, cliType, resumeID)
	} else {
		cmdName, cmdArgs = cli.GetCommand(ct)
	}

	// For Copilot, use -i flag to pass startup prompt directly as argument.
	// copilotComposed is hoisted so it can be fingerprinted for ingestion below
	// (#65) — Copilot records the -i prompt as the first user message.
	var copilotComposed string
	if ct == cli.CLICopilot && agentName != "" {
		copilotComposed = a.composeAgentPrompt(teamID, agentName, promptID, mode)
		if copilotComposed != "" {
			cmdArgs = append(cmdArgs, "-i", copilotComposed)
			log.Printf("[STARTUP] Copilot: using -i flag, promptLen=%d", len(copilotComposed))
		}
	}

	env := []string{
		"AGENT_CHAT_DATA_DIR=" + a.dataDir,
		"AGENT_CHAT_ROOM=" + teamName,
		"TERM=xterm-256color",
	}

	// Capture the ingestion spawn time BEFORE starting the CLI process, so the
	// session file the CLI is about to create is strictly newer than this and
	// discovery can't lock onto a prior session's file in the same cwd (#65).
	ingestSpawnedAt := time.Now().UnixNano()

	// Pin the room (teamName) on the session — the SAME value set as AGENT_CHAT_ROOM
	// above — so logged prompts always target the room the agent's MCP session is
	// actually in, even after a later team rename (#58).
	sessionID, err := a.ptyManager.Create(teamID, agentName, teamName, workDir, env, cmdName, cmdArgs, cliType)
	if err != nil {
		// Rollback worktree only if we newly created it (not reused)
		if wtNewlyCreated && wtDir != "" && origWorkDir != "" {
			mu := a.worktreeMu(wtDir)
			mu.Lock()
			if rmErr := git.RemoveWorktree(origWorkDir, wtDir); rmErr != nil {
				log.Printf("[WORKTREE] Rollback failed after PTY error: %v", rmErr)
			} else {
				log.Printf("[WORKTREE] Rolled back worktree after PTY error: %s", wtDir)
			}
			mu.Unlock()
		}
		return "", err
	}

	// Store promptID and worktree info for restart
	if s := a.ptyManager.GetSession(sessionID); s != nil {
		s.PromptID = promptID
		s.SlotIndex = slotIndex
		if wtDir != "" {
			s.WorktreeDir = wtDir
			s.WorktreeRepo = origWorkDir
		} else if resumeWtDir != "" {
			// #40 Faz-2: resuming directly into a managed worktree (no fresh creation) —
			// restore its metadata so Close/Restart can still clean it up (Codex P2).
			s.WorktreeDir = resumeWtDir
			s.WorktreeRepo = resumeWtRepo
		}
	}

	// Persist the agent config to the team template (auto-upsert). When a worktree
	// was created, workDir was reassigned to the worktree path above; persist the
	// user-selected ORIGINAL repo dir (origWorkDir) instead so reopening doesn't
	// nest a worktree inside a worktree. Role is intentionally omitted — UpsertAgent
	// preserves any Role the user set earlier.
	//
	// #40 Faz-2: persist the config workDir (persistWorkDir), NOT the resume launch
	// override — so resuming an EXISTING agent doesn't rewrite its config to the
	// historical cwd, while a history-only agent the user just picked in SetupWizard (no
	// existing config) STILL gets saved to the team. useWorktree here is the role-adjusted
	// value (resume only touched workDir, never the worktree flag) (Codex P2).
	if teamID != "" {
		cfgWorkDir := persistWorkDir
		if origWorkDir != "" {
			cfgWorkDir = origWorkDir
		}
		// When reopening an existing worktree directly (restart), workDir points
		// into our worktrees dir and origWorkDir is empty. The config was already
		// captured correctly on the initial create, so skip persistence to avoid
		// overwriting it with the worktree path + useWorktree=false. Use filepath.Rel
		// (not raw string prefix) for a robust, normalized subdirectory check.
		worktreesRoot := filepath.Join(a.dataDir, "worktrees")
		isWorktreePath := false
		if cfgWorkDir != "" {
			if rel, err := filepath.Rel(worktreesRoot, cfgWorkDir); err == nil && !strings.HasPrefix(rel, "..") {
				isWorktreePath = true
			}
		}
		if origWorkDir == "" && isWorktreePath {
			log.Printf("[TEAM] CreateTerminal: agent=%s worktree dizininde yeniden açıldı, config korunuyor", agentName)
		} else if _, err := a.teamStore.UpsertAgent(teamID, team.AgentConfig{
			Name:        agentName,
			PromptID:    promptID,
			WorkDir:     cfgWorkDir,
			CLIType:     cliType,
			SlotIndex:   slotIndex,
			UseWorktree: persistUseWorktree,
		}); err != nil {
			log.Printf("[TEAM] UpsertAgent failed for agent=%s team=%s: %v", agentName, teamID, err)
		}
	}

	// Register agent session for orchestrator (using room name). An observer is
	// deliberately NOT registered (#17): the orchestrator's broadcast path notifies
	// every registered session, and an observer must receive no automatic PTY
	// notifications (it is user-driven). Skipping registration is the cleanest
	// isolation — the broadcast loop never sees the observer's session.
	if agentName != "" && !isObserver {
		a.orchestrator.RegisterAgent(teamName, agentName, sessionID)
	}

	// #65: ingest the CLI's own session file so messages the user types DIRECTLY
	// into the terminal are logged to the room transcript. Non-AI shells are skipped;
	// observers get a CLAIM-ONLY (muted) watcher below — it claims their file so a
	// sibling same-cwd watcher can't ingest the observer's private prompts, but never
	// emits them (#17). workDir here is the final PTY cwd (the worktree dir if one was
	// set up) — the CLI keys its session file by exactly this directory.
	if ad := ingest.AdapterFor(cliType); ad != nil {
		room, agent := teamName, agentName
		// Effective cwd: when the Workspace field is blank, pty.Manager.Create leaves
		// cmd.Dir unset so the CLI runs in the app's working directory — the
		// cwd-derived adapters (Claude slug / Gemini sha256) must look there, not under
		// an empty path (#65 / Codex P2).
		ingestCwd := workDir
		if ingestCwd == "" {
			if wd, werr := os.Getwd(); werr == nil {
				ingestCwd = wd
			}
		}
		// ready: only ingest while the hub is connected, so a prompt parsed while the
		// hub is restarting isn't parsed-and-dropped (the cursor stays put) (#65).
		ready := func() bool { return a.hubClient.Load() != nil }
		// #40: on resume, skip the prior transcript a same-file CLI (Copilot) appends
		// to — snapshotted now, before the CLI writes, so a prompt typed right after
		// resume isn't skipped. nil for fresh creates and for CLIs that resume into a
		// new file (Claude/Codex), which need no seed.
		var resumeSeed *ingest.ResumeSeed
		if resuming {
			resumeSeed = ingest.ResumeSeedFor(cliType, resumeID)
		}
		// exited closes when this terminal's CLI process dies (incl. an in-CLI /exit
		// with no app-side close), so the watcher stops and frees its file claim (#65).
		a.ingestMgr.StartSession(sessionID, ad, ingestCwd, ingestSpawnedAt, ready, a.ptyManager.SessionDone(sessionID), func(content, ts string) bool {
			client := a.hubClient.Load()
			if client == nil {
				return false // hub down — keep the cursor, retry next tick (#65)
			}
			// Convert the CLI file's RFC3339/UTC timestamp into the hub's canonical
			// local layout so the ingested message sorts correctly in the
			// lexically-ordered transcript (#65).
			if err := client.LogMessage(room, agent, content, types.NormalizeTimestamp(ts)); err != nil {
				log.Printf("[INGEST] mesaj loglanamadı (agent=%s): %v", agent, err)
				return false // delivery failed — don't advance the cursor past this message
			}
			return true
		}, func(id string) {
			// #40: capture the CLI's session ID for opt-in resume. Only for CLIs
			// whose native resume we support — others (Gemini/shell) never enable
			// the "Devam Et" button.
			if !cli.ResumeSupported(ct) {
				return
			}
			a.ptyManager.SetCLISessionID(sessionID, id)
			// #40 Faz-2: also log to the persistent history so this session can be
			// resumed/correlated later (room/agent/cliType/cwd from the enclosing
			// createTerminal scope).
			a.sessionLog.Record(id, room, agentName, cliType, ingestCwd)
			runtime.EventsEmit(a.ctx, "terminal:resume-available", map[string]string{
				"sessionID":    sessionID,
				"cliSessionID": id,
			})
		}, resumeSeed)
		if isObserver {
			// Claim-only: the watcher holds the observer's file claim (sibling
			// same-cwd watchers skip it) but discards every message (#17/#65 P1).
			a.ingestMgr.Mute(sessionID)
		} else if copilotComposed != "" {
			// Copilot's startup prompt is the -i launch arg (recorded as its first user
			// message); record it so ingestion suppresses the CLI's copy. Other CLIs get
			// the startup prompt via sendStartupPrompt, which records it there.
			a.ingestMgr.RecordInjection(sessionID, copilotComposed)
		}
		// #40 Faz-2: close this session's history open-window when its PTY dies, INCLUDING
		// an in-CLI /exit or crash that never routes through closeTerminalInternal. Without
		// it the window stays at discovery time (and a later app-quit Touch would wrongly
		// extend it to quit time) (Codex P2). GetCLISessionID is "" until captured, so this
		// no-ops for a too-early exit; CapturedSessionIDs skips already-dead sessions so the
		// shutdown Touch can't re-extend this one.
		if cli.ResumeSupported(ct) && a.sessionLog != nil {
			// Skip the watcher entirely when the history store failed to init (nil) — the
			// goroutine's only job is sessionLog.Touch, so it would do nothing but leak
			// (Gemini). nil-guard on doneCh too: SessionDone returns nil for an unknown
			// session, and <-nil blocks forever. The session was just created so it's
			// non-nil, but guard defensively (Gemini).
			if doneCh := a.ptyManager.SessionDone(sessionID); doneCh != nil {
				go func(sid string, done <-chan struct{}) {
					<-done
					if cid := a.ptyManager.GetCLISessionID(sid); cid != "" {
						// Pin to the exit time (just stamped before done closed), so it matches
						// what a later UI-close records and the window can't drift (Codex P2).
						if exitedAt, exited := a.ptyManager.SessionExitedAt(sid); exited {
							a.sessionLog.TouchAt(cid, exitedAt)
						} else {
							a.sessionLog.Touch(cid)
						}
					}
				}(sessionID, doneCh)
			}
		}
	}

	// Send startup prompt in background
	go a.sendStartupPrompt(sessionID, teamID, agentName, cliType, promptID, mode)

	return sessionID, nil
}

// RestartTerminal closes a terminal and creates a fresh one with the same
// parameters (no resume).
func (a *App) RestartTerminal(sessionID string) (string, error) {
	return a.restartInternal(sessionID, "")
}

// ResumeTerminal restarts a terminal resuming its captured CLI session (#40). If
// nothing was captured (the CLI hasn't written its session file yet, or it is an
// unsupported CLI), it falls back to a fresh restart so the user still gets a
// working terminal.
func (a *App) ResumeTerminal(sessionID string) (string, error) {
	resumeID := a.ptyManager.GetCLISessionID(sessionID)
	cliType := ""
	if s := a.ptyManager.GetSession(sessionID); s != nil {
		cliType = s.CLIType
	}
	if resumeID == "" || !cli.ResumeSupported(cli.CLIType(cliType)) {
		log.Printf("[RESUME] session=%s — yakalı oturum yok, düz restart", ptymgr.ShortID(sessionID))
		return a.restartInternal(sessionID, "")
	}
	return a.restartInternal(sessionID, resumeID)
}

// restartInternal closes a terminal and recreates it, optionally resuming from
// resumeID. If the terminal was using a worktree, the worktree is preserved.
func (a *App) restartInternal(sessionID, resumeID string) (string, error) {
	session := a.ptyManager.GetSession(sessionID)
	if session == nil {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}

	// Capture restart params before closing
	teamID := session.TeamID
	agentName := session.AgentName
	workDir := session.WorkDir
	cliType := session.CLIType
	promptID := session.PromptID
	slotIndex := session.SlotIndex
	wtDir := session.WorktreeDir
	wtRepo := session.WorktreeRepo

	// A worktree-backed agent promoted to manager must restart in the MAIN repo, not
	// the worktree (managers always run in main repo — see the isManager guard in
	// CreateTerminal). For non-managers, reopen the existing worktree DIRECTLY (run
	// the PTY in wtDir, useWorktree=false): recreating it via useWorktree=true would
	// call git.CreateWorktree, which rejects a worktree whose branch has drifted from
	// agent/<team>/<agent> — and since the old PTY is already closed below, that would
	// leave the user with no terminal. The team config was already captured correctly
	// on the initial create, and CreateTerminal skips re-persisting a worktree path
	// (see its persist block), so it isn't corrupted.
	// Manager AND observer always run from the main repo, never a worktree (#17):
	// without this an observer promoted from a worktree-backed agent would restart
	// inside its stale worktree because workDir was captured as wtDir (Codex P2).
	mainRepoRole := false
	if teamID != "" {
		if t, err := a.teamStore.Get(teamID); err == nil {
			mainRepoRole = t.IsManagerAgent(agentName)
		}
		mainRepoRole = mainRepoRole || a.isObserverAgent(teamID, agentName)
	}
	workDir = restartWorkDir(mainRepoRole, workDir, wtDir, wtRepo)

	// Close PTY but do NOT cleanup worktree (it will be reused)
	if err := a.closeTerminalInternal(sessionID, false); err != nil {
		return "", fmt.Errorf("eski session kapatılamadı %s: %w", ptymgr.ShortID(sessionID), err)
	}

	// On RESUME, a same-file CLI (Copilot) reopens the SAME transcript the old
	// watcher was on. Wait (bounded) for that watcher to finish its final drain and
	// release its claim BEFORE the resumed CLI spawns, so it can't ingest the new
	// session's bootstrap prompt under the old fingerprint store and log it as a
	// user message (#40, Codex round-3). Skipped for plain restart (new file, no
	// shared watcher) to avoid needless latency.
	if resumeID != "" {
		a.ingestMgr.StopAndWait(sessionID, 2*time.Second)
	}

	log.Printf("[RESTART] Restarting terminal: agent=%s cli=%s team=%s", agentName, cliType, teamID)

	newSessionID, err := a.createTerminal(teamID, agentName, workDir, cliType, promptID, false, slotIndex, resumeID)
	if err != nil {
		return "", err
	}

	// Restore worktree info on the new session so future restarts still find it.
	if s := a.ptyManager.GetSession(newSessionID); s != nil && wtDir != "" {
		s.WorktreeDir = wtDir
		s.WorktreeRepo = wtRepo
	}

	return newSessionID, nil
}

// OpenTeamResult is one row returned by OpenTeamFromConfig: the agent it tried to
// open, plus either SessionID (success) or Error (failure/skipped). Empty strings
// mark the absent one. Wails generates a typed TS interface from this struct, so
// the frontend reads SlotIndex as a number without string conversion.
type OpenTeamResult struct {
	AgentName string `json:"agentName"`
	CLIType   string `json:"cliType"`
	SlotIndex int    `json:"slotIndex"`
	SessionID string `json:"sessionID"`
	Error     string `json:"error"`
}

// OpenTeamFromConfig opens every terminal saved in a team's template — one per
// stored agent config — in slot order, returning one OpenTeamResult per agent.
// A per-agent failure does NOT abort the batch: that agent is skipped and its
// error is reported so the user keeps the remaining terminals.
func (a *App) OpenTeamFromConfig(teamID string) ([]OpenTeamResult, error) {
	return a.openTeamFromConfig(teamID, nil)
}

// OpenTeamFromConfigResume opens a team from config like OpenTeamFromConfig but
// resumes each agent from a chosen past session (resumeIDs[agentName]) when present.
// It reuses the SAME ordering/capacity/observer-phantom guards, so the resume picker
// can't open over-capacity, duplicate-slot, or phantom agents the modal would
// otherwise launch raw (#40 Faz-2, Codex P2). A nil/partial map or empty entry opens
// that agent fresh. Per-agent failures are skipped (reported in results), so a retry
// after partial success doesn't duplicate the agents that already opened (Codex P2).
func (a *App) OpenTeamFromConfigResume(teamID string, resumeIDs map[string]string) ([]OpenTeamResult, error) {
	return a.openTeamFromConfig(teamID, resumeIDs)
}

func (a *App) openTeamFromConfig(teamID string, resumeIDs map[string]string) ([]OpenTeamResult, error) {
	t, err := a.teamStore.Get(teamID)
	if err != nil {
		return nil, err
	}
	if len(t.Agents) == 0 {
		return nil, fmt.Errorf("takımda kayıtlı agent yapılandırması yok")
	}

	ordered := team.AgentsInOpenOrder(t.Agents)
	// Don't launch agents that fall outside the current fixed grid: a fixed grid
	// only renders slots 0..capacity-1, so an over-capacity agent would spawn a PTY
	// that can't be shown or closed from the UI. capacity < 0 means unlimited
	// (custom layout), so nothing is skipped there.
	capacity := team.GridCapacity(t.GridLayout)
	results := make([]OpenTeamResult, 0, len(ordered))
	for _, cfg := range ordered {
		res := OpenTeamResult{AgentName: cfg.Name, CLIType: cfg.CLIType, SlotIndex: cfg.SlotIndex}
		// Skip an observer phantom: SetTeamObserver pre-persists {Name, Role:observer}
		// before CreateTerminal fills in the CLIType. If that create failed, reopening
		// the phantom would launch an unintended login shell in its slot (Codex P2).
		// Narrowed to the observer-role+blank-CLI case so legitimate blank-CLI shells
		// (legacy/manual configs) still reopen.
		if isObserverPhantom(cfg) {
			res.Error = "yarım kalan observer config (CLI tipi yok) — atlandı"
			log.Printf("[TEAM] OpenTeamFromConfig: agent=%s observer phantom (CLIType boş), atlandı", cfg.Name)
			results = append(results, res)
			continue
		}
		if capacity >= 0 && cfg.SlotIndex >= capacity {
			res.Error = fmt.Sprintf("slot %d, %s grid kapasitesini (%d) aşıyor — atlandı", cfg.SlotIndex, t.GridLayout, capacity)
			log.Printf("[TEAM] OpenTeamFromConfig: agent=%s slot=%d > capacity=%d (%s), atlandı", cfg.Name, cfg.SlotIndex, capacity, t.GridLayout)
			results = append(results, res)
			continue
		}
		// Idempotent per slot: if a terminal already occupies this agent's slot (e.g. a
		// retry after a partial-failure batch), return the existing one instead of
		// spawning a duplicate — making the documented "no-duplicate on retry" behavior
		// real, not just UI-enforced by the modal closing (Gemini).
		existingID := ""
		for _, s := range a.ptyManager.GetSessionsByTeam(teamID) {
			if s.SlotIndex == cfg.SlotIndex {
				existingID = s.ID
				break
			}
		}
		if existingID != "" {
			res.SessionID = existingID
			results = append(results, res)
			continue
		}
		// resumeIDs[cfg.Name] is "" for a nil/absent entry (fresh). The picker filters
		// sessions to the agent's configured CLI, so the id always matches cfg.CLIType.
		sessionID, err := a.createTerminal(teamID, cfg.Name, cfg.WorkDir, cfg.CLIType, cfg.PromptID, cfg.UseWorktree, cfg.SlotIndex, resumeIDs[cfg.Name])
		if err != nil {
			res.Error = err.Error()
			log.Printf("[TEAM] OpenTeamFromConfig: agent=%s team=%s failed: %v", cfg.Name, teamID, err)
		} else {
			res.SessionID = sessionID
		}
		results = append(results, res)
	}
	return results, nil
}

// composeAgentPrompt builds the startup prompt for an agent without sending it.
// agentMode (#17) is "manager", "observer", or "" and selects which role prompt
// (if any) is appended and how the join instruction is framed.
func (a *App) composeAgentPrompt(teamID, agentName, promptID, agentMode string) string {
	if agentName == "" {
		return ""
	}

	basePrompt := a.readEmbeddedPrompt("prompts/base_prompt.md")
	globalPromptPath := filepath.Join(a.dataDir, "global_prompt.md")
	globalPrompt, err := os.ReadFile(globalPromptPath)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("[PROMPT] global_prompt.md okunamadı: %v", err)
	}

	var teamPrompt string
	var teamName string
	var agentRole string
	if t, err := a.teamStore.Get(teamID); err == nil {
		teamName = t.Name
		teamPrompt = t.CustomPrompt
		normalizedAgent := strings.TrimSpace(agentName)
		for _, cfg := range t.Agents {
			if strings.EqualFold(strings.TrimSpace(cfg.Name), normalizedAgent) {
				agentRole = strings.TrimSpace(cfg.Role)
				break
			}
		}
	}

	var selectedPrompt string
	if promptID != "" {
		if p, err := a.promptStore.Get(promptID); err == nil {
			selectedPrompt = p.Content
		}
	}

	// Append the role prompt for manager/observer. Both fold into the selected
	// prompt slot the same way (replace-if-empty, else append-if-absent).
	var rolePromptFile string
	switch agentMode {
	case "manager":
		rolePromptFile = "prompts/manager_prompt.md"
	case "observer":
		rolePromptFile = "prompts/observer_prompt.md"
	}
	if rolePromptFile != "" {
		roleText := strings.TrimSpace(string(a.readEmbeddedPrompt(rolePromptFile)))
		if roleText != "" {
			if strings.TrimSpace(selectedPrompt) == "" {
				selectedPrompt = roleText
			} else if !strings.Contains(selectedPrompt, roleText) {
				selectedPrompt = strings.TrimSpace(selectedPrompt) + "\n\n" + roleText
			}
		}
	}

	// #29: inject the previous session's summary (if any) as its own segment after
	// the charter, so a continuing agent inherits prior context. Best-effort — a
	// read failure must never block agent startup. The summary is keyed by room
	// name (Team.Name, or "default" for the default room).
	summaryRoom := teamName
	if summaryRoom == "" {
		summaryRoom = "default"
	}
	var roomSummary string
	if doc, ok, serr := summary.Latest(a.dataDir, summaryRoom); serr != nil {
		log.Printf("[PROMPT] oda özeti okunamadı (%s): %v", summaryRoom, serr)
	} else if ok {
		roomSummary = doc.Text
	}

	return cli.ComposeStartupPrompt(string(basePrompt), string(globalPrompt), teamPrompt, roomSummary, selectedPrompt, agentName, agentRole, teamName, agentMode)
}

// sendStartupPrompt sends the initial prompt to a CLI agent
func (a *App) sendStartupPrompt(sessionID, teamID, agentName, cliType, promptID, agentMode string) {
	if cliType == "" || cliType == "shell" || cliType == "copilot" || agentName == "" {
		return
	}

	// Wait for CLI to become idle
	switch cliType {
	case "gemini":
		time.Sleep(5 * time.Second)
	default:
		time.Sleep(3 * time.Second)
	}
	idle := a.ptyManager.WaitForIdle(sessionID, 2*time.Second, 25*time.Second)
	log.Printf("[STARTUP] WaitForIdle: cli=%s agent=%s idle=%v", cliType, agentName, idle)

	composed := a.composeAgentPrompt(teamID, agentName, promptID, agentMode)
	if composed == "" {
		return
	}
	// Record the startup prompt so ingestion (#65) suppresses the copy the CLI
	// writes to its session file (it is our bootstrap, not the user's message).
	a.ingestMgr.RecordInjection(sessionID, composed)

	log.Printf("[STARTUP] Sending prompt to cli=%s agent=%s session=%s promptLen=%d",
		cliType, agentName, ptymgr.ShortID(sessionID), len(composed))

	// Claude/Gemini: bracketed paste. The paste block is a single (atomic) Write;
	// the 200ms settle then runs OUTSIDE any held lock so a user keystroke during
	// startup is never blocked. There's no conditional-CR here (the startup prompt
	// must always submit), so the two writes don't need to be one atomic block.
	const (
		bracketOpen  = "\x1b[200~"
		bracketClose = "\x1b[201~"
	)
	if err := a.ptyManager.Write(sessionID, []byte(bracketOpen+composed+bracketClose)); err != nil {
		log.Printf("[STARTUP] prompt write error cli=%s agent=%s: %v", cliType, agentName, err)
		return
	}
	time.Sleep(200 * time.Millisecond)
	// Submit the prompt AND clear the pending-input flag atomically under the
	// session write mutex. A separate Write("\r")+ClearUserInput pair leaves a
	// window where a concurrent user keystroke can set the flag between them and
	// then be wrongly cleared here, letting a later notification inject into the
	// user's freshly typed line (review CXF3 — same class as CX4). WriteUserInput
	// with submit=true writes the CR and clears the flag as one locked operation.
	// (The startup CR submits the line, so if the user typed during the startup
	// wait that input was submitted with the prompt — the buffer ends up empty,
	// review CR2.)
	if err := a.ptyManager.WriteUserInput(sessionID, []byte("\r"), true); err != nil {
		log.Printf("[STARTUP] prompt CR write error cli=%s agent=%s: %v", cliType, agentName, err)
	}
}

// WriteToTerminal writes data to a terminal
func (a *App) WriteToTerminal(sessionID, data string) error {
	session := a.ptyManager.GetSession(sessionID)
	if session != nil && session.CLIType == "copilot" {
		// Filter Focus Out events
		if data == "\x1b[O" {
			return nil
		}
		raw := []byte(data)
		log.Printf("[USER-INPUT] copilot agent=%s len=%d hex=%x ascii=%q",
			session.AgentName, len(raw), raw, data)
	}

	// Focus events are not user typing — write them without touching the pending
	// flag.
	if data == "\x1b[I" || data == "\x1b[O" {
		return a.ptyManager.Write(sessionID, []byte(data))
	}

	// Track pending user input so the orchestrator can defer notification
	// injection and avoid splitting a half-typed line (issue #15). Enter (CR/LF)
	// and Ctrl+C (\x03) submit/clear the line; anything else is pending input.
	// WriteUserInput writes the bytes and updates the flag together under the
	// session write mutex, so concurrent keystrokes can't desync the flag from
	// the write ordering (review C5/G1/CX4).
	submit := data == "\x03" || strings.HasSuffix(data, "\r") || strings.HasSuffix(data, "\n")
	return a.ptyManager.WriteUserInput(sessionID, []byte(data), submit)
}

// BroadcastToTeam injects the same text into every agent terminal of a team at
// once, as if the user typed it into each one. It is NOT a chat message: the text
// goes straight to each PTY's input line (raw fan-out, hub bypass) so it never
// appears in room history. With submit=false (the UI default) the text is left
// pending for the user to confirm in each terminal; submit=true also presses
// Enter. Observer agents (#17) are excluded. Per-session failures are logged but
// never abort the broadcast — one dead PTY must not cancel the rest.
// maxBroadcastChars caps a broadcast's length, mirroring BroadcastBar's
// textarea maxLength. Enforced server-side too (defense-in-depth) so the bound
// doesn't depend on a single frontend attribute — and so the copilot
// char-by-char path (which holds the session write mutex ~5ms/char) can't be
// driven to lock a terminal for an unbounded time.
const maxBroadcastChars = 1000

func (a *App) BroadcastToTeam(teamID, text string, submit bool) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("broadcast metni boş olamaz")
	}
	if utf8.RuneCountInString(text) > maxBroadcastChars {
		return fmt.Errorf("broadcast metni çok uzun (en fazla %d karakter) ✂️", maxBroadcastChars)
	}
	sessions := a.ptyManager.GetSessionsByTeam(teamID)
	if len(sessions) == 0 {
		return fmt.Errorf("takımda açık terminal yok 📭")
	}

	roleOf := a.broadcastRoleLookup(teamID)
	// Record broadcast fingerprints BEFORE the fan-out (when submitting): a fast
	// target's CLI can record the message and be polled by the 700ms watcher while
	// a slower target is still being injected, so recording after the whole fan-out
	// leaves a double-log window (logTeamBroadcast + ingestion). An unconsumed
	// fingerprint (a target that fails injection) is harmless — it's dropped when
	// the terminal closes (#65 / Codex P2).
	if submit {
		for _, s := range sessions {
			if isAICLIType(s.CLIType) {
				a.ingestMgr.RecordInjection(s.ID, text)
			}
		}
	}
	injected, errs, aiDelivered := broadcastToSessions(sessions, text, submit, roleOf, a.ptyManager.InjectText)

	log.Printf("[BROADCAST] team=%s injected=%d/%d submit=%v errors=%d",
		teamID, injected, len(sessions), submit, len(errs))
	for _, e := range errs {
		log.Printf("[BROADCAST] session error: %s", e)
	}

	// Partial failure (some PTYs got it, some didn't) is NOT a whole-broadcast
	// error — the text still cleared on most terminals and re-sending would
	// double-inject into the ones that succeeded. Surface it as a non-blocking
	// advisory instead (mirrors the worktree:dirty / notification:deferred notice
	// pattern) so the user learns which agents were missed.
	if injected > 0 && len(errs) > 0 {
		runtime.EventsEmit(a.ctx, "broadcast:partial", map[string]interface{}{
			"teamID":   teamID,
			"injected": injected,
			"total":    len(sessions),
			"errors":   errs,
		})
	}

	// #29: record the broadcast in the room transcript (as a user_prompt to "all")
	// so it feeds the session summary — but ONLY on a submitted delivery where every
	// AI target received it (aiDelivered). submit=false (the UI default) just leaves
	// draft text the user may edit or never send; aiDelivered is false when no AI
	// participant exists (shell-only team) or any AI target failed, while a shell-
	// only failure no longer suppresses logging a broadcast all agents received.
	if submit && aiDelivered {
		// Log to the room pinned on the broadcast's sessions, not the (mutable)
		// current team name (#58). Fingerprints were already recorded above (before
		// the fan-out) so ingestion suppresses each CLI's recorded copy (#65).
		a.logTeamBroadcast(pinnedRoomForSessions(sessions, a.roomForTeam(teamID)), text)
	}
	return broadcastOutcomeError(injected, errs)
}

// logUserPrompt records a human→agent prompt the app delivered to a single
// terminal into the room transcript as a user_prompt (#29), so the user's
// instructions feed the session summary. Best-effort: it never blocks or fails
// the actual delivery, and is skipped for non-agent (plain shell) terminals.
func (a *App) logUserPrompt(sessionID, content string) {
	// Capture the hub client before spawning: the goroutine must not Load the
	// reassignable a.hubClient field (monitorHub swaps it on hub-crash recovery),
	// which would read a possibly-stale pointer mid-flight.
	client := a.hubClient.Load()
	if client == nil {
		return
	}
	sess := a.ptyManager.GetSession(sessionID)
	if sess == nil || sess.AgentName == "" {
		return
	}
	// Only real AI agents (MCP room participants) join the room; a plain shell — or
	// a legacy/empty CLI type that falls through to the login shell — never does, so
	// a prompt sent to it isn't room agent traffic and must not be logged (#29).
	if !isAICLIType(sess.CLIType) {
		return
	}
	// Observer sessions are the user's PRIVATE drafting/discussion space (#17). Their
	// prompts must NOT enter the room transcript, or the private draft could be
	// summarized and injected into worker agents — defeating the point of the role.
	if a.isObserverAgent(sess.TeamID, sess.AgentName) {
		return
	}
	// Fire-and-forget: this is best-effort summary bookkeeping and LogMessage is a
	// synchronous 15s hub RPC — it must not block/delay the already-delivered send.
	// Tracked by promptLogWG so GetRoomTranscript can drain in-flight logs first.
	room, agent := a.logRoomForSession(sess), sess.AgentName
	// Stamp the delivery moment NOW, synchronously — the prompt was just written to
	// the agent's PTY, so this precedes any reply the agent can produce. Letting the
	// hub stamp on (delayed) RPC arrival could order the prompt AFTER that reply in
	// the timestamp-sorted transcript (#58).
	ts := types.Timestamp()
	a.promptLogN.Add(1)
	go func() {
		defer a.promptLogN.Add(-1)
		if err := client.LogMessage(room, agent, content, ts); err != nil {
			log.Printf("[SUMMARY] prompt loglanamadı (agent=%s): %v", agent, err)
		}
	}()
}

// logTeamBroadcast records a user broadcast (fan-out to all agents) as a single
// user_prompt addressed to "all" (#29). room is the pre-resolved pinned room (#58).
func (a *App) logTeamBroadcast(room, content string) {
	// Capture the hub client before spawning (see logUserPrompt): the goroutine
	// must not Load the reassignable a.hubClient field.
	client := a.hubClient.Load()
	if client == nil {
		return
	}
	// Fire-and-forget (see logUserPrompt): summary bookkeeping must not block a
	// broadcast that already reached every agent. Tracked by promptLogN.
	// Stamp the delivery moment synchronously (see logUserPrompt) — the broadcast
	// already reached every agent's PTY, so this precedes any reply (#58).
	ts := types.Timestamp()
	a.promptLogN.Add(1)
	go func() {
		defer a.promptLogN.Add(-1)
		if err := client.LogMessage(room, "all", content, ts); err != nil {
			log.Printf("[SUMMARY] broadcast loglanamadı (room=%s): %v", room, err)
		}
	}()
}

// drainPromptLogs waits for in-flight prompt-log goroutines to reach the hub,
// bounded by timeout so a stalled/wedged hub can't hang the caller (transcript
// generation). Best-effort: on timeout it returns and the read proceeds.
func (a *App) drainPromptLogs(timeout time.Duration) {
	// Poll the atomic in-flight counter rather than WaitGroup.Wait(): new log
	// goroutines may start (Add) concurrently with this drain, which a WaitGroup
	// forbids racing with Wait (can panic). Bounded by timeout.
	deadline := time.Now().Add(timeout)
	for a.promptLogN.Load() > 0 {
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// roomForTeam resolves a team ID to its room name, falling back to the default
// room — matching CreateTerminal's derivation and composeAgentPrompt's summary
// lookup so logged prompts land in the same room as agent traffic.
func (a *App) roomForTeam(teamID string) string {
	if teamID != "" {
		if t, err := a.teamStore.Get(teamID); err == nil && t.Name != "" {
			return t.Name
		}
	}
	return "default"
}

// logRoomForSession resolves the room a session's logged prompt should land in:
// the room PINNED on the session at creation (the same value as its
// AGENT_CHAT_ROOM), so a mid-life team rename can't reroute the log to a room the
// agent's MCP session isn't in. Falls back to the team's current room only for
// legacy sessions created before pinning (#58).
func (a *App) logRoomForSession(sess *ptymgr.PTYSession) string {
	if sess.Room != "" {
		return sess.Room
	}
	return a.roomForTeam(sess.TeamID)
}

// pinnedRoomForSessions returns the room pinned on the broadcast's sessions (all
// sessions of a team normally share it), falling back when none is pinned (legacy
// sessions). So a team rename can't reroute a logged broadcast either (#58).
func pinnedRoomForSessions(sessions []*ptymgr.PTYSession, fallback string) string {
	for _, s := range sessions {
		if s != nil && s.Room != "" {
			return s.Room
		}
	}
	return fallback
}

// broadcastOutcomeError reports a broadcast as failed only when EVERY target
// errored (injected == 0 with at least one error) — so the UI keeps the user's
// typed text and surfaces the failure instead of silently clearing it. A
// zero-injection run with no errors (e.g. every agent is an observer) is a no-op,
// not an error.
func broadcastOutcomeError(injected int, errs []string) error {
	if injected == 0 && len(errs) > 0 {
		return fmt.Errorf("hiçbir terminale gönderilemedi: %s", strings.Join(errs, "; "))
	}
	return nil
}

// broadcastToSessions injects text into every session that should receive a
// broadcast, skipping observer-role agents (#17-forward-wired: no agent has the
// observer role today, so the filter is currently a no-op). Per-session inject
// errors are collected but never abort the fan-out. Returns the number of
// sessions injected and any per-session error strings.
func broadcastToSessions(
	sessions []*ptymgr.PTYSession,
	text string,
	submit bool,
	roleOf func(agentName string) string,
	inject func(sessionID, text string, submit bool) error,
) (injected int, errs []string, aiDelivered bool) {
	// Inject into every target concurrently: each session has its own PTY fd and
	// write mutex, so parallel writes don't contend, and the slow paths (copilot's
	// per-char sleeps, submit=true's 200ms settle) no longer serialize on the
	// Wails IPC goroutine — wall-clock becomes the slowest single session instead
	// of the sum (review: Gemini HIGH). Each goroutine writes its own outcomes
	// slot (distinct indices → race-free; wg.Wait establishes the happens-before),
	// so no shared counter/slice needs locking and error order stays deterministic.
	type outcome struct {
		agentName string
		err       error
		skipped   bool
	}
	outcomes := make([]outcome, len(sessions))
	var wg sync.WaitGroup
	for i, s := range sessions {
		if isObserverRole(roleOf(s.AgentName)) {
			outcomes[i].skipped = true
			continue
		}
		wg.Add(1)
		go func(i int, sess *ptymgr.PTYSession) {
			defer wg.Done()
			outcomes[i] = outcome{agentName: sess.AgentName, err: inject(sess.ID, text, submit)}
		}(i, s)
	}
	wg.Wait()

	aiTotal, aiOK := 0, 0
	for i, o := range outcomes {
		if o.skipped {
			continue
		}
		// Only real AI agents (MCP participants) count toward broadcast delivery for
		// the summary; a plain shell or an empty/legacy CLI type does not (#29).
		isAI := isAICLIType(sessions[i].CLIType)
		if o.err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", o.agentName, o.err))
		} else {
			injected++
		}
		if isAI {
			aiTotal++
			if o.err == nil {
				aiOK++
			}
		}
	}
	// aiDelivered: at least one AI target existed and every AI target received the
	// broadcast (a shell-only failure no longer suppresses logging).
	aiDelivered = aiTotal > 0 && aiOK == aiTotal
	return injected, errs, aiDelivered
}

// isObserverRole reports whether a team role designates an observer agent, which
// must be excluded from broadcasts (#17). Compared case-insensitively so the
// observer feature can store the role in any casing.
func isObserverRole(role string) bool {
	return strings.EqualFold(strings.TrimSpace(role), "observer")
}

// isAICLIType reports whether a CLI type is an MCP room participant (a real AI
// agent that runs join_room), as opposed to a plain shell or an empty/unknown
// type (login-shell fallback, no MCP startup). #29 records prompts / counts
// broadcast targets only for these — positively whitelisting AI CLIs avoids
// mistaking an empty/legacy CLI type for an agent.
func isAICLIType(cliType string) bool {
	switch cliType {
	case string(cli.CLIClaude), string(cli.CLIGemini), string(cli.CLICopilot), string(cli.CLICodex):
		return true
	default:
		return false
	}
}

// broadcastRoleLookup returns a role resolver for a team's agents, used to filter
// observers out of a broadcast. A team-load failure yields an all-empty resolver
// (no agent treated as observer) so a transient store error never silently drops
// every target.
func (a *App) broadcastRoleLookup(teamID string) func(agentName string) string {
	empty := func(string) string { return "" }
	if a.teamStore == nil {
		return empty
	}
	t, err := a.teamStore.Get(teamID)
	if err != nil {
		log.Printf("[BROADCAST] takım rolleri okunamadı team=%s: %v", teamID, err)
		return empty
	}
	// Key by a normalized (lower-cased, trimmed) name so a PTY whose AgentName
	// drifts in casing/whitespace still resolves to its role — matching
	// composeAgentPrompt's EqualFold+TrimSpace lookup and the casing-independent
	// manager identity (#22). Otherwise an observer could dodge the filter.
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	roles := make(map[string]string, len(t.Agents))
	for _, ag := range t.Agents {
		roles[norm(ag.Name)] = ag.Role
	}
	return func(name string) string { return roles[norm(name)] }
}

// ResizeTerminal resizes a terminal
func (a *App) ResizeTerminal(sessionID string, cols, rows int) error {
	return a.ptyManager.Resize(sessionID, uint16(cols), uint16(rows))
}

// closeTerminalInternal closes the PTY and optionally cleans up the worktree.
func (a *App) closeTerminalInternal(sessionID string, cleanupWorktree bool) error {
	// NOTE: the session-file watcher is intentionally NOT stopped here. Stopping it
	// before ptyManager.Close would make its final drain read the pre-flush file and
	// miss a line the CLI flushes while exiting. Instead the watcher stops via its
	// SessionDone (PTY-exit) path below, draining the file AFTER the process flushed
	// (#65 / Codex round-4). finish() then releases its claim.

	session := a.ptyManager.GetSession(sessionID)
	if session == nil {
		return a.ptyManager.Close(sessionID)
	}

	// Capture metadata before closing
	wtDir := session.WorktreeDir
	wtRepo := session.WorktreeRepo
	agentName := session.AgentName

	// Unregister from orchestrator
	if session.TeamID != "" && agentName != "" {
		t, err := a.teamStore.Get(session.TeamID)
		if err == nil {
			teamName := t.Name
			if teamName == "" {
				teamName = "default"
			}
			a.orchestrator.UnregisterAgent(teamName, agentName)
		}
	}

	// #40 Faz-2: close the history open-window (lastSeen) here, BEFORE Close deletes the
	// session from the map (after which the PTY-death watcher's GetCLISessionID returns ""
	// and never touches). For a session that ALREADY self-exited (in-CLI /exit or crash),
	// pin lastSeen to the REAL exit time — a dead pane the user closes minutes or hours
	// later must NOT stretch the window forward to close time (Codex P2). TouchAt only
	// advances if newer, so this is idempotent with the done-watcher and also recovers the
	// case where that watcher raced behind an immediate close and missed the id. A still-
	// live terminal is touched at now() — the UI-close IS the end of its window.
	if cid := a.ptyManager.GetCLISessionID(sessionID); cid != "" {
		if exitedAt, exited := a.ptyManager.SessionExitedAt(sessionID); exited {
			a.sessionLog.TouchAt(cid, exitedAt)
		} else {
			a.sessionLog.Touch(cid)
		}
	}

	// Close PTY (terminates process).
	if err := a.ptyManager.Close(sessionID); err != nil {
		return err
	}

	// Worktree cleanup (after PTY is closed — no open process)
	if cleanupWorktree && wtDir != "" && wtRepo != "" {
		mu := a.worktreeMu(wtDir)
		mu.Lock()
		defer mu.Unlock()

		dirty, err := git.IsDirty(wtDir)
		if err != nil {
			log.Printf("[WORKTREE] Dirty check failed, keeping: %s (%v)", wtDir, err)
			runtime.EventsEmit(a.ctx, "worktree:dirty", map[string]string{
				"sessionID":   sessionID,
				"agentName":   agentName,
				"worktreeDir": wtDir,
				"error":       err.Error(),
			})
		} else if dirty {
			log.Printf("[WORKTREE] Dirty worktree, keeping: %s", wtDir)
			runtime.EventsEmit(a.ctx, "worktree:dirty", map[string]string{
				"sessionID":   sessionID,
				"agentName":   agentName,
				"worktreeDir": wtDir,
			})
		} else {
			if err := git.RemoveWorktree(wtRepo, wtDir); err != nil {
				log.Printf("[WORKTREE] Cleanup failed: %v", err)
			} else {
				log.Printf("[WORKTREE] Cleaned up: %s", wtDir)
			}
		}
	}

	return nil
}

// CloseTerminal closes a terminal and cleans up its worktree if clean.
func (a *App) CloseTerminal(sessionID string) error {
	return a.closeTerminalInternal(sessionID, true)
}

// GetTerminalSessions returns all active terminal sessions for a team
func (a *App) GetTerminalSessions(teamID string) []map[string]string {
	sessions := a.ptyManager.GetSessionsByTeam(teamID)
	var result []map[string]string
	for _, s := range sessions {
		result = append(result, map[string]string{
			"sessionID": s.ID,
			"agentName": s.AgentName,
			"teamID":    s.TeamID,
		})
	}
	return result
}

// ===================== Team Bindings =====================

// ListTeams returns all teams
func (a *App) ListTeams() []team.Team {
	return a.teamStore.List()
}

// GetTeam returns a team by ID
func (a *App) GetTeam(id string) (team.Team, error) {
	return a.teamStore.Get(id)
}

// CreateTeam creates a new team
func (a *App) CreateTeam(name, gridLayout string, agents []team.AgentConfig) (team.Team, error) {
	t, err := a.teamStore.Create(name, gridLayout, agents)
	if err != nil {
		return team.Team{}, err
	}

	// Subscribe to hub events for this team
	if client := a.hubClient.Load(); client != nil {
		if err := client.Subscribe([]string{name}); err != nil {
			log.Printf("[HUB] Subscribe failed for room=%s: %v", name, err)
		}
	}
	a.syncHubManager(t.Name, strings.TrimSpace(t.ManagerAgent))

	return t, nil
}

// DeleteRoom removes an orphan room (one that no team owns) from the hub. Team-backed
// rooms must go through DeleteTeam instead; the default room cannot be deleted. The
// hub re-checks subscribers as defense-in-depth, but the authoritative "is this orphan"
// decision lives here because the hub does not know about teams.
func (a *App) DeleteRoom(room string) error {
	room = roomNameOrDefault(room)
	if room == "default" {
		return fmt.Errorf("varsayılan oda silinemez")
	}
	if err := validation.ValidateName(room); err != nil {
		return fmt.Errorf("geçersiz oda adı: %w", err)
	}
	// EqualFold: room names are filenames (hub-state/{room}.json). On case-insensitive
	// filesystems (macOS/Windows) team "Alpha" and room "alpha" resolve to the SAME file,
	// so a case-sensitive compare would let "alpha" past this guard and delete the team's
	// state file. Match case-insensitively to keep team-backed rooms protected.
	for _, t := range a.teamStore.List() {
		if strings.EqualFold(roomNameOrDefault(t.Name), room) {
			return fmt.Errorf("'%s' bir takıma bağlı; önce takımı silin", room)
		}
	}
	client := a.hubClient.Load()
	if client == nil {
		return fmt.Errorf("hub bağlı değil")
	}
	return client.DeleteRoom(room)
}

// UpdateTeam updates a team
func (a *App) UpdateTeam(id, name, gridLayout string, agents []team.AgentConfig) (team.Team, error) {
	prev, err := a.teamStore.Get(id)
	if err != nil {
		return team.Team{}, err
	}

	updated, err := a.teamStore.Update(id, name, gridLayout, agents)
	if err != nil {
		return team.Team{}, err
	}

	// A rename changes the room: clear the OLD room's manager + observer hub state so
	// it doesn't linger, then sync the new/updated room. Observer authorization lives
	// only in hub memory, so an UpdateTeam that changes Agents/roles must re-sync it
	// here too — otherwise the room keeps a stale allow-list (Codex P2). Compare the
	// RESOLVED room names (empty → "default"): a default-room team renamed to a named
	// one must still have its old "default" state cleared (Codex P2).
	prevRoom := roomNameOrDefault(prev.Name)
	newRoom := roomNameOrDefault(updated.Name)
	if prevRoom != newRoom {
		a.syncHubManager(prevRoom, "")
		a.syncHubObservers(prevRoom, nil)
		// #40 Faz-2: re-index session history to the new room name so the resume picker
		// (queries by current room) still shows the team's past sessions (Codex P2).
		a.sessionLog.RenameRoom(prevRoom, newRoom)
	}
	a.syncHubManager(newRoom, strings.TrimSpace(updated.ManagerAgent))
	a.syncHubObservers(newRoom, observerNames(updated))

	// A team edit can flip any agent's observer role; re-sync each live session's
	// ingest mute state so a newly-observer agent stops emitting (and a de-observed
	// one resumes), not just future spawns (#65 / Codex round-5).
	for _, s := range a.ptyManager.GetSessionsByTeam(id) {
		a.syncIngestMute(id, s.AgentName)
	}

	return updated, nil
}

// SetTeamManager sets or clears the manager agent for a team.
func (a *App) SetTeamManager(id, managerAgent string) (team.Team, error) {
	managerAgent = strings.TrimSpace(managerAgent)
	if managerAgent != "" {
		if err := validation.ValidateName(managerAgent); err != nil {
			return team.Team{}, fmt.Errorf("invalid manager agent: %w", err)
		}
	}

	t, err := a.teamStore.Get(id)
	if err != nil {
		return team.Team{}, err
	}
	if t.ManagerAgent != "" && managerAgent != "" && t.ManagerAgent != managerAgent {
		return team.Team{}, fmt.Errorf("team already has manager '%s'; clear first before assigning '%s'", t.ManagerAgent, managerAgent)
	}

	updated, err := a.teamStore.SetManager(id, managerAgent)
	if err != nil {
		return team.Team{}, err
	}
	a.syncHubManager(updated.Name, strings.TrimSpace(updated.ManagerAgent))
	// Promoting an agent to manager clears its observer role (manager XOR observer);
	// re-sync the observer allow-list so the hub no longer authorizes the now-manager
	// as an observer — otherwise it would stay read-only (send-blocked) on the hub (#17).
	a.syncHubObservers(updated.Name, observerNames(updated))
	// An agent promoted from observer to manager must resume ingestion (unmute its
	// live watcher) — otherwise it would keep discarding its typed prompts (#65).
	if managerAgent != "" {
		a.syncIngestMute(id, managerAgent)
	}
	return updated, nil
}

// SetTeamObserver marks an agent as the room's read-only observer (#17), mirroring
// SetTeamManager. It persists AgentConfig.Role="observer" (the value the hub
// IsObserver gate and broadcastRoleLookup read), clearing the manager assignment if
// this agent held it (an agent is manager XOR observer). If that cleared the team's
// manager, the hub manager lock is synced to empty so routing stops treating the
// agent as manager.
func (a *App) SetTeamObserver(id, agentName string) (team.Team, error) {
	agentName = strings.TrimSpace(agentName)
	if err := validation.ValidateName(agentName); err != nil {
		return team.Team{}, fmt.Errorf("invalid observer agent: %w", err)
	}

	updated, err := a.teamStore.SetObserver(id, agentName)
	if err != nil {
		return team.Team{}, err
	}
	// SetObserver may have cleared the manager (an agent is manager XOR observer);
	// re-sync both so the hub's manager lock and observer authorization match.
	a.syncHubManager(updated.Name, strings.TrimSpace(updated.ManagerAgent))
	a.syncHubObservers(updated.Name, observerNames(updated))
	// If the agent is already running (converted in place), unregister its live PTY
	// session from the orchestrator so it stops receiving automatic broadcast/direct
	// notifications — the create-time observer skip only covers future spawns (Codex
	// P2). The orchestrator keyed it by the resolved room (CreateTerminal maps empty
	// → "default"), so resolve here too. No-op when the agent isn't registered.
	a.orchestrator.UnregisterAgent(roomNameOrDefault(updated.Name), agentName)
	// Bring the live watcher's mute state in line with the new role: an observer's
	// directly-typed prompts must be discarded (claim-only) — the watcher keeps its
	// file claim (so a sibling same-cwd watcher can't grab the now-private transcript)
	// but stops emitting (#17/#65 P1). The create-time path covers future spawns.
	a.syncIngestMute(id, agentName)
	return updated, nil
}

// syncIngestMute aligns the ingest watcher's mute state with an agent's CURRENT
// persisted role for any LIVE session of that agent: an observer's prompts are
// discarded (claim-only), a non-observer's are emitted. Called after any role
// change (set/clear observer or manager, team edit) so a promotion to OR from
// observer is reflected in the running watcher rather than leaving `muted` stale
// (#65 / Codex round-5).
func (a *App) syncIngestMute(teamID, agentName string) {
	observer := a.isObserverAgent(teamID, agentName)
	for _, s := range a.ptyManager.GetSessionsByTeam(teamID) {
		if strings.EqualFold(strings.TrimSpace(s.AgentName), agentName) {
			if observer {
				a.ingestMgr.Mute(s.ID)
			} else {
				a.ingestMgr.Unmute(s.ID)
			}
		}
	}
}

// SetCustomPrompt sets a team's room charter (start-of-room context / mission).
// The charter is injected into every agent's startup prompt via composeAgentPrompt
// → ComposeStartupPrompt (the teamPrompt slot), so it reaches new agents on join.
// It does NOT affect already-running agents. No hub sync is needed: the charter is
// startup-prompt context only and does not touch manager routing.
func (a *App) SetCustomPrompt(teamID, text string) (team.Team, error) {
	return a.teamStore.SetCustomPrompt(teamID, text)
}

// SaveSessionResult reports the outcome of a manual session save to the UI.
type SaveSessionResult struct {
	Saved bool `json:"saved"` // false when the room was empty or unchanged
	Count int  `json:"count"` // snapshotted message count
}

// SaveSession writes an immutable per-session snapshot of a team's room (messages
// + agent roster) to hub-state/sessions/{room}/{epoch}.json via the hub. It backs
// the manual "Session'u Kaydet" button. The hub skips an empty or unchanged room
// (Saved=false). Each save is a distinct, never-overwritten file; #29 later reads
// these snapshots to summarize past sessions and inject them at room setup.
func (a *App) SaveSession(teamID string) (SaveSessionResult, error) {
	t, err := a.teamStore.Get(teamID)
	if err != nil {
		return SaveSessionResult{}, err
	}
	client := a.hubClient.Load()
	if client == nil {
		return SaveSessionResult{}, fmt.Errorf("hub bağlantısı yok")
	}
	// An empty team name means the default room (the hub's resolveRoom maps "" →
	// default), so it is valid here — not an error.
	count, saved, err := client.SaveSession(t.Name)
	if err != nil {
		return SaveSessionResult{}, err
	}
	return SaveSessionResult{Saved: saved, Count: count}, nil
}

// RoomSummaryInfo carries a saved per-session summary to the UI. Exists is false
// when the room has no summary yet (Text empty).
type RoomSummaryInfo struct {
	Room      string `json:"room"`
	Text      string `json:"text"`
	Epoch     string `json:"epoch"`
	CreatedAt string `json:"created_at"`
	Exists    bool   `json:"exists"`
}

// GetRoomTranscript returns the room's full conversation (snapshot ∪ archive,
// deduped, oldest-first) as readable text for display and for embedding into the
// summary prompt (#29). For a live room it first snapshots so the in-flight tail
// is included; the snapshot call is best-effort.
func (a *App) GetRoomTranscript(room string) (string, error) {
	// Drain in-flight prompt-log goroutines first so a prompt the user sent moments
	// ago (logged fire-and-forget) is already in the hub before we snapshot+read —
	// otherwise the transcript could miss it (#29). Bounded so a wedged hub can't
	// hang the read.
	a.drainPromptLogs(2 * time.Second)
	// Load the client once (avoid a TOCTOU between the nil-check and the call —
	// a.hubClient is reassignable by monitorHub).
	if client := a.hubClient.Load(); client != nil {
		if _, _, err := client.SaveSession(room); err != nil {
			log.Printf("[SUMMARY] transcript için snapshot alınamadı (%s): %v", room, err)
		}
	}
	msgs, err := hub.ReadFullTranscript(a.dataDir, room, 0, 0)
	if err != nil {
		return "", err
	}
	return formatTranscript(msgs), nil
}

// RenderSummaryPrompt returns the editable summary prompt with {{TRANSCRIPT}} and
// {{ROOM}} filled in, ready to paste into a fresh, NEUTRAL agent (not a room
// worker) so the summary is produced by an impartial observer (#29).
func (a *App) RenderSummaryPrompt(room string) (string, error) {
	transcript, err := a.GetRoomTranscript(room)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(transcript) == "" {
		return "", fmt.Errorf("'%s' odasında özetlenecek mesaj yok", room)
	}
	return renderSummaryPromptText(a.summaryPromptContent(), room, transcript), nil
}

// renderSummaryPromptText fills the summary prompt template, rendering the fixed
// fields (ROOM) FIRST and inserting the (untrusted) transcript LAST via a single
// replace. RenderPrompt iterates its var map in unspecified order, so passing
// TRANSCRIPT and ROOM together risks expanding TRANSCRIPT first and then having
// the ROOM pass rewrite any {{ROOM}} the transcript itself contains (agents
// editing prompt templates). Inserting the transcript last leaves its content
// verbatim.
func renderSummaryPromptText(template, room, transcript string) string {
	withVars := prompt.RenderPrompt(template, map[string]string{"ROOM": room})
	var rendered string
	if strings.Contains(withVars, "{{TRANSCRIPT}}") {
		rendered = strings.ReplaceAll(withVars, "{{TRANSCRIPT}}", transcript)
	} else {
		// The user-edited template lost its {{TRANSCRIPT}} placeholder (removal or
		// typo): append the conversation so the neutral agent always gets it rather
		// than summarizing nothing / hallucinating history.
		rendered = withVars + "\n\n--- TRANSCRIPT ---\n" + transcript
	}
	// Sanitize the FINAL prompt: the user-editable template can carry control /
	// bracketed-paste-escape runes that would otherwise reach the clipboard the
	// user pastes into a neutral CLI. Idempotent for the already-clean transcript.
	return sanitize.StripForTerminalPaste(rendered)
}

// GetRoomSummary returns the room's newest saved session summary (#29).
func (a *App) GetRoomSummary(room string) (RoomSummaryInfo, error) {
	doc, ok, err := summary.Latest(a.dataDir, room)
	if err != nil {
		return RoomSummaryInfo{}, err
	}
	return RoomSummaryInfo{Room: room, Text: doc.Text, Epoch: doc.Epoch, CreatedAt: doc.CreatedAt, Exists: ok}, nil
}

// SaveRoomSummary persists a user-produced/edited summary as a new immutable
// per-session summary; "continue" later injects the newest one (#29).
func (a *App) SaveRoomSummary(room, text string) (RoomSummaryInfo, error) {
	// Clean control / bracketed-paste-escape runes before persisting: the saved
	// summary is later injected into continued agents' startup prompts inside a
	// bracketed paste (composeAgentPrompt → sendStartupPrompt), the same sink the
	// charter sanitizes for. Done before the empty-check so an all-control payload
	// is rejected rather than stored blank.
	text = sanitize.StripForTerminalPaste(text)
	if strings.TrimSpace(text) == "" {
		return RoomSummaryInfo{}, fmt.Errorf("boş özet kaydedilmez")
	}
	// Enforce the rune cap server-side (the UI advertises it, but a direct/older
	// runtime caller could bypass it). An oversized summary is injected into every
	// continued agent's startup prompt, so cap it like the charter does.
	if runes := []rune(text); len(runes) > summaryMaxRunes {
		text = string(runes[:summaryMaxRunes])
	}
	doc, err := summary.Write(a.dataDir, room, text)
	if err != nil {
		return RoomSummaryInfo{}, err
	}
	return RoomSummaryInfo{Room: room, Text: doc.Text, Epoch: doc.Epoch, CreatedAt: doc.CreatedAt, Exists: true}, nil
}

// formatTranscript renders a message slice as readable, summarizer-friendly text.
// Message content is cleaned of control / bracketed-paste-escape runes: the
// rendered transcript feeds RenderSummaryPrompt, whose output the user copies and
// pastes into a fresh CLI — an embedded \x1b[201~ in a stored message must not
// break out of that paste. (From/To are validated agent names, so only the free
// Content field needs cleaning.)
func formatTranscript(msgs []types.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		content := sanitize.StripForTerminalPaste(m.Content)
		switch m.Type {
		case types.MsgTypeSystem:
			fmt.Fprintf(&b, "[%s] SYSTEM: %s\n", m.Timestamp, content)
		case types.MsgTypeUserPrompt:
			fmt.Fprintf(&b, "[%s] 👤 KULLANICI → %s: %s\n", m.Timestamp, m.To, content)
		default:
			if m.OriginalTo != "" && m.OriginalTo != m.To {
				fmt.Fprintf(&b, "[%s] %s → %s (orijinal: %s): %s\n", m.Timestamp, m.From, m.To, m.OriginalTo, content)
			} else {
				fmt.Fprintf(&b, "[%s] %s → %s: %s\n", m.Timestamp, m.From, m.To, content)
			}
		}
	}
	return b.String()
}

// DeleteTeam deletes a team
func (a *App) DeleteTeam(id string) error {
	t, getErr := a.teamStore.Get(id)
	sessions := a.ptyManager.GetSessionsByTeam(id)

	// Flush the room's conversation to the append-only archive BEFORE closing
	// terminals. A terminal close failure returns early below, so archiving must
	// happen first and stay error-tolerant — losing the team is no reason to also
	// lose its history. The immutable per-session snapshot (#28) is taken by the
	// frontend BEFORE it calls this (and before it tears the terminals down):
	// closing terminals makes the agents leave the room, which would empty the
	// roster the snapshot is meant to capture, so snapshotting here would record an
	// already-emptied roster. An empty team name means the default room (the hub's
	// resolveRoom maps "" → default), so it is archived too rather than skipped.
	if getErr == nil {
		if client := a.hubClient.Load(); client != nil {
			if err := client.ArchiveRoom(t.Name); err != nil {
				log.Printf("[DELETE-TEAM] Oda arşivlenemedi (%s): %v", t.Name, err)
			}
		}
	}

	var closeErrors []string
	for _, s := range sessions {
		if err := a.closeTerminalInternal(s.ID, true); err != nil {
			log.Printf("[DELETE-TEAM] Failed to close session %s: %v", ptymgr.ShortID(s.ID), err)
			closeErrors = append(closeErrors, fmt.Sprintf("%s: %v", ptymgr.ShortID(s.ID), err))
		}
	}
	if len(closeErrors) > 0 {
		return fmt.Errorf("bazı terminaller kapatılamadı: %s", strings.Join(closeErrors, "; "))
	}

	if err := a.teamStore.Delete(id); err != nil {
		return err
	}
	if getErr == nil {
		// Clear the deleted room's hub manager + observer state so a later team
		// reusing the same room name doesn't inherit it. The sync helpers resolve an
		// empty name to "default", so this also covers the default-room team (#17).
		a.syncHubManager(t.Name, "")
		a.syncHubObservers(t.Name, nil)
	}
	return nil
}

// ===================== Prompt Bindings =====================

// ListPrompts returns all prompts
func (a *App) ListPrompts() []prompt.Prompt {
	return a.promptStore.List()
}

// GetPrompt returns a prompt by ID
func (a *App) GetPrompt(id string) (prompt.Prompt, error) {
	return a.promptStore.Get(id)
}

// CreatePrompt creates a new prompt
func (a *App) CreatePrompt(name, content, category string, tags []string) (prompt.Prompt, error) {
	return a.promptStore.Create(name, content, category, tags)
}

// UpdatePrompt updates a prompt
func (a *App) UpdatePrompt(id, name, content, category string, tags []string) (prompt.Prompt, error) {
	return a.promptStore.Update(id, name, content, category, tags)
}

// DeletePrompt deletes a prompt
func (a *App) DeletePrompt(id string) error {
	return a.promptStore.Delete(id)
}

// SendPromptToAgent renders a prompt and sends it to an agent's terminal. The
// delivered prompt is also logged into the room transcript (#29) so the user's
// instructions feed the session summary.
func (a *App) SendPromptToAgent(sessionID, promptContent string, vars map[string]string) error {
	rendered := prompt.RenderPrompt(promptContent, vars)
	// Record the fingerprint BEFORE the PTY write (as the startup and broadcast paths
	// do): otherwise a fast CLI could append this prompt and an ingestion tick could
	// poll it before RecordInjection runs, logging it as a directly-typed message
	// while logUserPrompt logs it too. An unconsumed fingerprint if the write fails
	// is harmless (#65 / Codex round-5 P3).
	a.ingestMgr.RecordInjection(sessionID, rendered)
	if err := a.ptyManager.Write(sessionID, []byte(rendered+"\n")); err != nil {
		return err
	}
	a.logUserPrompt(sessionID, rendered)
	return nil
}

// IsGitRepo checks if a directory is inside a git repository.
func (a *App) IsGitRepo(dir string) bool {
	return git.IsGitRepo(dir)
}

// ===================== CLI Bindings =====================

// DetectCLIs returns all detected AI CLIs on the system
func (a *App) DetectCLIs() []cli.CLIInfo {
	return cli.DetectAll()
}

// GetGlobalPrompt returns the global custom prompt content
func (a *App) GetGlobalPrompt() string {
	data, err := os.ReadFile(filepath.Join(a.dataDir, "global_prompt.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

// SetGlobalPrompt saves the global custom prompt
func (a *App) SetGlobalPrompt(content string) error {
	return os.WriteFile(filepath.Join(a.dataDir, "global_prompt.md"), []byte(content), 0644)
}

// ===================== Hub Bindings =====================

// GetMessages returns all messages from a room
func (a *App) GetMessages(room string) ([]types.Message, error) {
	client := a.hubClient.Load()
	if client == nil {
		return nil, fmt.Errorf("hub not connected")
	}
	msgs, err := client.GetMessagesRaw(room)
	if err != nil {
		log.Printf("[HUB] GetMessages error for room %s: %v", room, err)
		return nil, err
	}
	return msgs, nil
}

// ListRooms returns structured summaries of all rooms (including orphan rooms
// that no longer map to a team) for the desktop room browser.
func (a *App) ListRooms() ([]types.RoomSummary, error) {
	client := a.hubClient.Load()
	if client == nil {
		return nil, fmt.Errorf("hub not connected")
	}
	rooms, err := client.ListRoomsDetailed()
	if err != nil {
		log.Printf("[HUB] ListRooms error: %v", err)
		return nil, err
	}
	return rooms, nil
}

// GetAgents returns all agents from a room
func (a *App) GetAgents(room string) (map[string]types.Agent, error) {
	client := a.hubClient.Load()
	if client == nil {
		return nil, fmt.Errorf("hub not connected")
	}
	agents, err := client.GetAgentsRaw(room)
	if err != nil {
		log.Printf("[HUB] GetAgents error for room %s: %v", room, err)
		return nil, err
	}
	return agents, nil
}

// WatchChatDir subscribes to a room (backward-compatible binding name).
func (a *App) WatchChatDir(room string) error {
	client := a.hubClient.Load()
	if client == nil {
		return fmt.Errorf("hub not connected")
	}
	return client.Subscribe([]string{room})
}

// emitVoiceState pushes a voice:state:<sessionID> event for the panel's mic UI.
func (a *App) emitVoiceState(sessionID, state, message string) {
	a.voiceEmit("voice:state:"+sessionID, map[string]string{
		"state":   state,
		"message": message,
	})
}

// stopActiveVoice terminates any in-flight microphone capture and clears the lock.
// Called on shutdown so a quit mid-recording doesn't orphan the ffmpeg subprocess
// (children aren't auto-killed when the Go process exits) or leak the temp WAV
// (Codex P2). Safe to call when nothing is recording; the audio is discarded.
func (a *App) stopActiveVoice() {
	a.voiceMu.Lock()
	rec := a.activeRecorder
	a.activeRecorder = nil
	a.activeVoiceSession = ""
	a.voiceMu.Unlock()
	if rec != nil {
		if _, err := rec.Stop(); err != nil {
			log.Printf("[VOICE] shutdown'da kayıt durdurma hatası: %v", err)
		}
	}
}

// StartVoiceCapture begins recording the microphone for a session (push-to-talk
// down). Only one capture runs at a time (single mic): a second Start while one is
// active returns an error the frontend surfaces. Emits voice:state events.
func (a *App) StartVoiceCapture(sessionID string) error {
	log.Printf("[VOICE] StartVoiceCapture session=%s", sessionID)
	a.voiceMu.Lock()
	if a.activeRecorder != nil {
		a.voiceMu.Unlock()
		log.Printf("[VOICE] StartVoiceCapture reddedildi (zaten kayıt var) session=%s", sessionID)
		return fmt.Errorf("⚠️ Zaten kayıt sürüyor")
	}
	if a.transcribingSessions[sessionID] {
		a.voiceMu.Unlock()
		log.Printf("[VOICE] StartVoiceCapture reddedildi (bu panel hâlâ çevriliyor) session=%s", sessionID)
		return fmt.Errorf("⚠️ Bu panelin önceki kaydı hâlâ çevriliyor")
	}
	rec, err := a.newVoiceRecorder()
	if err != nil {
		a.voiceMu.Unlock()
		log.Printf("[VOICE] recorder oluşturulamadı session=%s err=%v", sessionID, err)
		a.emitVoiceState(sessionID, "error", err.Error())
		return err
	}
	if err := rec.Start(a.ctx); err != nil {
		a.voiceMu.Unlock()
		log.Printf("[VOICE] recorder Start hatası session=%s err=%v", sessionID, err)
		a.emitVoiceState(sessionID, "error", err.Error())
		return err
	}
	a.activeRecorder = rec
	a.activeVoiceSession = sessionID
	a.voiceMu.Unlock()
	log.Printf("[VOICE] kayıt başladı session=%s", sessionID)
	a.emitVoiceState(sessionID, "recording", "")
	return nil
}

// StopVoiceCapture ends recording for a session (push-to-talk up), transcribes the
// audio, and injects the transcript into that session's PTY input line WITHOUT
// submitting (autosubmit off — the user reviews and presses Enter). A Stop for a
// session that isn't the active recording one is a silent no-op. The recorder is
// detached under the lock and released before the network call, so another panel
// may start recording while this transcript uploads.
func (a *App) StopVoiceCapture(sessionID string) error {
	log.Printf("[VOICE] StopVoiceCapture session=%s", sessionID)
	a.voiceMu.Lock()
	if a.activeRecorder == nil || a.activeVoiceSession != sessionID {
		a.voiceMu.Unlock()
		log.Printf("[VOICE] StopVoiceCapture no-op (aktif kayıt yok ya da farklı session) session=%s active=%s", sessionID, a.activeVoiceSession)
		return nil
	}
	rec := a.activeRecorder
	// Keep activeRecorder set (so a concurrent Start from another panel is rejected)
	// but null the session id (so a double Stop for this session no-ops) while ffmpeg
	// finalizes. Only once rec.Stop() returns has ffmpeg released the avfoundation
	// device — clear the lock then, before the Whisper upload, so another panel can
	// record while this transcript uploads without contending for the mic (Codex P2).
	a.activeVoiceSession = ""
	a.voiceMu.Unlock()

	wav, err := rec.Stop()

	a.voiceMu.Lock()
	if a.activeRecorder == rec {
		a.activeRecorder = nil
	}
	if a.transcribingSessions == nil {
		a.transcribingSessions = make(map[string]bool)
	}
	a.transcribingSessions[sessionID] = true
	a.voiceMu.Unlock()
	// Removed on EVERY return path below so a failed/stuck transcription can't
	// permanently block this session from recording again (Codex P2).
	defer func() {
		a.voiceMu.Lock()
		delete(a.transcribingSessions, sessionID)
		a.voiceMu.Unlock()
	}()

	if err != nil {
		log.Printf("[VOICE] kayıt durdurma/okuma hatası session=%s err=%v", sessionID, err)
		a.emitVoiceState(sessionID, "error", "⚠️ Kayıt okunamadı: "+err.Error())
		return err
	}
	log.Printf("[VOICE] kayıt durdu session=%s wavBytes=%d", sessionID, len(wav))

	// No-speech gate: Whisper hallucinates subtitle artifacts (e.g. "Altyazı M.K.")
	// on silent audio, so a capture with no real speech must never be sent or
	// injected. Skip before the API call to also save the request.
	if silent, db := voice.IsLikelySilent(wav); silent {
		log.Printf("[VOICE] sessiz kayıt, transkripsiyon atlandı session=%s dBFS=%.1f", sessionID, db)
		a.emitVoiceState(sessionID, "idle", "")
		return nil
	}

	a.emitVoiceState(sessionID, "transcribing", "")
	base := a.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, 30*time.Second)
	defer cancel()
	text, err := a.voiceTranscribe(ctx, wav)
	if err != nil {
		log.Printf("[VOICE] transkripsiyon hatası session=%s err=%v", sessionID, err)
		a.emitVoiceState(sessionID, "error", err.Error())
		return err
	}
	log.Printf("[VOICE] transkript session=%s len=%d", sessionID, len(text))
	// Drop empty results and known no-speech hallucinations that slipped past the
	// energy gate (quiet ambient that Whisper still "heard" as a subtitle credit).
	if strings.TrimSpace(text) == "" || voice.IsHallucination(text) {
		log.Printf("[VOICE] boş/halüsinasyon transkript atlandı session=%s text=%q", sessionID, text)
		a.emitVoiceState(sessionID, "idle", "")
		return nil
	}
	if err := a.voiceInject(sessionID, text, false); err != nil {
		a.emitVoiceState(sessionID, "error", "⚠️ Enjeksiyon hatası: "+err.Error())
		return err
	}
	a.voiceEmit("voice:transcript:"+sessionID, text)
	a.emitVoiceState(sessionID, "idle", "")
	return nil
}

// VoiceStatus is the Settings-panel view of voice config. The real API key never
// crosses to the frontend — only whether one is set, a short hint (last 4 chars),
// and whether ffmpeg is available.
type VoiceStatus struct {
	HasKey      bool   `json:"hasKey"`
	KeyHint     string `json:"keyHint"`
	FFmpegFound bool   `json:"ffmpegFound"`
}

// GetVoiceStatus reports voice config state for the Settings panel (no raw key).
func (a *App) GetVoiceStatus() (VoiceStatus, error) {
	cfg, err := voice.LoadConfig(a.dataDir)
	if err != nil {
		return VoiceStatus{}, err
	}
	key := strings.TrimSpace(cfg.OpenAIAPIKey)
	st := VoiceStatus{HasKey: key != "", FFmpegFound: voice.FFmpegAvailable()}
	if r := []rune(key); len(r) > 0 {
		last := r
		if len(r) > 4 {
			last = r[len(r)-4:]
		}
		st.KeyHint = "…" + string(last)
	}
	return st, nil
}

// SetVoiceConfig persists the OpenAI API key (set-only). An empty string clears it.
func (a *App) SetVoiceConfig(apiKey string) error {
	return voice.SaveConfig(a.dataDir, voice.Config{OpenAIAPIKey: strings.TrimSpace(apiKey)})
}

// SessionInfo is one past CLI session of an agent, enriched for the resume picker
// (#40 Faz-2). Times are unix seconds. Wails generates the TS interface from this.
type SessionInfo struct {
	SessionID    string  `json:"sessionID"`
	CLIType      string  `json:"cliType"`
	StartUnix    float64 `json:"startUnix"`
	LastUnix     float64 `json:"lastUnix"`
	DurationSec  float64 `json:"durationSec"`
	MessageCount int     `json:"messageCount"`
	Snippet      string  `json:"snippet"`
	FileMissing  bool    `json:"fileMissing"`
}

// ListKnownAgents returns agent names previously seen in the team's room (session
// history) unioned with the team's configured agents — newest-activity first, then
// any config-only names (#40 Faz-2).
func (a *App) ListKnownAgents(teamID string) []string {
	room := a.roomForTeam(teamID)
	// Dedup case-insensitively: history may store "alice" while the team config has
	// "Alice" — they're the same agent app-wide, so they must not both appear (Gemini).
	seen := map[string]bool{}
	// Non-nil: a nil slice marshals to JSON null across Wails, and SetupWizard does
	// knownAgents.map(...) which throws on null (Copilot). Empty → [].
	out := []string{}
	for _, n := range a.sessionLog.ListAgents(room) {
		key := strings.ToLower(n)
		if !seen[key] {
			seen[key] = true
			out = append(out, n)
		}
	}
	if t, err := a.teamStore.Get(teamID); err == nil {
		for _, ag := range t.Agents {
			key := strings.ToLower(ag.Name)
			if ag.Name != "" && !seen[key] {
				seen[key] = true
				out = append(out, ag.Name)
			}
		}
	}
	return out
}

// ListAgentSessions returns an agent's past sessions in the team's room, newest
// first, enriched with duration + message count + first-message snippet read live
// from each CLI transcript (#40 Faz-2). A pruned transcript yields FileMissing.
func (a *App) ListAgentSessions(teamID, agentName string) []SessionInfo {
	room := a.roomForTeam(teamID)
	recs := a.sessionLog.ListSessions(room, agentName)
	out := make([]SessionInfo, 0, len(recs))
	for _, r := range recs {
		si := SessionInfo{
			SessionID:   r.SessionID,
			CLIType:     r.CLIType,
			StartUnix:   r.FirstSeen,
			LastUnix:    r.LastSeen,
			DurationSec: r.LastSeen - r.FirstSeen,
		}
		if path, ok := ingest.SessionFilePath(r.CLIType, r.Cwd, r.SessionID, r.FirstSeen); ok {
			if _, statErr := os.Stat(path); statErr == nil {
				si.MessageCount, si.Snippet = ingest.SessionStats(r.CLIType, path)
			} else {
				si.FileMissing = true
			}
		} else {
			si.FileMissing = true
		}
		out = append(out, si)
	}
	return out
}

// CreateTerminalResume creates a terminal resuming a SPECIFIC past session
// (resumeID), or fresh when resumeID is empty. Thin exported wrapper over the
// Faz-1 internal createTerminal (#40 Faz-2). The resume picker calls this per agent.
func (a *App) CreateTerminalResume(teamID, agentName, workDir, cliType, promptID string, useWorktree bool, slotIndex int, resumeID string) (string, error) {
	return a.createTerminal(teamID, agentName, workDir, cliType, promptID, useWorktree, slotIndex, resumeID)
}
