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
	"syscall"
	"time"

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
	ctx          context.Context
	ptyManager   *ptymgr.Manager
	hubClient    *hubclient.HubClient
	hubProcess   *os.Process
	hubAuthToken string
	orchestrator *orchestrator.Orchestrator
	promptStore  *prompt.Store
	teamStore    *team.Store
	dataDir      string
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
		if managerFromPrompt && managerFromTeam != agentName {
			return false, fmt.Errorf("team manager already set to '%s'; '%s' cannot use manager prompt", managerFromTeam, agentName)
		}
		return managerFromTeam == agentName, nil
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
func (a *App) CreateTerminal(teamID, agentName, workDir, cliType, promptID string, useWorktree bool) (string, error) {
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
	if useWorktree && workDir != "" && git.IsGitRepo(workDir) {
		origWorkDir = workDir
		teamSlug := git.Slug(teamName)
		agentSlug := git.Slug(agentName)
		branchName := fmt.Sprintf("agent/%s/%s", teamSlug, agentSlug)
		wtDir = filepath.Join(a.dataDir, "worktrees", teamSlug, agentSlug)

		if err := git.CreateWorktree(workDir, wtDir, branchName); err != nil {
			return "", fmt.Errorf("worktree oluşturulamadı: %w", err)
		}
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
		// Rollback worktree if PTY creation failed
		if wtDir != "" && origWorkDir != "" {
			if rmErr := git.RemoveWorktree(origWorkDir, wtDir); rmErr != nil {
				log.Printf("[WORKTREE] Rollback failed after PTY error: %v", rmErr)
			} else {
				log.Printf("[WORKTREE] Rolled back worktree after PTY error: %s", wtDir)
			}
		}
		return "", err
	}

	// Store promptID and worktree info for restart
	if s := a.ptyManager.GetSession(sessionID); s != nil {
		s.PromptID = promptID
		if wtDir != "" {
			s.WorktreeDir = wtDir
			s.WorktreeRepo = origWorkDir
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
	wtDir := session.WorktreeDir
	wtRepo := session.WorktreeRepo

	// If worktree exists, use it as workDir (it's already created)
	if wtDir != "" {
		workDir = wtDir
	}

	// Close PTY but do NOT cleanup worktree (it will be reused)
	if err := a.closeTerminalInternal(sessionID, false); err != nil {
		return "", fmt.Errorf("eski session kapatılamadı %s: %w", ptymgr.ShortID(sessionID), err)
	}

	log.Printf("[RESTART] Restarting terminal: agent=%s cli=%s team=%s", agentName, cliType, teamID)

	// useWorktree=false because worktree already exists, workDir already points to it
	newSessionID, err := a.CreateTerminal(teamID, agentName, workDir, cliType, promptID, false)
	if err != nil {
		return "", err
	}

	// Transfer worktree info to new session
	if s := a.ptyManager.GetSession(newSessionID); s != nil && wtDir != "" {
		s.WorktreeDir = wtDir
		s.WorktreeRepo = wtRepo
	}

	return newSessionID, nil
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

	// Claude/Gemini: bracketed paste
	const (
		bracketOpen  = "\x1b[200~"
		bracketClose = "\x1b[201~"
	)
	a.ptyManager.Write(sessionID, []byte(bracketOpen+composed+bracketClose))
	time.Sleep(200 * time.Millisecond)
	a.ptyManager.Write(sessionID, []byte("\r"))
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
	return a.ptyManager.Write(sessionID, []byte(data))
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

// DeleteTeam deletes a team
func (a *App) DeleteTeam(id string) error {
	t, getErr := a.teamStore.Get(id)
	sessions := a.ptyManager.GetSessionsByTeam(id)
	for _, s := range sessions {
		a.closeTerminalInternal(s.ID, true)
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
func (a *App) GetMessages(room string) []types.Message {
	if a.hubClient == nil {
		return nil
	}
	msgs, err := a.hubClient.GetMessagesRaw(room)
	if err != nil {
		log.Printf("[HUB] GetMessages error for room %s: %v", room, err)
		return nil
	}
	return msgs
}

// GetAgents returns all agents from a room
func (a *App) GetAgents(room string) map[string]types.Agent {
	if a.hubClient == nil {
		return nil
	}
	agents, err := a.hubClient.GetAgentsRaw(room)
	if err != nil {
		log.Printf("[HUB] GetAgents error for room %s: %v", room, err)
		return nil
	}
	return agents
}

// WatchChatDir subscribes to a room (backward-compatible binding name).
func (a *App) WatchChatDir(room string) error {
	if a.hubClient == nil {
		return fmt.Errorf("hub not connected")
	}
	return a.hubClient.Subscribe([]string{room})
}
