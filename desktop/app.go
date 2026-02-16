package main

import (
	"context"
	"os"
	"path/filepath"

	"desktop/internal/orchestrator"
	ptymgr "desktop/internal/pty"
	"desktop/internal/prompt"
	"desktop/internal/team"
	"desktop/internal/watcher"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

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

	// Watch existing teams' chat directories
	a.watchExistingTeams()
}

// watchExistingTeams starts file watchers for all previously saved teams
func (a *App) watchExistingTeams() {
	if a.watcher == nil {
		return
	}
	teams := a.teamStore.List()
	for _, t := range teams {
		if t.ChatDir != "" {
			os.MkdirAll(t.ChatDir, 0755)
			a.watcher.WatchDir(t.ChatDir)
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

func (a *App) seedPrompts() {
	// Try to read seed files from project root
	projectDir := filepath.Join(filepath.Dir(os.Args[0]), "..")
	basePrompt, _ := os.ReadFile(filepath.Join(projectDir, "config", "base_prompt.txt"))
	managerPrompt, _ := os.ReadFile(filepath.Join(projectDir, "docs", "MANAGER_PROMPT.md"))

	if len(basePrompt) == 0 {
		basePrompt = []byte(defaultBasePrompt)
	}
	if len(managerPrompt) == 0 {
		managerPrompt = []byte(defaultManagerPrompt)
	}

	a.promptStore.Seed(string(basePrompt), string(managerPrompt))
}

// ===================== PTY Bindings =====================

// CreateTerminal creates a new terminal and returns its session ID
func (a *App) CreateTerminal(teamID, agentName, workDir string) (string, error) {
	// Get team info for chat dir
	var chatDir string
	if teamID != "" {
		t, err := a.teamStore.Get(teamID)
		if err == nil {
			chatDir = t.ChatDir
		}
	}
	if chatDir == "" {
		chatDir = "/tmp/agent-chat-room"
	}

	// Ensure chat dir is watched
	if a.watcher != nil {
		os.MkdirAll(chatDir, 0755)
		a.watcher.WatchDir(chatDir)
	}

	env := []string{
		"AGENT_CHAT_DIR=" + chatDir,
		"TERM=xterm-256color",
	}

	sessionID, err := a.ptyManager.Create(teamID, agentName, workDir, env)
	if err != nil {
		return "", err
	}

	// Register agent session for orchestrator
	if agentName != "" {
		a.orchestrator.RegisterAgent(chatDir, agentName, sessionID)
	}

	return sessionID, nil
}

// WriteToTerminal writes data to a terminal
func (a *App) WriteToTerminal(sessionID, data string) error {
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
		var chatDir string
		if session.TeamID != "" {
			t, err := a.teamStore.Get(session.TeamID)
			if err == nil {
				chatDir = t.ChatDir
			}
		}
		if chatDir != "" && session.AgentName != "" {
			a.orchestrator.UnregisterAgent(chatDir, session.AgentName)
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

	// Start watching this team's chat directory
	if a.watcher != nil {
		a.watcher.WatchDir(t.ChatDir)
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

// ===================== Watcher Bindings =====================

// GetMessages returns all messages from a chat directory
func (a *App) GetMessages(chatDir string) []watcher.Message {
	if chatDir == "" {
		chatDir = "/tmp/agent-chat-room"
	}
	return a.watcher.GetAllMessages(chatDir)
}

// GetAgents returns all agents from a chat directory
func (a *App) GetAgents(chatDir string) map[string]watcher.Agent {
	if chatDir == "" {
		chatDir = "/tmp/agent-chat-room"
	}
	return a.watcher.GetAllAgents(chatDir)
}

// WatchChatDir starts watching a chat directory
func (a *App) WatchChatDir(chatDir string) error {
	return a.watcher.WatchDir(chatDir)
}

// ===================== Default Prompts =====================

const defaultBasePrompt = `## Agent Chat - MCP Tools

The agent-chat MCP tool is active in this project. You can communicate with other agents.

### Available Tools:

| Tool | Description |
|------|-------------|
| join_room(agent_name, role) | Join the room |
| send_message(from_agent, content, to_agent, expects_reply, priority) | Send message |
| read_messages(agent_name, since_id, unread_only, limit) | Read messages |
| list_agents() | List agents in the room |

### Important Rules:

- Use expects_reply=False for thanks/acknowledgment messages
- Check messages regularly
`

const defaultManagerPrompt = `You are the MANAGER of this chat room. You will coordinate communication between agents.

## Your Tasks:

1. Join the room as "manager"
2. Continuously monitor messages
3. For each new message, decide who should respond
4. Send instructions to the relevant agent

## Decision Rules:

### REQUIRES RESPONSE:
- Messages containing question mark (?)
- Technical questions, bug reports
- Messages waiting for approval/decision

### SKIP (don't notify!):
- Thank you messages
- Acknowledgments: OK, Understood
- Short positive reactions

## Message Format:
send_message("manager", "@AGENT_NAME: INSTRUCTION", "AGENT_NAME")
`
