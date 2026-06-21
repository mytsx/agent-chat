package orchestrator

import (
	"strings"
	"testing"
	"time"
)

// ── Pending-input deferral tests (issue #15 + review) ──

// While the user has an unsubmitted input line, an out-of-cooldown notification
// must be queued (deferred) instead of injected.
func TestNotifyAgent_DefersWhilePendingInput(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.pendingInputFunc = func(string) bool { return true }
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
		t.Errorf("pending input: notification must not be injected immediately, got %d", len(*sent))
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

// With no pending input, behaviour is unchanged: inject immediately.
func TestNotifyAgent_ImmediateWhenNoPending(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.pendingInputFunc = func(string) bool { return false }

	o.notifyAgent("/rooms/t", "agent-1", "sess-1", "agent-2", false)

	if len(*sent) != 1 {
		t.Fatalf("no pending input: expected immediate inject, got %d", len(*sent))
	}
	if !strings.Contains((*sent)[0].text, "agent-2") {
		t.Errorf("notification should mention sender, got %q", (*sent)[0].text)
	}
}

// tryInject must refuse to write (and report false) when the user has pending
// input — the last-line-of-defence against corrupting a half-typed line.
func TestTryInject_SkipsWhenPending(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.pendingInputFunc = func(string) bool { return true }

	if o.tryInject("sess-1", "[agent-chat] hi") {
		t.Error("tryInject should return false when input is pending")
	}
	if len(*sent) != 0 {
		t.Errorf("tryInject must not write while pending, got %d", len(*sent))
	}
}

func TestTryInject_InjectsWhenNoPending(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.pendingInputFunc = func(string) bool { return false }

	if !o.tryInject("sess-1", "[agent-chat] hi") {
		t.Error("tryInject should return true when no pending input")
	}
	if len(*sent) != 1 {
		t.Errorf("expected 1 injection, got %d", len(*sent))
	}
}

// At flush time, if the user still has pending input and we are within
// maxDeferral, the single timer slot must be RE-ARMED (not a second timer).
func TestFlushPending_ReArmsWhilePending(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.pendingInputFunc = func(string) bool { return true }
	key := "/rooms/t:agent-1"
	o.RegisterAgent("/rooms/t", "agent-1", "sess-1")

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
		t.Errorf("still pending within cap: must not send, got %d", len(*sent))
	}
	if pending != 1 {
		t.Errorf("pending must be kept for the re-arm, got %d", pending)
	}
	if !hasTimer {
		t.Error("expected the timer to be re-armed")
	}
}

// C2: if the queue was cleared (e.g. UnregisterAgent) before flush acquires the
// lock, flush must NOT re-arm a stale timer even if input is pending.
func TestFlushPending_EmptyQueueDoesNotReArm(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.pendingInputFunc = func(string) bool { return true }
	key := "/rooms/t:agent-1"
	o.RegisterAgent("/rooms/t", "agent-1", "sess-1")

	o.mu.Lock()
	// pendingMsgs[key] intentionally empty/absent; stale timer + defer stamp present.
	o.deferStartedAt[key] = time.Now()
	o.pendingTimers[key] = time.AfterFunc(time.Hour, func() {})
	o.mu.Unlock()

	o.flushPending("/rooms/t", "agent-1", "sess-1")

	o.mu.Lock()
	_, hasTimer := o.pendingTimers[key]
	_, hasDefer := o.deferStartedAt[key]
	if tm := o.pendingTimers[key]; tm != nil {
		tm.Stop()
	}
	o.mu.Unlock()

	if len(*sent) != 0 {
		t.Errorf("empty queue: nothing should be sent, got %d", len(*sent))
	}
	if hasTimer {
		t.Error("empty queue: stale timer must NOT be re-armed")
	}
	if hasDefer {
		t.Error("empty queue: deferStartedAt must be cleaned up")
	}
}

// Once maxDeferral is exceeded while the user keeps a pending line, the
// notification is routed to the UI fallback instead of the PTY.
func TestFlushPending_FallbackWhenMaxDeferralExceeded(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.pendingInputFunc = func(string) bool { return true }
	var uiPrompts []string
	o.SetDeferredHandler(func(sessionID, agentName, prompt string) {
		uiPrompts = append(uiPrompts, prompt)
	})
	key := "/rooms/t:agent-1"
	o.RegisterAgent("/rooms/t", "agent-1", "sess-1")

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

// When the input line clears, a deferred batch flushes normally and the
// deferral bookkeeping is cleared.
func TestFlushPending_SendsWhenInputCleared(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.pendingInputFunc = func(string) bool { return false } // input line cleared
	key := "/rooms/t:agent-1"
	o.RegisterAgent("/rooms/t", "agent-1", "sess-1")

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
		t.Fatalf("input cleared: should flush exactly once, got %d", len(*sent))
	}
	if !strings.Contains((*sent)[0].text, "2 new messages") {
		t.Errorf("expected batched wording, got %q", (*sent)[0].text)
	}
	if hasDefer {
		t.Error("deferStartedAt should be cleared after a successful flush")
	}
}

// GR2: when an agent is restarted (same chatDir/agentName, new sessionID), an
// old timer firing with the STALE sessionID must not flush the new session's
// pending messages to the dead old session.
func TestFlushPending_StaleSessionDropped(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.pendingInputFunc = func(string) bool { return false }
	// The agent is now bound to the NEW session.
	o.RegisterAgent("/rooms/t", "agent-1", "sess-NEW")
	key := "/rooms/t:agent-1"

	o.mu.Lock()
	o.pendingMsgs[key] = []pendingNotification{{from: "agent-2"}}
	o.pendingTimers[key] = time.AfterFunc(time.Hour, func() {})
	o.mu.Unlock()

	// An old timer fires with the STALE sessionID.
	o.flushPending("/rooms/t", "agent-1", "sess-OLD")

	o.mu.Lock()
	remaining := len(o.pendingMsgs[key])
	if tm := o.pendingTimers[key]; tm != nil {
		tm.Stop()
	}
	o.mu.Unlock()

	if len(*sent) != 0 {
		t.Errorf("stale session: must not flush to the old session, got %d", len(*sent))
	}
	if remaining != 1 {
		t.Errorf("stale flush must leave the new session's pending intact, got %d", remaining)
	}
}

// When a flush races into pending input (outside check passed, but tryInject's
// atomic pre-check sees pending), the batch must be re-deferred preserving
// chronological order (old msgs first) and the ORIGINAL deferStartedAt (so
// maxDeferral still eventually fires — review G3).
func TestFlushPending_RaceReDefersPreservingOrderAndCap(t *testing.T) {
	o, sent := newTestOrchestrator()
	calls := 0
	// First call (flushPending's outside check) → not pending → proceed to flush.
	// Second call (tryInject's atomic pre-check) → pending → tryInject returns false.
	o.pendingInputFunc = func(string) bool {
		calls++
		return calls > 1
	}
	key := "/rooms/t:agent-1"
	o.RegisterAgent("/rooms/t", "agent-1", "sess-1")
	orig := time.Now().Add(-100 * time.Millisecond)

	o.mu.Lock()
	o.pendingMsgs[key] = []pendingNotification{{from: "old1"}, {from: "old2"}}
	o.deferStartedAt[key] = orig
	o.pendingTimers[key] = time.AfterFunc(time.Hour, func() {})
	o.mu.Unlock()

	o.flushPending("/rooms/t", "agent-1", "sess-1")

	o.mu.Lock()
	msgs := o.pendingMsgs[key]
	ds, hasDefer := o.deferStartedAt[key]
	if tm := o.pendingTimers[key]; tm != nil {
		tm.Stop()
	}
	o.mu.Unlock()

	if len(*sent) != 0 {
		t.Errorf("raced into pending: nothing should be injected, got %d", len(*sent))
	}
	if len(msgs) != 2 || msgs[0].from != "old1" || msgs[1].from != "old2" {
		t.Errorf("re-defer must preserve order [old1, old2], got %+v", msgs)
	}
	if !hasDefer || !ds.Equal(orig) {
		t.Errorf("re-defer must restore original deferStartedAt %v (so maxDeferral still fires), got %v (has=%v)", orig, ds, hasDefer)
	}
}

// A single deferred message should read naturally ("New message from"), not the
// awkward "1 new messages".
func TestFlushPending_SingleMessageWording(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.pendingInputFunc = func(string) bool { return false }
	key := "/rooms/t:agent-1"
	o.RegisterAgent("/rooms/t", "agent-1", "sess-1")

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
