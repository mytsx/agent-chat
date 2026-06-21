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
	"syscall"
	"time"
	"unicode/utf8"

	"desktop/internal/cli"
	"desktop/internal/git"
	"desktop/internal/hubclient"
	"desktop/internal/orchestrator"
	"desktop/internal/prompt"
	ptymgr "desktop/internal/pty"
	"desktop/internal/team"
	"desktop/internal/types"
	"desktop/internal/validation"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed prompts/*.md
var promptsFS embed.FS

//go:embed build/mcp-server-bin
var mcpServerBin []byte

// App struct
type App struct {
	ctx           context.Context
	ptyManager    *ptymgr.Manager
	hubClient     *hubclient.HubClient
	hubProcess    *os.Process
	hubAuthToken  string
	orchestrator  *orchestrator.Orchestrator
	promptStore   *prompt.Store
	teamStore     *team.Store
	dataDir       string
	worktreeLocks sync.Map // path → *sync.Mutex — per-path worktree lock
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

	a.hubProcess = cmd.Process
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

	a.hubClient = client
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
	if a.hubClient == nil {
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
	}
	if len(rooms) > 0 {
		if err := a.hubClient.Subscribe(rooms); err != nil {
			log.Printf("[HUB] Subscribe failed: %v", err)
		}
	}
}

func (a *App) syncHubManager(room, managerAgent string) {
	if a.hubClient == nil || strings.TrimSpace(room) == "" {
		return
	}
	if err := a.hubClient.SetManager(room, strings.TrimSpace(managerAgent)); err != nil {
		log.Printf("[HUB] set_manager failed for room=%s manager=%s: %v", room, managerAgent, err)
	}
}

// monitorHub watches the hub process and restarts if it crashes.
func (a *App) monitorHub() {
	if a.hubProcess == nil {
		return
	}
	go func() {
		state, err := a.hubProcess.Wait()
		if err != nil {
			log.Printf("[HUB-MONITOR] Hub process wait error: %v", err)
		}
		if state != nil && !state.Success() {
			log.Printf("[HUB-MONITOR] Hub crashed (exit=%d), restarting...", state.ExitCode())
			// Clean up old client
			if a.hubClient != nil {
				a.hubClient.Close()
			}
			// Restart
			time.Sleep(500 * time.Millisecond)
			if err := a.startHub(); err != nil {
				log.Printf("[HUB-MONITOR] Hub restart failed: %v", err)
				return
			}
			if err := a.connectToHub(); err != nil {
				log.Printf("[HUB-MONITOR] Hub reconnect failed: %v", err)
				return
			}
			a.subscribeExistingTeams()
		}
	}()
}

// shutdown is called when the app is closing
func (a *App) shutdown(ctx context.Context) {
	// Snapshot each team's room as a session BEFORE closing the hub client, so a
	// quit captures the in-flight conversation (#28). Best-effort and skip-aware:
	// the hub skips empty/unchanged rooms, so idle quits write nothing.
	if a.hubClient != nil && a.teamStore != nil {
		for _, t := range a.teamStore.List() {
			if t.Name == "" {
				continue
			}
			if _, _, err := a.hubClient.SaveSession(t.Name); err != nil {
				log.Printf("[SHUTDOWN] Session kaydedilemedi (%s): %v", t.Name, err)
			}
		}
	}

	// Close hub client
	if a.hubClient != nil {
		a.hubClient.Close()
	}

	// Stop hub process gracefully
	if a.hubProcess != nil {
		a.hubProcess.Signal(syscall.SIGTERM)
		// Wait up to 3s for hub to persist and shut down
		done := make(chan struct{})
		go func() {
			a.hubProcess.Wait()
			close(done)
		}()
		select {
		case <-done:
			log.Printf("[SHUTDOWN] Hub process exited gracefully")
		case <-time.After(3 * time.Second):
			log.Printf("[SHUTDOWN] Hub process did not exit in 3s, killing")
			a.hubProcess.Kill()
		}
	}

	// Close PTY sessions
	if a.ptyManager != nil {
		a.ptyManager.CloseAll()
	}
}

func (a *App) seedPrompts() {
	basePrompt := a.readEmbeddedPrompt("prompts/base_prompt.md")
	managerPrompt := a.readEmbeddedPrompt("prompts/manager_prompt.md")

	a.promptStore.Seed(string(basePrompt), string(managerPrompt))
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

// CreateTerminal creates a new terminal and returns its session ID.
// If useWorktree is true and workDir is a git repo, a worktree is created for the agent.
// slotIndex is the grid position the terminal occupies; it is persisted to the team
// template so the agent reopens into the same slot.
func (a *App) CreateTerminal(teamID, agentName, workDir, cliType, promptID string, useWorktree bool, slotIndex int) (string, error) {
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

	isManager, err := a.resolveManagerIntent(teamID, agentName, promptID, true)
	if err != nil {
		return "", err
	}

	// Manager agent always works in main repo — backend guard
	if isManager {
		useWorktree = false
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
	if teamID != "" {
		if t, err := a.teamStore.Get(teamID); err == nil {
			managerAgent = strings.TrimSpace(t.ManagerAgent)
		}
	}
	if managerAgent == "" && isManager {
		managerAgent = agentName
	}
	a.syncHubManager(teamName, managerAgent)

	// Subscribe to room events
	if a.hubClient != nil {
		if err := a.hubClient.Subscribe([]string{teamName}); err != nil {
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

	// Get command for CLI type
	cmdName, cmdArgs := cli.GetCommand(ct)

	// For Copilot, use -i flag to pass startup prompt directly as argument
	if ct == cli.CLICopilot && agentName != "" {
		composed := a.composeAgentPrompt(teamID, agentName, promptID, isManager)
		if composed != "" {
			cmdArgs = append(cmdArgs, "-i", composed)
			log.Printf("[STARTUP] Copilot: using -i flag, promptLen=%d", len(composed))
		}
	}

	env := []string{
		"AGENT_CHAT_DATA_DIR=" + a.dataDir,
		"AGENT_CHAT_ROOM=" + teamName,
		"TERM=xterm-256color",
	}

	sessionID, err := a.ptyManager.Create(teamID, agentName, workDir, env, cmdName, cmdArgs, cliType)
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
		}
	}

	// Persist the agent config to the team template (auto-upsert). When a worktree
	// was created, workDir was reassigned to the worktree path above; persist the
	// user-selected ORIGINAL repo dir (origWorkDir) instead so reopening doesn't
	// nest a worktree inside a worktree. Role is intentionally omitted — UpsertAgent
	// preserves any Role the user set earlier.
	if teamID != "" {
		cfgWorkDir := workDir
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
			UseWorktree: useWorktree,
		}); err != nil {
			log.Printf("[TEAM] UpsertAgent failed for agent=%s team=%s: %v", agentName, teamID, err)
		}
	}

	// Register agent session for orchestrator (using room name)
	if agentName != "" {
		a.orchestrator.RegisterAgent(teamName, agentName, sessionID)
	}

	// Send startup prompt in background
	go a.sendStartupPrompt(sessionID, teamID, agentName, cliType, promptID, isManager)

	return sessionID, nil
}

// RestartTerminal closes a terminal and creates a new one with the same parameters.
// If the terminal was using a worktree, the worktree is preserved.
func (a *App) RestartTerminal(sessionID string) (string, error) {
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
	isManager := false
	if teamID != "" {
		if t, err := a.teamStore.Get(teamID); err == nil {
			isManager = t.IsManagerAgent(agentName)
		}
	}
	if wtDir != "" {
		if isManager {
			if wtRepo != "" {
				workDir = wtRepo
			}
		} else {
			workDir = wtDir
		}
	}

	// Close PTY but do NOT cleanup worktree (it will be reused)
	if err := a.closeTerminalInternal(sessionID, false); err != nil {
		return "", fmt.Errorf("eski session kapatılamadı %s: %w", ptymgr.ShortID(sessionID), err)
	}

	log.Printf("[RESTART] Restarting terminal: agent=%s cli=%s team=%s", agentName, cliType, teamID)

	newSessionID, err := a.CreateTerminal(teamID, agentName, workDir, cliType, promptID, false, slotIndex)
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
		if capacity >= 0 && cfg.SlotIndex >= capacity {
			res.Error = fmt.Sprintf("slot %d, %s grid kapasitesini (%d) aşıyor — atlandı", cfg.SlotIndex, t.GridLayout, capacity)
			log.Printf("[TEAM] OpenTeamFromConfig: agent=%s slot=%d > capacity=%d (%s), atlandı", cfg.Name, cfg.SlotIndex, capacity, t.GridLayout)
			results = append(results, res)
			continue
		}
		sessionID, err := a.CreateTerminal(teamID, cfg.Name, cfg.WorkDir, cfg.CLIType, cfg.PromptID, cfg.UseWorktree, cfg.SlotIndex)
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

// composeAgentPrompt builds the startup prompt for an agent without sending it
func (a *App) composeAgentPrompt(teamID, agentName, promptID string, isManager bool) string {
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

	if isManager {
		managerPrompt := a.readEmbeddedPrompt("prompts/manager_prompt.md")
		managerText := strings.TrimSpace(string(managerPrompt))
		if managerText != "" {
			if strings.TrimSpace(selectedPrompt) == "" {
				selectedPrompt = managerText
			} else if !strings.Contains(selectedPrompt, managerText) {
				selectedPrompt = strings.TrimSpace(selectedPrompt) + "\n\n" + managerText
			}
		}
	}

	return cli.ComposeStartupPrompt(string(basePrompt), string(globalPrompt), teamPrompt, selectedPrompt, agentName, agentRole, teamName, isManager)
}

// sendStartupPrompt sends the initial prompt to a CLI agent
func (a *App) sendStartupPrompt(sessionID, teamID, agentName, cliType, promptID string, isManager bool) {
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

	composed := a.composeAgentPrompt(teamID, agentName, promptID, isManager)
	if composed == "" {
		return
	}

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
	injected, errs := broadcastToSessions(sessions, text, submit, roleOf, a.ptyManager.InjectText)

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
	return broadcastOutcomeError(injected, errs)
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
) (injected int, errs []string) {
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

	for _, o := range outcomes {
		if o.skipped {
			continue
		}
		if o.err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", o.agentName, o.err))
			continue
		}
		injected++
	}
	return injected, errs
}

// isObserverRole reports whether a team role designates an observer agent, which
// must be excluded from broadcasts (#17). Compared case-insensitively so the
// observer feature can store the role in any casing.
func isObserverRole(role string) bool {
	return strings.EqualFold(strings.TrimSpace(role), "observer")
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

	// Close PTY (terminates process)
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
	if a.hubClient != nil {
		if err := a.hubClient.Subscribe([]string{name}); err != nil {
			log.Printf("[HUB] Subscribe failed for room=%s: %v", name, err)
		}
	}
	a.syncHubManager(t.Name, strings.TrimSpace(t.ManagerAgent))

	return t, nil
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

	if prev.Name != "" && prev.Name != updated.Name {
		a.syncHubManager(prev.Name, "")
	}
	a.syncHubManager(updated.Name, strings.TrimSpace(updated.ManagerAgent))

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
	return updated, nil
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
	if t.Name == "" {
		return SaveSessionResult{}, fmt.Errorf("oda adı boş")
	}
	if a.hubClient == nil {
		return SaveSessionResult{}, fmt.Errorf("hub bağlantısı yok")
	}
	count, saved, err := a.hubClient.SaveSession(t.Name)
	if err != nil {
		return SaveSessionResult{}, err
	}
	return SaveSessionResult{Saved: saved, Count: count}, nil
}

// DeleteTeam deletes a team
func (a *App) DeleteTeam(id string) error {
	t, getErr := a.teamStore.Get(id)
	sessions := a.ptyManager.GetSessionsByTeam(id)

	// Preserve the room's history BEFORE closing terminals. A terminal close
	// failure returns early below, so both writes must happen first and stay
	// error-tolerant — losing the team is no reason to also lose its history.
	// Two complementary artifacts: the rolling append-only archive (Phase-A) and
	// an immutable per-session snapshot (#28) that #29 can later summarize.
	if getErr == nil && t.Name != "" && a.hubClient != nil {
		if err := a.hubClient.ArchiveRoom(t.Name); err != nil {
			log.Printf("[DELETE-TEAM] Oda arşivlenemedi (%s): %v", t.Name, err)
		}
		if _, _, err := a.hubClient.SaveSession(t.Name); err != nil {
			log.Printf("[DELETE-TEAM] Session kaydedilemedi (%s): %v", t.Name, err)
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
	if getErr == nil && t.Name != "" {
		a.syncHubManager(t.Name, "")
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

// SendPromptToAgent renders a prompt and sends it to an agent's terminal
func (a *App) SendPromptToAgent(sessionID, promptContent string, vars map[string]string) error {
	rendered := prompt.RenderPrompt(promptContent, vars)
	return a.ptyManager.Write(sessionID, []byte(rendered+"\n"))
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
	if a.hubClient == nil {
		return nil, fmt.Errorf("hub not connected")
	}
	msgs, err := a.hubClient.GetMessagesRaw(room)
	if err != nil {
		log.Printf("[HUB] GetMessages error for room %s: %v", room, err)
		return nil, err
	}
	return msgs, nil
}

// ListRooms returns structured summaries of all rooms (including orphan rooms
// that no longer map to a team) for the desktop room browser.
func (a *App) ListRooms() ([]types.RoomSummary, error) {
	if a.hubClient == nil {
		return nil, fmt.Errorf("hub not connected")
	}
	rooms, err := a.hubClient.ListRoomsDetailed()
	if err != nil {
		log.Printf("[HUB] ListRooms error: %v", err)
		return nil, err
	}
	return rooms, nil
}

// GetAgents returns all agents from a room
func (a *App) GetAgents(room string) (map[string]types.Agent, error) {
	if a.hubClient == nil {
		return nil, fmt.Errorf("hub not connected")
	}
	agents, err := a.hubClient.GetAgentsRaw(room)
	if err != nil {
		log.Printf("[HUB] GetAgents error for room %s: %v", room, err)
		return nil, err
	}
	return agents, nil
}

// WatchChatDir subscribes to a room (backward-compatible binding name).
func (a *App) WatchChatDir(room string) error {
	if a.hubClient == nil {
		return fmt.Errorf("hub not connected")
	}
	return a.hubClient.Subscribe([]string{room})
}
