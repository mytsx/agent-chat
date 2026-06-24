package orchestrator

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
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
	// because the user had pending input — used to cap the deferral at maxDeferral.
	deferStartedAt map[string]time.Time

	// reArmInterval: how often a deferred notification re-checks whether the
	// user's input line has cleared (Enter) before trying to inject again.
	reArmInterval time.Duration
	// maxDeferral: hard cap on how long a notification may be deferred while the
	// user keeps a pending line; once exceeded, it is routed to the UI instead of
	// the PTY.
	maxDeferral time.Duration

	// onDeferredToUI is the mandatory fallback: when maxDeferral is exceeded and
	// the user still has pending input, the notification is surfaced in the UI
	// rather than injected into the PTY (where it would corrupt the user's input).
	onDeferredToUI func(sessionID, agentName, prompt string)

	// deferralEnabled gates the pending-input check. When false (default) the
	// orchestrator never defers notifications — inject happens immediately
	// regardless of what the user has typed in the terminal. Toggle via
	// SetDeferralEnabled (wired from the Settings panel).
	deferralEnabled atomic.Bool

	// injectFunc overrides the real PTY injection for testing. If nil, the real
	// PTY path is used.
	injectFunc func(sessionID, text string)
	// pendingInputFunc overrides the pending-input query for testing. If nil, the
	// real pty.Manager query is used.
	pendingInputFunc func(sessionID string) bool
}

// pendingNotification holds info about a message waiting in the cooldown window.
type pendingNotification struct {
	from string
}

const (
	// defaultReArmInterval: how often a deferred notification re-checks whether
	// the user's input line has cleared (Enter) before trying to inject again.
	defaultReArmInterval = 1500 * time.Millisecond
	// defaultMaxDeferral caps how long a notification is held while the user
	// keeps a pending line before falling back to the UI. Kept well under the
	// 300s stale timeout so notifications never silently rot.
	defaultMaxDeferral = 12 * time.Second
)

// New creates a new orchestrator
func New(ptyManager *ptymgr.Manager) *Orchestrator {
	return &Orchestrator{
		ptyManager:     ptyManager,
		agentSessions:  make(map[string]map[string]string),
		lastNotified:   make(map[string]time.Time),
		pendingTimers:  make(map[string]*time.Timer),
		pendingMsgs:    make(map[string][]pendingNotification),
		deferStartedAt: make(map[string]time.Time),
		reArmInterval:  defaultReArmInterval,
		maxDeferral:    defaultMaxDeferral,
	}
}

// SetDeferredHandler installs the UI fallback invoked when a notification cannot
// be safely injected into the PTY (maxDeferral exceeded while the user has a
// pending input line).
func (o *Orchestrator) SetDeferredHandler(fn func(sessionID, agentName, prompt string)) {
	o.onDeferredToUI = fn
}

// SetDeferralEnabled toggles the pending-input deferral check. When disabled
// (the default) notifications are injected immediately without inspecting the
// terminal's input buffer. Enable only when you want the "don't interrupt
// typing" protection.
func (o *Orchestrator) SetDeferralEnabled(enabled bool) {
	o.deferralEnabled.Store(enabled)
}

// hasPendingInput reports whether the user has an unsubmitted input line in the
// session (anything typed since the last Enter). Injecting into such a line
// would corrupt it, so notifications are deferred while it is true.
// IMPORTANT: this queries pty.Manager (which locks its own mutex); callers must
// invoke it OUTSIDE o.mu to preserve lock ordering (never hold o.mu while
// locking pty.Manager.mu).
func (o *Orchestrator) hasPendingInput(sessionID string) bool {
	// Deferral feature is disabled (default) — never block on pending input.
	if !o.deferralEnabled.Load() {
		return false
	}
	if o.pendingInputFunc != nil {
		return o.pendingInputFunc(sessionID)
	}
	if o.ptyManager != nil {
		return o.ptyManager.HasPendingInput(sessionID)
	}
	return false
}

// isCurrentSessionLocked reports whether sessionID is still the session bound to
// (chatDir, agentName). Caller MUST hold o.mu. Used to drop stale timer/inject
// callbacks after an agent is unregistered or restarted with a new sessionID
// (review GR2/GR3).
func (o *Orchestrator) isCurrentSessionLocked(chatDir, agentName, sessionID string) bool {
	sessions, ok := o.agentSessions[chatDir]
	return ok && sessions[agentName] == sessionID
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

// buildPrompt builds the single-message notification text.
func buildPrompt(isBroadcast bool, fromAgent, agentName string) string {
	if isBroadcast {
		return fmt.Sprintf("[agent-chat] Broadcast from %s. read_messages(\"%s\") to read and respond.", fromAgent, agentName)
	}
	return fmt.Sprintf("[agent-chat] New message from %s. read_messages(\"%s\") to read and respond.", fromAgent, agentName)
}

// buildBatchedPrompt builds the notification text for a batch of pending msgs,
// deduplicating senders (first-seen order) and reading naturally for one msg.
func buildBatchedPrompt(pending []pendingNotification, agentName string) string {
	seen := make(map[string]struct{}, len(pending))
	senders := make([]string, 0, len(pending))
	for _, p := range pending {
		if _, ok := seen[p.from]; ok {
			continue
		}
		seen[p.from] = struct{}{}
		senders = append(senders, p.from)
	}
	if len(pending) == 1 {
		return fmt.Sprintf("[agent-chat] New message from %s. read_messages(\"%s\") to read and respond.",
			senders[0], agentName)
	}
	return fmt.Sprintf("[agent-chat] %d new messages from %s. read_messages(\"%s\") to read and respond.",
		len(pending), strings.Join(senders, ", "), agentName)
}

// tryInject performs an atomic, lag-free notification injection into a PTY. It
// returns false WITHOUT writing anything if the user has pending input (the
// caller must keep deferring) — this is the last-line-of-defence check against
// corrupting a half-typed line.
//
// For Claude/Gemini the pending-input check and the bracketed-paste write run
// together under the per-session write mutex (a short critical section, no
// sleep), so a racing keystroke is either observed by the check (→ we skip) or
// blocked until the paste lands (→ it appends after our text, never splitting
// it). The settle then runs OUTSIDE the lock (no input lag), and the trailing CR
// is always sent: the buffer was empty at paste time, so submitting is safe and
// pokes the agent. For copilot the whole char-by-char sequence must stay atomic.
func (o *Orchestrator) tryInject(sessionID, text string) bool {
	if o.injectFunc != nil {
		if o.hasPendingInput(sessionID) {
			return false
		}
		o.injectFunc(sessionID, text)
		return true
	}

	session := o.ptyManager.GetSession(sessionID)
	if session == nil {
		log.Printf("[ORCH] tryInject: session not found id=%s", ptymgr.ShortID(sessionID))
		return true // nothing to defer to
	}
	agentName := session.AgentName
	log.Printf("[ORCH] tryInject: cli=%s agent=%s textLen=%d", session.CLIType, agentName, len(text))

	if session.CLIType == "copilot" {
		// Copilot's Ink/React TUI needs character-by-character input; the whole
		// sequence (focus-in + chars + CR) must be one atomic block, so a user
		// keystroke can't interleave and garble the command. The inter-char sleeps
		// therefore run under the write mutex (injection only fires when the user
		// has no pending line, so this rarely blocks anyone).
		injected := false
		err := o.ptyManager.WriteAtomic(sessionID, func(write func([]byte) error) error {
			if o.hasPendingInput(sessionID) {
				return nil
			}
			if err := write([]byte("\x1b[I")); err != nil {
				return err
			}
			time.Sleep(50 * time.Millisecond)
			for _, c := range text {
				if err := write([]byte(string(c))); err != nil {
					return err
				}
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(100 * time.Millisecond)
			if err := write([]byte("\r")); err != nil {
				return err
			}
			// Only mark injected after every write succeeded, so a write failure
			// (e.g. a closing PTY) is reported as not-injected and re-deferred
			// rather than silently lost (review GR1).
			injected = true
			return nil
		})
		if err != nil {
			log.Printf("[ORCH] tryInject write error agent=%s: %v", agentName, err)
		}
		return injected
	}

	// Claude/Gemini: the ENTIRE injection — pending pre-check, bracketed paste,
	// settle, and submitting CR — runs under the write mutex so a user keystroke
	// cannot land between the paste and the CR (which would append it to the
	// notification and submit it — review CR1). The settle lets the Ink TUI
	// register the paste before Enter. This mirrors the copilot path above.
	// Injection only fires when the user has no pending line, so the brief
	// lock-hold during the settle rarely blocks anyone — and a rare ~200ms input
	// lag is strictly preferable to the input corruption that releasing the lock
	// here would reintroduce.
	const (
		bracketOpen  = "\x1b[200~"
		bracketClose = "\x1b[201~"
	)
	injected := false
	err := o.ptyManager.WriteAtomic(sessionID, func(write func([]byte) error) error {
		if o.hasPendingInput(sessionID) {
			return nil
		}
		if err := write([]byte(bracketOpen + text + bracketClose)); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
		if err := write([]byte("\r")); err != nil {
			return err
		}
		// Only mark injected after every write succeeded, so a write failure
		// (e.g. a closing PTY) is reported as not-injected and re-deferred
		// rather than silently lost (review GR1).
		injected = true
		return nil
	})
	if err != nil {
		log.Printf("[ORCH] tryInject write error agent=%s: %v", agentName, err)
	}
	return injected
}

// queueLocked appends a pending notification and arms the flush timer if one is
// not already running. Caller MUST hold o.mu. When pending is true the
// deferStartedAt stamp is set so maxDeferral can be enforced.
func (o *Orchestrator) queueLocked(key, chatDir, agentName, sessionID, fromAgent string, pending, withinCooldown bool, elapsed time.Duration) {
	o.pendingMsgs[key] = append(o.pendingMsgs[key], pendingNotification{from: fromAgent})
	if pending {
		if _, ok := o.deferStartedAt[key]; !ok {
			o.deferStartedAt[key] = time.Now()
		}
	}
	if _, exists := o.pendingTimers[key]; !exists {
		delay := o.reArmInterval
		if withinCooldown {
			if rem := NotifyCooldown - elapsed; rem > delay {
				delay = rem
			}
		}
		o.pendingTimers[key] = time.AfterFunc(delay, func() {
			o.flushPending(chatDir, agentName, sessionID)
		})
	}
}

// notifyAgent sends a notification to an agent. It batches within the cooldown
// window and defers while the user has a pending input line; otherwise it
// injects immediately.
func (o *Orchestrator) notifyAgent(chatDir, agentName, sessionID, fromAgent string, isBroadcast bool) {
	key := chatDir + ":" + agentName

	// Query pending-input OUTSIDE o.mu (lock ordering: never hold o.mu while
	// locking pty.Manager.mu).
	pending := o.hasPendingInput(sessionID)

	o.mu.Lock()
	elapsed := time.Since(o.lastNotified[key])
	withinCooldown := elapsed < NotifyCooldown
	if withinCooldown || pending {
		o.queueLocked(key, chatDir, agentName, sessionID, fromAgent, pending, withinCooldown, elapsed)
		pendingCount := len(o.pendingMsgs[key])
		o.mu.Unlock()
		log.Printf("[ORCH] Notification queued for agent=%s (pending=%v cooldown=%v), count=%d",
			agentName, pending, withinCooldown, pendingCount)
		return
	}
	// Reserve the cooldown UNDER THE LOCK, before the slow PTY injection, so a
	// concurrent notifyAgent for the same target sees the cooldown and batches
	// instead of injecting a duplicate (review CX2).
	o.lastNotified[key] = time.Now()
	o.mu.Unlock()

	// Outside cooldown and no pending input — inject immediately.
	if o.tryInject(sessionID, buildPrompt(isBroadcast, fromAgent, agentName)) {
		log.Printf("[ORCH] Notified agent=%s session=%s", agentName, ptymgr.ShortID(sessionID))
		return
	}

	// Raced: pending input appeared between the check and the injection → defer.
	o.mu.Lock()
	o.queueLocked(key, chatDir, agentName, sessionID, fromAgent, true, false, 0)
	o.mu.Unlock()
	log.Printf("[ORCH] Immediate inject raced into pending input, deferred agent=%s", agentName)
}

// flushPending injects (or re-defers) the accumulated notifications for a key.
// If the user still has a pending line it RE-ARMs the single timer (up to
// maxDeferral); once the cap is exceeded it routes to the UI fallback instead of
// corrupting the PTY input.
func (o *Orchestrator) flushPending(chatDir, agentName, sessionID string) {
	key := chatDir + ":" + agentName

	// Query pending-input OUTSIDE o.mu (lock ordering).
	pending := o.hasPendingInput(sessionID)

	o.mu.Lock()
	if !o.isCurrentSessionLocked(chatDir, agentName, sessionID) {
		// The agent was unregistered or restarted (now bound to a different
		// sessionID) since this timer was armed. This callback is stale — drop it
		// without touching the new session's pending state/timer (review GR2).
		o.mu.Unlock()
		return
	}
	if len(o.pendingMsgs[key]) == 0 {
		// Nothing to flush — e.g. UnregisterAgent cleared the queue between the
		// check above and acquiring the lock. Don't re-arm a stale timer (C2).
		delete(o.pendingTimers, key)
		delete(o.deferStartedAt, key)
		o.mu.Unlock()
		return
	}

	if pending {
		startedAt, hasStart := o.deferStartedAt[key]
		if !hasStart {
			startedAt = time.Now()
			o.deferStartedAt[key] = startedAt
		}
		if time.Since(startedAt) < o.maxDeferral {
			// Within the cap — RE-ARM the SAME timer slot (no second timer), keep
			// the queued messages, and try again after another interval.
			if tm := o.pendingTimers[key]; tm != nil {
				tm.Stop()
			}
			o.pendingTimers[key] = time.AfterFunc(o.reArmInterval, func() {
				o.flushPending(chatDir, agentName, sessionID)
			})
			o.mu.Unlock()
			log.Printf("[ORCH] Notification deferred (pending input) agent=%s", agentName)
			return
		}
		// maxDeferral exceeded — fall through to the UI fallback below.
	}

	// Capture the original deferral start before clearing it, so a re-defer (on a
	// flush/inject race) can restore it rather than resetting the maxDeferral cap.
	startedAt, hasStart := o.deferStartedAt[key]
	msgs := o.pendingMsgs[key]
	delete(o.pendingMsgs, key)
	delete(o.pendingTimers, key)
	delete(o.deferStartedAt, key)
	o.lastNotified[key] = time.Now()
	o.mu.Unlock()

	prompt := buildBatchedPrompt(msgs, agentName)

	if pending {
		// maxDeferral exceeded but the user still has a pending line: injecting
		// would corrupt it. Route to the UI instead (mandatory fallback).
		if o.onDeferredToUI != nil {
			log.Printf("[ORCH] maxDeferral exceeded, routing notification to UI agent=%s", agentName)
			o.onDeferredToUI(sessionID, agentName, prompt)
		} else {
			// No handler wired (e.g. headless): don't fail silently. The message
			// is still in the hub — the agent can read_messages later.
			log.Printf("[ORCH] WARN: maxDeferral exceeded but no UI handler set; auto-poke dropped for agent=%s", agentName)
		}
		return
	}

	log.Printf("[ORCH] Flushing %d batched notifications for agent=%s", len(msgs), agentName)
	if !o.tryInject(sessionID, prompt) {
		if !o.hasPendingInput(sessionID) {
			// tryInject failed but the user has NO pending line — so this was a
			// write failure (e.g. a dead/exited PTY that is still registered), not
			// a pending-input race. Re-deferring would retry every interval
			// forever (the maxDeferral→UI path only triggers while pending). Surface
			// it once via the UI fallback instead of looping (review CX3).
			log.Printf("[ORCH] WARN: inject failed with no pending input (write error?); routing to UI agent=%s", agentName)
			if o.onDeferredToUI != nil {
				o.onDeferredToUI(sessionID, agentName, prompt)
			}
			return
		}
		// Raced into pending input → re-defer the whole batch.
		o.mu.Lock()
		if !o.isCurrentSessionLocked(chatDir, agentName, sessionID) {
			// Agent was unregistered/restarted while we were injecting — don't
			// resurrect a timer/pending state for the stale session (review GR3).
			o.mu.Unlock()
			return
		}
		// Prepend the older batch so chronological order is preserved if new
		// messages were queued while we were injecting (review G3).
		o.pendingMsgs[key] = append(msgs, o.pendingMsgs[key]...)
		// Restore the ORIGINAL deferral start so maxDeferral still eventually
		// fires — never reset the cap on a race (review G3).
		if cur, ok := o.deferStartedAt[key]; !ok {
			if hasStart {
				o.deferStartedAt[key] = startedAt
			} else {
				o.deferStartedAt[key] = time.Now()
			}
		} else if hasStart && startedAt.Before(cur) {
			o.deferStartedAt[key] = startedAt
		}
		if _, exists := o.pendingTimers[key]; !exists {
			o.pendingTimers[key] = time.AfterFunc(o.reArmInterval, func() {
				o.flushPending(chatDir, agentName, sessionID)
			})
		}
		o.mu.Unlock()
		log.Printf("[ORCH] Flush raced into pending input, re-deferred agent=%s", agentName)
	}
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

	// Skip user_prompt messages: they are a transcript record of a human→agent
	// prompt that was ALREADY delivered to the target agent's PTY (#29). Injecting
	// it would echo the prompt straight back into the terminals.
	if msg.Type == types.MsgTypeUserPrompt {
		log.Printf("[ORCH] Skipping user_prompt message (already delivered to PTY)")
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
