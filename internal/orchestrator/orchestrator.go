package orchestrator

import (
	"fmt"
	"log"
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
	DirectMsgPreviewLimit = 100
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
	log.Printf("[ORCH] RegisterAgent: chatDir=%s agent=%s session=%s", chatDir, agentName, sessionID[:8])
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

// sendToTerminal writes text to a PTY and presses Enter.
// Uses CLI-specific input handling to ensure proper submission.
func (o *Orchestrator) sendToTerminal(sessionID string, text string) {
	session := o.ptyManager.GetSession(sessionID)
	if session == nil {
		log.Printf("[ORCH] sendToTerminal: session not found id=%s", sessionID[:8])
		return
	}

	log.Printf("[ORCH] sendToTerminal: cli=%s agent=%s textLen=%d text_preview=%.60s",
		session.CLIType, session.AgentName, len(text), text)

	switch session.CLIType {
	case "copilot":
		// Copilot (Ink/React TUI): simulate keyboard input character by character.
		// Bulk PTY writes don't trigger Ink's input handler correctly.
		log.Printf("[ORCH] Copilot: sending char-by-char (%d chars) + \\r", len(text))
		for _, c := range text {
			o.ptyManager.Write(sessionID, []byte(string(c)))
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		o.ptyManager.Write(sessionID, []byte("\r"))
		log.Printf("[ORCH] Copilot: \\r sent")
	default:
		// Claude/Gemini: bracketed paste prevents shell mode triggers
		const (
			bracketOpen  = "\x1b[200~"
			bracketClose = "\x1b[201~"
		)
		o.ptyManager.Write(sessionID, []byte(bracketOpen+text+bracketClose))
		time.Sleep(200 * time.Millisecond)
		o.ptyManager.Write(sessionID, []byte("\r"))
	}
}

// ProcessMessage processes a single message and notifies relevant agents
func (o *Orchestrator) ProcessMessage(chatDir string, msg watcher.Message) {
	log.Printf("[ORCH] ProcessMessage: chatDir=%s from=%s to=%s type=%s expects_reply=%v content_len=%d",
		chatDir, msg.From, msg.To, msg.Type, msg.ExpectsReply, len(msg.Content))

	// Skip system messages
	if msg.Type == "system" {
		log.Printf("[ORCH] Skipping system message")
		return
	}

	analysis := AnalyzeMessage(msg)
	log.Printf("[ORCH] Analysis: action=%s reason=%s", analysis.Action, analysis.Reason)
	if analysis.Action == "skip" {
		return
	}

	sessions := o.agentSessions[chatDir]
	if sessions == nil {
		log.Printf("[ORCH] No agent sessions for chatDir=%s (registered dirs: %v)", chatDir, mapKeys(o.agentSessions))
		return
	}

	log.Printf("[ORCH] Registered agents for chatDir: %v", mapKeys(sessions))

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
				log.Printf("[ORCH] Broadcasting to agent=%s session=%s", agent, sessionID[:8])
				o.sendToTerminal(sessionID, prompt)
				time.Sleep(time.Duration(BroadcastDelay) * time.Millisecond)
			}
		}
	} else if sessionID, ok := sessions[toAgent]; ok {
		// Direct message - notify target only
		prompt := fmt.Sprintf("%s sent you a message: \"%s\" - Read messages and respond.", fromAgent, content)
		log.Printf("[ORCH] Direct message to agent=%s session=%s", toAgent, sessionID[:8])
		o.sendToTerminal(sessionID, prompt)
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
