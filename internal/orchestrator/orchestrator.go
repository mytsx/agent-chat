package orchestrator

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	ptymgr "desktop/internal/pty"
	"desktop/internal/types"
)

const (
	AckMsgMaxLength = 80
	// NotifyCooldown prevents rapid-fire notifications to the same agent.
	// If an agent was notified within this window, subsequent messages are
	// batched into a single "N new messages" notification.
	NotifyCooldown = 3 * time.Second
)

// ACK patterns - short acknowledgment messages to skip
var ACKPatterns = []string{
	"tesekkur", "sagol", "eyvallah", "tamam", "anladim", "ok", "oldu",
	"super", "harika", "mukemmel", "guzel", "rica ederim", "bir sey degil",
	"thanks", "thank you", "got it", "okay", "perfect", "great",
	"tamamdir", "anlasildi", "gorusuruz", "iyi calismalar",
	"evet", "hayir", "peki", "olur", "elbette",
}

// Question patterns - these should always be notified
var QuestionPatterns = []string{
	"?", "nasil", "neden", "ne zaman", "nerede", "kim", "hangi",
	"yapabilir mi", "mumkun mu", "var mi", "bilir mi", "ister mi",
	"how", "what", "when", "where", "who", "which", "can you", "could you",
}

// AnalysisResult represents the decision about a message
type AnalysisResult struct {
	Action     string `json:"action"` // "skip" or "notify"
	Reason     string `json:"reason"`
	IsQuestion bool   `json:"is_question"`
}

// AgentSession maps agent name to PTY session ID
type AgentSession struct {
	AgentName string
	SessionID string
}

// Orchestrator handles message routing to PTY sessions
type Orchestrator struct {
	ptyManager *ptymgr.Manager
	// Map of chatDir -> map of agentName -> sessionID
	agentSessions map[string]map[string]string

	// Per-agent cooldown tracking: key = "chatDir:agentName"
	mu            sync.Mutex
	lastNotified  map[string]time.Time
	pendingTimers map[string]*time.Timer
	pendingMsgs   map[string][]pendingNotification

	// sendFunc overrides sendToTerminal for testing. If nil, the real PTY path is used.
	sendFunc func(sessionID, text string)
}

// pendingNotification holds info about a message waiting in the cooldown window.
type pendingNotification struct {
	from string
}

// New creates a new orchestrator
func New(ptyManager *ptymgr.Manager) *Orchestrator {
	return &Orchestrator{
		ptyManager:    ptyManager,
		agentSessions: make(map[string]map[string]string),
		lastNotified:  make(map[string]time.Time),
		pendingTimers: make(map[string]*time.Timer),
		pendingMsgs:   make(map[string][]pendingNotification),
	}
}

// RegisterAgent registers an agent's PTY session for a chat directory
func (o *Orchestrator) RegisterAgent(chatDir, agentName, sessionID string) {
	o.mu.Lock()
	if o.agentSessions[chatDir] == nil {
		o.agentSessions[chatDir] = make(map[string]string)
	}
	o.agentSessions[chatDir][agentName] = sessionID
	o.mu.Unlock()
	log.Printf("[ORCH] RegisterAgent: chatDir=%s agent=%s session=%s", chatDir, agentName, ptymgr.ShortID(sessionID))
}

// UnregisterAgent removes an agent's PTY session mapping and cleans up cooldown state
func (o *Orchestrator) UnregisterAgent(chatDir, agentName string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if sessions, ok := o.agentSessions[chatDir]; ok {
		delete(sessions, agentName)
	}
	// F007: Clean up cooldown tracking for this agent
	key := chatDir + ":" + agentName
	delete(o.lastNotified, key)
	if timer, ok := o.pendingTimers[key]; ok {
		timer.Stop()
		delete(o.pendingTimers, key)
	}
	delete(o.pendingMsgs, key)
}

// AnalyzeMessage analyzes a message and decides what action to take
func AnalyzeMessage(msg types.Message) AnalysisResult {
	content := msg.Content
	contentLower := strings.ToLower(content)
	expectsReply := msg.ExpectsReply

	// Is it a question?
	isQuestion := false
	for _, p := range QuestionPatterns {
		if strings.Contains(contentLower, p) {
			isQuestion = true
			break
		}
	}

	// Is it a short acknowledgment?
	isShort := len([]rune(content)) < AckMsgMaxLength
	hasAck := false
	for _, p := range ACKPatterns {
		if strings.Contains(contentLower, p) {
			hasAck = true
			break
		}
	}
	isAck := isShort && hasAck && !isQuestion

	// Decision
	if isAck && !expectsReply {
		return AnalysisResult{Action: "skip", Reason: "Acknowledgment (expects_reply=false)", IsQuestion: false}
	} else if isAck {
		return AnalysisResult{Action: "skip", Reason: "Short acknowledgment message", IsQuestion: false}
	} else if isQuestion {
		return AnalysisResult{Action: "notify", Reason: "Question - response needed", IsQuestion: true}
	} else if expectsReply {
		return AnalysisResult{Action: "notify", Reason: "Response expected", IsQuestion: false}
	}
	return AnalysisResult{Action: "notify", Reason: "Informational", IsQuestion: false}
}

// sendToTerminal writes a short notification to a PTY.
// No user content is included — the agent reads the full message via MCP.
func (o *Orchestrator) sendToTerminal(sessionID string, text string) {
	if o.sendFunc != nil {
		o.sendFunc(sessionID, text)
		return
	}

	session := o.ptyManager.GetSession(sessionID)
	if session == nil {
		log.Printf("[ORCH] sendToTerminal: session not found id=%s", ptymgr.ShortID(sessionID))
		return
	}

	log.Printf("[ORCH] sendToTerminal: cli=%s agent=%s textLen=%d",
		session.CLIType, session.AgentName, len(text))

	switch session.CLIType {
	case "copilot":
		// Send Focus In so Copilot's Ink TUI accepts input even if the
		// terminal pane is not visually focused.
		o.ptyManager.Write(sessionID, []byte("\x1b[I"))
		time.Sleep(50 * time.Millisecond)
		// Copilot (Ink/React TUI): simulate keyboard input character by character.
		for _, c := range text {
			o.ptyManager.Write(sessionID, []byte(string(c)))
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		o.ptyManager.Write(sessionID, []byte("\r"))
	default:
		// Claude/Gemini: bracketed paste
		const (
			bracketOpen  = "\x1b[200~"
			bracketClose = "\x1b[201~"
		)
		o.ptyManager.Write(sessionID, []byte(bracketOpen+text+bracketClose))
		time.Sleep(200 * time.Millisecond)
		o.ptyManager.Write(sessionID, []byte("\r"))
	}
}

// notifyAgent sends a notification to an agent with cooldown/batching.
// If the agent was recently notified, subsequent messages are batched.
func (o *Orchestrator) notifyAgent(chatDir, agentName, sessionID, fromAgent string, isBroadcast bool) {
	key := chatDir + ":" + agentName

	o.mu.Lock()
	last := o.lastNotified[key]
	elapsed := time.Since(last)

	if elapsed < NotifyCooldown {
		// Within cooldown — batch this notification
		o.pendingMsgs[key] = append(o.pendingMsgs[key], pendingNotification{from: fromAgent})

		// Start flush timer if not already running
		if _, exists := o.pendingTimers[key]; !exists {
			remaining := NotifyCooldown - elapsed
			o.pendingTimers[key] = time.AfterFunc(remaining, func() {
				o.flushPending(chatDir, agentName, sessionID)
			})
		}
		pendingCount := len(o.pendingMsgs[key])
		o.mu.Unlock()
		log.Printf("[ORCH] Notification batched for agent=%s (cooldown), pending=%d", agentName, pendingCount)
		return
	}

	// Outside cooldown — send immediately
	o.lastNotified[key] = time.Now()
	o.mu.Unlock()

	var prompt string
	if isBroadcast {
		prompt = fmt.Sprintf("[agent-chat] Broadcast from %s. read_messages(\"%s\") to read and respond.", fromAgent, agentName)
	} else {
		prompt = fmt.Sprintf("[agent-chat] New message from %s. read_messages(\"%s\") to read and respond.", fromAgent, agentName)
	}
	log.Printf("[ORCH] Notifying agent=%s session=%s", agentName, ptymgr.ShortID(sessionID))
	o.sendToTerminal(sessionID, prompt)
}

// flushPending sends a batched notification for accumulated messages.
func (o *Orchestrator) flushPending(chatDir, agentName, sessionID string) {
	key := chatDir + ":" + agentName

	o.mu.Lock()
	pending := o.pendingMsgs[key]
	delete(o.pendingMsgs, key)
	delete(o.pendingTimers, key)
	o.lastNotified[key] = time.Now()
	o.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	// Collect unique senders
	senders := make(map[string]struct{})
	for _, p := range pending {
		senders[p.from] = struct{}{}
	}
	senderList := make([]string, 0, len(senders))
	for s := range senders {
		senderList = append(senderList, s)
	}

	prompt := fmt.Sprintf("[agent-chat] %d new messages from %s. read_messages(\"%s\") to read and respond.",
		len(pending), strings.Join(senderList, ", "), agentName)

	log.Printf("[ORCH] Flushing %d batched notifications for agent=%s", len(pending), agentName)
	o.sendToTerminal(sessionID, prompt)
}

// ProcessMessage processes a single message and notifies relevant agents
func (o *Orchestrator) ProcessMessage(chatDir string, msg types.Message) {
	log.Printf("[ORCH] ProcessMessage: chatDir=%s from=%s to=%s type=%s expects_reply=%v content_len=%d",
		chatDir, msg.From, msg.To, msg.Type, msg.ExpectsReply, len(msg.Content))

	// Skip system messages
	if msg.Type == "system" {
		log.Printf("[ORCH] Skipping system message")
		return
	}

	// Manager-routed messages must always notify the manager target, even for ACK-like content.
	if msg.RoutedByManager {
		o.mu.Lock()
		sessions := o.agentSessions[chatDir]
		if sessions == nil {
			o.mu.Unlock()
			log.Printf("[ORCH] No agent sessions for chatDir=%s (manager-routed)", chatDir)
			return
		}
		sessionsCopy := make(map[string]string, len(sessions))
		for k, v := range sessions {
			sessionsCopy[k] = v
		}
		o.mu.Unlock()

		target := msg.To
		if sessionID, ok := sessionsCopy[target]; ok {
			log.Printf("[ORCH] Manager-routed notify: from=%s manager=%s original_to=%s", msg.From, target, msg.OriginalTo)
			o.notifyAgent(chatDir, target, sessionID, msg.From, false)
		} else {
			log.Printf("[ORCH] Manager-routed target not found: %s", target)
		}
		return
	}

	analysis := AnalyzeMessage(msg)
	log.Printf("[ORCH] Analysis: action=%s reason=%s", analysis.Action, analysis.Reason)
	if analysis.Action == "skip" {
		return
	}

	// Snapshot sessions under lock to avoid race with RegisterAgent/UnregisterAgent
	o.mu.Lock()
	sessions := o.agentSessions[chatDir]
	if sessions == nil {
		log.Printf("[ORCH] No agent sessions for chatDir=%s (registered dirs: %v)", chatDir, mapKeys(o.agentSessions))
		o.mu.Unlock()
		return
	}
	// Copy map so we can release the lock before sending notifications
	sessionsCopy := make(map[string]string, len(sessions))
	for k, v := range sessions {
		sessionsCopy[k] = v
	}
	o.mu.Unlock()

	log.Printf("[ORCH] Registered agents for chatDir: %v", mapKeys(sessionsCopy))

	fromAgent := msg.From
	toAgent := msg.To

	if toAgent == "all" {
		// Broadcast - notify everyone except sender
		for agent, sessionID := range sessionsCopy {
			if agent != fromAgent {
				o.notifyAgent(chatDir, agent, sessionID, fromAgent, true)
			}
		}
	} else if sessionID, ok := sessionsCopy[toAgent]; ok {
		// Direct message - notify target only
		o.notifyAgent(chatDir, toAgent, sessionID, fromAgent, false)
	} else {
		log.Printf("[ORCH] Target agent=%s not found in sessions", toAgent)
	}
}

// mapKeys returns the keys of a map as a slice (for logging)
func mapKeys[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// HandleNewMessages is the callback for the file watcher
func (o *Orchestrator) HandleNewMessages(chatDir string, messages []types.Message) {
	for _, msg := range messages {
		o.ProcessMessage(chatDir, msg)
	}
}
