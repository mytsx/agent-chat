package eventlog

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeStream builds an event stream in a temp dir by driving a real Logger, so
// the analyzer is always tested against the format the hub actually writes.
func writeStream(t *testing.T, write func(l *Logger, tick func(d time.Duration))) string {
	t.Helper()
	dir := t.TempDir()
	clock := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	l, err := New(Options{Dir: dir, now: func() time.Time { return clock }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	write(l, func(d time.Duration) { clock = clock.Add(d) })
	l.Flush()
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return dir
}

func TestReadRecordsInTimeOrder(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		tick(time.Minute)
		l.Log(EventAgentJoined, String(AttrAgentName, "alice"))
	})

	recs, _, err := Read(dir, time.Time{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("kayıt sayısı = %d, want 2", len(recs))
	}
	if recs[0].Name != EventHubStarted || recs[1].Name != EventAgentJoined {
		t.Errorf("sıra = %s,%s", recs[0].Name, recs[1].Name)
	}
	if recs[1].Str(AttrAgentName) != "alice" {
		t.Errorf("agent = %q", recs[1].Str(AttrAgentName))
	}
	if !recs[1].Time.After(recs[0].Time) {
		t.Errorf("zaman damgaları artmıyor: %v, %v", recs[0].Time, recs[1].Time)
	}
}

func TestReadHonoursSince(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		tick(time.Hour)
		l.Log(EventHubStopped)
	})

	cut := time.Date(2026, 9, 8, 9, 30, 0, 0, time.UTC)
	recs, _, err := Read(dir, cut)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) != 1 || recs[0].Name != EventHubStopped {
		t.Fatalf("since filtresi çalışmadı: %v", recs)
	}
}

// Question 1: how often did each agent drop out, and why.
func TestAnalyzeDropsByReason(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		l.Log(EventAgentLeft, room, String(AttrAgentName, "alice"), String(AttrLeaveReason, LeaveReasonDisconnect))
		tick(time.Minute)
		l.Log(EventAgentLeft, room, String(AttrAgentName, "alice"), String(AttrLeaveReason, LeaveReasonDisconnect))
		tick(time.Minute)
		l.Log(EventAgentLeft, room, String(AttrAgentName, "alice"), String(AttrLeaveReason, LeaveReasonExplicit))
		tick(time.Minute)
		l.Log(EventAgentEvicted, room, String(AttrAgentName, "bob"), Float64(AttrIdleSeconds, 400))
	})

	rep := analyzeDir(t, dir)

	drops := map[string]AgentDrops{}
	for _, d := range rep.Drops {
		drops[d.Agent] = d
	}
	if got := drops["alice"]; got.Disconnect != 2 || got.Explicit != 1 || got.Evicted != 0 {
		t.Errorf("alice = %+v, want disconnect 2 / explicit 1 / evicted 0", got)
	}
	if got := drops["bob"]; got.Evicted != 1 || got.Disconnect != 0 {
		t.Errorf("bob = %+v, want evicted 1", got)
	}
	// Ordering puts the worst offender first so the report leads with the problem.
	if rep.Drops[0].Agent != "alice" {
		t.Errorf("ilk sıra = %q, want alice (en çok düşen)", rep.Drops[0].Agent)
	}
}

// Question 2: which messages went to somebody who was not in the room.
func TestAnalyzeMisaddressed(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
			String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
			Int(AttrMessageID, 1), String(AttrInputMessages, "ulaştı"))
		tick(time.Minute)
		l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
			String(AttrRecipientName, "hayalet"), Bool(AttrRecipientInRoom, false),
			Int(AttrMessageID, 2), String(AttrInputMessages, "kayboldu"))
	})

	rep := analyzeDir(t, dir)

	if len(rep.Misaddressed) != 1 {
		t.Fatalf("yanlış adresli sayısı = %d, want 1", len(rep.Misaddressed))
	}
	m := rep.Misaddressed[0]
	if m.From != "alice" || m.To != "hayalet" || m.MessageID != 2 {
		t.Errorf("kayıt = %+v", m)
	}
	if m.Content != "kayboldu" {
		t.Errorf("içerik = %q", m.Content)
	}
}

// Question 3: which messages were never read by their recipient.
func TestAnalyzeUnread(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		send := func(id int, to string) {
			l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
				String(AttrRecipientName, to), Bool(AttrRecipientInRoom, true), Int(AttrMessageID, id))
			tick(time.Second)
		}
		send(1, "bob")
		send(2, "bob")
		send(3, "bob")
		// bob only ever got as far as message 2.
		l.Log(EventMessagesRead, room, String(AttrAgentName, "bob"),
			Int(AttrReadReturned, 2), Int(AttrReadMaxID, 2))
	})

	rep := analyzeDir(t, dir)

	if len(rep.Unread) != 1 {
		t.Fatalf("okunmamış sayısı = %d, want 1 (%+v)", len(rep.Unread), rep.Unread)
	}
	if rep.Unread[0].MessageID != 3 || rep.Unread[0].To != "bob" {
		t.Errorf("kayıt = %+v, want id 3 / bob", rep.Unread[0])
	}

	// Copilot review, PR #103: with a manager gateway active, a message is stored
	// for the manager but addressed to somebody else. Tracking read progress
	// against the addressee would leave every such message unread forever — in a
	// manager-gated room that is nearly all traffic.
	t.Run("manager'a yönlendirilen mesaj özgün alıcıya borç yazılmaz", func(t *testing.T) {
		dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
			room := String(AttrConversationID, "r1")
			l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
				String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
				String(AttrDeliveryTarget, "yonetici"), Int(AttrMessageID, 1))
			tick(time.Second)
			l.Log(EventMessageRerouted, room, String(AttrAgentName, "alice"),
				String(AttrRecipientName, "bob"), String(AttrRerouteTarget, "yonetici"),
				Int(AttrMessageID, 1))
			tick(time.Second)
			// Manager read it; bob never did — and never should have.
			l.Log(EventMessagesRead, room, String(AttrAgentName, "yonetici"),
				Int(AttrReadReturned, 1), Int(AttrReadMaxID, 1))
		})

		if got := analyzeDir(t, dir).Unread; len(got) != 0 {
			t.Errorf("yönlendirilen mesaj özgün alıcıda okunmamış sayılmış: %+v", got)
		}
	})

	t.Run("manager okumadıysa mesaj manager'a borç yazılır", func(t *testing.T) {
		dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
			l.Log(EventMessageSent, String(AttrConversationID, "r1"),
				String(AttrAgentName, "alice"), String(AttrRecipientName, "bob"),
				Bool(AttrRecipientInRoom, true), String(AttrDeliveryTarget, "yonetici"),
				Int(AttrMessageID, 1))
		})

		got := analyzeDir(t, dir).Unread
		if len(got) != 1 {
			t.Fatalf("okunmamış sayısı = %d, want 1", len(got))
		}
		// The report must show both names: who it was addressed to and who
		// actually owed a read.
		if got[0].To != "bob" || got[0].DeliveredTo != "yonetici" {
			t.Errorf("kayıt = %+v, want To=bob DeliveredTo=yonetici", got[0])
		}
	})

	t.Run("delivery.target taşımayan eski akış alıcıya düşer", func(t *testing.T) {
		// Streams written before the attribute existed must still report.
		dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
			l.Log(EventMessageSent, String(AttrConversationID, "r1"),
				String(AttrAgentName, "alice"), String(AttrRecipientName, "bob"),
				Bool(AttrRecipientInRoom, true), Int(AttrMessageID, 1))
		})

		got := analyzeDir(t, dir).Unread
		if len(got) != 1 || got[0].To != "bob" || got[0].DeliveredTo != "" {
			t.Errorf("kayıt = %+v, want To=bob DeliveredTo boş", got)
		}
	})

	t.Run("odada olmayan alıcı okunmamış sayılmaz", func(t *testing.T) {
		// Already reported as misaddressed; counting it twice would present one
		// problem as two.
		dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
			l.Log(EventMessageSent, String(AttrConversationID, "r1"),
				String(AttrAgentName, "alice"), String(AttrRecipientName, "hayalet"),
				Bool(AttrRecipientInRoom, false), Int(AttrMessageID, 1))
		})
		rep := analyzeDir(t, dir)
		if len(rep.Unread) != 0 {
			t.Errorf("yanlış adresli mesaj ayrıca okunmamış sayılmış: %+v", rep.Unread)
		}
		if len(rep.Misaddressed) != 1 {
			t.Errorf("yanlış adresli olarak sayılmamış: %+v", rep.Misaddressed)
		}
	})

	t.Run("broadcast okunmamış sayılmaz", func(t *testing.T) {
		// "all" has no single recipient whose progress could be compared.
		dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
			l.Log(EventMessageSent, String(AttrConversationID, "r1"),
				String(AttrAgentName, "alice"), String(AttrRecipientName, "all"),
				Bool(AttrRecipientInRoom, true), Int(AttrMessageID, 1))
		})
		if got := analyzeDir(t, dir).Unread; len(got) != 0 {
			t.Errorf("broadcast okunmamış sayılmış: %+v", got)
		}
	})
}

// Question 4: when was the hub down, and for how long.
func TestAnalyzeHubOutages(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		tick(time.Minute)
		l.Log(EventHubStopped)
		tick(5 * time.Minute)
		l.Log(EventHubStarted)
	})

	rep := analyzeDir(t, dir)

	if len(rep.Outages) != 1 {
		t.Fatalf("kesinti sayısı = %d, want 1", len(rep.Outages))
	}
	if got := rep.Outages[0].Duration; got != 5*time.Minute {
		t.Errorf("süre = %v, want 5m", got)
	}
}

func TestAnalyzeUnterminatedOutageIsOngoing(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		tick(time.Minute)
		l.Log(EventHubStopped) // hiç geri başlamamış
	})

	rep := analyzeDir(t, dir)
	if len(rep.Outages) != 1 || !rep.Outages[0].Ongoing {
		t.Fatalf("kapanıp geri açılmayan hub sürmekte olan kesinti sayılmalı: %+v", rep.Outages)
	}
}

func TestAnalyzeFiltersByRoom(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventAgentEvicted, String(AttrConversationID, "r1"), String(AttrAgentName, "alice"))
		l.Log(EventAgentEvicted, String(AttrConversationID, "r2"), String(AttrAgentName, "bob"))
	})

	rep, err := Analyze(AnalyzeOptions{Dir: dir, Room: "r2"})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(rep.Drops) != 1 || rep.Drops[0].Agent != "bob" {
		t.Errorf("oda filtresi çalışmadı: %+v", rep.Drops)
	}
}

// The legacy plain-text log is the only record of "could not reach the hub at
// all" — those MCP instances never got a connection to log through.
func TestAnalyzeLegacyLogCountsUnreachableHub(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "mcp-server.log")
	lines := "" +
		"[MCP] 2026/09/08 09:02:00 main.go:108: Hub discovery failed: hub.port not found: open /x/hub.port: no such file or directory\n" +
		"[MCP] 2026/09/08 09:03:00 client.go:70: Hub connect attempt 1/5 failed: hub connect: dial tcp [::1]:1: connect: connection refused (retrying in 1s)\n" +
		"[MCP] 2026/09/08 09:04:00 tools.go:12: read_messages: agent=\"x\" since_id=0\n"
	if err := os.WriteFile(legacy, []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}

	rep, err := Analyze(AnalyzeOptions{Dir: dir, LegacyLog: legacy})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.LegacyUnreachable != 2 {
		t.Errorf("ulaşılamayan hub satırı = %d, want 2", rep.LegacyUnreachable)
	}
	if rep.LegacyFirst.IsZero() || rep.LegacyLast.IsZero() {
		t.Errorf("zaman aralığı çıkarılmamış: %v–%v", rep.LegacyFirst, rep.LegacyLast)
	}
}

func TestAnalyzeMissingStreamIsNotAnError(t *testing.T) {
	// Nothing logged yet is a normal state, not a failure.
	rep, err := Analyze(AnalyzeOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.Events != 0 {
		t.Errorf("Events = %d, want 0", rep.Events)
	}
}

func analyzeDir(t *testing.T, dir string) Report {
	t.Helper()
	rep, err := Analyze(AnalyzeOptions{Dir: dir})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return rep
}

// Codex review, PR #103: a read returns only the newest matching tail once its
// limit bites, so the highest returned ID does not prove the lower ones were
// shown. Read progress is a set, not a watermark.
func TestAnalyzeReadProgressIsPerMessageNotWatermark(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		for id := 1; id <= 3; id++ {
			l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
				String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
				String(AttrDeliveryTarget, "bob"), Int(AttrMessageID, id))
			tick(time.Second)
		}
		// bob's read was limited and returned only 2 and 3; message 1 was never
		// shown even though a higher ID came back.
		l.Log(EventMessagesRead, room, String(AttrAgentName, "bob"),
			Int(AttrReadReturned, 2), Int(AttrReadMaxID, 3),
			Ints(AttrReadMessageIDs, []int{2, 3}))
	})

	got := analyzeDir(t, dir).Unread
	if len(got) != 1 {
		t.Fatalf("okunmamış sayısı = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].MessageID != 1 {
		t.Errorf("okunmamış id = %d, want 1", got[0].MessageID)
	}
}

func TestAnalyzeTruncatedReadFallsBackToWatermark(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		for id := 1; id <= 2; id++ {
			l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
				String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
				String(AttrDeliveryTarget, "bob"), Int(AttrMessageID, id))
		}
		l.Log(EventMessagesRead, room, String(AttrAgentName, "bob"),
			Int(AttrReadMaxID, 2), Bool(AttrReadIDsTruncated, true))
	})

	if got := analyzeDir(t, dir).Unread; len(got) != 0 {
		t.Errorf("kırpılmış okuma watermark'a düşmedi: %+v", got)
	}
}

// Codex review, PR #103: a crashed hub never logs its stop, so two starts with
// nothing between them are exactly the failure the outage report must surface.
func TestAnalyzeDetectsUncleanRestart(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		tick(time.Minute)
		l.Log(EventAgentJoined, String(AttrConversationID, "r1"), String(AttrAgentName, "alice"))
		tick(3 * time.Minute)
		l.Log(EventHubStarted) // stop yok: çökmüş
	})

	rep := analyzeDir(t, dir)
	if len(rep.Outages) != 1 {
		t.Fatalf("kesinti sayısı = %d, want 1 (%+v)", len(rep.Outages), rep.Outages)
	}
	o := rep.Outages[0]
	if !o.Unclean {
		t.Errorf("kesinti Unclean işaretlenmedi: %+v", o)
	}
	// Start is only a lower bound: the last thing the dead instance managed to log.
	if o.Duration != 3*time.Minute {
		t.Errorf("süre = %v, want 3m (son olaydan yeni başlangıca)", o.Duration)
	}
}

// Codex review, PR #103: clear_room restarts message IDs at 1, so read state
// from the previous room generation must not carry over.
func TestAnalyzeResetsReadStateOnRoomReset(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
			String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
			String(AttrDeliveryTarget, "bob"), Int(AttrMessageID, 100))
		l.Log(EventMessagesRead, room, String(AttrAgentName, "bob"),
			Int(AttrReadMaxID, 100), Ints(AttrReadMessageIDs, []int{100}))
		tick(time.Minute)

		l.Log(EventRoomReset, room, String(AttrRoomLifecycle, RoomLifecycleCleared))
		tick(time.Minute)

		// Fresh generation reuses low IDs; bob has read none of them.
		l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
			String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
			String(AttrDeliveryTarget, "bob"), Int(AttrMessageID, 1))
	})

	got := analyzeDir(t, dir).Unread
	if len(got) != 1 || got[0].MessageID != 1 {
		t.Fatalf("oda sıfırlaması sonrası okunmamış = %+v, want id 1", got)
	}
}

// Codex review, PR #103: --since must bound the legacy scan too, or a one-day
// report silently includes months of historical unreachable-hub lines.
func TestAnalyzeLegacyLogHonoursSince(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "mcp-server.log")
	lines := "" +
		"[MCP] 2026/01/01 09:00:00 main.go:1: Hub discovery failed: hub.port not found\n" +
		"[MCP] 2026/09/08 09:00:00 main.go:1: Hub discovery failed: hub.port not found\n"
	if err := os.WriteFile(legacy, []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}

	cut := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	rep, err := Analyze(AnalyzeOptions{Dir: dir, LegacyLog: legacy, Since: cut})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.LegacyUnreachable != 1 {
		t.Errorf("since sınırı uygulanmadı: %d satır sayıldı, want 1", rep.LegacyUnreachable)
	}
}

// Codex review round 2, PR #103: the room-reset fix must not erase history.
// Messages the clear wiped while still unread are among the most interesting
// findings the report has, so a reset starts a new generation instead.
func TestAnalyzeKeepsUnreadFromBeforeRoomReset(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
			String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
			String(AttrDeliveryTarget, "bob"), Int(AttrMessageID, 7))
		tick(time.Minute)
		// Watermark 7: message 7 was wiped by the clear, nothing survived it.
		l.Log(EventRoomReset, room, String(AttrRoomLifecycle, RoomLifecycleCleared),
			Int(AttrRoomResetMaxID, 7))
		tick(time.Minute)
		// Fresh generation reuses ID 7 and bob reads it; the old one stays unread.
		l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
			String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
			String(AttrDeliveryTarget, "bob"), Int(AttrMessageID, 7))
		l.Log(EventMessagesRead, room, String(AttrAgentName, "bob"),
			Int(AttrReadMaxID, 7), Ints(AttrReadMessageIDs, []int{7}))
	})

	got := analyzeDir(t, dir).Unread
	if len(got) != 1 {
		t.Fatalf("okunmamış sayısı = %d, want 1 (sıfırlama öncesi mesaj korunmalı): %+v", len(got), got)
	}
	if got[0].MessageID != 7 {
		t.Errorf("okunmamış id = %d, want 7", got[0].MessageID)
	}
}

// Codex review round 2: an outage that spans the --since boundary must still be
// reported; the stop that opens it lies before the cutoff.
func TestAnalyzeOutageSpanningSinceCutoff(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		tick(time.Hour)
		l.Log(EventHubStopped) // 10:00 — kesim noktasından önce
		tick(4 * time.Hour)
		l.Log(EventHubStarted) // 14:00 — kesim noktasından sonra
	})

	// Cutoff between the stop and the restart.
	cut := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	rep, err := Analyze(AnalyzeOptions{Dir: dir, Since: cut})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(rep.Outages) != 1 {
		t.Fatalf("kesinti sayısı = %d, want 1 (kesim noktasını aşan kesinti): %+v", len(rep.Outages), rep.Outages)
	}
	if got := rep.Outages[0].Duration; got != 4*time.Hour {
		t.Errorf("süre = %v, want 4h", got)
	}
}

func TestAnalyzeDropsOutagesEntirelyBeforeCutoff(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		l.Log(EventHubStopped)
		tick(time.Minute)
		l.Log(EventHubStarted) // kesinti tamamen kesim noktasından önce
		tick(10 * time.Hour)
		l.Log(EventAgentJoined, String(AttrConversationID, "r1"), String(AttrAgentName, "alice"))
	})

	cut := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	rep, err := Analyze(AnalyzeOptions{Dir: dir, Since: cut})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(rep.Outages) != 0 {
		t.Errorf("pencere dışı kesinti raporlanmış: %+v", rep.Outages)
	}
}

// Codex review round 3, PR #103: the durable drop marker exists so a crash
// cannot hide loss — the analyzer has to actually read it.
func TestAnalyzeConsumesDurableDropMarker(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		tick(time.Minute)
		l.Log(EventEventsDropped, Uint64(AttrEventsDropped, 12))
		tick(time.Minute)
		l.Log(EventEventsDropped, Uint64(AttrEventsDropped, 30))
		// Hiç hub.stopped yok: çökme.
	})

	rep := analyzeDir(t, dir)
	// Both markers are cumulative, so the run contributes their maximum.
	if rep.Dropped != 30 {
		t.Errorf("Dropped = %d, want 30 (kümülatif işaretlerin en büyüğü)", rep.Dropped)
	}
}

func TestAnalyzeSumsDropsAcrossHubRuns(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		l.Log(EventEventsDropped, Uint64(AttrEventsDropped, 5))
		l.Log(EventHubStopped, Uint64(AttrEventsDropped, 5)) // aynı tur, kümülatif
		tick(time.Minute)
		l.Log(EventHubStarted) // yeni süreç, sayaç sıfırdan
		l.Log(EventEventsDropped, Uint64(AttrEventsDropped, 7))
	})

	if got := analyzeDir(t, dir).Dropped; got != 12 {
		t.Errorf("Dropped = %d, want 12 (5 + 7; tur içi çift sayılmamalı)", got)
	}
}

// Codex review round 3: clear_room keeps messages that arrived while its
// archive I/O ran. Those survive into the new room and stay readable, so they
// must move to the new generation instead of being stranded as unread.
func TestAnalyzeMigratesClearRaceSurvivors(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		send := func(id int) {
			l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
				String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
				String(AttrDeliveryTarget, "bob"), Int(AttrMessageID, id))
		}
		send(5) // arşivlendi, silindi
		send(9) // arşiv I/O sırasında geldi → clear'dan sağ çıkar
		tick(time.Second)
		l.Log(EventRoomReset, room, String(AttrRoomLifecycle, RoomLifecycleCleared),
			Int(AttrRoomResetMaxID, 5))
		tick(time.Second)
		// bob rejoins and reads the surviving message.
		l.Log(EventMessagesRead, room, String(AttrAgentName, "bob"),
			Int(AttrReadMaxID, 9), Ints(AttrReadMessageIDs, []int{9}))
	})

	got := analyzeDir(t, dir).Unread
	if len(got) != 1 {
		t.Fatalf("okunmamış sayısı = %d, want 1: %+v", len(got), got)
	}
	// 9 survived and was read; only the wiped 5 stays unread.
	if got[0].MessageID != 5 {
		t.Errorf("okunmamış id = %d, want 5 (sağ kalan 9 okundu sayılmalı)", got[0].MessageID)
	}
}

// Codex review round 3: an ordinary pre-cutoff event is the last evidence the
// dead hub was alive; skipping it stretches a short outage back to the previous
// lifecycle record.
func TestAnalyzeUncleanOutageUsesLastPreCutoffEvent(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted) // 09:00
		tick(2 * time.Hour)
		l.Log(EventAgentJoined, String(AttrConversationID, "r1"), String(AttrAgentName, "alice")) // 11:00
		tick(time.Hour)
		l.Log(EventHubStarted) // 12:00 — stop yok: çökmüş
	})

	cut := time.Date(2026, 9, 8, 11, 30, 0, 0, time.UTC)
	rep, err := Analyze(AnalyzeOptions{Dir: dir, Since: cut})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(rep.Outages) != 1 {
		t.Fatalf("kesinti sayısı = %d, want 1: %+v", len(rep.Outages), rep.Outages)
	}
	// From the join at 11:00, not the start at 09:00.
	if got := rep.Outages[0].Duration; got != time.Hour {
		t.Errorf("süre = %v, want 1h (son canlılık kanıtından)", got)
	}
}

// Codex review round 3: a torn final line is expected after a kill, but a bad
// record with valid ones after it is real damage the report must disclose.
func TestReadCountsCorruptionButToleratesTornTail(t *testing.T) {
	t.Run("ortadaki bozuk kayıt bildirilir", func(t *testing.T) {
		dir := t.TempDir()
		lines := `{"time":"2026-09-08T09:00:00Z","event.name":"agent_chat.hub.started"}
{bozuk
{"time":"2026-09-08T09:01:00Z","event.name":"agent_chat.hub.stopped"}
`
		if err := os.WriteFile(filepath.Join(dir, fileName), []byte(lines), 0600); err != nil {
			t.Fatal(err)
		}
		recs, corrupted, err := Read(dir, time.Time{})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(recs) != 2 {
			t.Errorf("okunan kayıt = %d, want 2", len(recs))
		}
		if corrupted != 1 {
			t.Errorf("bozuk sayısı = %d, want 1", corrupted)
		}
	})

	t.Run("yarım kalan son satır hoş görülür", func(t *testing.T) {
		dir := t.TempDir()
		lines := `{"time":"2026-09-08T09:00:00Z","event.name":"agent_chat.hub.started"}
{"time":"2026-09-08T09:01:00Z","eve`
		if err := os.WriteFile(filepath.Join(dir, fileName), []byte(lines), 0600); err != nil {
			t.Fatal(err)
		}
		_, corrupted, err := Read(dir, time.Time{})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if corrupted != 0 {
			t.Errorf("bozuk sayısı = %d, want 0 (yarım son satır normaldir)", corrupted)
		}
	})
}

// Codex review round 3: lumberjack can remove a rotated backup between listing
// and opening it while the hub is live; that race must not fail the report.
func TestReadToleratesBackupRemovedMidRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileName),
		[]byte(`{"time":"2026-09-08T09:00:00Z","event.name":"agent_chat.hub.started"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A backup that vanishes before it is opened: simulated by a dangling symlink,
	// which lists fine and fails to open with ENOENT exactly as a deleted file does.
	backup := filepath.Join(dir, "events-2026-09-08T08-00-00.000.jsonl")
	if err := os.Symlink(filepath.Join(dir, "gitti.jsonl"), backup); err != nil {
		t.Skipf("symlink desteklenmiyor: %v", err)
	}

	recs, _, err := Read(dir, time.Time{})
	if err != nil {
		t.Fatalf("kaybolan yedek tüm raporu düşürdü: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("okunan kayıt = %d, want 1 (canlı akış okunabilmeli)", len(recs))
	}
}

// Codex review round 4, PR #103: --room selects findings, not evidence. A
// roomless loss marker and other rooms' activity must still be processed, or a
// filtered report claims zero losses after a crash.
func TestAnalyzeRoomFilterKeepsGlobalEvidence(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		tick(time.Minute)
		l.Log(EventAgentEvicted, String(AttrConversationID, "r2"), String(AttrAgentName, "bob"))
		tick(time.Minute)
		l.Log(EventEventsDropped, Uint64(AttrEventsDropped, 9)) // odasız
		tick(time.Minute)
		l.Log(EventAgentEvicted, String(AttrConversationID, "r1"), String(AttrAgentName, "alice"))
		// Hiç hub.stopped yok: çökme.
	})

	rep, err := Analyze(AnalyzeOptions{Dir: dir, Room: "r1"})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.Dropped != 9 {
		t.Errorf("Dropped = %d, want 9 (odasız işaret oda filtresine takılmamalı)", rep.Dropped)
	}
	if len(rep.Drops) != 1 || rep.Drops[0].Agent != "alice" {
		t.Errorf("bulgular odaya göre süzülmedi: %+v", rep.Drops)
	}
}

// Codex review round 4: an unclean restart bound must use the hub's real last
// activity, even when it happened in another room.
func TestAnalyzeUncleanOutageUsesOtherRoomActivity(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		l.Log(EventHubStarted)
		tick(2 * time.Hour)
		l.Log(EventAgentJoined, String(AttrConversationID, "r2"), String(AttrAgentName, "bob"))
		tick(time.Minute)
		l.Log(EventHubStarted) // stop yok: çökmüş
	})

	rep, err := Analyze(AnalyzeOptions{Dir: dir, Room: "r1"})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(rep.Outages) != 1 {
		t.Fatalf("kesinti sayısı = %d, want 1", len(rep.Outages))
	}
	if got := rep.Outages[0].Duration; got != time.Minute {
		t.Errorf("süre = %v, want 1m (başka odadaki son aktiviteden)", got)
	}
}

// Codex review round 4: ClearArchived(0) retains every racing message, so a
// zero watermark must still migrate survivors.
func TestAnalyzeMigratesSurvivorsOnZeroWatermark(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
			String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
			String(AttrDeliveryTarget, "bob"), Int(AttrMessageID, 3))
		tick(time.Second)
		// Empty snapshot: nothing was wiped, message 3 survives.
		l.Log(EventRoomReset, room, String(AttrRoomLifecycle, RoomLifecycleCleared),
			Int(AttrRoomResetMaxID, 0))
		tick(time.Second)
		l.Log(EventMessagesRead, room, String(AttrAgentName, "bob"),
			Int(AttrReadMaxID, 3), Ints(AttrReadMessageIDs, []int{3}))
	})

	if got := analyzeDir(t, dir).Unread; len(got) != 0 {
		t.Errorf("sıfır eşikli clear'da sağ kalan mesaj okunmuş sayılmadı: %+v", got)
	}
}

// Codex review round 4: the reset event is logged after the room lock is
// released, so a read of a surviving message can land in the old generation.
// Migrating only the sends would strand that read.
func TestAnalyzeMigratesSurvivorReadState(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
			String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
			String(AttrDeliveryTarget, "bob"), Int(AttrMessageID, 9))
		// Read lands BEFORE the reset event — the window where the room lock is
		// already released but the reset has not been logged.
		l.Log(EventMessagesRead, room, String(AttrAgentName, "bob"),
			Int(AttrReadMaxID, 9), Ints(AttrReadMessageIDs, []int{9}))
		tick(time.Second)
		l.Log(EventRoomReset, room, String(AttrRoomLifecycle, RoomLifecycleCleared),
			Int(AttrRoomResetMaxID, 5))
	})

	if got := analyzeDir(t, dir).Unread; len(got) != 0 {
		t.Errorf("sağ kalan mesajın okuması taşınmadı, sonsuza kadar okunmamış görünüyor: %+v", got)
	}
}

// Codex review round 4: room state is persisted every five seconds, so an
// unclean restart can roll back and reuse message IDs. A read in the new run
// must not clear a send that was lost in the crash.
func TestAnalyzeCrashRollbackSeparatesMessageIDs(t *testing.T) {
	dir := writeStream(t, func(l *Logger, tick func(time.Duration)) {
		room := String(AttrConversationID, "r1")
		l.Log(EventHubStarted)
		l.Log(EventMessageSent, room, String(AttrAgentName, "alice"),
			String(AttrRecipientName, "bob"), Bool(AttrRecipientInRoom, true),
			String(AttrDeliveryTarget, "bob"), Int(AttrMessageID, 10))
		tick(time.Minute)
		l.Log(EventHubStarted) // stop yok: çökme, snapshot geri sarabilir
		tick(time.Minute)
		// New run reuses ID 10 and bob reads it.
		l.Log(EventMessagesRead, room, String(AttrAgentName, "bob"),
			Int(AttrReadMaxID, 10), Ints(AttrReadMessageIDs, []int{10}))
	})

	got := analyzeDir(t, dir).Unread
	if len(got) != 1 || got[0].MessageID != 10 {
		t.Errorf("çökme öncesi kaybolan mesaj, yeniden kullanılan kimliğin okunmasıyla kapatılmış: %+v", got)
	}
}

// Codex review round 5, PR #103: a backup being compressed exists twice for a
// moment. Reading both sides would double-count every record in it.
func TestReadIgnoresInProgressCompressionDuplicate(t *testing.T) {
	dir := t.TempDir()
	rec := `{"time":"2026-09-08T09:00:00Z","event.name":"agent_chat.hub.started"}` + "\n"
	backup := filepath.Join(dir, "events-2026-09-08T08-00-00.000.jsonl")
	if err := os.WriteFile(backup, []byte(rec), 0600); err != nil {
		t.Fatal(err)
	}
	// The .gz lumberjack is midway through writing, holding the same record.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(rec)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup+".gz", buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	recs, _, err := Read(dir, time.Time{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("kayıt sayısı = %d, want 1 (sıkıştırma sırasında çift sayılmamalı)", len(recs))
	}
}

func TestReadSkipsHalfWrittenArchiveButReportsIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileName),
		[]byte(`{"time":"2026-09-08T09:00:00Z","event.name":"agent_chat.hub.started"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A .gz truncated mid-write, with no uncompressed sibling left.
	if err := os.WriteFile(filepath.Join(dir, "events-2026-09-08T08-00-00.000.jsonl.gz"),
		[]byte{0x1f, 0x8b, 0x08}, 0600); err != nil {
		t.Fatal(err)
	}

	recs, corrupted, err := Read(dir, time.Time{})
	if err != nil {
		t.Fatalf("yarım sıkıştırma tüm raporu düşürdü: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("kayıt sayısı = %d, want 1", len(recs))
	}
	if corrupted == 0 {
		t.Error("atlanan yarım arşiv bildirilmedi; rapor eksikliğini gizlerdi")
	}
}

// Codex review round 5: a torn tail is normal only for the newest file. In a
// rotated backup it is real damage — that file was closed before the next opened.
func TestReadTornTailForgivenOnlyInFinalStreamFile(t *testing.T) {
	dir := t.TempDir()
	torn := `{"time":"2026-09-08T08:00:00Z","event.name":"agent_chat.hub.started"}` + "\n{yarim"
	if err := os.WriteFile(filepath.Join(dir, "events-2026-09-08T08-00-00.000.jsonl"), []byte(torn), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName),
		[]byte(`{"time":"2026-09-08T09:00:00Z","event.name":"agent_chat.hub.stopped"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	_, corrupted, err := Read(dir, time.Time{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if corrupted != 1 {
		t.Errorf("bozuk sayısı = %d, want 1 (yedekteki yarım satır hasar sayılmalı)", corrupted)
	}
}
