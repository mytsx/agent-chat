package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"desktop/internal/cli"
	"desktop/internal/orchestrator"
	"desktop/internal/prompt"
	ptymgr "desktop/internal/pty"
	"desktop/internal/team"
	"desktop/internal/watcher"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed prompts/*.md
var promptsFS embed.FS

//go:embed all:mcp-server
var mcpServerFS embed.FS

// App struct
type App struct {
	ctx          context.Context
	ptyManager   *ptymgr.Manager
	watcher      *watcher.Watcher
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
	os.MkdirAll(a.dataDir, 0755)

	// Initialize PTY manager
	a.ptyManager = ptymgr.NewManager(func(sessionID string, data []byte) {
		runtime.EventsEmit(a.ctx, "pty:output:"+sessionID, string(data))
	})

	// Initialize orchestrator
	a.orchestrator = orchestrator.New(a.ptyManager)

	// Initialize file watcher
	var err error
	a.watcher, err = watcher.New(
		func(chatDir string, messages []watcher.Message) {
			// Emit to frontend
			runtime.EventsEmit(a.ctx, "messages:new", map[string]interface{}{
				"chatDir":  chatDir,
				"messages": messages,
			})
			// Process through orchestrator
			a.orchestrator.HandleNewMessages(chatDir, messages)
		},
		func(chatDir string, agents map[string]watcher.Agent) {
			runtime.EventsEmit(a.ctx, "agents:updated", map[string]interface{}{
				"chatDir": chatDir,
				"agents":  agents,
			})
		},
	)
	if err == nil {
		a.watcher.Start()
	}

	// Initialize stores
	a.promptStore, _ = prompt.NewStore(a.dataDir)
	a.teamStore, _ = team.NewStore(a.dataDir)

	// Seed prompts from existing files
	a.seedPrompts()

	// Clear old session data (messages + agents) from all rooms
	a.clearAllRooms()

	// Watch existing teams' chat directories
	a.watchExistingTeams()

	// Setup local MCP server in background (extract + venv + pip install)
	go func() {
		subFS, err := fs.Sub(mcpServerFS, "mcp-server")
		if err != nil {
			log.Printf("MCP server embed error: %v", err)
			return
		}
		if err := cli.EnsureMCPServer(subFS, a.dataDir); err != nil {
			log.Printf("MCP server setup error: %v", err)
			return
		}
		// Migrate existing CLI configs from uvx to local python
		for _, ct := range []cli.CLIType{cli.CLIClaude, cli.CLIGemini, cli.CLICopilot} {
			cli.ResetMCPConfig(ct, a.dataDir)
		}
	}()
}

// watchExistingTeams starts file watchers for all previously saved teams
func (a *App) watchExistingTeams() {
	if a.watcher == nil {
		return
	}
	teams := a.teamStore.List()
	for _, t := range teams {
		if t.ChatDir != "" {
			teamName := t.Name
			if teamName == "" {
				teamName = "default"
			}
			roomDir := filepath.Join(t.ChatDir, teamName)
			os.MkdirAll(roomDir, 0755)
			a.watcher.WatchDir(roomDir)
		}
	}
}

// shutdown is called when the app is closing
func (a *App) shutdown(ctx context.Context) {
	if a.watcher != nil {
		a.watcher.Stop()
	}
	if a.ptyManager != nil {
		a.ptyManager.CloseAll()
	}
}

// clearAllRooms removes messages.json and agents.json from all room directories
// so each app launch starts with a clean slate.
func (a *App) clearAllRooms() {
	roomsDir := cli.GetRoomsDir(a.dataDir)
	entries, err := os.ReadDir(roomsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		roomDir := filepath.Join(roomsDir, e.Name())
		os.Remove(filepath.Join(roomDir, "messages.json"))
		os.Remove(filepath.Join(roomDir, "agents.json"))
		log.Printf("[STARTUP] Cleared room: %s", e.Name())
	}
}

func (a *App) seedPrompts() {
	basePrompt, _ := promptsFS.ReadFile("prompts/base_prompt.md")
	managerPrompt, _ := promptsFS.ReadFile("prompts/manager_prompt.md")

	a.promptStore.Seed(string(basePrompt), string(managerPrompt))
}

// ===================== PTY Bindings =====================

// OpenDirectoryDialog opens a native directory picker and returns the selected path
func (a *App) OpenDirectoryDialog() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select Workspace Directory",
	})
}

// CreateTerminal creates a new terminal and returns its session ID
func (a *App) CreateTerminal(teamID, agentName, workDir, cliType, promptID string) (string, error) {
	// Get team info for chat dir and room name
	var chatDir string
	var teamName string
	if teamID != "" {
		t, err := a.teamStore.Get(teamID)
		if err == nil {
			chatDir = t.ChatDir
			teamName = t.Name
		}
	}
	if chatDir == "" {
		chatDir = cli.GetRoomsDir(a.dataDir)
	}
	if teamName == "" {
		teamName = "default"
	}

	// Room dir = chatDir/teamName (matches MCP server's _get_room_dir)
	roomDir := filepath.Join(chatDir, teamName)

	// Ensure room dir is watched
	if a.watcher != nil {
		os.MkdirAll(roomDir, 0755)
		a.watcher.WatchDir(roomDir)
	}

	// Ensure local MCP server is ready and configured for the selected CLI
	ct := cli.CLIType(cliType)
	if ct != cli.CLIShell && cliType != "" {
		subFS, _ := fs.Sub(mcpServerFS, "mcp-server")
		if err := cli.EnsureMCPServer(subFS, a.dataDir); err != nil {
			log.Printf("MCP server setup failed: %v", err)
		}
		if err := cli.EnsureMCPConfig(ct, a.dataDir, teamName); err != nil {
			log.Printf("MCP config setup failed for %s: %v", cliType, err)
		}
	}

	// Get command for CLI type
	cmdName, cmdArgs := cli.GetCommand(ct)

	// For Copilot, use -i flag to pass startup prompt directly as argument
	// (Copilot's Ink TUI doesn't accept programmatic PTY input for submission)
	if ct == cli.CLICopilot && agentName != "" {
		composed := a.composeAgentPrompt(teamID, agentName, promptID)
		if composed != "" {
			cmdArgs = append(cmdArgs, "-i", composed)
			log.Printf("[STARTUP] Copilot: using -i flag, promptLen=%d", len(composed))
		}
	}

	env := []string{
		"AGENT_CHAT_DIR=" + chatDir,
		"AGENT_CHAT_ROOM=" + teamName,
		"TERM=xterm-256color",
	}

	sessionID, err := a.ptyManager.Create(teamID, agentName, workDir, env, cmdName, cmdArgs, cliType)
	if err != nil {
		return "", err
	}

	// Register agent session for orchestrator (using roomDir)
	if agentName != "" {
		a.orchestrator.RegisterAgent(roomDir, agentName, sessionID)
	}

	// Send startup prompt in background
	go a.sendStartupPrompt(sessionID, teamID, agentName, cliType, promptID)

	return sessionID, nil
}

// composeAgentPrompt builds the startup prompt for an agent without sending it
func (a *App) composeAgentPrompt(teamID, agentName, promptID string) string {
	if agentName == "" {
		return ""
	}

	basePrompt, _ := promptsFS.ReadFile("prompts/base_prompt.md")
	globalPromptPath := filepath.Join(a.dataDir, "global_prompt.md")
	globalPrompt, _ := os.ReadFile(globalPromptPath)

	var teamPrompt string
	var teamName string
	if t, err := a.teamStore.Get(teamID); err == nil {
		teamName = t.Name
		teamPrompt = t.CustomPrompt
	}

	var selectedPrompt string
	if promptID != "" {
		if p, err := a.promptStore.Get(promptID); err == nil {
			selectedPrompt = p.Content
		}
	}

	return cli.ComposeStartupPrompt(string(basePrompt), string(globalPrompt), teamPrompt, selectedPrompt, agentName, teamName)
}

// sendStartupPrompt sends the initial prompt to a CLI agent
func (a *App) sendStartupPrompt(sessionID, teamID, agentName, cliType, promptID string) {
	if cliType == "" || cliType == "shell" || cliType == "copilot" || agentName == "" {
		return
	}

	// Wait for CLI to become idle (prompt ready) instead of fixed sleep.
	// First give a minimum startup time, then wait for output to settle.
	switch cliType {
	case "gemini":
		time.Sleep(5 * time.Second) // initial minimum wait
	default:
		time.Sleep(3 * time.Second)
	}
	// Wait until CLI has been idle for 2s (max 30s total wait)
	idle := a.ptyManager.WaitForIdle(sessionID, 2*time.Second, 25*time.Second)
	log.Printf("[STARTUP] WaitForIdle: cli=%s agent=%s idle=%v", cliType, agentName, idle)

	composed := a.composeAgentPrompt(teamID, agentName, promptID)
	if composed == "" {
		return
	}

	log.Printf("[STARTUP] Sending prompt to cli=%s agent=%s session=%s promptLen=%d",
		cliType, agentName, sessionID[:8], len(composed))

	switch cliType {
	default:
		// Claude/Gemini: bracketed paste prevents newlines from triggering submit
		const (
			bracketOpen  = "\x1b[200~"
			bracketClose = "\x1b[201~"
		)
		a.ptyManager.Write(sessionID, []byte(bracketOpen+composed+bracketClose))
		time.Sleep(200 * time.Millisecond)
		a.ptyManager.Write(sessionID, []byte("\r"))
	}
}

// WriteToTerminal writes data to a terminal
func (a *App) WriteToTerminal(sessionID, data string) error {
	// Debug: log user input to see what xterm.js sends for key presses
	session := a.ptyManager.GetSession(sessionID)
	if session != nil && session.CLIType == "copilot" {
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

// CloseTerminal closes a terminal
func (a *App) CloseTerminal(sessionID string) error {
	session := a.ptyManager.GetSession(sessionID)
	if session != nil {
		if session.TeamID != "" && session.AgentName != "" {
			t, err := a.teamStore.Get(session.TeamID)
			if err == nil {
				teamName := t.Name
				if teamName == "" {
					teamName = "default"
				}
				roomDir := filepath.Join(t.ChatDir, teamName)
				a.orchestrator.UnregisterAgent(roomDir, session.AgentName)
			}
		}
	}
	return a.ptyManager.Close(sessionID)
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

	// Start watching this team's chat directory (room-specific)
	if a.watcher != nil {
		roomDir := filepath.Join(t.ChatDir, name)
		os.MkdirAll(roomDir, 0755)
		a.watcher.WatchDir(roomDir)
	}

	return t, nil
}

// UpdateTeam updates a team
func (a *App) UpdateTeam(id, name, gridLayout string, agents []team.AgentConfig) (team.Team, error) {
	return a.teamStore.Update(id, name, gridLayout, agents)
}

// DeleteTeam deletes a team
func (a *App) DeleteTeam(id string) error {
	t, err := a.teamStore.Get(id)
	if err != nil {
		return err
	}

	if a.watcher != nil {
		a.watcher.UnwatchDir(t.ChatDir)
	}

	sessions := a.ptyManager.GetSessionsByTeam(id)
	for _, s := range sessions {
		a.ptyManager.Close(s.ID)
	}

	return a.teamStore.Delete(id)
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

// ===================== Watcher Bindings =====================

// GetMessages returns all messages from a chat directory
func (a *App) GetMessages(chatDir string) []watcher.Message {
	if chatDir == "" {
		chatDir = cli.GetRoomsDir(a.dataDir)
	}
	return a.watcher.GetAllMessages(chatDir)
}

// GetAgents returns all agents from a chat directory
func (a *App) GetAgents(chatDir string) map[string]watcher.Agent {
	if chatDir == "" {
		chatDir = cli.GetRoomsDir(a.dataDir)
	}
	return a.watcher.GetAllAgents(chatDir)
}

// WatchChatDir starts watching a chat directory
func (a *App) WatchChatDir(chatDir string) error {
	return a.watcher.WatchDir(chatDir)
}
