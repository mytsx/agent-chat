package orchestrator

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	ptymgr "desktop/internal/pty"
	"desktop/internal/watcher"
)

// shellMetaChars matches shell metacharacters that could enable command injection
var shellMetaChars = regexp.MustCompile("[;|&$`\\\\\"'\\n\\r]")

const (
	AckMsgMaxLength       = 80
	ContentPreviewLimit   = 60
	DirectMsgPreviewLimit = 50
	BroadcastDelay        = 500 // milliseconds
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
}

// New creates a new orchestrator
func New(ptyManager *ptymgr.Manager) *Orchestrator {
	return &Orchestrator{
		ptyManager:    ptyManager,
		agentSessions: make(map[string]map[string]string),
	}
}

// RegisterAgent registers an agent's PTY session for a chat directory
func (o *Orchestrator) RegisterAgent(chatDir, agentName, sessionID string) {
	if o.agentSessions[chatDir] == nil {
		o.agentSessions[chatDir] = make(map[string]string)
	}
	o.agentSessions[chatDir][agentName] = sessionID
}

// UnregisterAgent removes an agent's PTY session mapping
func (o *Orchestrator) UnregisterAgent(chatDir, agentName string) {
	if sessions, ok := o.agentSessions[chatDir]; ok {
		delete(sessions, agentName)
	}
}

// AnalyzeMessage analyzes a message and decides what action to take
func AnalyzeMessage(msg watcher.Message) AnalysisResult {
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

// sendToTerminal writes text to a PTY and presses Enter (like tmux send-keys + C-m)
func (o *Orchestrator) sendToTerminal(sessionID string, text string) {
	// Write the text (without newline)
	o.ptyManager.Write(sessionID, []byte(text))
	// Small delay like the original orchestrator.py (time.sleep(0.3))
	time.Sleep(300 * time.Millisecond)
	// Press Enter: send carriage return (\r) - equivalent to tmux send-keys C-m
	o.ptyManager.Write(sessionID, []byte("\r"))
}

// ProcessMessage processes a single message and notifies relevant agents
func (o *Orchestrator) ProcessMessage(chatDir string, msg watcher.Message) {
	// Skip system messages
	if msg.Type == "system" {
		return
	}

	analysis := AnalyzeMessage(msg)
	if analysis.Action == "skip" {
		return
	}

	sessions := o.agentSessions[chatDir]
	if sessions == nil {
		return
	}

	fromAgent := msg.From
	toAgent := msg.To
	content := sanitizeContent(msg.Content)
	runes := []rune(content)
	if len(runes) > DirectMsgPreviewLimit {
		content = string(runes[:DirectMsgPreviewLimit]) + "..."
	}

	if toAgent == "all" {
		// Broadcast - notify everyone except sender
		for agent, sessionID := range sessions {
			if agent != fromAgent {
				prompt := fmt.Sprintf("%s sent a message: \"%s\" - Read messages and respond.", fromAgent, content)
				o.sendToTerminal(sessionID, prompt)
				time.Sleep(time.Duration(BroadcastDelay) * time.Millisecond)
			}
		}
	} else if sessionID, ok := sessions[toAgent]; ok {
		// Direct message - notify target only
		prompt := fmt.Sprintf("%s sent you a message: \"%s\" - Read messages and respond.", fromAgent, content)
		o.sendToTerminal(sessionID, prompt)
	}
}

// sanitizeContent removes shell metacharacters from message content
// to prevent command injection when content is sent to terminal sessions
func sanitizeContent(content string) string {
	return shellMetaChars.ReplaceAllString(content, "")
}

// HandleNewMessages is the callback for the file watcher
func (o *Orchestrator) HandleNewMessages(chatDir string, messages []watcher.Message) {
	for _, msg := range messages {
		o.ProcessMessage(chatDir, msg)
	}
}
