# CLI Session Ingestion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture every message the user sends to an AI agent — including text typed directly into the agent's terminal — into the room transcript as `user_prompt`, by ingesting the CLI's own session file (the terminal stays an untouched raw passthrough).

**Architecture:** A new `internal/ingest` package owns a `Manager` that, per AI terminal, runs a `Watcher`: a CLI-specific `SessionAdapter` discovers the CLI's session file, the watcher polls/tails it, extracts verbatim `role:user` messages, suppresses ones that match the app's own injections (startup prompt / broadcast / prompt-send — tracked via a per-session fingerprint store), and emits the rest through an injected emit function wired in `app.go` to `hubClient.LogMessage(room, agent, content, ts)` (the #29/#58 `user_prompt` path). The xterm terminal is never modified.

**Tech Stack:** Go (module `desktop`), standard library only (`encoding/json`, `bufio`, `os`, `path/filepath`, `crypto/sha256`, `time`, `sync`). No new dependencies. React/TS frontend is NOT touched.

## Global Constraints

- Go module name is `desktop` (not URL-based). Import paths: `desktop/internal/ingest`, etc.
- Standard library ONLY — no fsnotify or third-party watchers (poll-based watcher).
- Agent-facing / user-facing log strings: Turkish + emoji where the codebase already does so; internal logs may be Turkish like the rest of `app.go`.
- `last_seen`/timestamps that the hub stores are `float64` Unix (existing); ingested message timestamps are the CLI file's own ISO-8601 **string**, passed straight to `LogMessage`'s `ts` param.
- **#58 dependency:** `hubclient.HubClient.LogMessage` MUST have the timestamp-carrying signature `LogMessage(room, to, content, timestamp string) error` (from #58 / PR #64). The `internal/ingest` package is independent of this (it emits through an injected `EmitFunc`), but the `app.go` wiring task (Task 8) requires it. Before Task 8: either rebase `feat/session-ingestion` onto `feat/post29-edgecases-58`, or merge PR #64 to main and rebase. Tasks 1–7 do NOT need #58.
- Observer terminals (#17) are NEVER ingested (privacy).
- Only AI CLIs are ingested: `claude`, `gemini`, `copilot`, `codex` (use the existing `isAICLIType` in `app.go`). Plain shell terminals are skipped.
- Build constraint: `make mcp-server` before `go build` (embed). Run `go test ./...` + `go vet ./...` + `gofmt -l` clean before each commit that touches Go.
- Tests are table-driven with `t.Run()` subtests, matching the existing suite style.

---

## File Structure

- Create `internal/ingest/ingest.go` — package types: `UserMessage`, `Cursor`, `SessionAdapter` interface, `EmitFunc`, `Manager`.
- Create `internal/ingest/fingerprint.go` — `fingerprintStore` (normalize + consumable-count suppression).
- Create `internal/ingest/watcher.go` — `watcher` (poll loop, tail via cursor, adapter parse, fingerprint suppression, emit).
- Create `internal/ingest/adapter_claude.go` — `claudeAdapter`.
- Create `internal/ingest/adapter_copilot.go` — `copilotAdapter`.
- Create `internal/ingest/adapter_codex.go` — `codexAdapter`.
- Create `internal/ingest/adapter_gemini.go` — `geminiAdapter`.
- Create `internal/ingest/adapterfor.go` — `AdapterFor(cliType string) SessionAdapter`.
- Create the matching `_test.go` files alongside each.
- Modify `app.go` — own an `*ingest.Manager`; record fingerprints at the 3 injection points; start/stop watchers on terminal create/close/restart.

---

## Task 0: M0 De-risk — confirm Claude writes a session file under app-spawn

**This is a manual verification gate, not code. If it fails, STOP and revise the spec (the whole Claude path depends on it).**

**Files:** none.

- [ ] **Step 1: Build and run the app**

```bash
cd /Users/yerli/Developer/agent-chat
make dev
```

- [ ] **Step 2: Spawn a Claude terminal and type a message**

In the app: create a team terminal with CLI type `claude` in some git repo dir (note that dir as `CWD`). Type a short message (e.g. `merhaba test`) into the xterm and press Enter.

- [ ] **Step 3: Verify a session file appeared and contains the message**

Compute the slug of `CWD` (every non-alphanumeric char → `-`, leading `/` → leading `-`). Then:

```bash
ls -lt ~/.claude/projects/<slug-of-CWD>/ | head
# open the newest .jsonl and confirm a line with type":"user" and "content":"merhaba test"
grep -l 'merhaba test' ~/.claude/projects/<slug-of-CWD>/*.jsonl
```

Expected: a `{uuid}.jsonl` exists, was just modified, and contains a `type:"user"` line whose `message.content` is the verbatim `merhaba test`.

- [ ] **Step 4: Record the outcome**

If PASS: proceed to Task 1. If FAIL (no file, or file lacks the message): STOP. The PTY env or a Claude flag suppresses transcript writing — revisit the spec's Claude adapter (alternate location/flag) before continuing.

---

## Task 1: `internal/ingest` core types + `AdapterFor`

**Files:**
- Create: `internal/ingest/ingest.go`
- Create: `internal/ingest/adapterfor.go`
- Test: `internal/ingest/adapterfor_test.go`

**Interfaces:**
- Produces: `UserMessage{Content, Timestamp string}`; `Cursor` (opaque progress token: `Offset int64`, `Count int`); `SessionAdapter` interface; `EmitFunc func(content, timestamp string)`; `AdapterFor(cliType string) SessionAdapter` (nil for non-AI / unknown).

- [ ] **Step 1: Write the core types file**

Create `internal/ingest/ingest.go`:

```go
// Package ingest reconstructs the user's typed messages to AI-CLI agents by
// reading each CLI's own session/transcript file, so they can be logged into the
// room transcript (#65). The terminal stays an untouched raw passthrough; this
// package only observes the CLI's session files.
package ingest

// UserMessage is one verbatim human message extracted from a CLI session file.
type UserMessage struct {
	Content   string // verbatim user text (no tool results / assistant output)
	Timestamp string // the CLI file's own ISO-8601 timestamp for this message
}

// Cursor records how far a session file has been ingested. JSONL adapters use
// Offset (byte offset of the next unread line). The monolithic-JSON Gemini
// adapter uses Count (number of messages already emitted). Zero value = start.
type Cursor struct {
	Offset int64
	Count  int
}

// SessionAdapter is the per-CLI strategy for locating and parsing a session file.
type SessionAdapter interface {
	// DiscoverFile returns the path to the session file for a CLI spawned in cwd
	// at spawnedAtUnixNano, or "" (no error) if none is found yet.
	DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error)
	// ParseNewUserMessages reads the file starting from cur and returns verbatim
	// human user messages plus the advanced cursor. It must skip assistant output,
	// tool results, and system/preamble entries, and gracefully skip records whose
	// schema version it does not recognize (returning no error).
	ParseNewUserMessages(path string, cur Cursor) (msgs []UserMessage, next Cursor, err error)
}

// EmitFunc receives one extracted, non-suppressed user message.
type EmitFunc func(content, timestamp string)
```

- [ ] **Step 2: Write the failing test for `AdapterFor`**

Create `internal/ingest/adapterfor_test.go`:

```go
package ingest

import "testing"

func TestAdapterFor(t *testing.T) {
	for _, ai := range []string{"claude", "copilot", "codex", "gemini"} {
		if AdapterFor(ai) == nil {
			t.Errorf("AdapterFor(%q) = nil, want an adapter", ai)
		}
	}
	for _, no := range []string{"", "shell", "bash", "unknown"} {
		if AdapterFor(no) != nil {
			t.Errorf("AdapterFor(%q) != nil, want nil (not an ingestable CLI)", no)
		}
	}
}
```

- [ ] **Step 3: Run the test, verify it fails**

Run: `go test ./internal/ingest/ -run TestAdapterFor`
Expected: build failure — `AdapterFor` and the adapter types are undefined.

- [ ] **Step 4: Write `AdapterFor` with stub adapters**

Create `internal/ingest/adapterfor.go`:

```go
package ingest

// AdapterFor returns the session adapter for a CLI type, or nil if that CLI is
// not ingestable (plain shell, empty, or unknown).
func AdapterFor(cliType string) SessionAdapter {
	switch cliType {
	case "claude":
		return claudeAdapter{}
	case "copilot":
		return copilotAdapter{}
	case "codex":
		return codexAdapter{}
	case "gemini":
		return geminiAdapter{}
	default:
		return nil
	}
}
```

Create empty stub types so it compiles — each adapter file (Tasks 4, 6, 7, 9) replaces the stub body. Add to a temporary `internal/ingest/adapters_stub.go`:

```go
package ingest

type claudeAdapter struct{}
type copilotAdapter struct{}
type codexAdapter struct{}
type geminiAdapter struct{}

func (claudeAdapter) DiscoverFile(string, int64) (string, error)                 { return "", nil }
func (claudeAdapter) ParseNewUserMessages(string, Cursor) ([]UserMessage, Cursor, error) { return nil, Cursor{}, nil }
func (copilotAdapter) DiscoverFile(string, int64) (string, error)                { return "", nil }
func (copilotAdapter) ParseNewUserMessages(string, Cursor) ([]UserMessage, Cursor, error) { return nil, Cursor{}, nil }
func (codexAdapter) DiscoverFile(string, int64) (string, error)                  { return "", nil }
func (codexAdapter) ParseNewUserMessages(string, Cursor) ([]UserMessage, Cursor, error) { return nil, Cursor{}, nil }
func (geminiAdapter) DiscoverFile(string, int64) (string, error)                 { return "", nil }
func (geminiAdapter) ParseNewUserMessages(string, Cursor) ([]UserMessage, Cursor, error) { return nil, Cursor{}, nil }
```

> NOTE: As each real adapter file is added (Tasks 4/6/7/9), delete that adapter's stub methods from `adapters_stub.go`. When all four are real, delete `adapters_stub.go` entirely.

- [ ] **Step 5: Run the test, verify it passes**

Run: `go test ./internal/ingest/ -run TestAdapterFor -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ingest/ingest.go internal/ingest/adapterfor.go internal/ingest/adapters_stub.go internal/ingest/adapterfor_test.go
git commit -m "feat(ingest): core types + AdapterFor (#65)"
```

---

## Task 2: Fingerprint store (self-injection suppression)

**Files:**
- Create: `internal/ingest/fingerprint.go`
- Test: `internal/ingest/fingerprint_test.go`

**Interfaces:**
- Produces: `normalizeFingerprint(s string) string`; `fingerprintStore` with `Add(text string)` and `Consume(text string) bool` (returns true if `text` matches a previously-added injection that still has remaining count, decrementing it).

- [ ] **Step 1: Write the failing test**

Create `internal/ingest/fingerprint_test.go`:

```go
package ingest

import "testing"

func TestFingerprintStore_ConsumeMatchesOnce(t *testing.T) {
	fs := newFingerprintStore()
	fs.Add("şu dosyayı düzelt")

	if !fs.Consume("şu dosyayı düzelt") {
		t.Fatal("first Consume of an added injection must match (suppress)")
	}
	if fs.Consume("şu dosyayı düzelt") {
		t.Fatal("second Consume must NOT match — the user genuinely retyping the same text is logged")
	}
}

func TestFingerprintStore_NormalizesWhitespaceAndPasteMarkers(t *testing.T) {
	fs := newFingerprintStore()
	fs.Add("merhaba dünya")
	// The CLI may store the message with a trailing newline / collapsed spaces /
	// stripped bracketed-paste markers — normalization must still match.
	if !fs.Consume("  merhaba   dünya\n") {
		t.Fatal("normalized variant of an injection must be suppressed")
	}
}

func TestFingerprintStore_UnrelatedNotConsumed(t *testing.T) {
	fs := newFingerprintStore()
	fs.Add("birinci mesaj")
	if fs.Consume("bambaşka bir mesaj") {
		t.Fatal("an unrelated (directly typed) message must NOT be suppressed")
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	cases := map[string]string{
		"  hello   world \n": "hello world",
		"a\x1b[200~b\x1b[201~c": "abc", // bracketed-paste markers stripped
		"line1\r\nline2":        "line1 line2",
	}
	for in, want := range cases {
		if got := normalizeFingerprint(in); got != want {
			t.Errorf("normalizeFingerprint(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/ingest/ -run 'Fingerprint|Normalize'`
Expected: build failure — `newFingerprintStore`, `normalizeFingerprint` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/ingest/fingerprint.go`:

```go
package ingest

import (
	"strings"
	"sync"
)

// fingerprintStore remembers texts the app itself injected into a PTY (startup
// prompt, broadcast, prompt-send) so the watcher can suppress the copy the CLI
// records in its session file — otherwise the app's own bootstrap/broadcast
// would be logged as if the user typed it (#65). Each injection is consumable
// once: if the user later genuinely retypes the same text, the second occurrence
// is NOT suppressed. Safe for concurrent use (app records, watcher consumes).
type fingerprintStore struct {
	mu     sync.Mutex
	counts map[string]int
}

func newFingerprintStore() *fingerprintStore {
	return &fingerprintStore{counts: make(map[string]int)}
}

// Add records one self-injected text.
func (f *fingerprintStore) Add(text string) {
	key := normalizeFingerprint(text)
	if key == "" {
		return
	}
	f.mu.Lock()
	f.counts[key]++
	f.mu.Unlock()
}

// Consume reports whether text matches a still-unconsumed injection, decrementing
// its count when it does.
func (f *fingerprintStore) Consume(text string) bool {
	key := normalizeFingerprint(text)
	if key == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts[key] > 0 {
		f.counts[key]--
		if f.counts[key] == 0 {
			delete(f.counts, key)
		}
		return true
	}
	return false
}

// normalizeFingerprint canonicalizes text so an injection matches the CLI's
// recorded copy despite reformatting: bracketed-paste markers are removed and all
// runs of whitespace collapse to single spaces, trimmed.
func normalizeFingerprint(s string) string {
	s = strings.ReplaceAll(s, "\x1b[200~", "")
	s = strings.ReplaceAll(s, "\x1b[201~", "")
	return strings.Join(strings.Fields(s), " ")
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./internal/ingest/ -run 'Fingerprint|Normalize' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/fingerprint.go internal/ingest/fingerprint_test.go
git commit -m "feat(ingest): self-injection fingerprint store (#65)"
```

---

## Task 3: Claude adapter — `ParseNewUserMessages`

**Files:**
- Create: `internal/ingest/adapter_claude.go`
- Test: `internal/ingest/adapter_claude_test.go`
- Modify: `internal/ingest/adapters_stub.go` (remove the `claudeAdapter` stub methods)

**Interfaces:**
- Consumes: `UserMessage`, `Cursor` (Task 1).
- Produces: `claudeAdapter.ParseNewUserMessages(path, cur) ([]UserMessage, Cursor, error)` — JSONL; emits only lines with `type=="user"` whose `message.content` is a plain string; advances `Cursor.Offset`.

- [ ] **Step 1: Write the failing test**

Create `internal/ingest/adapter_claude_test.go`:

```go
package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestClaudeParse_ExtractsOnlyHumanUserMessages(t *testing.T) {
	dir := t.TempDir()
	// A realistic Claude JSONL: a human user line (content is a STRING),
	// an assistant line (content is an array), and a tool_result line
	// (type "user" but content is an ARRAY of tool_result blocks — NOT human).
	jsonl := `{"type":"user","timestamp":"2026-06-23T10:00:00.000Z","message":{"role":"user","content":"merhaba claude"}}
{"type":"assistant","timestamp":"2026-06-23T10:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"selam"}]}}
{"type":"user","timestamp":"2026-06-23T10:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}
{"type":"user","timestamp":"2026-06-23T10:00:03.000Z","message":{"role":"user","content":"ikinci mesaj"}}
`
	p := writeFile(t, dir, "s.jsonl", jsonl)

	msgs, next, err := claudeAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (only string-content user lines): %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "merhaba claude" || msgs[1].Content != "ikinci mesaj" {
		t.Fatalf("contents = %q, %q", msgs[0].Content, msgs[1].Content)
	}
	if msgs[0].Timestamp != "2026-06-23T10:00:00.000Z" {
		t.Fatalf("timestamp = %q", msgs[0].Timestamp)
	}
	if next.Offset != int64(len(jsonl)) {
		t.Fatalf("next.Offset = %d, want %d (whole file consumed)", next.Offset, len(jsonl))
	}
}

func TestClaudeParse_ResumesFromCursorNoDuplicates(t *testing.T) {
	dir := t.TempDir()
	first := `{"type":"user","timestamp":"2026-06-23T10:00:00.000Z","message":{"role":"user","content":"bir"}}` + "\n"
	p := writeFile(t, dir, "s.jsonl", first)
	_, cur, err := claudeAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	// Append a second line and re-parse from the cursor — only the new line returns.
	appended := first + `{"type":"user","timestamp":"2026-06-23T10:00:05.000Z","message":{"role":"user","content":"iki"}}` + "\n"
	if err := os.WriteFile(p, []byte(appended), 0644); err != nil {
		t.Fatal(err)
	}
	msgs, _, err := claudeAdapter{}.ParseNewUserMessages(p, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "iki" {
		t.Fatalf("resume returned %+v, want only [iki]", msgs)
	}
}

func TestClaudeParse_SkipsCorruptLine(t *testing.T) {
	dir := t.TempDir()
	jsonl := `{"type":"user","timestamp":"t1","message":{"role":"user","content":"iyi"}}
{bozuk json
{"type":"user","timestamp":"t2","message":{"role":"user","content":"devam"}}
`
	p := writeFile(t, dir, "s.jsonl", jsonl)
	msgs, _, err := claudeAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatalf("a corrupt line must not fail the parse: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d, want 2 (corrupt line skipped)", len(msgs))
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/ingest/ -run TestClaudeParse`
Expected: build failure / FAIL — stub `claudeAdapter` returns nil.

- [ ] **Step 3: Write the implementation**

First remove the `claudeAdapter` stub methods from `internal/ingest/adapters_stub.go` (keep the other three). Then create `internal/ingest/adapter_claude.go`:

```go
package ingest

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// claudeAdapter ingests Claude Code's JSONL session file:
// ~/.claude/projects/{slug(cwd)}/{uuid}.jsonl. A human message is a line with
// type=="user" whose message.content is a plain STRING; assistant output uses a
// content array, and tool results arrive as type=="user" lines whose content is
// an ARRAY of blocks — both are skipped (#65).
type claudeAdapter struct{}

type claudeLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func (claudeAdapter) ParseNewUserMessages(path string, cur Cursor) ([]UserMessage, Cursor, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cur, nil
		}
		return nil, cur, err
	}
	defer f.Close()

	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return nil, cur, err
	}

	var out []UserMessage
	consumed := cur.Offset
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		consumed += int64(len(line)) + 1 // +1 for the newline scanner stripped
		if len(line) == 0 {
			continue
		}
		var cl claudeLine
		if json.Unmarshal(line, &cl) != nil {
			continue // skip a corrupt/partial line, keep the rest
		}
		if cl.Type != "user" {
			continue
		}
		var content string
		// Only a JSON string content is a human-typed message; an array is a
		// tool_result envelope, not human text.
		if json.Unmarshal(cl.Message.Content, &content) != nil {
			continue
		}
		if content == "" {
			continue
		}
		out = append(out, UserMessage{Content: content, Timestamp: cl.Timestamp})
	}
	if err := sc.Err(); err != nil {
		return out, Cursor{Offset: consumed}, err
	}
	return out, Cursor{Offset: consumed}, nil
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./internal/ingest/ -run TestClaudeParse -v`
Expected: PASS (all three subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/adapter_claude.go internal/ingest/adapter_claude_test.go internal/ingest/adapters_stub.go
git commit -m "feat(ingest): Claude adapter parse (#65)"
```

---

## Task 4: Claude adapter — `DiscoverFile`

**Files:**
- Modify: `internal/ingest/adapter_claude.go`
- Test: `internal/ingest/adapter_claude_test.go` (add)

**Interfaces:**
- Produces: `claudeAdapter.DiscoverFile(cwd, spawnedAtUnixNano)` — slugifies cwd to the `~/.claude/projects/{slug}` dir, returns the newest `*.jsonl` modified at/after spawn (minus a small skew), or "" if none.
- Produces (helper, reused by other adapters' tests): `claudeSlug(cwd string) string`.

- [ ] **Step 1: Write the failing test**

Add to `internal/ingest/adapter_claude_test.go`:

```go
func TestClaudeSlug(t *testing.T) {
	cases := map[string]string{
		"/Users/yerli/Developer/MAPEG/YtkService": "-Users-yerli-Developer-MAPEG-YtkService",
		"/a/b.c":                                   "-a-b-c", // dots become dashes too
	}
	for in, want := range cases {
		if got := claudeSlug(in); got != want {
			t.Errorf("claudeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaudeDiscover_PicksNewestAfterSpawn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/tmp/myrepo"
	dir := filepath.Join(home, ".claude", "projects", claudeSlug(cwd))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	old := writeFile(t, dir, "old.jsonl", "{}\n")
	newer := writeFile(t, dir, "new.jsonl", "{}\n")
	// Make 'old' clearly older than spawn, 'new' clearly newer.
	spawn := time.Now()
	past := spawn.Add(-time.Hour)
	future := spawn.Add(time.Second)
	os.Chtimes(old, past, past)
	os.Chtimes(newer, future, future)

	got, err := claudeAdapter{}.DiscoverFile(cwd, spawn.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if got != newer {
		t.Fatalf("DiscoverFile = %q, want %q (newest at/after spawn)", got, newer)
	}
}

func TestClaudeDiscover_MissingDirReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := claudeAdapter{}.DiscoverFile("/tmp/never", time.Now().UnixNano())
	if err != nil || got != "" {
		t.Fatalf("missing dir → (%q, %v), want (\"\", nil)", got, err)
	}
}
```

Add `"time"` to the test file's imports.

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/ingest/ -run TestClaudeDiscover`
Expected: build failure — `claudeSlug`, `DiscoverFile` (real) undefined.

- [ ] **Step 3: Write the implementation**

Add to `internal/ingest/adapter_claude.go` (and add imports `path/filepath`, `strings`, `time`):

```go
// claudeSlug turns a cwd into Claude's project folder name: every non-alphanumeric
// character becomes '-' (so a leading '/' yields a leading '-', and dots become
// dashes too). Matches Claude Code's path-slugify scheme.
func claudeSlug(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func (claudeAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".claude", "projects", claudeSlug(cwd))
	return newestJSONLAfter(dir, "*.jsonl", spawnedAtUnixNano)
}
```

Then create the shared discovery helper `internal/ingest/discover.go`:

```go
package ingest

import (
	"os"
	"path/filepath"
	"time"
)

// discoverSkew tolerates a small clock gap between the recorded spawn time and
// the session file's mtime (the CLI may create the file a moment before/after).
const discoverSkew = 5 * time.Second

// newestJSONLAfter returns the most-recently-modified file in dir matching glob
// whose modtime is at/after spawn (minus a small skew), or "" if none. A missing
// dir yields ("", nil).
func newestJSONLAfter(dir, glob string, spawnedAtUnixNano int64) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	cutoff := time.Unix(0, spawnedAtUnixNano).Add(-discoverSkew)
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ok, _ := filepath.Match(glob, e.Name()); !ok {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			best, bestMod = filepath.Join(dir, e.Name()), info.ModTime()
		}
	}
	return best, nil
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./internal/ingest/ -run 'TestClaudeDiscover|TestClaudeSlug' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/adapter_claude.go internal/ingest/discover.go internal/ingest/adapter_claude_test.go
git commit -m "feat(ingest): Claude session-file discovery (#65)"
```

---

## Task 5: Watcher (poll, tail, suppress, emit) + Manager lifecycle

**Files:**
- Create: `internal/ingest/watcher.go`
- Test: `internal/ingest/watcher_test.go`

**Interfaces:**
- Consumes: `SessionAdapter`, `Cursor`, `UserMessage`, `EmitFunc`, `fingerprintStore` (Tasks 1–2).
- Produces: `Manager` with `New() *Manager`; `(*Manager).StartSession(sessionID string, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, emit EmitFunc)`; `(*Manager).RecordInjection(sessionID, text string)`; `(*Manager).StopSession(sessionID string)`; `(*Manager).StopAll()`. Also `pollOnce(ad, path, cur, fp, emit) Cursor` as the testable single-tick core.

- [ ] **Step 1: Write the failing test**

Create `internal/ingest/watcher_test.go`:

```go
package ingest

import (
	"sync"
	"testing"
)

// fakeAdapter ignores cwd/spawn and parses a preset slice once, then nothing.
type fakeAdapter struct {
	batches [][]UserMessage // returned successively per pollOnce call
	calls   int
}

func (f *fakeAdapter) DiscoverFile(string, int64) (string, error) { return "fake", nil }
func (f *fakeAdapter) ParseNewUserMessages(_ string, cur Cursor) ([]UserMessage, Cursor, error) {
	i := f.calls
	f.calls++
	if i < len(f.batches) {
		return f.batches[i], Cursor{Count: cur.Count + len(f.batches[i])}, nil
	}
	return nil, cur, nil
}

func TestPollOnce_EmitsUnsuppressedSuppressesInjections(t *testing.T) {
	ad := &fakeAdapter{batches: [][]UserMessage{{
		{Content: "startup prompt metni", Timestamp: "t0"},
		{Content: "kullanıcı elle yazdı", Timestamp: "t1"},
	}}}
	fp := newFingerprintStore()
	fp.Add("startup prompt metni") // the app injected this — must be suppressed

	var mu sync.Mutex
	var emitted []string
	emit := func(content, ts string) {
		mu.Lock()
		emitted = append(emitted, content)
		mu.Unlock()
	}

	pollOnce(ad, "fake", Cursor{}, fp, emit)

	if len(emitted) != 1 || emitted[0] != "kullanıcı elle yazdı" {
		t.Fatalf("emitted = %v, want only the directly-typed message", emitted)
	}
}

func TestPollOnce_AdvancesCursor(t *testing.T) {
	ad := &fakeAdapter{batches: [][]UserMessage{
		{{Content: "bir", Timestamp: "t1"}},
		{{Content: "iki", Timestamp: "t2"}},
	}}
	fp := newFingerprintStore()
	var got []string
	emit := func(c, _ string) { got = append(got, c) }

	cur := pollOnce(ad, "fake", Cursor{}, fp, emit)
	cur = pollOnce(ad, "fake", cur, fp, emit)

	if len(got) != 2 || got[0] != "bir" || got[1] != "iki" {
		t.Fatalf("got %v, want [bir iki] across two polls (cursor advanced)", got)
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/ingest/ -run TestPollOnce`
Expected: build failure — `pollOnce` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/ingest/watcher.go`:

```go
package ingest

import (
	"log"
	"sync"
	"time"
)

// pollInterval is how often a watcher re-reads its session file. 700ms balances
// log latency against churn; the CLI append is durable so nothing is lost.
const pollInterval = 700 * time.Millisecond

// pollOnce performs one ingest tick: parse new user messages from path starting
// at cur, suppress any that match a recorded self-injection, emit the rest, and
// return the advanced cursor. The single testable unit of a watcher.
func pollOnce(ad SessionAdapter, path string, cur Cursor, fp *fingerprintStore, emit EmitFunc) Cursor {
	msgs, next, err := ad.ParseNewUserMessages(path, cur)
	if err != nil {
		log.Printf("[INGEST] parse error (%s): %v", path, err)
		// next is still advanced past the bad region by the adapter; keep it.
	}
	for _, m := range msgs {
		if fp.Consume(m.Content) {
			continue // app's own injection (startup/broadcast/prompt-send)
		}
		emit(m.Content, m.Timestamp)
	}
	return next
}

// session is one terminal's running watcher.
type session struct {
	cancel chan struct{}
	fp     *fingerprintStore
}

// Manager owns one watcher per AI terminal and the per-terminal fingerprint
// stores. Safe for concurrent use from the app (StartSession/RecordInjection/
// StopSession run on the Wails/event goroutines).
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*session
}

func New() *Manager {
	return &Manager{sessions: make(map[string]*session)}
}

// StartSession begins watching the CLI session file for a terminal. The watcher
// discovers the file (retrying until it appears), then polls it on an interval.
// A duplicate sessionID is ignored (idempotent).
func (m *Manager) StartSession(sessionID string, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, emit EmitFunc) {
	if ad == nil || sessionID == "" {
		return
	}
	m.mu.Lock()
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		return
	}
	s := &session{cancel: make(chan struct{}), fp: newFingerprintStore()}
	m.sessions[sessionID] = s
	m.mu.Unlock()

	go m.run(s, ad, cwd, spawnedAtUnixNano, emit)
}

func (m *Manager) run(s *session, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, emit EmitFunc) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var path string
	var cur Cursor
	for {
		select {
		case <-s.cancel:
			return
		case <-ticker.C:
			if path == "" {
				p, err := ad.DiscoverFile(cwd, spawnedAtUnixNano)
				if err != nil {
					log.Printf("[INGEST] discover error: %v", err)
					continue
				}
				if p == "" {
					continue // not created yet — keep waiting
				}
				path = p
			}
			cur = pollOnce(ad, path, cur, s.fp, emit)
		}
	}
}

// RecordInjection notes that the app injected text into this terminal's PTY, so
// the watcher suppresses the CLI's recorded copy. No-op for an unknown session.
func (m *Manager) RecordInjection(sessionID, text string) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s != nil {
		s.fp.Add(text)
	}
}

// StopSession stops and forgets a terminal's watcher.
func (m *Manager) StopSession(sessionID string) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	if s != nil {
		close(s.cancel)
	}
}

// StopAll stops every watcher (app shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := m.sessions
	m.sessions = make(map[string]*session)
	m.mu.Unlock()
	for _, s := range all {
		close(s.cancel)
	}
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./internal/ingest/ -run TestPollOnce -v`
Expected: PASS.

- [ ] **Step 5: Run the whole package + vet**

Run: `go test ./internal/ingest/ && go vet ./internal/ingest/`
Expected: PASS, no vet output.

- [ ] **Step 6: Commit**

```bash
git add internal/ingest/watcher.go internal/ingest/watcher_test.go
git commit -m "feat(ingest): watcher poll/suppress/emit + Manager lifecycle (#65)"
```

---

## Task 6: Copilot adapter

**Files:**
- Create: `internal/ingest/adapter_copilot.go`
- Test: `internal/ingest/adapter_copilot_test.go`
- Modify: `internal/ingest/adapters_stub.go` (remove `copilotAdapter` stubs)

**Interfaces:**
- Produces: `copilotAdapter` implementing `SessionAdapter`. Parses `~/.copilot/session-state/{uuid}/events.jsonl`; a human message is a line `type=="user.message"` with `data.content` (NOT `transformedContent`). Discovery: the newest `events.jsonl` under any `{uuid}/` dir created at/after spawn.

- [ ] **Step 1: Write the failing test**

Create `internal/ingest/adapter_copilot_test.go`:

```go
package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCopilotParse_ExtractsRawUserContent(t *testing.T) {
	dir := t.TempDir()
	jsonl := `{"type":"session.start","timestamp":"2026-06-22T06:00:00.000Z","data":{}}
{"type":"user.message","timestamp":"2026-06-22T06:14:51.587Z","data":{"content":"review my PRs","transformedContent":"<reminder>review my PRs"}}
{"type":"assistant.message","timestamp":"2026-06-22T06:14:55.000Z","data":{"content":"sure"}}
`
	p := writeFile(t, dir, "events.jsonl", jsonl)
	msgs, _, err := copilotAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "review my PRs" {
		t.Fatalf("got %+v, want one msg with raw data.content (not transformedContent)", msgs)
	}
	if msgs[0].Timestamp != "2026-06-22T06:14:51.587Z" {
		t.Fatalf("timestamp = %q", msgs[0].Timestamp)
	}
}

func TestCopilotDiscover_PicksNewestEventsFileAfterSpawn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Join(home, ".copilot", "session-state")
	d1 := filepath.Join(base, "uuid-old")
	d2 := filepath.Join(base, "uuid-new")
	os.MkdirAll(d1, 0755)
	os.MkdirAll(d2, 0755)
	f1 := writeFile(t, d1, "events.jsonl", "{}\n")
	f2 := writeFile(t, d2, "events.jsonl", "{}\n")
	spawn := time.Now()
	os.Chtimes(f1, spawn.Add(-time.Hour), spawn.Add(-time.Hour))
	os.Chtimes(f2, spawn.Add(time.Second), spawn.Add(time.Second))

	got, err := copilotAdapter{}.DiscoverFile("/anything", spawn.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if got != f2 {
		t.Fatalf("DiscoverFile = %q, want %q", got, f2)
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/ingest/ -run TestCopilot`
Expected: FAIL / build error (stub copilotAdapter).

- [ ] **Step 3: Write the implementation**

Remove `copilotAdapter` stub methods from `adapters_stub.go`. Create `internal/ingest/adapter_copilot.go`:

```go
package ingest

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// copilotAdapter ingests GitHub Copilot CLI's JSONL transcript:
// ~/.copilot/session-state/{uuid}/events.jsonl. A human message is a line with
// type=="user.message"; data.content is the verbatim typed text (data.
// transformedContent is the model-facing augmented version — NOT used) (#65).
type copilotAdapter struct{}

type copilotLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Data      struct {
		Content string `json:"content"`
	} `json:"data"`
}

func (copilotAdapter) ParseNewUserMessages(path string, cur Cursor) ([]UserMessage, Cursor, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cur, nil
		}
		return nil, cur, err
	}
	defer f.Close()
	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return nil, cur, err
	}
	var out []UserMessage
	consumed := cur.Offset
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		consumed += int64(len(line)) + 1
		if len(line) == 0 {
			continue
		}
		var cl copilotLine
		if json.Unmarshal(line, &cl) != nil {
			continue
		}
		if cl.Type != "user.message" || cl.Data.Content == "" {
			continue
		}
		out = append(out, UserMessage{Content: cl.Data.Content, Timestamp: cl.Timestamp})
	}
	if err := sc.Err(); err != nil {
		return out, Cursor{Offset: consumed}, err
	}
	return out, Cursor{Offset: consumed}, nil
}

func (copilotAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".copilot", "session-state")
	dirs, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	// Each session is a {uuid}/ dir; pick the newest events.jsonl at/after spawn.
	var best string
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		p, derr := newestJSONLAfter(filepath.Join(base, d.Name()), "events.jsonl", spawnedAtUnixNano)
		if derr != nil || p == "" {
			continue
		}
		if best == "" {
			best = p
			continue
		}
		bi, _ := os.Stat(best)
		pi, _ := os.Stat(p)
		if bi != nil && pi != nil && pi.ModTime().After(bi.ModTime()) {
			best = p
		}
	}
	return best, nil
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./internal/ingest/ -run TestCopilot -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/adapter_copilot.go internal/ingest/adapter_copilot_test.go internal/ingest/adapters_stub.go
git commit -m "feat(ingest): Copilot adapter (#65)"
```

---

## Task 7: Codex adapter (version-gated)

**Files:**
- Create: `internal/ingest/adapter_codex.go`
- Test: `internal/ingest/adapter_codex_test.go`
- Modify: `internal/ingest/adapters_stub.go` (remove `codexAdapter` stubs)

**Interfaces:**
- Produces: `codexAdapter` implementing `SessionAdapter`. Parses `~/.codex/sessions/Y/M/D/rollout-*.jsonl`. New format (2026): a human message is `type=="event_msg"` with `payload.type=="user_message"` and `payload.message`; the `response_item` copy of the same turn is IGNORED (dedup). Old format (2025): `type=="message"` with `role=="user"` and string `content`. Discovery: newest `rollout-*.jsonl` under today's `Y/M/D` dir at/after spawn (also checks yesterday for around-midnight spawns).

- [ ] **Step 1: Write the failing test**

Create `internal/ingest/adapter_codex_test.go`:

```go
package ingest

import "testing"

func TestCodexParse_NewFormatUsesEventMsgOnly(t *testing.T) {
	dir := t.TempDir()
	// New (2026) format: the user turn appears as BOTH an event_msg/user_message
	// AND a response_item/message(role:user). Only the event_msg counts (dedup).
	jsonl := `{"timestamp":"2026-06-23T17:00:00.000Z","type":"session_meta","payload":{"cwd":"/x","cli_version":"0.142.0"}}
{"timestamp":"2026-06-23T17:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"build the thing"}}
{"timestamp":"2026-06-23T17:00:05.001Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"build the thing"}]}}
{"timestamp":"2026-06-23T17:00:09.000Z","type":"event_msg","payload":{"type":"agent_message","message":"done"}}
`
	p := writeFile(t, dir, "rollout-x.jsonl", jsonl)
	msgs, _, err := codexAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "build the thing" {
		t.Fatalf("got %+v, want exactly one user_message (no response_item dup)", msgs)
	}
}

func TestCodexParse_OldFormatMessageRoleUser(t *testing.T) {
	dir := t.TempDir()
	jsonl := `{"type":"message","role":"user","content":"eski format mesaj"}
{"type":"message","role":"assistant","content":"cevap"}
`
	p := writeFile(t, dir, "rollout-y.jsonl", jsonl)
	msgs, _, err := codexAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "eski format mesaj" {
		t.Fatalf("got %+v, want the old-format user message", msgs)
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/ingest/ -run TestCodexParse`
Expected: FAIL / build error.

- [ ] **Step 3: Write the implementation**

Remove `codexAdapter` stubs. Create `internal/ingest/adapter_codex.go`:

```go
package ingest

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// codexAdapter ingests OpenAI Codex CLI's "rollout" JSONL:
// ~/.codex/sessions/Y/M/D/rollout-{ts}-{uuid}.jsonl. Two schemas exist (#65):
//   - new (2026): {timestamp,type,payload}; human msg = type"event_msg" +
//     payload.type"user_message" + payload.message. The same turn also appears as
//     a response_item — ignored to avoid double-counting.
//   - old (2025): {type"message",role"user",content:"..."} (string content).
type codexAdapter struct{}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Role      string          `json:"role"`           // old format
	Content   json.RawMessage `json:"content"`        // old format (string)
	Payload   struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"payload"` // new format
}

func (codexAdapter) ParseNewUserMessages(path string, cur Cursor) ([]UserMessage, Cursor, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cur, nil
		}
		return nil, cur, err
	}
	defer f.Close()
	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return nil, cur, err
	}
	var out []UserMessage
	consumed := cur.Offset
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		consumed += int64(len(line)) + 1
		if len(line) == 0 {
			continue
		}
		var cl codexLine
		if json.Unmarshal(line, &cl) != nil {
			continue
		}
		switch {
		case cl.Type == "event_msg" && cl.Payload.Type == "user_message" && cl.Payload.Message != "":
			out = append(out, UserMessage{Content: cl.Payload.Message, Timestamp: cl.Timestamp})
		case cl.Type == "message" && cl.Role == "user":
			var s string
			if json.Unmarshal(cl.Content, &s) == nil && s != "" {
				out = append(out, UserMessage{Content: s, Timestamp: cl.Timestamp})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return out, Cursor{Offset: consumed}, err
	}
	return out, Cursor{Offset: consumed}, nil
}

func (codexAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".codex", "sessions")
	spawn := time.Unix(0, spawnedAtUnixNano)
	// Check the spawn day and the previous day (around-midnight spawns).
	var best string
	var bestMod time.Time
	for _, day := range []time.Time{spawn, spawn.Add(-24 * time.Hour)} {
		dir := filepath.Join(base, day.Format("2006"), day.Format("01"), day.Format("02"))
		p, derr := newestJSONLAfter(dir, "rollout-*.jsonl", spawnedAtUnixNano)
		if derr != nil || p == "" {
			continue
		}
		info, _ := os.Stat(p)
		if best == "" || (info != nil && info.ModTime().After(bestMod)) {
			best = p
			if info != nil {
				bestMod = info.ModTime()
			}
		}
	}
	return best, nil
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./internal/ingest/ -run TestCodexParse -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/adapter_codex.go internal/ingest/adapter_codex_test.go internal/ingest/adapters_stub.go
git commit -m "feat(ingest): Codex adapter, version-gated (#65)"
```

---

## Task 8: Gemini adapter (monolithic JSON)

**Files:**
- Create: `internal/ingest/adapter_gemini.go`
- Test: `internal/ingest/adapter_gemini_test.go`
- Modify: delete `internal/ingest/adapters_stub.go` (last stub removed)

**Interfaces:**
- Produces: `geminiAdapter` implementing `SessionAdapter`. Parses `~/.gemini/tmp/{sha256(cwd)}/chats/session-*.json` — a SINGLE JSON object `{messages:[{type,timestamp,content:[{text}]}]}`; human msg = `type=="user"`, content = concatenated `content[].text`. Cursor uses `Count` (messages already emitted) since the file is re-parsed wholesale. Discovery uses sha256-hex of cwd.

- [ ] **Step 1: Write the failing test**

Create `internal/ingest/adapter_gemini_test.go`:

```go
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGeminiParse_UserMessagesWithCountCursor(t *testing.T) {
	dir := t.TempDir()
	obj := `{"sessionId":"s","messages":[
{"id":1,"timestamp":"2026-02-17T12:01:19.989Z","type":"user","content":[{"text":"merhaba "},{"text":"gemini"}]},
{"id":2,"timestamp":"2026-02-17T12:01:25.000Z","type":"gemini","content":[{"text":"selam"}]},
{"id":3,"timestamp":"2026-02-17T12:02:00.000Z","type":"user","content":[{"text":"ikinci"}]}
]}`
	p := writeFile(t, dir, "session-x.json", obj)

	msgs, cur, err := geminiAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Content != "merhaba gemini" || msgs[1].Content != "ikinci" {
		t.Fatalf("got %+v, want [merhaba gemini, ikinci] (gemini-type skipped, text joined)", msgs)
	}
	// Re-parse with the returned cursor — nothing new.
	again, _, _ := geminiAdapter{}.ParseNewUserMessages(p, cur)
	if len(again) != 0 {
		t.Fatalf("re-parse with cursor returned %+v, want none", again)
	}
}

func TestGeminiDiscover_Sha256Folder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/tmp/myrepo"
	sum := sha256.Sum256([]byte(cwd))
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(home, ".gemini", "tmp", hash, "chats")
	os.MkdirAll(dir, 0755)
	f := writeFile(t, dir, "session-x.json", "{}")
	spawn := time.Now()
	os.Chtimes(f, spawn.Add(time.Second), spawn.Add(time.Second))

	got, err := geminiAdapter{}.DiscoverFile(cwd, spawn.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if got != f {
		t.Fatalf("DiscoverFile = %q, want %q", got, f)
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/ingest/ -run TestGemini`
Expected: FAIL / build error.

- [ ] **Step 3: Write the implementation**

Delete `internal/ingest/adapters_stub.go` (its last stubs go now). Create `internal/ingest/adapter_gemini.go`:

```go
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// geminiAdapter ingests Gemini CLI's monolithic JSON chat record:
// ~/.gemini/tmp/{sha256(cwd)}/chats/session-*.json — a single object
// {messages:[{type,timestamp,content:[{text}]}]}. type=="user" is a human
// message; content[].text is concatenated. Because it is one JSON object (not
// JSONL) the whole file is re-parsed each tick; Cursor.Count tracks how many user
// messages were already emitted so a re-parse only yields new ones (#65).
type geminiAdapter struct{}

type geminiFile struct {
	Messages []struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Content   []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
}

func (geminiAdapter) ParseNewUserMessages(path string, cur Cursor) ([]UserMessage, Cursor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cur, nil
		}
		return nil, cur, err
	}
	var gf geminiFile
	if json.Unmarshal(data, &gf) != nil {
		// Partial write mid-append — skip this tick, keep the cursor.
		return nil, cur, nil
	}
	var all []UserMessage
	for _, m := range gf.Messages {
		if m.Type != "user" {
			continue
		}
		var text string
		for _, c := range m.Content {
			text += c.Text
		}
		if text == "" {
			continue
		}
		all = append(all, UserMessage{Content: text, Timestamp: m.Timestamp})
	}
	if cur.Count >= len(all) {
		return nil, cur, nil
	}
	fresh := all[cur.Count:]
	return fresh, Cursor{Count: len(all)}, nil
}

func (geminiAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(cwd))
	dir := filepath.Join(home, ".gemini", "tmp", hex.EncodeToString(sum[:]), "chats")
	return newestJSONLAfter(dir, "session-*.json", spawnedAtUnixNano)
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./internal/ingest/ -run TestGemini -v`
Expected: PASS.

- [ ] **Step 5: Run the whole ingest package**

Run: `go test ./internal/ingest/ && go vet ./internal/ingest/ && gofmt -l internal/ingest/`
Expected: PASS, no vet output, no gofmt output.

- [ ] **Step 6: Commit**

```bash
git add internal/ingest/adapter_gemini.go internal/ingest/adapter_gemini_test.go
git rm internal/ingest/adapters_stub.go
git commit -m "feat(ingest): Gemini adapter (monolithic JSON) (#65)"
```

---

## Task 9: Wire the Manager into `app.go`

> **PREREQUISITE:** This branch must be on top of #58 (PR #64) so `hubClient.LogMessage` has the `(room, to, content, timestamp)` signature. Verify before starting: `grep -n "func (c \*HubClient) LogMessage" internal/hubclient/client.go` should show 4 params. If it shows 3, rebase onto `feat/post29-edgecases-58` or merge #64 first.

**Files:**
- Modify: `app.go` — add the `*ingest.Manager` field + init; start/stop watchers; record injections.
- Test: `app_ingest_test.go` (new) — fingerprint-recording + non-AI/observer skip at the wiring boundary.

**Interfaces:**
- Consumes: `ingest.New()`, `ingest.AdapterFor`, `(*Manager).StartSession/RecordInjection/StopSession/StopAll` (Tasks 1, 5); existing `isAICLIType`, `isObserverAgent`, `logRoomForSession`/`roomForTeam`, `hubClient.LogMessage(room,to,content,ts)` (#58).

- [ ] **Step 1: Add the Manager field and initialize it**

In `app.go`, add to the `App` struct (near `ptyManager`):

```go
	ingestMgr *ingest.Manager
```

Add the import `"desktop/internal/ingest"`. In `startup()` (where `ptyManager` / `orchestrator` are initialized), add:

```go
	a.ingestMgr = ingest.New()
```

In `shutdown()` add (before closing PTYs):

```go
	if a.ingestMgr != nil {
		a.ingestMgr.StopAll()
	}
```

- [ ] **Step 2: Start a watcher when an AI, non-observer terminal is created**

In `CreateTerminal`, AFTER `a.ptyManager.Create(...)` succeeds and `sessionID` is known, add:

```go
	// #65: ingest the CLI's own session file so messages the user types DIRECTLY
	// into the terminal are logged to the room transcript. Skip non-AI shells and
	// observers (their terminal is a private drafting space, #17).
	if ad := ingest.AdapterFor(cliType); ad != nil && !isObserver {
		room := teamName
		agent := agentName
		spawnedAt := time.Now().UnixNano()
		a.ingestMgr.StartSession(sessionID, ad, workDir, spawnedAt, func(content, ts string) {
			if client := a.hubClient.Load(); client != nil {
				if err := client.LogMessage(room, agent, content, ts); err != nil {
					log.Printf("[INGEST] mesaj loglanamadı (agent=%s): %v", agent, err)
				}
			}
		})
	}
```

(`workDir` here is the final PTY working directory — the worktree dir if one was set up, matching where the CLI actually runs and writes its session file.)

- [ ] **Step 3: Stop the watcher on close/restart**

In `closeTerminalInternal` (the function that tears a session down), add near the top (after resolving the session):

```go
	if a.ingestMgr != nil {
		a.ingestMgr.StopSession(sessionID)
	}
```

(`RestartTerminal` already calls `closeTerminalInternal` then `CreateTerminal`, so the old watcher stops and a fresh one starts against the new session file — no extra code needed.)

- [ ] **Step 4: Record injections at the three injection points**

The app injects text the CLI will record as a `user` message; record each so the watcher suppresses the duplicate.

(a) **Startup prompt** — in `CreateTerminal`/`sendStartupPrompt` where the composed startup prompt is injected into the PTY, after the watcher is started add:

```go
	a.ingestMgr.RecordInjection(sessionID, composedStartupPrompt)
```

(Use the exact composed prompt string that is written to the PTY. For the Copilot `-i` path the prompt is a launch arg, not typed input — Copilot still records it as the first user message, so record it there too.)

(b) **Broadcast** — in `BroadcastToTeam`, after a successful submitted delivery, for each AI session that received it:

```go
	for _, s := range sessions {
		if isAICLIType(s.CLIType) {
			a.ingestMgr.RecordInjection(s.ID, text)
		}
	}
```

(c) **Prompt-send** — in `SendPromptToAgent`, after the write:

```go
	a.ingestMgr.RecordInjection(sessionID, rendered)
```

> NOTE on `logUserPrompt`: KEEP the existing `logUserPrompt` calls in `BroadcastToTeam` and `SendPromptToAgent` — they log those messages immediately and reliably. RecordInjection ensures ingestion then SKIPS the CLI's recorded copy, so each is logged exactly once. The startup prompt is recorded but NOT passed to `logUserPrompt` (it never was), so it is suppressed entirely.

- [ ] **Step 5: Write the wiring test**

Create `app_ingest_test.go`:

```go
package main

import (
	"testing"

	"desktop/internal/ingest"
)

// AdapterFor gates which terminals get ingested; the wiring must only start
// watchers for AI CLIs. This guards the CreateTerminal condition.
func TestIngestAdapterForGate(t *testing.T) {
	if ingest.AdapterFor("claude") == nil {
		t.Error("claude must be ingestable")
	}
	if ingest.AdapterFor("shell") != nil {
		t.Error("shell must NOT be ingestable")
	}
}

// RecordInjection then StopSession on an unknown/expired session must not panic
// (defensive: close races with a late inject).
func TestManagerRecordInjectionUnknownSessionSafe(t *testing.T) {
	m := ingest.New()
	m.RecordInjection("ghost", "x") // no such session — must be a safe no-op
	m.StopSession("ghost")
	m.StopAll()
}
```

- [ ] **Step 6: Run tests + build**

Run:
```bash
go test ./internal/ingest/ ./... 2>&1 | tail -20
go vet ./...
make mcp-server && go build ./...
```
Expected: all PASS, no vet output, build succeeds.

- [ ] **Step 7: Commit**

```bash
git add app.go app_ingest_test.go
git commit -m "feat(ingest): wire Manager into app — start/stop watchers + record injections (#65)"
```

---

## Task 10: End-to-end native verification + docs

**Files:**
- Modify: `docs/TODO.md` (mark direct-typing capture done / cross-reference #65)

- [ ] **Step 1: Native end-to-end test (all four CLIs available locally)**

```bash
make dev
```
For each CLI you have installed (claude, copilot, codex, gemini): create a terminal, type a message DIRECTLY into the xterm, press Enter. Then open the room transcript / generate a summary and confirm the typed message appears as a `user_prompt`. Also confirm:
- The startup prompt does NOT appear as a user message.
- A broadcast / prompt-send appears exactly ONCE (not duplicated by ingestion).
- An observer terminal's typed messages do NOT appear.

- [ ] **Step 2: Update docs**

In `docs/TODO.md`, under the session/persistence area, add a line noting direct-terminal-typing capture is implemented via `internal/ingest` (#65), distinct from CLI resume (#40).

- [ ] **Step 3: Final full verification**

Run:
```bash
go test ./... && go vet ./... && gofmt -l . && make mcp-server && go build ./...
```
Expected: all clean.

- [ ] **Step 4: Commit + push + PR**

```bash
git add docs/TODO.md
git commit -m "docs: note direct-typing capture via internal/ingest (#65)"
git push -u origin feat/session-ingestion
gh pr create --repo mytsx/agent-chat --base main \
  --title "feat: CLI session ingestion — terminale yazılan mesajları yakala (#65)" \
  --body "Closes #65. Terminal=ayna (değişmez); internal/ingest 4 CLI adapter + watcher + self-injection fingerprint. Detay: docs/superpowers/plans/2026-06-23-cli-session-ingestion.md"
```

---

## Self-Review (completed during planning)

- **Spec coverage:** terminal-untouched (Tasks: no xterm change), internal/ingest + adapters (1,3,4,6,7,8), watcher (5), self-injection fingerprint (2 + wiring 9.4), app wiring/lifecycle (9), observer-skip (9.2), version-gate/graceful-skip (3,7 + parse error handling), M0 de-risk (0), 4-CLI scope (3-8), testing (every task), #58 dependency (Global Constraints + Task 9 prereq). All covered.
- **Placeholder scan:** no TBD/“handle errors”/“similar to” — every code step has real code.
- **Type consistency:** `SessionAdapter`/`Cursor`/`UserMessage`/`EmitFunc`/`Manager` names and signatures consistent across Tasks 1, 5, 9; `newestJSONLAfter`, `claudeSlug`, `normalizeFingerprint`, `pollOnce` used consistently where introduced.
