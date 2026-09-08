// Package eventlog writes agent-room events — joins, leaves, evictions,
// messages, reads — as one JSON-lines stream, using OpenTelemetry semantic
// convention attribute names (#101).
//
// It exists because the plain-text mcp-server.log cannot answer why agents drop
// out of a room or miss each other's messages: it has no rotation, no structure,
// and no message content. This package is the measurement layer for #98 and #99.
//
// Design constraints, in priority order:
//
//  1. Logging must never block, slow, or panic the hub's routing path. Log is
//     fire-and-forget; a full buffer drops the event and counts the drop.
//  2. A logging failure must never stop the hub. New returns a usable NopLogger
//     alongside its error.
//  3. Field names are OTel's wherever OTel has one, so this stream can later be
//     mapped onto OTLP without renaming anything. See semconv.go.
//
// Only the hub process writes here. That keeps a single writer on the file,
// which is also why lumberjack's multi-process weakness does not apply.
package eventlog

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// fileName is the single event stream. One stream rather than one file per
// room: the room is an attribute, so cross-room questions (which agents did a
// hub outage affect?) stay answerable, rotation stays a single concern, and a
// user-supplied room name never reaches a file path.
const fileName = "events.jsonl"

// maxContentBytes bounds a captured message. OTel asks instrumentations that
// capture content to provide truncation; this also stops one agent pasting a
// large file from dominating the log.
const maxContentBytes = 8 * 1024

// Defaults sized for a desktop app: ~32 MB live plus at most 5 compressed
// backups, discarded after 30 days.
const (
	defaultMaxSizeMB  = 32
	defaultMaxBackups = 5
	defaultMaxAgeDays = 30
	defaultBufferSize = 1024
)

// ContentCapture is the tri-state gate on message content. The zero value means
// "not configured", so an Options literal that omits the field still gets the
// configured default rather than silently capturing nothing.
type ContentCapture int

const (
	// CaptureDefault consults AGENT_CHAT_CAPTURE_MESSAGE_CONTENT, falling back
	// to OTel's OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT, and captures
	// when neither is set. See Options.CaptureContent for why on-by-default.
	CaptureDefault ContentCapture = iota
	CaptureOn
	CaptureOff
)

// Env vars gating content capture. The OTel name is honoured too so someone who
// already knows that knob finds the switch where they expect it.
const (
	envCaptureContent     = "AGENT_CHAT_CAPTURE_MESSAGE_CONTENT"
	envOTelCaptureContent = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"
)

// Options configures a Logger. The zero value is usable: every field falls back
// to a default, and an empty Dir means the current directory.
type Options struct {
	Dir        string
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	// Compress gzips rotated backups. Nil means the default (true); a pointer so
	// a caller can ask for false without it looking like "unset".
	Compress *bool
	// CaptureContent gates AttrInputMessages. OTel says instrumentations SHOULD
	// NOT capture message content by default and SHOULD gate it behind an
	// explicit opt-in. We deliberately default to capturing: this log exists to
	// answer "why did the agent misunderstand", which content alone answers, and
	// the file never leaves the user's machine (mode 0600). The switch is an
	// explicit, documented env var either way.
	CaptureContent ContentCapture
	BufferSize     int

	// now and beforeWrite are test seams.
	now         func() time.Time
	beforeWrite func()
}

// entry is one queued event. flush carries a barrier instead of an event.
type entry struct {
	t     time.Time
	name  string
	attrs []Attr
	flush chan struct{}
}

// Logger writes events to a rotating JSON-lines file. Safe for concurrent use.
// The nil-behaviour equivalent is NopLogger, not the zero value.
type Logger struct {
	handler slog.Handler
	sink    *lumberjack.Logger
	ch      chan entry
	done    chan struct{}

	// writeMu serialises handler writes: the writer goroutine and a synchronous
	// LogSync (used for the final hub.stopped record) can both reach the handler.
	writeMu sync.Mutex

	// closeMu guards the queue against Close. A producer holds it for reading
	// while it checks closed and sends; Close takes it for writing before it
	// closes the channel, so no send can ever land on a closed one. Without this
	// the hub's shutdown races its client-manager goroutine — which logs
	// disconnects — and panics on the way out.
	closeMu sync.RWMutex
	closed  bool

	dropped        atomic.Uint64
	captureContent bool
	now            func() time.Time
	beforeWrite    func()

	closeOnce sync.Once
	// reportedWriteErr keeps a failing sink from turning into a log storm: the
	// first write error is reported to stderr, the rest are silent.
	reportedWriteErr atomic.Bool
}

// New opens the event stream under opts.Dir.
//
// On failure it returns a usable NopLogger together with the error, so a caller
// can log the problem and keep running. A hub must never refuse to start
// because its event log could not be opened.
func New(opts Options) (*Logger, error) {
	if err := os.MkdirAll(opts.Dir, 0700); err != nil {
		return NopLogger(), err
	}

	// lumberjack creates the file lazily, so probe now: a caller that gets no
	// error should be able to trust that events will actually land.
	path := filepath.Join(opts.Dir, fileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return NopLogger(), err
	}
	if err := f.Close(); err != nil {
		return NopLogger(), err
	}

	sink := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    orDefault(opts.MaxSizeMB, defaultMaxSizeMB),
		MaxBackups: orDefault(opts.MaxBackups, defaultMaxBackups),
		MaxAge:     orDefault(opts.MaxAgeDays, defaultMaxAgeDays),
		Compress:   opts.Compress == nil || *opts.Compress,
	}

	l := &Logger{
		sink:           sink,
		ch:             make(chan entry, orDefault(opts.BufferSize, defaultBufferSize)),
		done:           make(chan struct{}),
		captureContent: resolveCapture(opts.CaptureContent),
		now:            opts.now,
		beforeWrite:    opts.beforeWrite,
	}
	if l.now == nil {
		l.now = time.Now
	}
	// Time and level come from the record itself, so the handler adds nothing on
	// its own; keeping the default keys means any slog-aware tool reads this file.
	l.handler = slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelInfo})

	go l.run()
	return l, nil
}

// NopLogger returns a Logger that discards everything. Used when the event log
// cannot be opened, and by callers that have no logging configured.
func NopLogger() *Logger { return &Logger{} }

func (l *Logger) isNop() bool { return l == nil || l.handler == nil }

// Log queues an event. It never blocks: if the buffer is full the event is
// dropped and counted, because stalling here would stall the hub's routing path.
func (l *Logger) Log(name string, attrs ...Attr) {
	if l.isNop() {
		return
	}
	l.closeMu.RLock()
	defer l.closeMu.RUnlock()
	if l.closed {
		return
	}
	e := entry{t: l.now(), name: name, attrs: attrs}
	select {
	case l.ch <- e:
	default:
		l.dropped.Add(1)
	}
}

// LogSync writes an event immediately, bypassing the buffer. Reserved for
// records that must not be lost to a full buffer — notably the final
// hub.stopped event, which reports the drop count and so cannot itself drop.
func (l *Logger) LogSync(name string, attrs ...Attr) {
	if l.isNop() {
		return
	}
	l.write(entry{t: l.now(), name: name, attrs: attrs})
}

// Dropped reports how many events were discarded because the buffer was full.
// Written to the hub.stopped event so the loss is never silent.
func (l *Logger) Dropped() uint64 {
	if l.isNop() {
		return 0
	}
	return l.dropped.Load()
}

// Flush blocks until every event queued so far has reached the sink. The
// barrier is FIFO behind the current backlog, so it cannot pass pending events.
func (l *Logger) Flush() {
	if l.isNop() {
		return
	}
	barrier := make(chan struct{})

	l.closeMu.RLock()
	if l.closed {
		l.closeMu.RUnlock()
		return // queue already drained by Close
	}
	select {
	case l.ch <- entry{flush: barrier}:
	case <-l.done:
		l.closeMu.RUnlock()
		return
	}
	l.closeMu.RUnlock()

	select {
	case <-barrier:
	case <-l.done:
	}
}

// Close drains the backlog and closes the file. Safe to call more than once.
func (l *Logger) Close() error {
	if l.isNop() {
		return nil
	}
	var err error
	l.closeOnce.Do(func() {
		// Shut the door before closing the channel: any producer is either
		// already past its send or will see closed and give up.
		l.closeMu.Lock()
		l.closed = true
		l.closeMu.Unlock()

		close(l.ch)
		<-l.done // writer drains what is queued, then exits
		err = l.sink.Close()
	})
	return err
}

// run drains the queue until Close closes the channel.
func (l *Logger) run() {
	defer close(l.done)
	for e := range l.ch {
		if e.flush != nil {
			close(e.flush)
			continue
		}
		if l.beforeWrite != nil {
			l.beforeWrite()
		}
		l.write(e)
	}
}

// write renders one event. Content policy (gate, truncation) lives here rather
// than at the call sites so every producer is covered by construction.
func (l *Logger) write(e entry) {
	rec := slog.NewRecord(e.t, slog.LevelInfo, e.name, 0)
	rec.AddAttrs(String(AttrEventName, e.name))

	for _, a := range e.attrs {
		if a.Key != AttrInputMessages {
			rec.AddAttrs(a)
			continue
		}
		if !l.captureContent {
			continue
		}
		content, truncated := truncate(a.Value.String())
		rec.AddAttrs(String(AttrInputMessages, content))
		if truncated {
			rec.AddAttrs(Bool(AttrContentTruncated, true))
		}
	}

	l.writeMu.Lock()
	err := l.handler.Handle(context.Background(), rec)
	l.writeMu.Unlock()

	// A broken sink must not become a log storm: report once, then stay quiet.
	if err != nil && l.reportedWriteErr.CompareAndSwap(false, true) {
		os.Stderr.WriteString("eventlog: yazma hatası (bundan sonrası susturuldu): " + err.Error() + "\n")
	}
}

// truncate caps content at maxContentBytes without splitting a rune, so a
// Turkish message cut at the limit stays valid UTF-8.
func truncate(s string) (string, bool) {
	if len(s) <= maxContentBytes {
		return s, false
	}
	cut := maxContentBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// resolveCapture applies the explicit setting, else the env vars, else captures.
func resolveCapture(c ContentCapture) bool {
	switch c {
	case CaptureOn:
		return true
	case CaptureOff:
		return false
	}
	for _, env := range []string{envCaptureContent, envOTelCaptureContent} {
		switch os.Getenv(env) {
		case "false", "0", "no", "off":
			return false
		case "true", "1", "yes", "on":
			return true
		}
	}
	return true
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
