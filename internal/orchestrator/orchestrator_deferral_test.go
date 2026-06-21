package orchestrator

import (
	"strings"
	"testing"
	"time"
)

// ── Typing-deferral tests (issue #15) ──

// While the user is typing, an out-of-cooldown notification must be queued
// (deferred) instead of injected immediately into the PTY.
func TestNotifyAgent_DefersWhileTyping(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.typingFunc = func(string) bool { return true }
	key := "/rooms/t:agent-1"

	o.notifyAgent("/rooms/t", "agent-1", "sess-1", "agent-2", false)

	o.mu.Lock()
	pending := len(o.pendingMsgs[key])
	_, hasTimer := o.pendingTimers[key]
	_, hasDefer := o.deferStartedAt[key]
	if tm := o.pendingTimers[key]; tm != nil {
		tm.Stop()
	}
	o.mu.Unlock()

	if len(*sent) != 0 {
		t.Errorf("typing user: notification must not be injected immediately, got %d", len(*sent))
	}
	if pending != 1 {
		t.Errorf("expected 1 pending notification, got %d", pending)
	}
	if !hasTimer {
		t.Error("expected a deferral timer to be armed")
	}
	if !hasDefer {
		t.Error("expected deferStartedAt to be stamped")
	}
}

// When the user is not typing, behaviour is unchanged: immediate send with CR.
func TestNotifyAgent_ImmediateWhenNotTyping(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.typingFunc = func(string) bool { return false }

	o.notifyAgent("/rooms/t", "agent-1", "sess-1", "agent-2", false)

	if len(*sent) != 1 {
		t.Fatalf("not typing: expected immediate send, got %d", len(*sent))
	}
	if !(*sent)[0].withCR {
		t.Error("not typing: trailing CR should be sent")
	}
}

// Even when an injection does happen, the trailing CR must be skipped if the
// user is typing — this is the deterministic guard against early-submit.
func TestSendToTerminal_SkipsCRWhileTyping(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.typingFunc = func(string) bool { return true }

	o.sendToTerminal("sess-1", "[agent-chat] hi")

	if len(*sent) != 1 {
		t.Fatalf("expected 1 injection, got %d", len(*sent))
	}
	if (*sent)[0].withCR {
		t.Error("typing: trailing CR must be skipped to avoid early submit")
	}
}

// At flush time, if the user is still typing and we are within maxDeferral, the
// single timer slot must be RE-ARMED (not a second timer) and nothing sent.
func TestFlushPending_ReArmsWhileTyping(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.typingFunc = func(string) bool { return true }
	key := "/rooms/t:agent-1"

	o.mu.Lock()
	o.pendingMsgs[key] = []pendingNotification{{from: "agent-2"}}
	o.deferStartedAt[key] = time.Now() // within maxDeferral
	o.pendingTimers[key] = time.AfterFunc(time.Hour, func() {})
	o.mu.Unlock()

	o.flushPending("/rooms/t", "agent-1", "sess-1")

	o.mu.Lock()
	pending := len(o.pendingMsgs[key])
	_, hasTimer := o.pendingTimers[key]
	if tm := o.pendingTimers[key]; tm != nil {
		tm.Stop()
	}
	o.mu.Unlock()

	if len(*sent) != 0 {
		t.Errorf("still typing within cap: must not send, got %d", len(*sent))
	}
	if pending != 1 {
		t.Errorf("pending must be kept for the re-arm, got %d", pending)
	}
	if !hasTimer {
		t.Error("expected the timer to be re-armed")
	}
}

// Once maxDeferral is exceeded while the user keeps typing, the notification is
// routed to the UI fallback instead of being injected into the PTY.
func TestFlushPending_FallbackWhenMaxDeferralExceeded(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.typingFunc = func(string) bool { return true }
	var uiPrompts []string
	o.SetDeferredHandler(func(sessionID, agentName, prompt string) {
		uiPrompts = append(uiPrompts, prompt)
	})
	key := "/rooms/t:agent-1"

	o.mu.Lock()
	o.pendingMsgs[key] = []pendingNotification{{from: "agent-2"}}
	o.deferStartedAt[key] = time.Now().Add(-o.maxDeferral - time.Second) // exceeded
	o.pendingTimers[key] = time.AfterFunc(time.Hour, func() {})
	o.mu.Unlock()

	o.flushPending("/rooms/t", "agent-1", "sess-1")

	o.mu.Lock()
	pending := len(o.pendingMsgs[key])
	_, hasTimer := o.pendingTimers[key]
	_, hasDefer := o.deferStartedAt[key]
	o.mu.Unlock()

	if len(*sent) != 0 {
		t.Errorf("max deferral exceeded: must NOT inject to PTY, got %d", len(*sent))
	}
	if len(uiPrompts) != 1 {
		t.Fatalf("expected exactly 1 UI fallback call, got %d", len(uiPrompts))
	}
	if !strings.Contains(uiPrompts[0], "agent-2") {
		t.Errorf("UI fallback prompt should mention the sender, got %q", uiPrompts[0])
	}
	if pending != 0 {
		t.Error("pending should be cleared after the fallback")
	}
	if hasTimer {
		t.Error("timer should be cleared after the fallback")
	}
	if hasDefer {
		t.Error("deferStartedAt should be cleared after the fallback")
	}
}

// When typing stops, a deferred batch flushes normally (with CR) and the
// deferral bookkeeping is cleared.
func TestFlushPending_SendsWhenTypingStopped(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.typingFunc = func(string) bool { return false } // user stopped typing
	key := "/rooms/t:agent-1"

	o.mu.Lock()
	o.pendingMsgs[key] = []pendingNotification{{from: "agent-2"}, {from: "agent-3"}}
	o.deferStartedAt[key] = time.Now()
	o.pendingTimers[key] = time.AfterFunc(time.Hour, func() {})
	o.mu.Unlock()

	o.flushPending("/rooms/t", "agent-1", "sess-1")

	o.mu.Lock()
	_, hasDefer := o.deferStartedAt[key]
	o.mu.Unlock()

	if len(*sent) != 1 {
		t.Fatalf("typing stopped: should flush exactly once, got %d", len(*sent))
	}
	if !strings.Contains((*sent)[0].text, "2 new messages") {
		t.Errorf("expected batched wording, got %q", (*sent)[0].text)
	}
	if !(*sent)[0].withCR {
		t.Error("not typing: CR should be sent on flush")
	}
	if hasDefer {
		t.Error("deferStartedAt should be cleared after a successful flush")
	}
}

// A single deferred message should read naturally ("New message from"), not the
// awkward "1 new messages".
func TestFlushPending_SingleMessageWording(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.typingFunc = func(string) bool { return false }
	key := "/rooms/t:agent-1"

	o.mu.Lock()
	o.pendingMsgs[key] = []pendingNotification{{from: "agent-2"}}
	o.pendingTimers[key] = time.AfterFunc(time.Hour, func() {})
	o.mu.Unlock()

	o.flushPending("/rooms/t", "agent-1", "sess-1")

	if len(*sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(*sent))
	}
	txt := (*sent)[0].text
	if strings.Contains(txt, "1 new messages") {
		t.Errorf("single deferred message should not say '1 new messages', got %q", txt)
	}
	if !strings.Contains(txt, "New message from agent-2") {
		t.Errorf("expected singular wording, got %q", txt)
	}
}
