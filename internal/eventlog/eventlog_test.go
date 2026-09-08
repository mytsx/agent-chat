package eventlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// readEvents reads every JSON line written to the logger's active file.
func readEvents(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("okunamadı: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("satır JSON değil: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func newTestLogger(t *testing.T, mut ...func(*Options)) (*Logger, string) {
	t.Helper()
	dir := t.TempDir()
	opts := Options{Dir: dir}
	for _, m := range mut {
		m(&opts)
	}
	l, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, dir
}

func TestLogWritesOTelAttributes(t *testing.T) {
	l, dir := newTestLogger(t)

	l.Log(EventMessageSent,
		String(AttrConversationID, "MapegCbs"),
		String(AttrAgentName, "Backend"),
		String(AttrRecipientName, "Frontend"),
		Bool(AttrRecipientInRoom, false),
		Int(AttrMessageID, 4711),
		String(AttrRequestID, "req-7f3a"),
	)
	l.Flush()

	events := readEvents(t, dir)
	if len(events) != 1 {
		t.Fatalf("olay sayısı = %d, want 1", len(events))
	}
	e := events[0]

	for key, want := range map[string]any{
		AttrEventName:       EventMessageSent,
		AttrConversationID:  "MapegCbs",
		AttrAgentName:       "Backend",
		AttrRecipientName:   "Frontend",
		AttrRecipientInRoom: false,
		AttrMessageID:       float64(4711), // JSON sayıları float64 çözülür
		AttrRequestID:       "req-7f3a",
	} {
		if got, ok := e[key]; !ok {
			t.Errorf("%q alanı yok", key)
		} else if got != want {
			t.Errorf("%q = %v (%T), want %v (%T)", key, got, got, want, want)
		}
	}

	// slog'un zorunlu alanları da olayı tanımlamalı: düz okumada msg işe yarar.
	if e["msg"] != EventMessageSent {
		t.Errorf("msg = %v, want %q", e["msg"], EventMessageSent)
	}
	if _, ok := e["time"]; !ok {
		t.Error("time alanı yok")
	}
}

func TestTimestampComesFromInjectedClock(t *testing.T) {
	fixed := time.Date(2026, 9, 8, 11, 4, 23, 0, time.UTC)
	l, dir := newTestLogger(t, func(o *Options) {
		o.now = func() time.Time { return fixed }
	})

	l.Log(EventHubStarted)
	l.Flush()

	got := readEvents(t, dir)[0]["time"]
	ts, err := time.Parse(time.RFC3339Nano, got.(string))
	if err != nil {
		t.Fatalf("time ayrıştırılamadı (%v): %v", got, err)
	}
	if !ts.Equal(fixed) {
		t.Errorf("time = %v, want %v", ts, fixed)
	}
}

func TestContentCaptureGate(t *testing.T) {
	t.Run("varsayılan olarak içerik yazılır", func(t *testing.T) {
		l, dir := newTestLogger(t)
		l.Log(EventMessageSent, String(AttrInputMessages, "servis katmanını ayırdım"))
		l.Flush()

		if got := readEvents(t, dir)[0][AttrInputMessages]; got != "servis katmanını ayırdım" {
			t.Errorf("%s = %v, want içerik", AttrInputMessages, got)
		}
	})

	t.Run("kapalıyken içerik hiç yazılmaz", func(t *testing.T) {
		l, dir := newTestLogger(t, func(o *Options) { o.CaptureContent = CaptureOff })
		l.Log(EventMessageSent,
			String(AttrAgentName, "Backend"),
			String(AttrInputMessages, "gizli kalmalı"),
		)
		l.Flush()

		e := readEvents(t, dir)[0]
		if _, ok := e[AttrInputMessages]; ok {
			t.Errorf("içerik kapalıyken %s yazılmış: %v", AttrInputMessages, e[AttrInputMessages])
		}
		// Kapı yalnızca içeriği düşürür; olayın kendisi kaydedilmeye devam eder.
		if e[AttrAgentName] != "Backend" {
			t.Errorf("içerik kapısı diğer alanları düşürmüş: %v", e)
		}
	})
}

func TestContentTruncation(t *testing.T) {
	l, dir := newTestLogger(t)
	long := strings.Repeat("a", maxContentBytes+500)

	l.Log(EventMessageSent, String(AttrInputMessages, long))
	l.Flush()

	e := readEvents(t, dir)[0]
	got, _ := e[AttrInputMessages].(string)
	if len(got) != maxContentBytes {
		t.Errorf("kırpılmış uzunluk = %d, want %d", len(got), maxContentBytes)
	}
	if e[AttrContentTruncated] != true {
		t.Errorf("%s = %v, want true", AttrContentTruncated, e[AttrContentTruncated])
	}

	t.Run("sınır altındaki içerik işaretlenmez", func(t *testing.T) {
		l2, dir2 := newTestLogger(t)
		l2.Log(EventMessageSent, String(AttrInputMessages, "kısa"))
		l2.Flush()

		if _, ok := readEvents(t, dir2)[0][AttrContentTruncated]; ok {
			t.Error("kısa içerik truncated işaretlenmiş")
		}
	})
}

func TestTruncationSplitsOnRuneBoundary(t *testing.T) {
	l, dir := newTestLogger(t)
	// Türkçe metin: sınıra denk gelen çok baytlı karakter yarıdan kesilmemeli.
	long := strings.Repeat("ğ", maxContentBytes) // her biri 2 bayt

	l.Log(EventMessageSent, String(AttrInputMessages, long))
	l.Flush()

	got, _ := readEvents(t, dir)[0][AttrInputMessages].(string)
	if !utf8.ValidString(got) {
		t.Errorf("kırpma geçersiz UTF-8 üretti (%d bayt)", len(got))
	}
	if len(got) > maxContentBytes {
		t.Errorf("kırpılmış uzunluk = %d, sınır %d", len(got), maxContentBytes)
	}
}

func TestLogSyncBypassesBuffer(t *testing.T) {
	l, dir := newTestLogger(t)

	// Flush çağrılmadan dosyada görünmeli.
	l.LogSync(EventHubStopped, Int64(AttrEventsDropped, 3))

	events := readEvents(t, dir)
	if len(events) != 1 || events[0][AttrEventName] != EventHubStopped {
		t.Fatalf("LogSync yazmadı: %v", events)
	}
	if events[0][AttrEventsDropped] != float64(3) {
		t.Errorf("%s = %v, want 3", AttrEventsDropped, events[0][AttrEventsDropped])
	}
}

func TestLogNeverBlocksAndCountsDrops(t *testing.T) {
	release := make(chan struct{})
	l, _ := newTestLogger(t, func(o *Options) {
		o.BufferSize = 1
		o.beforeWrite = func() { <-release } // yazar goroutine'i kilitle
	})
	defer close(release)

	// Yazar ilk olayda kilitli; tampon 1. Kalanların düşmesi gerekir.
	const n = 50
	done := make(chan struct{})
	go func() {
		for range n {
			l.Log(EventMessageSent)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Log bloke oldu — log yazımı hub'ın sıcak yolunu asla kilitlememeli")
	}

	if l.Dropped() == 0 {
		t.Error("Dropped() = 0, tampon dolduğunda olay düşmeli ve sayılmalı")
	}
}

func TestRotation(t *testing.T) {
	l, dir := newTestLogger(t, func(o *Options) {
		o.MaxSizeMB = 1
		o.MaxBackups = 2
		o.Compress = new(bool) // sıkıştırma kapalı: yedekleri sayabilmek için
	})

	// ~1.5 MB içerik: en az bir rotasyon tetiklenmeli.
	payload := strings.Repeat("x", 4000)
	for range 400 {
		l.Log(EventMessageSent, String(AttrInputMessages, payload))
	}
	l.Flush()

	countBackups := func() int {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		for _, e := range entries {
			if e.Name() != fileName {
				n++
			}
		}
		return n
	}

	if countBackups() == 0 {
		t.Fatal("rotasyon olmadı")
	}
	// lumberjack prunes old backups in a goroutine it starts per rotation, so the
	// cap is eventually true rather than immediately — poll instead of racing it.
	deadline := time.Now().Add(2 * time.Second)
	for countBackups() > 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := countBackups(); n > 2 {
		t.Errorf("yedek sayısı = %d, MaxBackups=2 sınırı aşıldı", n)
	}
}

func TestNopLoggerIsSafe(t *testing.T) {
	l := NopLogger()
	l.Log(EventMessageSent, String(AttrAgentName, "x"))
	l.LogSync(EventHubStopped)
	l.Flush()
	if got := l.Dropped(); got != 0 {
		t.Errorf("Dropped() = %d, want 0", got)
	}
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestNewFallsBackToNopOnUnwritableDir(t *testing.T) {
	// Dosya olan bir yolu dizin olarak vermek: hub başlamayı reddetmemeli.
	f := filepath.Join(t.TempDir(), "engel")
	if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	l, err := New(Options{Dir: filepath.Join(f, "alt")})
	if err == nil {
		t.Fatal("New hata döndürmeliydi")
	}
	if l == nil {
		t.Fatal("New hata durumunda kullanılabilir bir NopLogger döndürmeli")
	}
	l.Log(EventMessageSent) // panik etmemeli
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestConcurrentLog(t *testing.T) {
	l, dir := newTestLogger(t)

	const goroutines, each = 20, 50
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				l.Log(EventMessageSent, Int(AttrMessageID, g))
			}
		}()
	}
	wg.Wait()
	l.Flush()

	got := len(readEvents(t, dir))
	if want := goroutines * each; got != want {
		t.Errorf("olay sayısı = %d, want %d (düşen: %d)", got, want, l.Dropped())
	}
}

func TestFilePermissionsAreOwnerOnly(t *testing.T) {
	l, dir := newTestLogger(t)
	l.Log(EventMessageSent)
	l.Flush()

	info, err := os.Stat(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	// Konuşma içeriği barındırıyor: başkası okuyamamalı.
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("izin = %o, want 600", perm)
	}
}

// Close closes the queue, so a Log racing it must not send on a closed channel.
// The hub's shutdown can race its client-manager goroutine, which logs
// disconnects — a panic there would take down the hub on every exit.
func TestLogRacingCloseDoesNotPanic(t *testing.T) {
	l, _ := newTestLogger(t)

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				l.Log(EventMessageSent)
				l.Flush()
			}
		}()
	}
	go func() { _ = l.Close() }()
	wg.Wait() // panik ederse test burada çöker
}

func TestLogAfterCloseIsIgnored(t *testing.T) {
	l, dir := newTestLogger(t)
	l.Log(EventAgentJoined)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	l.Log(EventMessageSent) // panik etmemeli
	l.Flush()               // panik etmemeli, bloke olmamalı

	events := readEvents(t, dir)
	if len(events) != 1 || events[0][AttrEventName] != EventAgentJoined {
		t.Errorf("kapanıştan sonraki olay yazılmış: %v", events)
	}
}

// Codex review, PR #103: the documented zero-value configuration must actually
// log, not silently degrade to a no-op because MkdirAll("") fails.
func TestNewWithEmptyDirUsesCurrentDirectory(t *testing.T) {
	t.Chdir(t.TempDir())

	l, err := New(Options{})
	if err != nil {
		t.Fatalf("New(Options{}): %v", err)
	}
	defer func() { _ = l.Close() }()

	l.Log(EventHubStarted)
	l.Flush()

	if _, err := os.Stat(fileName); err != nil {
		t.Errorf("boş Dir geçerli dizine yazmadı: %v", err)
	}
}

// Codex review round 2, PR #103: waiting for hub.stopped to report the drop
// count loses it entirely when the hub crashes — exactly the case this log is
// meant to investigate. The writer must make the loss durable as it happens.
func TestDropsAreAnnouncedWithoutGracefulShutdown(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	l, dir := newTestLogger(t, func(o *Options) {
		o.BufferSize = 1
		o.beforeWrite = func() { once.Do(func() { <-release }) }
	})

	for range 50 {
		l.Log(EventMessageSent)
	}
	if l.Dropped() == 0 {
		t.Fatal("kurulum hatası: hiç olay düşmedi")
	}
	close(release) // yazar serbest; Close ÇAĞRILMIYOR
	l.Flush()

	var found bool
	for _, e := range readEvents(t, dir) {
		if e[AttrEventName] == EventEventsDropped {
			found = true
			if n, ok := e[AttrEventsDropped].(float64); !ok || n == 0 {
				t.Errorf("%s = %v, want > 0", AttrEventsDropped, e[AttrEventsDropped])
			}
		}
	}
	if !found {
		t.Error("düşen olaylar akışa kalıcı olarak işaretlenmedi; çökme sonrası kayıp görünmez olurdu")
	}
}

// Codex review round 2: a sink that starts failing after New must not be
// silent. The desktop starts the hub with stderr unset, so the failure has to
// reach the injected reporter and be counted as loss.
func TestSinkWriteFailureIsCountedAndReported(t *testing.T) {
	var reported []error
	l, dir := newTestLogger(t, func(o *Options) {
		o.OnError = func(err error) { reported = append(reported, err) }
	})

	l.Log(EventHubStarted)
	l.Flush()

	// Break the sink underneath the logger, then keep logging.
	if err := l.sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("artık dizin değil"), 0600); err != nil {
		t.Fatal(err)
	}

	before := l.Dropped()
	l.Log(EventMessageSent)
	l.Flush()

	if l.Dropped() <= before {
		t.Errorf("yazma hatası kayıp olarak sayılmadı: %d → %d", before, l.Dropped())
	}
	if len(reported) == 0 {
		t.Error("yazma hatası OnError'a bildirilmedi")
	}
}

// Codex review round 4, PR #103: a queued event that fails to write increments
// the drop count, so a stopped record written before the drain reports a count
// taken too early — with no later event to carry a durable marker.
func TestDrainBeforeFinalRecordSeesLateLosses(t *testing.T) {
	hold := make(chan struct{})
	var once sync.Once
	l, dir := newTestLogger(t, func(o *Options) {
		o.beforeWrite = func() { once.Do(func() { <-hold }) }
	})

	// Two events queued; the writer is parked on the first.
	l.Log(EventHubStarted)
	l.Log(EventMessageSent)

	// Break the sink while both are still queued, then let the writer run.
	if err := l.sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("artık dizin değil"), 0600); err != nil {
		t.Fatal(err)
	}
	close(hold)

	// Drain is what makes the losses visible in time to report them; reading the
	// count before it would race the writer, which is precisely the bug — the
	// hub used to snapshot Dropped() while events were still queued.
	l.Drain()
	if l.Dropped() == 0 {
		t.Error("drain sonrası başarısız yazmalar sayılmadı; hub.stopped kaybı gizlerdi")
	}
	if err := l.Close(); err != nil {
		t.Logf("Close: %v", err) // bozuk sink'te beklenebilir
	}
}

func TestDrainIsIdempotentAndCloseStillWorks(t *testing.T) {
	l, _ := newTestLogger(t)
	l.Log(EventHubStarted)
	l.Drain()
	l.Drain() // ikinci çağrı panik etmemeli
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("ikinci Close: %v", err)
	}
	l.Log(EventMessageSent) // kapanıştan sonra sessizce yutulmalı
}

// Codex review round 6, PR #103: an oversized read must keep its exact IDs.
// Ranges make that possible without a cap — a read is a contiguous tail, so it
// costs two numbers however many messages came back.
func TestIDRangeRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		ids       []int
		wantPairs int
	}{
		{"bitişik kuyruk", []int{4, 5, 6, 7}, 2},
		{"boşluklu", []int{1, 2, 5, 9, 10}, 6},
		{"tek", []int{42}, 2},
		{"sırasız ve tekrarlı", []int{7, 5, 6, 5}, 2},
		{"boş", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pairs := EncodeIDRanges(tc.ids)
			if len(pairs) != tc.wantPairs {
				t.Errorf("aralık uzunluğu = %d, want %d (%v)", len(pairs), tc.wantPairs, pairs)
			}
			got := DecodeIDRanges(pairs)
			want := map[int]bool{}
			for _, id := range tc.ids {
				want[id] = true
			}
			if len(got) != len(want) {
				t.Fatalf("çözülen = %v, want kümesi %v", got, tc.ids)
			}
			for _, id := range got {
				if !want[id] {
					t.Errorf("beklenmeyen id %d", id)
				}
			}
		})
	}

	t.Run("büyük bitişik okuma iki sayıya sığar", func(t *testing.T) {
		ids := make([]int, 0, 1000)
		for i := 1; i <= 1000; i++ {
			ids = append(ids, i)
		}
		if pairs := EncodeIDRanges(ids); len(pairs) != 2 {
			t.Errorf("1000 kimlik %d sayıya kodlandı, want 2", len(pairs))
		}
	})

	t.Run("bozuk aralık tahmin edilmez", func(t *testing.T) {
		if got := DecodeIDRanges([]int{5, 3}); len(got) != 0 {
			t.Errorf("ters aralık çözülmüş: %v", got)
		}
		if got := DecodeIDRanges([]int{1}); len(got) != 0 {
			t.Errorf("tek sayılı aralık çözülmüş: %v", got)
		}
	})
}
