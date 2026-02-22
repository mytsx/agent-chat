package orchestrator

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"desktop/internal/types"
)

// ── Fake PTY types (unused but kept for potential future expansion) ──

type writtenData struct {
	sessionID string
	data      []byte
}

type fakePTYManager struct {
	mu     sync.Mutex
	writes []writtenData
}

// ── Test helper ──

type sentNotification struct {
	sessionID string
	text      string
}

func newTestOrchestrator() (*Orchestrator, *[]sentNotification) {
	var sent []sentNotification
	var mu sync.Mutex
	o := &Orchestrator{
		ptyManager:    nil,
		agentSessions: make(map[string]map[string]string),
		lastNotified:  make(map[string]time.Time),
		pendingTimers: make(map[string]*time.Timer),
		pendingMsgs:   make(map[string][]pendingNotification),
		sendFunc: func(sessionID, text string) {
			mu.Lock()
			sent = append(sent, sentNotification{sessionID, text})
			mu.Unlock()
		},
	}
	return o, &sent
}

// ── AnalyzeMessage tests ──

func TestAnalyzeMessage_NormalNotify(t *testing.T) {
	msg := types.Message{
		Content:      "Backend API deployed successfully",
		Type:         "direct",
		ExpectsReply: true,
	}
	result := AnalyzeMessage(msg)
	if result.Action != "notify" {
		t.Errorf("expected notify, got %s", result.Action)
	}
}

func TestAnalyzeMessage_AckSkip(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		reply      bool
		wantAction string
	}{
		{"short_ack_no_reply", "tesekkurler", false, "skip"},
		{"short_ack_with_reply", "tamam", true, "skip"},
		{"thanks_english", "thanks!", false, "skip"},
		{"ok_short", "ok", false, "skip"},
		{"long_with_ack_word", "Bu bir cok uzun mesaj ve burada tesekkur kelimesi gecse bile 80 karakterden uzun oldugu icin skip edilmemeli cunku gercekten uzun", false, "notify"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := types.Message{Content: tt.content, Type: "direct", ExpectsReply: tt.reply}
			r := AnalyzeMessage(msg)
			if r.Action != tt.wantAction {
				t.Errorf("%s: want %s, got %s (%s)", tt.name, tt.wantAction, r.Action, r.Reason)
			}
		})
	}
}

func TestAnalyzeMessage_QuestionAlwaysNotify(t *testing.T) {
	questions := []string{
		"API hazir mi?",
		"Bu nasil calisiyor?",
		"how does this work",
		"can you fix the bug",
		"ok tamam ama nasil?",
	}
	for _, q := range questions {
		t.Run(q, func(t *testing.T) {
			msg := types.Message{Content: q, Type: "direct", ExpectsReply: false}
			r := AnalyzeMessage(msg)
			if r.Action != "notify" {
				t.Errorf("question %q: want notify, got %s", q, r.Action)
			}
			if !r.IsQuestion {
				t.Errorf("question %q: IsQuestion should be true", q)
			}
		})
	}
}

func TestAnalyzeMessage_ExpectsReply(t *testing.T) {
	msg := types.Message{Content: "Deploy the new version", Type: "direct", ExpectsReply: true}
	r := AnalyzeMessage(msg)
	if r.Action != "notify" {
		t.Errorf("expects_reply=true should notify, got %s", r.Action)
	}
}

func TestAnalyzeMessage_Informational(t *testing.T) {
	msg := types.Message{Content: "I just deployed the backend to production", Type: "broadcast", ExpectsReply: false}
	r := AnalyzeMessage(msg)
	if r.Action != "notify" {
		t.Errorf("informational should notify, got %s", r.Action)
	}
}

func TestAnalyzeMessage_EmptyContent(t *testing.T) {
	msg := types.Message{Content: "", Type: "direct", ExpectsReply: false}
	r := AnalyzeMessage(msg)
	if r.Action != "notify" {
		t.Errorf("empty content should default to notify, got %s", r.Action)
	}
}

func TestAnalyzeMessage_CodeContent(t *testing.T) {
	msg := types.Message{
		Content:      `func main() { fmt.Println("hello $world"); os.Exit(0) }`,
		Type:         "direct",
		ExpectsReply: true,
	}
	r := AnalyzeMessage(msg)
	if r.Action != "notify" {
		t.Errorf("code content should notify, got %s", r.Action)
	}
}

// ── Register / Unregister tests ──

func TestRegisterUnregisterAgent(t *testing.T) {
	o, _ := newTestOrchestrator()
	o.RegisterAgent("/rooms/team1", "agent-1", "sess-1234-5678")
	o.RegisterAgent("/rooms/team1", "agent-2", "sess-aaaa-bbbb")

	if len(o.agentSessions["/rooms/team1"]) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(o.agentSessions["/rooms/team1"]))
	}
	if o.agentSessions["/rooms/team1"]["agent-1"] != "sess-1234-5678" {
		t.Error("agent-1 session mismatch")
	}

	o.UnregisterAgent("/rooms/team1", "agent-1")
	if _, exists := o.agentSessions["/rooms/team1"]["agent-1"]; exists {
		t.Error("agent-1 should be unregistered")
	}
	if len(o.agentSessions["/rooms/team1"]) != 1 {
		t.Error("should have 1 session remaining")
	}
}

func TestUnregisterNonexistent(t *testing.T) {
	o, _ := newTestOrchestrator()
	o.UnregisterAgent("/rooms/team1", "ghost") // should not panic
}

// ── ProcessMessage routing tests ──

func TestProcessMessage_SystemSkipped(t *testing.T) {
	o, _ := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "a1", "sess-11111111")
	msg := types.Message{From: "SYSTEM", To: "all", Content: "agent joined", Type: "system"}
	o.ProcessMessage("/rooms/t", msg) // should not panic
}

func TestProcessMessage_AckSkipped(t *testing.T) {
	o, _ := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "agent-1", "sess-11111111")
	msg := types.Message{From: "agent-2", To: "agent-1", Content: "tesekkurler", Type: "direct", ExpectsReply: false}
	o.ProcessMessage("/rooms/t", msg)

	o.mu.Lock()
	p := len(o.pendingMsgs)
	n := len(o.lastNotified)
	o.mu.Unlock()
	if p != 0 {
		t.Error("ack should not create pending")
	}
	if n != 0 {
		t.Error("ack should not update lastNotified")
	}
}

func TestProcessMessage_NoSessionsForDir(t *testing.T) {
	o, _ := newTestOrchestrator()
	msg := types.Message{From: "a1", To: "a2", Content: "Hello", Type: "direct", ExpectsReply: true}
	o.ProcessMessage("/rooms/unknown", msg) // should not panic
}

func TestProcessMessage_DirectTargetNotFound(t *testing.T) {
	o, _ := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "agent-1", "sess-11111111")
	msg := types.Message{From: "agent-1", To: "agent-3", Content: "Hey there?", Type: "direct", ExpectsReply: true}
	o.ProcessMessage("/rooms/t", msg) // should not panic
}

func TestProcessMessage_ManagerRoutedAlwaysNotifiesManager(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "manager", "sess-manager")

	msg := types.Message{
		From:            "agent-2",
		To:              "manager",
		OriginalTo:      "agent-1",
		Content:         "tamam",
		Type:            "direct",
		RoutedByManager: true,
		ExpectsReply:    false,
	}
	o.ProcessMessage("/rooms/t", msg)

	if len(*sent) != 1 {
		t.Fatalf("expected manager notification, got %d", len(*sent))
	}
	if (*sent)[0].sessionID != "sess-manager" {
		t.Fatalf("expected manager session notify, got %s", (*sent)[0].sessionID)
	}
}

// ── Cooldown / Batching tests ──

func TestNotifyAgent_FirstCallImmediate(t *testing.T) {
	o, sent := newTestOrchestrator()
	key := "/rooms/t:agent-1"

	o.mu.Lock()
	_, existed := o.lastNotified[key]
	o.mu.Unlock()
	if existed {
		t.Fatal("should not have lastNotified before first call")
	}

	o.notifyAgent("/rooms/t", "agent-1", "sess-11111111", "agent-2", false)

	o.mu.Lock()
	ts, existed := o.lastNotified[key]
	o.mu.Unlock()
	if !existed {
		t.Fatal("lastNotified should be set after first notify")
	}
	if time.Since(ts) > 1*time.Second {
		t.Error("lastNotified should be recent")
	}
	if len(*sent) != 1 {
		t.Fatalf("expected 1 sent notification, got %d", len(*sent))
	}
	if (*sent)[0].sessionID != "sess-11111111" {
		t.Error("wrong sessionID")
	}
	if !strings.Contains((*sent)[0].text, "agent-2") {
		t.Errorf("notification should mention sender, got: %s", (*sent)[0].text)
	}
	if !strings.Contains((*sent)[0].text, "read_messages") {
		t.Errorf("notification should contain read_messages instruction, got: %s", (*sent)[0].text)
	}
}

func TestNotifyAgent_Batching(t *testing.T) {
	o, _ := newTestOrchestrator()
	key := "/rooms/t:agent-1"

	o.mu.Lock()
	o.lastNotified[key] = time.Now()
	o.mu.Unlock()

	o.notifyAgent("/rooms/t", "agent-1", "sess-11111111", "agent-2", false)

	o.mu.Lock()
	pc := len(o.pendingMsgs[key])
	ht := o.pendingTimers[key] != nil
	o.mu.Unlock()

	if pc != 1 {
		t.Errorf("expected 1 pending, got %d", pc)
	}
	if !ht {
		t.Error("expected timer to be set")
	}

	o.notifyAgent("/rooms/t", "agent-1", "sess-11111111", "agent-3", false)

	o.mu.Lock()
	pc = len(o.pendingMsgs[key])
	o.mu.Unlock()
	if pc != 2 {
		t.Errorf("expected 2 pending, got %d", pc)
	}
}

func TestNotifyAgent_BatchFlushDeduplicates(t *testing.T) {
	o, _ := newTestOrchestrator()
	key := "/rooms/t:agent-1"

	o.mu.Lock()
	o.lastNotified[key] = time.Now()
	o.mu.Unlock()

	o.notifyAgent("/rooms/t", "agent-1", "sess-11111111", "agent-2", false)
	o.notifyAgent("/rooms/t", "agent-1", "sess-11111111", "agent-3", false)
	o.notifyAgent("/rooms/t", "agent-1", "sess-11111111", "agent-2", false)

	o.mu.Lock()
	if timer := o.pendingTimers[key]; timer != nil {
		timer.Stop()
	}
	pLen := len(o.pendingMsgs[key])
	o.mu.Unlock()

	if pLen != 3 {
		t.Errorf("expected 3 pending, got %d", pLen)
	}

	o.flushPending("/rooms/t", "agent-1", "sess-11111111")

	o.mu.Lock()
	rp := len(o.pendingMsgs[key])
	_, hasTimer := o.pendingTimers[key]
	o.mu.Unlock()

	if rp != 0 {
		t.Errorf("expected 0 pending after flush, got %d", rp)
	}
	if hasTimer {
		t.Error("timer should be cleaned up")
	}
}

func TestFlushPending_Empty(t *testing.T) {
	o, _ := newTestOrchestrator()
	o.flushPending("/rooms/t", "agent-1", "sess-11111111") // should not panic
}

func TestNotifyAgent_AfterCooldownExpired(t *testing.T) {
	o, _ := newTestOrchestrator()
	key := "/rooms/t:agent-1"

	o.mu.Lock()
	o.lastNotified[key] = time.Now().Add(-5 * time.Second)
	o.mu.Unlock()

	o.notifyAgent("/rooms/t", "agent-1", "sess-11111111", "agent-2", false)

	o.mu.Lock()
	pc := len(o.pendingMsgs[key])
	ts := o.lastNotified[key]
	o.mu.Unlock()

	if pc != 0 {
		t.Errorf("should not batch after cooldown, got %d pending", pc)
	}
	if time.Since(ts) > 1*time.Second {
		t.Error("lastNotified should be updated to now")
	}
}

// ── Broadcast routing test ──

func TestProcessMessage_BroadcastExcludesSender(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "agent-1", "sess-11111111")
	o.RegisterAgent("/rooms/t", "agent-2", "sess-22222222")
	o.RegisterAgent("/rooms/t", "agent-3", "sess-33333333")

	msg := types.Message{From: "agent-1", To: "all", Content: "Deploy done, review pls", Type: "broadcast", ExpectsReply: true}
	o.ProcessMessage("/rooms/t", msg)

	o.mu.Lock()
	_, a1 := o.lastNotified["/rooms/t:agent-1"]
	_, a2 := o.lastNotified["/rooms/t:agent-2"]
	_, a3 := o.lastNotified["/rooms/t:agent-3"]
	o.mu.Unlock()

	if a1 {
		t.Error("sender should NOT be notified on own broadcast")
	}
	if !a2 {
		t.Error("agent-2 should be notified")
	}
	if !a3 {
		t.Error("agent-3 should be notified")
	}
	// Verify exactly 2 notifications sent (not 3)
	if len(*sent) != 2 {
		t.Errorf("expected 2 sent notifications, got %d", len(*sent))
	}
	// All sent notifications should mention Broadcast
	for _, s := range *sent {
		if !strings.Contains(s.text, "Broadcast") {
			t.Errorf("broadcast notification should contain 'Broadcast', got: %s", s.text)
		}
	}
}

// ── HandleNewMessages test ──

func TestHandleNewMessages_Multiple(t *testing.T) {
	o, _ := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "agent-1", "sess-11111111")
	o.RegisterAgent("/rooms/t", "agent-2", "sess-22222222")

	msgs := []types.Message{
		{From: "SYSTEM", To: "all", Content: "joined", Type: "system"},
		{From: "agent-1", To: "agent-2", Content: "tesekkurler", Type: "direct", ExpectsReply: false},
		{From: "agent-1", To: "agent-2", Content: "Can you deploy the API?", Type: "direct", ExpectsReply: true},
	}
	o.HandleNewMessages("/rooms/t", msgs)

	o.mu.Lock()
	_, notified := o.lastNotified["/rooms/t:agent-2"]
	o.mu.Unlock()
	if !notified {
		t.Error("agent-2 should be notified for the question")
	}
}

// ── mapKeys test ──

func TestMapKeys(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2, "c": 3}
	keys := mapKeys(m)
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}
}

func TestMapKeys_Empty(t *testing.T) {
	m := map[string]int{}
	keys := mapKeys(m)
	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}
}

// ══════════════════════════════════════════════
// Integration Tests
// ══════════════════════════════════════════════

// TestRegisterUnregister_ThreadSafety tests concurrent Register/Unregister calls.
func TestRegisterUnregister_ThreadSafety(t *testing.T) {
	o, _ := newTestOrchestrator()
	const goroutines = 50
	var wg sync.WaitGroup

	// Concurrently register agents
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			agent := fmt.Sprintf("agent-%d", idx)
			session := fmt.Sprintf("sess-%d", idx)
			o.RegisterAgent("/rooms/team1", agent, session)
		}(i)
	}
	wg.Wait()

	o.mu.Lock()
	count := len(o.agentSessions["/rooms/team1"])
	o.mu.Unlock()
	if count != goroutines {
		t.Errorf("expected %d agents, got %d", goroutines, count)
	}

	// Concurrently unregister half, register new half
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				o.UnregisterAgent("/rooms/team1", fmt.Sprintf("agent-%d", idx))
			} else {
				o.RegisterAgent("/rooms/team1", fmt.Sprintf("new-agent-%d", idx), fmt.Sprintf("new-sess-%d", idx))
			}
		}(i)
	}
	wg.Wait()

	o.mu.Lock()
	finalCount := len(o.agentSessions["/rooms/team1"])
	o.mu.Unlock()
	// Half unregistered (25 even), half new registered (25 odd). Original odd agents (25) remain.
	// Expected: 25 (original odd) + 25 (new odd) = 50.
	expected := goroutines
	if finalCount != expected {
		t.Errorf("expected %d agents, got %d", expected, finalCount)
	}
}

// TestProcessMessage_DirectMessageFlow tests direct message routing end-to-end.
func TestProcessMessage_DirectMessageFlow(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "alice", "sess-alice")
	o.RegisterAgent("/rooms/t", "bob", "sess-bob")
	o.RegisterAgent("/rooms/t", "charlie", "sess-charlie")

	// Alice sends direct message to Bob
	msg := types.Message{From: "alice", To: "bob", Content: "Hey Bob, can you review my PR?", Type: "direct", ExpectsReply: true}
	o.ProcessMessage("/rooms/t", msg)

	if len(*sent) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(*sent))
	}
	if (*sent)[0].sessionID != "sess-bob" {
		t.Errorf("expected notification to bob, got session %s", (*sent)[0].sessionID)
	}
	if strings.Contains((*sent)[0].text, "Broadcast") {
		t.Error("direct message should not say Broadcast")
	}
	if !strings.Contains((*sent)[0].text, "alice") {
		t.Error("notification should mention sender alice")
	}
}

// TestProcessMessage_BroadcastMessageFlow tests broadcast routing end-to-end.
func TestProcessMessage_BroadcastMessageFlow(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "alice", "sess-alice")
	o.RegisterAgent("/rooms/t", "bob", "sess-bob")
	o.RegisterAgent("/rooms/t", "charlie", "sess-charlie")

	// Alice broadcasts
	msg := types.Message{From: "alice", To: "all", Content: "Deployment complete!", Type: "broadcast", ExpectsReply: true}
	o.ProcessMessage("/rooms/t", msg)

	if len(*sent) != 2 {
		t.Fatalf("expected 2 notifications (bob + charlie), got %d", len(*sent))
	}

	// Verify sender not notified
	for _, s := range *sent {
		if s.sessionID == "sess-alice" {
			t.Error("sender alice should not be notified")
		}
		if !strings.Contains(s.text, "Broadcast") {
			t.Errorf("broadcast notification should contain 'Broadcast', got: %s", s.text)
		}
	}
}

// TestRaceCondition_RegisterUnregisterDuringProcessMessage tests concurrent
// Register/Unregister calls while ProcessMessage is executing.
func TestRaceCondition_RegisterUnregisterDuringProcessMessage(t *testing.T) {
	o, _ := newTestOrchestrator()

	// Pre-register some agents
	for i := 0; i < 10; i++ {
		o.RegisterAgent("/rooms/t", fmt.Sprintf("agent-%d", i), fmt.Sprintf("sess-%d", i))
	}

	var wg sync.WaitGroup
	const iterations = 100

	// Concurrent ProcessMessage calls
	wg.Add(iterations)
	for i := 0; i < iterations; i++ {
		go func(idx int) {
			defer wg.Done()
			msg := types.Message{
				From:         fmt.Sprintf("agent-%d", idx%10),
				To:           "all",
				Content:      fmt.Sprintf("Message %d from agent", idx),
				Type:         "broadcast",
				ExpectsReply: true,
			}
			o.ProcessMessage("/rooms/t", msg)
		}(i)
	}

	// Concurrent Register/Unregister while ProcessMessage runs
	wg.Add(iterations)
	for i := 0; i < iterations; i++ {
		go func(idx int) {
			defer wg.Done()
			agent := fmt.Sprintf("dynamic-agent-%d", idx)
			o.RegisterAgent("/rooms/t", agent, fmt.Sprintf("dyn-sess-%d", idx))
			o.UnregisterAgent("/rooms/t", agent)
		}(i)
	}

	wg.Wait()
	// Test passes if no panic, deadlock, or race detected (use -race flag)
}

// TestCooldown_IntegrationFlow tests the full cooldown→batch→flush cycle.
func TestCooldown_IntegrationFlow(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "bob", "sess-bob")

	// First message: immediate notification
	msg1 := types.Message{From: "alice", To: "bob", Content: "Hey bob, first message", Type: "direct", ExpectsReply: true}
	o.ProcessMessage("/rooms/t", msg1)

	if len(*sent) != 1 {
		t.Fatalf("first message should send immediately, got %d", len(*sent))
	}

	// Second message within cooldown: should be batched
	msg2 := types.Message{From: "charlie", To: "bob", Content: "Hey bob, second message", Type: "direct", ExpectsReply: true}
	o.ProcessMessage("/rooms/t", msg2)

	if len(*sent) != 1 {
		t.Errorf("second message within cooldown should be batched, sent=%d", len(*sent))
	}

	// Third message within cooldown: also batched
	msg3 := types.Message{From: "alice", To: "bob", Content: "Hey bob, third message", Type: "direct", ExpectsReply: true}
	o.ProcessMessage("/rooms/t", msg3)

	if len(*sent) != 1 {
		t.Errorf("third message within cooldown should be batched, sent=%d", len(*sent))
	}

	// Verify pending messages accumulated
	key := "/rooms/t:bob"
	o.mu.Lock()
	pendingCount := len(o.pendingMsgs[key])
	hasTimer := o.pendingTimers[key] != nil
	if hasTimer {
		o.pendingTimers[key].Stop()
	}
	o.mu.Unlock()

	if pendingCount != 2 {
		t.Errorf("expected 2 pending messages, got %d", pendingCount)
	}
	if !hasTimer {
		t.Error("expected a flush timer to be running")
	}

	// Manual flush (simulating timer expiry)
	o.flushPending("/rooms/t", "bob", "sess-bob")

	if len(*sent) != 2 {
		t.Errorf("after flush, expected 2 total sent notifications, got %d", len(*sent))
	}

	// Verify flush notification mentions count and senders
	flushed := (*sent)[1]
	if !strings.Contains(flushed.text, "2 new messages") {
		t.Errorf("flush notification should mention '2 new messages', got: %s", flushed.text)
	}
	if !strings.Contains(flushed.text, "alice") || !strings.Contains(flushed.text, "charlie") {
		t.Errorf("flush notification should mention both senders, got: %s", flushed.text)
	}
}

// TestUnregisterAgent_CleansCooldownState verifies that unregistering cleans up cooldown data.
func TestUnregisterAgent_CleansCooldownState(t *testing.T) {
	o, _ := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "agent-1", "sess-1")

	// Create cooldown state
	key := "/rooms/t:agent-1"
	o.mu.Lock()
	o.lastNotified[key] = time.Now()
	o.pendingMsgs[key] = []pendingNotification{{from: "agent-2"}}
	o.pendingTimers[key] = time.AfterFunc(10*time.Second, func() {})
	o.mu.Unlock()

	// Unregister should clean everything
	o.UnregisterAgent("/rooms/t", "agent-1")

	o.mu.Lock()
	_, hasLN := o.lastNotified[key]
	_, hasPM := o.pendingMsgs[key]
	_, hasPT := o.pendingTimers[key]
	o.mu.Unlock()

	if hasLN {
		t.Error("lastNotified should be cleaned up after unregister")
	}
	if hasPM {
		t.Error("pendingMsgs should be cleaned up after unregister")
	}
	if hasPT {
		t.Error("pendingTimers should be cleaned up after unregister")
	}
}

// TestMultipleChatDirs verifies agents in different chatDirs are isolated.
func TestMultipleChatDirs(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.RegisterAgent("/rooms/team1", "alice", "sess-alice-1")
	o.RegisterAgent("/rooms/team2", "alice", "sess-alice-2")
	o.RegisterAgent("/rooms/team1", "bob", "sess-bob-1")

	// Message in team1 should only notify team1 agents
	msg := types.Message{From: "alice", To: "all", Content: "Team1 update", Type: "broadcast", ExpectsReply: true}
	o.ProcessMessage("/rooms/team1", msg)

	if len(*sent) != 1 {
		t.Fatalf("expected 1 notification (bob in team1), got %d", len(*sent))
	}
	if (*sent)[0].sessionID != "sess-bob-1" {
		t.Errorf("expected notification to bob in team1, got %s", (*sent)[0].sessionID)
	}
}
