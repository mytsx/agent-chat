package eventlog

import (
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

	recs, err := Read(dir, time.Time{})
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
	recs, err := Read(dir, cut)
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
