package orchestrator

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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

// ackPatterns - short acknowledgment messages to skip.
// Bir slice burada en hızlı idiomatic yapıdır — map yalnız tam-eşitlik
// aramasında O(1) verir, "contains" değil, ve map iterasyonu ölçülebilir
// biçimde daha yavaştır (bench: ~2x). Eşleştirme matchesAckPattern ile TAM
// KELİME (whole-word) yapılır; bu yüzden Türkçe ekli yaygın ack formları
// ("tesekkurler", "sagolun"...) listede AÇIKÇA yer alır — stem prefix'i değil.
var ackPatterns = []string{
	"tesekkur", "tesekkurler", "sagol", "sagolun", "sagolasin", "eyvallah",
	"tamam", "tamamdir", "anladim", "ok", "oldu",
	"super", "harika", "mukemmel", "guzel", "rica ederim", "bir sey degil",
	"thanks", "thank you", "got it", "okay", "perfect", "great",
	"anlasildi", "gorusuruz", "iyi calismalar",
	"evet", "hayir", "peki", "olur", "elbette", "okey", "oke",
}

// questionPatterns - patterns indicating questions, should always be notified.
// Not: questionPatterns kasıtlı olarak substring (strings.Contains) ile eşleşir.
// Buradaki yanlış-pozitifin sonucu "fazladan bildirim" olduğundan (zararsız),
// ack tarafındaki "sessiz atlama" riskinin aksine, kelime-sınırı uygulanmadı.
var questionPatterns = []string{
	"?", "nasil", "neden", "ne zaman", "nerede", "kim", "hangi",
	"yapabilir mi", "mumkun mu", "var mi", "bilir mi", "ister mi",
	"how", "what", "when", "where", "who", "which", "can you", "could you",
}

// isWordRune: kelime-içi karakter mi (harf, rakam veya alt çizgi). Standart \w
// tanımıyla uyumlu — alt çizgi sayesinde "status_ok"/"exit_ok" gibi teknik
// ifadelerdeki "ok" bağımsız kelime sayılmaz. Türkçe dahil tüm Unicode harfleri
// kapsar. ASCII için inline range check (stdlib idiom'u) — ölçülen ~%24 kazanç.
func isWordRune(r rune) bool {
	if r < utf8.RuneSelf { // ASCII fast-path
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_'
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

// matchesAckPattern: p, s içinde TAM KELİME olarak (her iki yanı kelime sınırı
// ya da string kenarı) geçiyor mu? Hem "ok" ⊂ "okul"/"doktor" gibi harf-önekli/
// soneki yanlış eşleşmeleri, hem de "oldu" ⊂ "oldukca" / "peki" ⊂ "pekistirmek"
// gibi stem-önekli yanlış eşleşmeleri eler. s ve p küçük harfli varsayılır.
//
// Tasarım notu: prefix (yalnız sol-sınır) eşleştirme "tesekkurler"i "tesekkur"
// stem'iyle yakalardı ama "oldukca"yı da "oldu" ile yanlış yakalardı (zararlı
// sessiz skip). Onun yerine tam-kelime kullanılır ve yaygın ekli ack formları
// ackPatterns'a açıkça eklenir. Başarısızlık yönü güvenlidir: listede olmayan
// ekli bir ack tam-kelime eşleşmez → yalnız fazladan bildirim (skip yerine
// notify), asla zararlı sessiz atlama.
func matchesAckPattern(s, p string) bool {
	from := 0
	for {
		i := strings.Index(s[from:], p)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(p)

		leftOK := start == 0
		if !leftOK {
			r, _ := utf8.DecodeLastRuneInString(s[:start])
			leftOK = !isWordRune(r)
		}
		// Sol sınır başarısızsa sağ sınırı çözmeye gerek yok (short-circuit).
		if leftOK {
			rightOK := end == len(s)
			if !rightOK {
				r, _ := utf8.DecodeRuneInString(s[end:])
				rightOK = !isWordRune(r)
			}
			if rightOK {
				return true
			}
		}
		// Eşleşmenin sonuna atla. Atlanan tek olası konumlar p ile örtüşen
		// occurrence'lardır; bunlar ancak p'nin bir border'ı (prefix==suffix)
		// varsa oluşur ve o örtüşme noktası p'nin İÇİNDE kalır. Mevcut
		// pattern'lerde border ya yok ya da (elbette → "e") harf olduğundan
		// örtüşen occurrence'ın solu daima harftir → zaten whole-word eşleşmez.
		// Dolayısıyla start+len(p) güvenli ve start+1'den daha verimlidir.
		from = start + len(p)
	}
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
	// deferStartedAt records when a notification first started being deferred
	// because the user was typing — used to cap the deferral at maxDeferral.
	deferStartedAt map[string]time.Time

	// typingQuietWindow: how long after the last keystroke the user is still
	// considered "typing" (injection is deferred during this window).
	typingQuietWindow time.Duration
	// maxDeferral: hard cap on how long a notification may be deferred while the
	// user keeps typing; once exceeded, it is routed to the UI instead of the PTY.
	maxDeferral time.Duration

	// onDeferredToUI is the mandatory fallback: when maxDeferral is exceeded and
	// the user is still typing, the notification is surfaced in the UI rather
	// than injected into the PTY (where it would corrupt the user's input).
	onDeferredToUI func(sessionID, agentName, prompt string)

	// injectFunc overrides the real PTY injection for testing. withCR reports
	// whether the trailing carriage return would be sent. If nil, the real PTY
	// path is used.
	injectFunc func(sessionID, text string, withCR bool)
	// typingFunc overrides the user-typing query for testing. If nil, the real
	// pty.Manager query is used.
	typingFunc func(sessionID string) bool
}

// pendingNotification holds info about a message waiting in the cooldown window.
type pendingNotification struct {
	from string
}

const (
	// defaultTypingQuietWindow: keystrokes within this window mean the user is
	// still typing, so injection is deferred.
	defaultTypingQuietWindow = 1500 * time.Millisecond
	// defaultMaxDeferral caps how long a notification is held while the user
	// keeps typing before falling back to the UI. Kept well under the 300s stale
	// timeout so notifications never silently rot.
	defaultMaxDeferral = 12 * time.Second
)

// New creates a new orchestrator
func New(ptyManager *ptymgr.Manager) *Orchestrator {
	return &Orchestrator{
		ptyManager:        ptyManager,
		agentSessions:     make(map[string]map[string]string),
		lastNotified:      make(map[string]time.Time),
		pendingTimers:     make(map[string]*time.Timer),
		pendingMsgs:       make(map[string][]pendingNotification),
		deferStartedAt:    make(map[string]time.Time),
		typingQuietWindow: defaultTypingQuietWindow,
		maxDeferral:       defaultMaxDeferral,
	}
}

// SetDeferredHandler installs the UI fallback invoked when a notification cannot
// be safely injected into the PTY (maxDeferral exceeded while the user types).
func (o *Orchestrator) SetDeferredHandler(fn func(sessionID, agentName, prompt string)) {
	o.onDeferredToUI = fn
}

// userTyping reports whether the user is currently typing into the session.
// IMPORTANT: this queries pty.Manager (which locks its own mutex); callers must
// invoke it OUTSIDE o.mu to preserve lock ordering (never hold o.mu while
// locking pty.Manager.mu).
func (o *Orchestrator) userTyping(sessionID string) bool {
	if o.typingFunc != nil {
		return o.typingFunc(sessionID)
	}
	if o.ptyManager != nil {
		return o.ptyManager.UserTypingRecently(sessionID, o.typingQuietWindow)
	}
	return false
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
	// F007: Clean up cooldown/deferral tracking for this agent
	key := chatDir + ":" + agentName
	delete(o.lastNotified, key)
	if timer, ok := o.pendingTimers[key]; ok {
		timer.Stop()
		delete(o.pendingTimers, key)
	}
	delete(o.pendingMsgs, key)
	delete(o.deferStartedAt, key)
}

// AnalyzeMessage analyzes a message and decides what action to take
func AnalyzeMessage(msg types.Message) AnalysisResult {
	content := msg.Content
	contentLower := strings.ToLower(content)
	expectsReply := msg.ExpectsReply

	// Is it a question?
	isQuestion := false
	for _, p := range questionPatterns {
		if strings.Contains(contentLower, p) {
			isQuestion = true
			break
		}
	}

	// Is it a short acknowledgment? Sorular zaten notify edilir ve ack olamaz
	// (isAck = isShort && hasAck && !isQuestion), bu yüzden soru ise ack taramasını
	// tamamen atla.
	// isShort: mesaj ack olacak kadar kısa mı? Rune sayımı O(N), byte uzunluğu
	// O(1). UTF-8'de her rune 1..4 byte olduğundan byte uzunluğu çoğu durumu
	// kesin belirler: byteLen<AckMsgMaxLength ise rune sayısı da kesin küçüktür;
	// byteLen>=AckMsgMaxLength*UTFMax ise kesin büyüktür. Yalnız bu iki sınır
	// arasındaki gri bölgede gerçek rune sayımı gerekir — böylece kısa (yaygın)
	// ve uzun mesajlarda tam tarama atlanır. (utf8.RuneCountInString < ... ile
	// birebir aynı sonucu verir; eşdeğerlik testle doğrulandı.)
	isAck := false
	if !isQuestion {
		byteLen := len(content)
		isShort := byteLen < AckMsgMaxLength ||
			(byteLen < AckMsgMaxLength*utf8.UTFMax && utf8.RuneCountInString(content) < AckMsgMaxLength)
		if isShort {
			for _, p := range ackPatterns {
				if matchesAckPattern(contentLower, p) {
					isAck = true
					break
				}
			}
		}
	}

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
//
// The whole injection runs under the session's write mutex (WriteAtomic) so a
// user keystroke cannot land between the notification's bytes. The trailing CR
// is conditional: if the user is typing it is skipped, so a half-typed line is
// never submitted early.
func (o *Orchestrator) sendToTerminal(sessionID string, text string) {
	// Decision at call time (also the test contract). The real PTY path
	// re-checks right before the CR to catch a keystroke that arrived while the
	// write mutex was held.
	sendCR := !o.userTyping(sessionID)

	if o.injectFunc != nil {
		o.injectFunc(sessionID, text, sendCR)
		return
	}

	session := o.ptyManager.GetSession(sessionID)
	if session == nil {
		log.Printf("[ORCH] sendToTerminal: session not found id=%s", ptymgr.ShortID(sessionID))
		return
	}

	log.Printf("[ORCH] sendToTerminal: cli=%s agent=%s textLen=%d sendCR=%v",
		session.CLIType, session.AgentName, len(text), sendCR)

	cliType := session.CLIType
	err := o.ptyManager.WriteAtomic(sessionID, func(write func([]byte) error) error {
		switch cliType {
		case "copilot":
			// Send Focus In so Copilot's Ink TUI accepts input even if the
			// terminal pane is not visually focused.
			if err := write([]byte("\x1b[I")); err != nil {
				return err
			}
			time.Sleep(50 * time.Millisecond)
			// Copilot (Ink/React TUI): simulate keyboard input character by character.
			for _, c := range text {
				if err := write([]byte(string(c))); err != nil {
					return err
				}
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(100 * time.Millisecond)
		default:
			// Claude/Gemini: bracketed paste
			const (
				bracketOpen  = "\x1b[200~"
				bracketClose = "\x1b[201~"
			)
			if err := write([]byte(bracketOpen + text + bracketClose)); err != nil {
				return err
			}
			time.Sleep(200 * time.Millisecond)
		}
		// Conditional trailing CR (re-checked at the last moment).
		if o.userTyping(sessionID) {
			log.Printf("[ORCH] sendToTerminal: skipping CR, user resumed typing agent=%s", session.AgentName)
			return nil
		}
		return write([]byte("\r"))
	})
	if err != nil {
		log.Printf("[ORCH] sendToTerminal write error agent=%s: %v", session.AgentName, err)
	}
}

// notifyAgent sends a notification to an agent with cooldown/batching.
// If the agent was recently notified, subsequent messages are batched.
func (o *Orchestrator) notifyAgent(chatDir, agentName, sessionID, fromAgent string, isBroadcast bool) {
	key := chatDir + ":" + agentName

	// Query typing OUTSIDE o.mu (lock ordering: never hold o.mu while locking
	// pty.Manager.mu).
	typing := o.userTyping(sessionID)

	o.mu.Lock()
	elapsed := time.Since(o.lastNotified[key])
	withinCooldown := elapsed < NotifyCooldown

	if withinCooldown || typing {
		// Queue this notification. The same pending mechanism serves both the
		// cooldown batch and the typing-defer; a single timer drives the flush.
		o.pendingMsgs[key] = append(o.pendingMsgs[key], pendingNotification{from: fromAgent})
		if typing {
			if _, ok := o.deferStartedAt[key]; !ok {
				o.deferStartedAt[key] = time.Now()
			}
		}
		if _, exists := o.pendingTimers[key]; !exists {
			delay := o.typingQuietWindow
			if withinCooldown {
				delay = NotifyCooldown - elapsed
			}
			o.pendingTimers[key] = time.AfterFunc(delay, func() {
				o.flushPending(chatDir, agentName, sessionID)
			})
		}
		pendingCount := len(o.pendingMsgs[key])
		o.mu.Unlock()
		log.Printf("[ORCH] Notification queued for agent=%s (typing=%v cooldown=%v), pending=%d",
			agentName, typing, withinCooldown, pendingCount)
		return
	}

	// Outside cooldown and not typing — send immediately
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

// flushPending sends a batched notification for accumulated messages. If the
// user is still typing it RE-ARMs the existing timer (up to maxDeferral); once
// the cap is exceeded it routes the notification to the UI fallback instead of
// injecting into the PTY.
func (o *Orchestrator) flushPending(chatDir, agentName, sessionID string) {
	key := chatDir + ":" + agentName

	// Query typing OUTSIDE o.mu (lock ordering).
	typing := o.userTyping(sessionID)

	o.mu.Lock()
	if typing {
		startedAt, hasStart := o.deferStartedAt[key]
		if !hasStart {
			// First time we observe typing for this batch (e.g. a cooldown batch
			// whose flush coincides with the user starting to type).
			startedAt = time.Now()
			o.deferStartedAt[key] = startedAt
		}
		if time.Since(startedAt) < o.maxDeferral {
			// Within the cap — RE-ARM the SAME timer slot (no second timer), keep
			// the queued messages, and try again after another quiet window.
			if tm, ok := o.pendingTimers[key]; ok {
				tm.Stop()
			}
			o.pendingTimers[key] = time.AfterFunc(o.typingQuietWindow, func() {
				o.flushPending(chatDir, agentName, sessionID)
			})
			o.mu.Unlock()
			log.Printf("[ORCH] Notification deferred (user typing) agent=%s", agentName)
			return
		}
		// maxDeferral exceeded — fall through to the fallback below.
	}

	pending := o.pendingMsgs[key]
	delete(o.pendingMsgs, key)
	delete(o.pendingTimers, key)
	delete(o.deferStartedAt, key)
	o.lastNotified[key] = time.Now()
	o.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	// Collect unique senders (preserving first-seen order).
	seen := make(map[string]struct{}, len(pending))
	senderList := make([]string, 0, len(pending))
	for _, p := range pending {
		if _, ok := seen[p.from]; ok {
			continue
		}
		seen[p.from] = struct{}{}
		senderList = append(senderList, p.from)
	}

	var prompt string
	if len(pending) == 1 {
		prompt = fmt.Sprintf("[agent-chat] New message from %s. read_messages(\"%s\") to read and respond.",
			senderList[0], agentName)
	} else {
		prompt = fmt.Sprintf("[agent-chat] %d new messages from %s. read_messages(\"%s\") to read and respond.",
			len(pending), strings.Join(senderList, ", "), agentName)
	}

	if typing {
		// maxDeferral exceeded but the user is still typing: injecting now would
		// corrupt their input line. Route to the UI instead (mandatory fallback).
		if o.onDeferredToUI != nil {
			log.Printf("[ORCH] maxDeferral exceeded, routing notification to UI agent=%s", agentName)
			o.onDeferredToUI(sessionID, agentName, prompt)
		} else {
			// No handler wired (e.g. headless): don't fail silently. The message
			// itself is still in the hub — the agent can read_messages later.
			log.Printf("[ORCH] WARN: maxDeferral exceeded but no UI handler set; auto-poke dropped for agent=%s", agentName)
		}
		return
	}

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
