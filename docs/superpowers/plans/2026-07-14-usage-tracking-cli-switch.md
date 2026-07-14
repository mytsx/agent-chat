# Usage Takibi + CLI Geçişi Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Her AI agent terminali için CLI session dosyasından pasif usage/limit sinyali topla, panelde göster, ve limite yaklaşınca (kullanıcı onaylı) agent'ı başka bir CLI'a devret.

**Architecture:** Yeni saf `internal/usage` leaf paketi (`Snapshot`/`Evaluate`) + mevcut `internal/ingest` watcher'ına `ParseUsage`/`onUsage` piggyback + `app.go`'da Wails `usage:updated` event, `SwitchTerminal`, eşik ayarları + `cli/startup.go`'da handoff segmenti + `frontend`'de `useUsage` store, rozet, `UsagePanelModal`, geçiş diyaloğu. Hub'a ve `prompts/` embed'ine dokunulmaz.

**Tech Stack:** Go (std-only), Wails v2, React 18 + TypeScript + Zustand, xterm.js.

## Global Constraints

- Build daima `GOFLAGS=-mod=readonly` ile (lokal wails CLI go.mod'u 2.12→2.11 düşürür); `go.mod` diff'siz kalmalı.
- Sıfır yeni runtime bağımlılığı (Go std + mevcut ingest); yeni Go/npm paketi yok.
- Hub protokolüne / `internal/hub` / `internal/hubclient`'a DOKUNULMAZ.
- `prompts/*.md` embed dizinine DOKUNULMAZ; handoff/uyarı metinleri kod içinde.
- Usage parse mevcut ingest poll tick'ine piggyback — ayrı goroutine/poll YOK.
- Agent-facing metinler Türkçe + emoji.
- Mevcut testler silinmez/gevşetilmez/skip'lenmez.
- Doğrulama kapısı (her backend task sonunda ilgili paket; PR öncesi tümü):
  `GOFLAGS=-mod=readonly go build ./... && GOFLAGS=-mod=readonly go test ./... && GOFLAGS=-mod=readonly go vet ./... && gofmt -l .`
- Codex `rate_limits` gerçek şeması: `type=="event_msg"` & `payload.type=="token_count"` satırında `payload.rate_limits.{primary,secondary}` (`used_percent`,`window_minutes`,`resets_at`) + `payload.rate_limits.plan_type`; `secondary` genelde `null`. Model: `payload.type=="thread_settings_applied"` → `payload.thread_settings.model`. Token: `payload.info.total_token_usage`.

---

### Task 1: `internal/usage` — tipler + `Evaluate` (saf leaf paket)

**Files:**
- Create: `internal/usage/usage.go`
- Test: `internal/usage/usage_test.go`

**Interfaces:**
- Consumes: (yok — leaf, yalnız std)
- Produces:
  - `type Kind int` sabitleri `KindNone=0`, `KindPercentLimit`, `KindTokenCount`
  - `type Window struct { UsedPercent float64; WindowMinutes int; ResetsAt int64 }` (json: `usedPercent`,`windowMinutes`,`resetsAt`)
  - `type Snapshot struct { SessionID, CLI string; Kind Kind; Primary, Secondary *Window; PlanType string; InputTokens, OutputTokens, CacheTokens int64; Model string; UpdatedAt int64 }`
  - `type Thresholds struct { WarnPercent, CriticalPercent float64 }`
  - `type Status int` sabitleri `StatusUnknown=0`, `StatusOK`, `StatusWarn`, `StatusCritical`
  - `func DefaultThresholds() Thresholds` → `{85, 95}`
  - `func (t Thresholds) Normalized() Thresholds` (0/negatif/aralık-dışı → default)
  - `func MaxUsedPercent(s Snapshot) (float64, bool)` — nil-slot güvenli, ikisi de nil → `(0,false)`
  - `func Evaluate(s Snapshot, t Thresholds) Status`

- [ ] **Step 1: Write the failing test**

```go
package usage

import "testing"

func w(p float64) *Window { return &Window{UsedPercent: p, WindowMinutes: 10080, ResetsAt: 1} }

func TestEvaluate(t *testing.T) {
	def := DefaultThresholds()
	tests := []struct {
		name string
		s    Snapshot
		th   Thresholds
		want Status
	}{
		{"nil-both-percentkind", Snapshot{Kind: KindPercentLimit}, def, StatusUnknown},
		{"primary-ok", Snapshot{Kind: KindPercentLimit, Primary: w(10)}, def, StatusOK},
		{"primary-warn-boundary", Snapshot{Kind: KindPercentLimit, Primary: w(85)}, def, StatusWarn},
		{"primary-just-below-warn", Snapshot{Kind: KindPercentLimit, Primary: w(84.9)}, def, StatusOK},
		{"primary-critical-boundary", Snapshot{Kind: KindPercentLimit, Primary: w(95)}, def, StatusCritical},
		{"secondary-drives-max", Snapshot{Kind: KindPercentLimit, Primary: w(10), Secondary: w(96)}, def, StatusCritical},
		{"tokencount-always-unknown", Snapshot{Kind: KindTokenCount, InputTokens: 999999}, def, StatusUnknown},
		{"none-kind-unknown", Snapshot{Kind: KindNone}, def, StatusUnknown},
		{"zero-thresholds-fall-back-to-default", Snapshot{Kind: KindPercentLimit, Primary: w(90)}, Thresholds{}, StatusWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Evaluate(tt.s, tt.th); got != tt.want {
				t.Fatalf("Evaluate(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestMaxUsedPercent(t *testing.T) {
	if _, ok := MaxUsedPercent(Snapshot{Kind: KindPercentLimit}); ok {
		t.Fatal("nil slotlarda ok=true beklenmiyordu")
	}
	if v, ok := MaxUsedPercent(Snapshot{Primary: w(30), Secondary: w(70)}); !ok || v != 70 {
		t.Fatalf("max = %v ok=%v, want 70 true", v, ok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOFLAGS=-mod=readonly go test ./internal/usage/ -run TestEvaluate -v`
Expected: FAIL — `undefined: Snapshot` / paket derlenmez.

- [ ] **Step 3: Write minimal implementation**

```go
// Package usage models per-CLI usage/limit signals extracted from CLI session
// files and evaluates them against warn/critical thresholds. Pure and std-only
// (leaf, like internal/sanitize) so it is CLI-agnostic and fully unit-testable.
package usage

// Kind classifies the usage signal a Snapshot carries.
type Kind int

const (
	KindNone Kind = iota // no usable signal (badge hidden / "—")
	// KindPercentLimit: authoritative used-percent + reset (Codex). Drives color
	// status and auto-switch.
	KindPercentLimit
	// KindTokenCount: consumption only, no denominator (Claude/Copilot/Gemini).
	// Display-only — never colored, never triggers auto-switch.
	KindTokenCount
)

// Window is one Codex rate-limit window. WindowMinutes identifies it (10080 =
// weekly, 300 = 5h, etc.); NO semantic meaning is attached to which slot
// (primary/secondary) a window occupies — the CLI orders them arbitrarily and
// the secondary slot is frequently null.
type Window struct {
	UsedPercent   float64 `json:"usedPercent"`
	WindowMinutes int     `json:"windowMinutes"`
	ResetsAt      int64   `json:"resetsAt"` // epoch seconds; 0 = unknown
}

// Snapshot is one terminal's latest usage reading. Primary/Secondary are non-nil
// only for KindPercentLimit (Codex); token fields carry consumption for every CLI.
type Snapshot struct {
	SessionID    string  `json:"sessionID"`
	CLI          string  `json:"cli"`
	Kind         Kind    `json:"kind"`
	Primary      *Window `json:"primary,omitempty"`
	Secondary    *Window `json:"secondary,omitempty"`
	PlanType     string  `json:"planType,omitempty"`
	InputTokens  int64   `json:"inputTokens,omitempty"`
	OutputTokens int64   `json:"outputTokens,omitempty"`
	CacheTokens  int64   `json:"cacheTokens,omitempty"`
	Model        string  `json:"model,omitempty"`
	UpdatedAt    int64   `json:"updatedAt"` // epoch seconds, stamped by the caller
}

// Thresholds are the warn/critical used-percent cutoffs (0..100).
type Thresholds struct {
	WarnPercent     float64 `json:"warnPercent"`
	CriticalPercent float64 `json:"criticalPercent"`
}

// Status is the evaluated severity of a Snapshot.
type Status int

const (
	StatusUnknown Status = iota // no percent signal (token-only / empty)
	StatusOK
	StatusWarn
	StatusCritical
)

// DefaultThresholds is the shipped default (warn 85%, critical 95%).
func DefaultThresholds() Thresholds { return Thresholds{WarnPercent: 85, CriticalPercent: 95} }

// Normalized coerces out-of-range or unset thresholds back to defaults, keeping
// warn < critical. A zero value (unset settings.json) yields DefaultThresholds.
func (t Thresholds) Normalized() Thresholds {
	d := DefaultThresholds()
	if t.WarnPercent <= 0 || t.WarnPercent > 100 {
		t.WarnPercent = d.WarnPercent
	}
	if t.CriticalPercent <= 0 || t.CriticalPercent > 100 {
		t.CriticalPercent = d.CriticalPercent
	}
	if t.CriticalPercent < t.WarnPercent {
		t.CriticalPercent = t.WarnPercent
	}
	return t
}

// MaxUsedPercent returns the larger used_percent across the non-nil windows and
// whether any window existed. Basis for the badge and Evaluate.
func MaxUsedPercent(s Snapshot) (float64, bool) {
	max, ok := 0.0, false
	for _, wnd := range []*Window{s.Primary, s.Secondary} {
		if wnd == nil {
			continue
		}
		if !ok || wnd.UsedPercent > max {
			max, ok = wnd.UsedPercent, true
		}
	}
	return max, ok
}

// Evaluate maps a Snapshot to a Status. Only KindPercentLimit with at least one
// window yields a colored status; everything else (token-only, empty) is
// StatusUnknown so it never triggers the switch dialog.
func Evaluate(s Snapshot, t Thresholds) Status {
	if s.Kind != KindPercentLimit {
		return StatusUnknown
	}
	pct, ok := MaxUsedPercent(s)
	if !ok {
		return StatusUnknown
	}
	th := t.Normalized()
	switch {
	case pct >= th.CriticalPercent:
		return StatusCritical
	case pct >= th.WarnPercent:
		return StatusWarn
	default:
		return StatusOK
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `GOFLAGS=-mod=readonly go test ./internal/usage/ -v`
Expected: PASS (TestEvaluate + TestMaxUsedPercent).

- [ ] **Step 5: Commit**

```bash
git add internal/usage/usage.go internal/usage/usage_test.go
git commit -m "feat(usage): add Snapshot/Window types and pure Evaluate (#10)"
```

---

### Task 2: `internal/usage` — `ScanRateLimitHit` (reaktif PTY tarama)

**Files:**
- Create: `internal/usage/ptyscan.go`
- Test: `internal/usage/ptyscan_test.go`

**Interfaces:**
- Consumes: (std-only)
- Produces: `func ScanRateLimitHit(chunk string) bool` — ANSI-strip sonrası dar regex; TUI gürültüsünde best-effort, normal çıktıda yanlış-pozitif vermez.

- [ ] **Step 1: Write the failing test**

```go
package usage

import "testing"

func TestScanRateLimitHit(t *testing.T) {
	hits := []string{
		"Error: you have hit your rate limit, try again later",
		"\x1b[31mUsage limit reached\x1b[0m for this model",
		"HTTP 429 Too Many Requests",
		"You've reached your usage limit",
	}
	for _, s := range hits {
		if !ScanRateLimitHit(s) {
			t.Errorf("beklenen hit yakalanmadı: %q", s)
		}
	}
	misses := []string{
		"",
		"Running tests at a high rate, all passed",
		"The limit parameter defaults to 15",
		"rate of change is 429 units/sec in this benchmark",
		"go test ./... limit reached? no, just checking",
	}
	for _, s := range misses {
		if ScanRateLimitHit(s) {
			t.Errorf("yanlış-pozitif: %q", s)
		}
	}
}
```

Not: `"limit reached"` tek başına ("limit reached? no...") yanlış-pozitif olmamalı; yalnız `usage limit reached` / `rate limit` / `429 too many` / `reached your ... limit` gibi kalıplar yakalanır. Regex'i buna göre yaz.

- [ ] **Step 2: Run test to verify it fails**

Run: `GOFLAGS=-mod=readonly go test ./internal/usage/ -run TestScanRateLimitHit -v`
Expected: FAIL — `undefined: ScanRateLimitHit`.

- [ ] **Step 3: Write minimal implementation**

```go
package usage

import (
	"regexp"
	"strings"
)

// ansiSeq matches CSI/OSC escape sequences so a rate-limit phrase split by TUI
// color codes still matches after stripping.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// rateLimitPhrases are the narrow patterns that indicate a CLI actually hit its
// usage/rate limit. Deliberately tight so ordinary output ("limit parameter",
// "high rate", a bare "429" in a benchmark) does NOT match. Codex stays the
// authoritative source; this is a best-effort reactive fallback for the
// token-only CLIs whose files don't expose a denominator.
var rateLimitPhrases = regexp.MustCompile(`(?i)(rate limit(ed| exceeded| reached)?|usage limit reached|reached your [a-z ]*limit|429 too many|too many requests|quota (exceeded|exhausted))`)

// ScanRateLimitHit reports whether a PTY output chunk signals a hit rate/usage
// limit. It strips ANSI escapes first, then applies the narrow phrase set.
func ScanRateLimitHit(chunk string) bool {
	if chunk == "" {
		return false
	}
	clean := ansiSeq.ReplaceAllString(chunk, "")
	return rateLimitPhrases.MatchString(clean)
}
```

Doğrulama notu: test'i çalıştırıp `"HTTP 429 Too Many Requests"` (→ "too many requests") ve `"rate of change is 429"` (→ hit OLMAMALI) ayrımını teyit et. `429` tek başına tetiklememeli; yalnız `429 too many` / `too many requests`.

- [ ] **Step 4: Run test to verify it passes**

Run: `GOFLAGS=-mod=readonly go test ./internal/usage/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/usage/ptyscan.go internal/usage/ptyscan_test.go
git commit -m "feat(usage): add ScanRateLimitHit reactive PTY detector (#10)"
```

---

### Task 3: `internal/ingest` — `UsageParser` arayüzü + Codex `ParseUsage`

**Files:**
- Modify: `internal/ingest/ingest.go` (yeni `UsageParser` arayüzü ekle — mevcut `SessionAdapter` DEĞİŞMEZ)
- Modify: `internal/ingest/adapter_codex.go` (Codex'e `ParseUsage` ekle)
- Test: `internal/ingest/adapter_codex_usage_test.go`

**Interfaces:**
- Consumes: `usage.Snapshot`, `usage.Window`, `usage.KindPercentLimit` (Task 1)
- Produces:
  - `type UsageParser interface { ParseUsage(path string) (*usage.Snapshot, error) }`
  - `func (codexAdapter) ParseUsage(path string) (*usage.Snapshot, error)` — dosyadaki SON `rate_limits` satırından primary/secondary/plan + son `thread_settings.model` + son `total_token_usage`. `rate_limits` yoksa `(nil,nil)`.

- [ ] **Step 1: Write the failing test**

```go
package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"desktop/internal/usage"
)

func writeLines(t *testing.T, lines []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout-test.jsonl")
	if err := os.WriteFile(p, []byte(joinNL(lines)), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func joinNL(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func TestCodexParseUsage(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","payload":{"id":"abc","cwd":"/x"}}`,
		`{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-sol"}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}},"rate_limits":{"primary":{"used_percent":10.0,"window_minutes":10080,"resets_at":111},"secondary":null,"plan_type":"pro"}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200,"cached_input_tokens":80,"output_tokens":50}},"rate_limits":{"primary":{"used_percent":43.0,"window_minutes":10080,"resets_at":222},"secondary":null,"plan_type":"pro"}}}`,
	}
	p := writeLines(t, lines)
	snap, err := codexAdapter{}.ParseUsage(p)
	if err != nil {
		t.Fatal(err)
	}
	if snap == nil {
		t.Fatal("snapshot nil, beklenen dolu")
	}
	if snap.Kind != usage.KindPercentLimit {
		t.Fatalf("Kind = %v", snap.Kind)
	}
	if snap.Primary == nil || snap.Primary.UsedPercent != 43.0 || snap.Primary.WindowMinutes != 10080 || snap.Primary.ResetsAt != 222 {
		t.Fatalf("Primary son satırdan alınmadı: %+v", snap.Primary)
	}
	if snap.Secondary != nil {
		t.Fatalf("Secondary null olmalı: %+v", snap.Secondary)
	}
	if snap.PlanType != "pro" {
		t.Fatalf("PlanType = %q", snap.PlanType)
	}
	if snap.Model != "gpt-5.6-sol" {
		t.Fatalf("Model = %q", snap.Model)
	}
	if snap.InputTokens != 200 || snap.OutputTokens != 50 || snap.CacheTokens != 80 {
		t.Fatalf("token toplamı son satırdan alınmadı: in=%d out=%d cache=%d", snap.InputTokens, snap.OutputTokens, snap.CacheTokens)
	}
}

func TestCodexParseUsageNoRateLimits(t *testing.T) {
	p := writeLines(t, []string{
		`{"type":"session_meta","payload":{"id":"abc"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"selam"}}`,
	})
	snap, err := codexAdapter{}.ParseUsage(p)
	if err != nil {
		t.Fatal(err)
	}
	if snap != nil {
		t.Fatalf("rate_limits yokken nil beklenirdi: %+v", snap)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOFLAGS=-mod=readonly go test ./internal/ingest/ -run TestCodexParseUsage -v`
Expected: FAIL — `codexAdapter has no field or method ParseUsage`.

- [ ] **Step 3: Write minimal implementation**

`internal/ingest/ingest.go` sonuna (import `"desktop/internal/usage"` ekle):

```go
// UsageParser is the optional per-CLI usage extractor. Adapters implement it to
// surface a usage.Snapshot from the SAME session file they already parse for
// messages, so the watcher can piggyback usage on the message poll tick without
// a second watcher. Returning (nil, nil) means "no usage signal yet".
type UsageParser interface {
	ParseUsage(path string) (*usage.Snapshot, error)
}
```

`internal/ingest/adapter_codex.go` sonuna (importlara `"encoding/json"` zaten var; `"desktop/internal/usage"` ekle):

```go
// codexRateLimits mirrors the rate_limits block inside a token_count event_msg.
type codexRateLimits struct {
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
	PlanType  string       `json:"plan_type"`
}
type codexWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

// codexUsageLine decodes the payloads ParseUsage cares about: token_count (which
// carries rate_limits + total token usage) and thread_settings_applied (model).
type codexUsageLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type string `json:"type"`
		Info struct {
			TotalTokenUsage struct {
				InputTokens       int64 `json:"input_tokens"`
				CachedInputTokens int64 `json:"cached_input_tokens"`
				OutputTokens      int64 `json:"output_tokens"`
			} `json:"total_token_usage"`
		} `json:"info"`
		RateLimits     *codexRateLimits `json:"rate_limits"`
		ThreadSettings struct {
			Model string `json:"model"`
		} `json:"thread_settings"`
	} `json:"payload"`
}

// ParseUsage reads the whole rollout and returns the LAST rate_limits reading
// (authoritative used-percent per window), the last-seen model, and the last
// token totals. Returns (nil, nil) when no rate_limits line exists yet (a fresh
// session before the first API response). A missing file is (nil, nil) too.
func (codexAdapter) ParseUsage(path string) (*usage.Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	snap := usage.Snapshot{CLI: "codex", Kind: usage.KindPercentLimit}
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // long JSON lines
	for sc.Scan() {
		var cl codexUsageLine
		if json.Unmarshal(sc.Bytes(), &cl) != nil || cl.Type != "event_msg" {
			continue
		}
		switch cl.Payload.Type {
		case "thread_settings_applied":
			if cl.Payload.ThreadSettings.Model != "" {
				snap.Model = cl.Payload.ThreadSettings.Model
			}
		case "token_count":
			tu := cl.Payload.Info.TotalTokenUsage
			if tu.InputTokens != 0 || tu.OutputTokens != 0 {
				snap.InputTokens, snap.OutputTokens, snap.CacheTokens = tu.InputTokens, tu.OutputTokens, tu.CachedInputTokens
			}
			if rl := cl.Payload.RateLimits; rl != nil {
				found = true
				snap.Primary = toUsageWindow(rl.Primary)
				snap.Secondary = toUsageWindow(rl.Secondary)
				snap.PlanType = rl.PlanType
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &snap, nil
}

func toUsageWindow(w *codexWindow) *usage.Window {
	if w == nil {
		return nil
	}
	return &usage.Window{UsedPercent: w.UsedPercent, WindowMinutes: w.WindowMinutes, ResetsAt: w.ResetsAt}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `GOFLAGS=-mod=readonly go test ./internal/ingest/ -run TestCodexParseUsage -v`
Expected: PASS (her iki alt-test).

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/ingest.go internal/ingest/adapter_codex.go internal/ingest/adapter_codex_usage_test.go
git commit -m "feat(ingest): add UsageParser + Codex ParseUsage (authoritative %) (#10)"
```

---

### Task 4: `internal/ingest` — Claude/Copilot/Gemini `ParseUsage` (token count)

**Files:**
- Modify: `internal/ingest/adapter_claude.go`, `adapter_copilot.go`, `adapter_gemini.go`
- Test: `internal/ingest/adapter_token_usage_test.go`

**Interfaces:**
- Consumes: `usage.Snapshot`, `usage.KindTokenCount` (Task 1)
- Produces: `ParseUsage` her token-CLI adapter'ında → `Kind=KindTokenCount`, token toplamı + model; limit-% YOK; sinyal yoksa `(nil,nil)`.

- [ ] **Step 1: Write the failing test**

```go
package ingest

import (
	"testing"

	"desktop/internal/usage"
)

func TestClaudeParseUsage(t *testing.T) {
	p := writeLines(t, []string{
		`{"type":"user","message":{"role":"user","content":"selam"}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"more"}],"usage":{"input_tokens":8,"output_tokens":7,"cache_read_input_tokens":50,"cache_creation_input_tokens":0}}}`,
	})
	snap, err := claudeAdapter{}.ParseUsage(p)
	if err != nil || snap == nil {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	if snap.Kind != usage.KindTokenCount {
		t.Fatalf("Kind = %v", snap.Kind)
	}
	if snap.Primary != nil || snap.Secondary != nil {
		t.Fatal("token-CLI'da limit penceresi olmamalı")
	}
	if snap.InputTokens != 18 || snap.OutputTokens != 12 {
		t.Fatalf("token toplamı yanlış: in=%d out=%d", snap.InputTokens, snap.OutputTokens)
	}
	if snap.CacheTokens != 170 {
		t.Fatalf("cache toplamı = %d, want 170", snap.CacheTokens)
	}
	if snap.Model != "claude-opus-4-8" {
		t.Fatalf("Model = %q", snap.Model)
	}
}

func TestCopilotParseUsage(t *testing.T) {
	p := writeLines(t, []string{
		`{"type":"user.message","data":{"content":"hi"}}`,
		`{"type":"session.shutdown","data":{"usage":{"inputTokens":300,"outputTokens":40,"cacheReadTokens":250},"currentModel":"gpt-5.3-codex"}}`,
	})
	snap, err := copilotAdapter{}.ParseUsage(p)
	if err != nil || snap == nil {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	if snap.Kind != usage.KindTokenCount || snap.InputTokens != 300 || snap.OutputTokens != 40 || snap.CacheTokens != 250 || snap.Model != "gpt-5.3-codex" {
		t.Fatalf("copilot usage yanlış: %+v", snap)
	}
}

func TestGeminiParseUsage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session-x.json")
	os.WriteFile(p, []byte(`{"sessionId":"g","messages":[{"type":"user","tokens":5},{"type":"gemini","model":"gemini-2.5-pro","tokens":30,"cached":12},{"type":"gemini","tokens":10,"cached":3}]}`), 0600)
	snap, err := geminiAdapter{}.ParseUsage(p)
	if err != nil || snap == nil {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	if snap.Kind != usage.KindTokenCount || snap.OutputTokens != 45 || snap.CacheTokens != 15 {
		t.Fatalf("gemini token toplamı yanlış: %+v", snap)
	}
	if snap.Model != "gemini-2.5-pro" {
		t.Fatalf("Model = %q", snap.Model)
	}
}

func TestParseUsageNoSignal(t *testing.T) {
	p := writeLines(t, []string{`{"type":"user","message":{"role":"user","content":"x"}}`})
	if snap, err := claudeAdapter{}.ParseUsage(p); err != nil || snap != nil {
		t.Fatalf("sinyalsiz dosyada nil beklendi: snap=%v err=%v", snap, err)
	}
}
```

(`filepath`/`os` importlarını test dosyasına ekle.)

Not: Gemini `tokens` alanı yanıt token'ı sayılır → `OutputTokens`'a toplanır (payda yok, yalnız gösterim; kesin input/output ayrımı Gemini dosyasında net değil — tek sayaç). Model, mesajlarda görülen son `model`.

- [ ] **Step 2: Run test to verify it fails**

Run: `GOFLAGS=-mod=readonly go test ./internal/ingest/ -run 'ParseUsage' -v`
Expected: FAIL — adapter'larda `ParseUsage` yok.

- [ ] **Step 3: Write minimal implementation**

`adapter_claude.go` (importlara `"bufio"`, `"desktop/internal/usage"` ekle):

```go
type claudeUsageLine struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseUsage sums per-message token usage across the transcript (no denominator
// exists — Claude's rateLimits field is null in practice) and returns the last
// model. (nil,nil) when no usage line is present.
func (claudeAdapter) ParseUsage(path string) (*usage.Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	snap := usage.Snapshot{CLI: "claude", Kind: usage.KindTokenCount}
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var cl claudeUsageLine
		if json.Unmarshal(sc.Bytes(), &cl) != nil {
			continue
		}
		u := cl.Message.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
			continue
		}
		found = true
		snap.InputTokens += u.InputTokens
		snap.OutputTokens += u.OutputTokens
		snap.CacheTokens += u.CacheReadInputTokens + u.CacheCreationInputTokens
		if cl.Message.Model != "" {
			snap.Model = cl.Message.Model
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &snap, nil
}
```

`adapter_copilot.go` (importlara `"bufio"`, `"desktop/internal/usage"` ekle):

```go
type copilotUsageLine struct {
	Type string `json:"type"`
	Data struct {
		Usage struct {
			InputTokens    int64 `json:"inputTokens"`
			OutputTokens   int64 `json:"outputTokens"`
			CacheReadTokens int64 `json:"cacheReadTokens"`
		} `json:"usage"`
		CurrentModel string `json:"currentModel"`
	} `json:"data"`
}

// ParseUsage reads the LAST line carrying a usage block (Copilot fills usage on
// session.shutdown, and some turn events) and returns its token totals + model.
// (nil,nil) when no usage is present yet (live session mid-turn).
func (copilotAdapter) ParseUsage(path string) (*usage.Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	snap := usage.Snapshot{CLI: "copilot", Kind: usage.KindTokenCount}
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var cl copilotUsageLine
		if json.Unmarshal(sc.Bytes(), &cl) != nil {
			continue
		}
		u := cl.Data.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 {
			continue
		}
		found = true
		snap.InputTokens, snap.OutputTokens, snap.CacheTokens = u.InputTokens, u.OutputTokens, u.CacheReadTokens
		if cl.Data.CurrentModel != "" {
			snap.Model = cl.Data.CurrentModel
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &snap, nil
}
```

`adapter_gemini.go` — mevcut `geminiFile` struct'ı `tokens`/`cached`/`model` taşımıyor; ParseUsage için genişletilmiş bir tip kullan (mevcut tipi değiştirme, mesaj parse'ını kırma):

```go
// ParseUsage sums Gemini's per-message token/cache counters (no denominator) and
// returns the last-seen model. (nil,nil) when the file is missing/unparsable or
// carries no token counters.
func (geminiAdapter) ParseUsage(path string) (*usage.Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var gf struct {
		Messages []struct {
			Type   string `json:"type"`
			Model  string `json:"model"`
			Tokens int64  `json:"tokens"`
			Cached int64  `json:"cached"`
		} `json:"messages"`
	}
	if json.Unmarshal(data, &gf) != nil {
		return nil, nil // partial write / not JSON yet — skip
	}
	snap := usage.Snapshot{CLI: "gemini", Kind: usage.KindTokenCount}
	found := false
	for _, m := range gf.Messages {
		if m.Tokens == 0 && m.Cached == 0 {
			continue
		}
		found = true
		snap.OutputTokens += m.Tokens
		snap.CacheTokens += m.Cached
		if m.Model != "" {
			snap.Model = m.Model
		}
	}
	if !found {
		return nil, nil
	}
	return &snap, nil
}
```

(`adapter_gemini.go` importlarına `"desktop/internal/usage"` ekle; `encoding/json`, `os` zaten var.)

- [ ] **Step 4: Run test to verify it passes**

Run: `GOFLAGS=-mod=readonly go test ./internal/ingest/ -run 'ParseUsage' -v`
Expected: PASS (Claude/Copilot/Gemini/NoSignal).

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/adapter_claude.go internal/ingest/adapter_copilot.go internal/ingest/adapter_gemini.go internal/ingest/adapter_token_usage_test.go
git commit -m "feat(ingest): add token-count ParseUsage for Claude/Copilot/Gemini (#10)"
```

---

### Task 5: `internal/ingest` — watcher `onUsage` piggyback + app.go çağrı güncelle

**Files:**
- Modify: `internal/ingest/watcher.go` (`StartSession` imzasına `onUsage func(*usage.Snapshot)` ekle; poll tick'te throttled `ParseUsage`)
- Modify: `app.go:1152` (`StartSession` çağrısına `onUsage` argümanı — bu task'ta nil-safe placeholder; gerçek emit Task 7)
- Test: `internal/ingest/watcher_usage_test.go`

**Interfaces:**
- Consumes: `UsageParser`, `usage.Snapshot`, `codexAdapter.ParseUsage` (Task 3)
- Produces: `StartSession(..., onUsage func(*usage.Snapshot))` — adapter `UsageParser` implement ediyorsa, path bulunduktan sonra her `usageParseInterval` (2s) dolunca `ParseUsage(path)` çağırır, non-nil sonucu `SessionID` doldurup `onUsage`'a verir. `onUsage` nil → usage parse atlanır.

Not: `StartSession` imza değişikliği TÜM çağıranları etkiler. Bu repoda tek çağıran `app.go:1152`. Mevcut `watcher_test.go` testleri `StartSession`'ı çağırıyorsa onlara da `nil` onUsage argümanı eklenir (davranış değişmez).

- [ ] **Step 1: Write the failing test**

```go
package ingest

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"desktop/internal/usage"
)

func TestWatcherEmitsUsage(t *testing.T) {
	// Fake adapter: discovers a fixed path, no messages, ParseUsage returns a codex-like snapshot.
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-x.jsonl")
	os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":50,"window_minutes":10080,"resets_at":1},"plan_type":"pro"}}}`+"\n"), 0600)

	ad := &fakeUsageAdapter{path: path}
	m := New()
	var mu sync.Mutex
	var got *usage.Snapshot
	m.StartSession("s1", ad, dir, time.Now().UnixNano(), nil, nil,
		func(content, ts string) bool { return true },
		nil,
		func(snap *usage.Snapshot) { mu.Lock(); got = snap; mu.Unlock() },
		nil)
	defer m.StopSession("s1")

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		g := got
		mu.Unlock()
		if g != nil {
			if g.SessionID != "s1" || g.Primary == nil || g.Primary.UsedPercent != 50 {
				t.Fatalf("beklenmeyen snapshot: %+v", g)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("onUsage 4s içinde çağrılmadı")
}

// fakeUsageAdapter implements SessionAdapter + UsageParser.
type fakeUsageAdapter struct{ path string }

func (f *fakeUsageAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64, claimed func(string) bool) (string, error) {
	return f.path, nil
}
func (f *fakeUsageAdapter) ParseNewUserMessages(path string, cur Cursor) ([]ParsedMessage, Cursor, error) {
	return nil, cur, nil
}
func (f *fakeUsageAdapter) SessionID(path string) string { return "cli-id" }
func (f *fakeUsageAdapter) ParseUsage(path string) (*usage.Snapshot, error) {
	return codexAdapter{}.ParseUsage(path)
}
```

Not: bu test, `StartSession`'ın YENİ imzasını (`onUsage` argümanı SessionID/resume'den sonra, resume'den önce/sonra — aşağıdaki imzaya göre) kullanır. İmza sırasını implementasyonla eşleştir.

- [ ] **Step 2: Run test to verify it fails**

Run: `GOFLAGS=-mod=readonly go test ./internal/ingest/ -run TestWatcherEmitsUsage -v`
Expected: FAIL — `StartSession` argüman sayısı uyuşmaz / derlenmez.

- [ ] **Step 3: Write minimal implementation**

`watcher.go`:

1. Import'a `"desktop/internal/usage"` ekle.
2. Sabit ekle: `const usageParseInterval = 2 * time.Second`.
3. `StartSession` imzasına `onUsage func(*usage.Snapshot)` parametresi ekle (imza sırası: `..., onSessionID func(id string), onUsage func(*usage.Snapshot), resume *ResumeSeed`), `go m.run(...)` çağrısına ilet.
4. `run` imzasına da `onUsage func(*usage.Snapshot)` ekle.
5. `discoverAndPoll` closure'ının sonuna (path bulunup poll edildikten sonra) usage parse ekle. `run` gövdesinde bir `var lastUsageParse time.Time` tut:

```go
// Usage piggyback: after the message poll, if this adapter can extract usage,
// re-read the file at most every usageParseInterval and hand a fresh snapshot to
// onUsage. Throttled so an active session (file changes every tick) doesn't
// re-scan a multi-MB rollout on every 700ms poll (spec §4).
up, canUsage := ad.(UsageParser)
if canUsage && onUsage != nil && path != "" {
	if lastUsageParse.IsZero() || time.Since(lastUsageParse) >= usageParseInterval {
		lastUsageParse = time.Now()
		if snap, uerr := up.ParseUsage(path); uerr != nil {
			log.Printf("[USAGE] parse error (%s): %v", path, uerr)
		} else if snap != nil {
			snap.SessionID = s.id
			onUsage(snap)
		}
	}
}
```

`lastUsageParse`'ı `run`'ın en üstünde tanımla (ticker'ın yanında) ve `discoverAndPoll` closure'ının onu yakalaması için closure'ı o scope'ta bırak. `time.Since` / `time.Now` test-time'ı bozmaz (mevcut kod da `time.Now` kullanıyor).

`app.go:1152` — `StartSession` çağrısına `onUsage` argümanı ekle (Task 7'de gerçek emit; şimdilik nil-safe köprü):

```go
// Task 7 bunu gerçek emit ile değiştirir. Şimdilik nil geçilirse usage parse
// atlanır; derlensin diye imza güncellenir.
a.ingestMgr.StartSession(sessionID, ad, ingestCwd, ingestSpawnedAt, ready,
	a.ptyManager.SessionDone(sessionID),
	func(content, ts string) bool { /* mevcut emit gövdesi */ },
	func(id string) { /* mevcut onSessionID gövdesi */ },
	nil, // onUsage — Task 7'de a.onUsage(sessionID) ile doldurulur
	resumeSeed)
```

Mevcut `watcher_test.go`/`watcher_broadcast` vb. testlerinde `StartSession` çağrısı varsa, hepsine yeni imza sırasına uygun `nil` onUsage argümanı ekle (davranış değişmez).

- [ ] **Step 4: Run tests**

Run: `GOFLAGS=-mod=readonly go test ./internal/ingest/ -v`
Expected: PASS (TestWatcherEmitsUsage + tüm mevcut watcher testleri yeşil).
Run: `GOFLAGS=-mod=readonly go build ./...`
Expected: derlenir (app.go güncel imzayla).

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/watcher.go internal/ingest/watcher_usage_test.go app.go
git commit -m "feat(ingest): piggyback usage parse on watcher poll tick (#10)"
```

---

### Task 6: `internal/cli/startup.go` — handoff segmenti

**Files:**
- Modify: `internal/cli/startup.go` (`ComposeStartupPrompt`'a `handoffFrom string` parametresi)
- Modify: `app.go:1419` (`composeAgentPrompt`'a `handoffFrom` parametresi) + çağıranları (`sendStartupPrompt`, Copilot `-i` yolu, `createTerminal`)
- Test: `internal/cli/startup_test.go` (yeni alt-test)

**Interfaces:**
- Consumes: (yok)
- Produces: `ComposeStartupPrompt(base, global, team, roomSummary, selectedPrompt, agentName, agentRole, teamName, agentMode, handoffFrom string) string` — `handoffFrom != ""` iken summary'den SONRA, selectedPrompt'tan ÖNCE labelli bir "Devralma Notu" segmenti ekler.

- [ ] **Step 1: Write the failing test**

`startup_test.go`'ya ekle:

```go
func TestComposeStartupPromptHandoff(t *testing.T) {
	out := ComposeStartupPrompt("BASE", "", "", "", "SEL", "pilot", "", "takim", "", "codex")
	if !strings.Contains(out, "Devralma Notu") || !strings.Contains(out, "codex") {
		t.Fatalf("handoff segmenti yok: %s", out)
	}
	// Segment sırası: BASE ... Devralma ... SEL ... join
	iBase := strings.Index(out, "BASE")
	iHand := strings.Index(out, "Devralma Notu")
	iSel := strings.Index(out, "SEL")
	iJoin := strings.Index(out, "join_room")
	if !(iBase < iHand && iHand < iSel && iSel < iJoin) {
		t.Fatalf("segment sırası yanlış: base=%d hand=%d sel=%d join=%d", iBase, iHand, iSel, iJoin)
	}

	none := ComposeStartupPrompt("BASE", "", "", "", "SEL", "pilot", "", "takim", "", "")
	if strings.Contains(none, "Devralma Notu") {
		t.Fatal("handoffFrom boşken segment olmamalı")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOFLAGS=-mod=readonly go test ./internal/cli/ -run TestComposeStartupPromptHandoff -v`
Expected: FAIL — `ComposeStartupPrompt` argüman sayısı uyuşmaz.

- [ ] **Step 3: Write minimal implementation**

`startup.go`:
1. Sabit ekle: `const handoffHeader = "## Devralma Notu (bağlam)"`.
2. İmzaya `handoffFrom string` ekle (son parametre).
3. Room summary segmentinden SONRA, selected prompt'tan ÖNCE ekle:

```go
// Handoff primer (#10): when this terminal replaces a CLI that hit its usage
// limit, tell the new agent it is taking over and to read room history. Its own
// labeled segment (like the summary) so it reads as background context, never as
// an instruction that overrides the charter.
if handoffFrom = strings.TrimSpace(handoffFrom); handoffFrom != "" {
	parts = append(parts, fmt.Sprintf(
		"%s\n⚠️ '%s' agent'ının CLI limiti doldu; görevi sen devralıyorsun. "+
			"Oda geçmişini read_all_messages(since_id=0, limit=1000) ile oku ve kaldığı yerden devam et.",
		handoffHeader, handoffFrom))
}
```

`app.go`:
- `composeAgentPrompt(teamID, agentName, promptID, agentMode string)` → `composeAgentPrompt(teamID, agentName, promptID, agentMode, handoffFrom string)`; son `return cli.ComposeStartupPrompt(...)` çağrısına `handoffFrom` ekle.
- `sendStartupPrompt(sessionID, teamID, agentName, cliType, promptID, agentMode string)` → sonuna `handoffFrom string` ekle; içindeki `a.composeAgentPrompt(...)` çağrısına ilet.
- `createTerminal(... slotIndex int, resumeID string)` → sonuna `handoffFrom string` ekle; Copilot `-i` yolundaki `a.composeAgentPrompt(...)` çağrısı ve `go a.sendStartupPrompt(...)` çağrısına ilet.
- `CreateTerminal` (exported) `createTerminal(..., "")` çağrısına ek olarak `""` handoffFrom geçir.
- Diğer `createTerminal` çağıranları (`restartInternal`, `openTeamFromConfig`) `""` handoffFrom ile güncelle.

- [ ] **Step 4: Run tests**

Run: `GOFLAGS=-mod=readonly go test ./internal/cli/ -v && GOFLAGS=-mod=readonly go build ./...`
Expected: PASS + derlenir.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/startup.go internal/cli/startup_test.go app.go
git commit -m "feat(startup): add handoff primer segment to ComposeStartupPrompt (#10)"
```

---

### Task 7: `app.go` — usage:updated emit + eşik ayarları

**Files:**
- Modify: `app.go` (`appSettings` alanları + `GetUsageThresholds`/`SetUsageThresholds` + `onUsage` metodu; Task 5'teki nil'i gerçek emit ile değiştir)
- Test: `app_usage_settings_test.go`

**Interfaces:**
- Consumes: `usage.Thresholds`, `usage.Evaluate`, `usage.Snapshot` (Task 1)
- Produces:
  - `appSettings` alanları `UsageWarnPercent float64`, `UsageCritPercent float64`
  - `func (a *App) GetUsageThresholds() usage.Thresholds`
  - `func (a *App) SetUsageThresholds(warn, crit float64) error`
  - `func (a *App) onUsage(snap *usage.Snapshot)` — `UpdatedAt` damgala + `Evaluate` + Wails `usage:updated` emit

- [ ] **Step 1: Write the failing test**

```go
package main

import "testing"

func TestUsageThresholdsRoundTrip(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	// default (dosya yok)
	def := a.GetUsageThresholds()
	if def.WarnPercent != 85 || def.CriticalPercent != 95 {
		t.Fatalf("default eşik = %+v", def)
	}
	if err := a.SetUsageThresholds(70, 90); err != nil {
		t.Fatal(err)
	}
	got := a.GetUsageThresholds()
	if got.WarnPercent != 70 || got.CriticalPercent != 90 {
		t.Fatalf("kayıt sonrası = %+v", got)
	}
	// bozuk aralık normalize edilir (crit<warn → crit=warn)
	if err := a.SetUsageThresholds(80, 50); err != nil {
		t.Fatal(err)
	}
	n := a.GetUsageThresholds()
	if n.CriticalPercent < n.WarnPercent {
		t.Fatalf("normalize edilmedi: %+v", n)
	}
	// deferral ayarı korunmalı (aynı dosya)
	if err := a.SetDeferralEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !a.GetDeferralEnabled() {
		t.Fatal("deferral usage kaydında ezildi")
	}
	if a.GetUsageThresholds().WarnPercent != 80 {
		t.Fatal("usage eşiği deferral kaydında ezildi")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOFLAGS=-mod=readonly go test . -run TestUsageThresholdsRoundTrip -v`
Expected: FAIL — `GetUsageThresholds` tanımsız.

- [ ] **Step 3: Write minimal implementation**

`app.go`:
1. Import'a `"desktop/internal/usage"` ekle (zaten ingest üzerinden dolaylı; doğrudan ekle).
2. `appSettings` struct'ına:

```go
	// Usage/limit uyarı eşikleri (#10). 0/aralık-dışı → usage.DefaultThresholds
	// (85/95). SettingsModal'dan konfigüre edilir.
	UsageWarnPercent float64 `json:"usage_warn_percent"`
	UsageCritPercent float64 `json:"usage_crit_percent"`
```

3. Metotlar:

```go
// GetUsageThresholds returns the persisted warn/critical cutoffs, normalized to
// defaults when unset or out of range.
func (a *App) GetUsageThresholds() usage.Thresholds {
	s := a.loadAppSettings()
	return usage.Thresholds{WarnPercent: s.UsageWarnPercent, CriticalPercent: s.UsageCritPercent}.Normalized()
}

// SetUsageThresholds persists the warn/critical cutoffs (normalized).
func (a *App) SetUsageThresholds(warn, crit float64) error {
	n := usage.Thresholds{WarnPercent: warn, CriticalPercent: crit}.Normalized()
	s := a.loadAppSettings()
	s.UsageWarnPercent, s.UsageCritPercent = n.WarnPercent, n.CriticalPercent
	return a.saveAppSettings(s)
}

// onUsage stamps a fresh snapshot, evaluates it against the current thresholds,
// and pushes it to the frontend. Wired as the ingest watcher's onUsage callback.
func (a *App) onUsage(snap *usage.Snapshot) {
	if snap == nil || a.ctx == nil {
		return
	}
	snap.UpdatedAt = time.Now().Unix()
	status := usage.Evaluate(*snap, a.GetUsageThresholds())
	runtime.EventsEmit(a.ctx, "usage:updated", map[string]interface{}{
		"snapshot": snap,
		"status":   int(status),
	})
}
```

4. Task 5'teki `StartSession` çağrısında `nil` onUsage'ı `func(s *usage.Snapshot) { a.onUsage(s) }` ile değiştir. (Sadece AI CLI adapter'ları `UsageParser` implement ettiği için shell güvenli.)

- [ ] **Step 4: Run test to verify it passes**

Run: `GOFLAGS=-mod=readonly go test . -run TestUsageThresholdsRoundTrip -v && GOFLAGS=-mod=readonly go build ./...`
Expected: PASS + derlenir.

- [ ] **Step 5: Commit**

```bash
git add app.go app_usage_settings_test.go
git commit -m "feat(app): usage:updated emit + configurable thresholds (#10)"
```

---

### Task 8: `app.go` — `SwitchTerminal` + reaktif onOutput taraması

**Files:**
- Modify: `app.go` (`SwitchTerminal` metodu; `startup()` içindeki `NewManager` onOutput handler'ına reaktif tarama)
- Test: `app_switch_test.go`

**Interfaces:**
- Consumes: `restartInternal` deseni, `createTerminal(..., handoffFrom)` (Task 6), `usage.ScanRateLimitHit` (Task 2)
- Produces: `func (a *App) SwitchTerminal(sessionID, targetCLI string) (string, error)` — eski session'ın team/agent/workDir/slot/room'unu okuyup aynı slot+oda'da `targetCLI` ile yeni terminal açar (resumeID YOK), handoff primeri (`agentName`) enjekte eder, eski terminali kapatır (in-slot replace).

- [ ] **Step 1: Write the failing test**

SwitchTerminal PTY/hub gerektirdiği için tam entegrasyon testi ağır; onun yerine SAF yardımcıyı test et: hedef CLI doğrulaması + handoff kaynağı. Yardımcıyı ayıkla:

```go
package main

import "testing"

func TestSwitchTargetValidation(t *testing.T) {
	// Boş / aynı / desteklenmeyen hedef reddedilir; geçerli AI CLI kabul edilir.
	cases := []struct {
		cur, target string
		ok          bool
	}{
		{"codex", "claude", true},
		{"codex", "codex", false},   // aynı CLI'a geçiş anlamsız
		{"codex", "", false},        // boş hedef
		{"codex", "shell", false},   // AI olmayan
		{"codex", "bogus", false},   // bilinmeyen
	}
	for _, c := range cases {
		if got := validSwitchTarget(c.cur, c.target); got != c.ok {
			t.Errorf("validSwitchTarget(%q,%q) = %v, want %v", c.cur, c.target, got, c.ok)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOFLAGS=-mod=readonly go test . -run TestSwitchTargetValidation -v`
Expected: FAIL — `validSwitchTarget` tanımsız.

- [ ] **Step 3: Write minimal implementation**

`app.go`:

```go
// validSwitchTarget reports whether target is a sensible CLI to switch to from
// current: a known AI CLI (join_room-capable) that differs from the current one.
func validSwitchTarget(current, target string) bool {
	if target == "" || target == current || !isAICLIType(target) {
		return false
	}
	return true
}

// SwitchTerminal replaces a terminal that is hitting its usage limit with a fresh
// one running targetCLI in the SAME slot and room (in-slot replace). Cross-CLI
// resume is impossible, so the new session starts fresh; a handoff primer tells
// it to take over and read room history. The old terminal is closed. Returns the
// new session ID.
func (a *App) SwitchTerminal(sessionID, targetCLI string) (string, error) {
	session := a.ptyManager.GetSession(sessionID)
	if session == nil {
		return "", fmt.Errorf("session bulunamadı: %s", sessionID)
	}
	if !validSwitchTarget(session.CLIType, targetCLI) {
		return "", fmt.Errorf("geçersiz geçiş hedefi: %q → %q", session.CLIType, targetCLI)
	}
	teamID := session.TeamID
	agentName := session.AgentName
	workDir := session.WorkDir
	promptID := session.PromptID
	slotIndex := session.SlotIndex
	wtDir := session.WorktreeDir
	wtRepo := session.WorktreeRepo

	// A manager/observer stays in the main repo; a worker keeps its worktree.
	mainRepoRole := false
	if teamID != "" {
		if t, err := a.teamStore.Get(teamID); err == nil {
			mainRepoRole = t.IsManagerAgent(agentName)
		}
		mainRepoRole = mainRepoRole || a.isObserverAgent(teamID, agentName)
	}
	workDir = restartWorkDir(mainRepoRole, workDir, wtDir, wtRepo)

	// Close the old terminal (in-slot replace) but keep the worktree — the new CLI
	// runs in the same dir.
	if err := a.closeTerminalInternal(sessionID, false); err != nil {
		return "", fmt.Errorf("eski terminal kapatılamadı: %w", err)
	}

	log.Printf("[SWITCH] agent=%s %s→%s team=%s slot=%d", agentName, session.CLIType, targetCLI, teamID, slotIndex)

	// resumeID="" (cross-CLI resume impossible); handoffFrom=agentName injects the primer.
	newID, err := a.createTerminal(teamID, agentName, workDir, targetCLI, promptID, false, slotIndex, "", agentName)
	if err != nil {
		return "", err
	}
	if s := a.ptyManager.GetSession(newID); s != nil && wtDir != "" {
		s.WorktreeDir = wtDir
		s.WorktreeRepo = wtRepo
	}
	return newID, nil
}
```

Reaktif onOutput taraması — `startup()` içindeki `ptymgr.NewManager(...)` callback'ini genişlet:

```go
a.ptyManager = ptymgr.NewManager(func(sessionID string, data []byte) {
	runtime.EventsEmit(a.ctx, "pty:output:"+sessionID, string(data))
	// Reaktif limit-hit sinyali (#10): token-CLI'ları payda vermediği için PTY
	// çıktısında "rate limit / 429 / usage limit reached" görülürse frontend'e
	// bildir. Best-effort; Codex authoritative kaldıkça yalnız ek katman.
	if usage.ScanRateLimitHit(string(data)) {
		runtime.EventsEmit(a.ctx, "usage:limit-hit", map[string]string{"sessionID": sessionID})
	}
})
```

- [ ] **Step 4: Run test + build**

Run: `GOFLAGS=-mod=readonly go test . -run TestSwitchTargetValidation -v && GOFLAGS=-mod=readonly go build ./...`
Expected: PASS + derlenir.

- [ ] **Step 5: Commit**

```bash
git add app.go app_switch_test.go
git commit -m "feat(app): SwitchTerminal in-slot replace + reactive limit-hit scan (#10)"
```

---

### Task 9: Wails bindings + `frontend/lib/types.ts` UsageSnapshot

**Files:**
- Modify: `frontend/src/lib/types.ts` (yeni `UsageSnapshot`, `UsageWindow`, `UsageStatus`, `UsageUpdatedEvent` tipleri)
- Generate/Modify: `frontend/wailsjs/go/main/App.d.ts` + `App.js` (`SwitchTerminal`, `GetUsageThresholds`, `SetUsageThresholds`)

**Interfaces:**
- Consumes: backend imzaları (Task 7/8)
- Produces: TS tipleri + Wails binding stub'ları frontend'e görünür.

- [ ] **Step 1: `types.ts`'e tipleri ekle** (dosya sonuna, mevcut event-payload bloğu stilinde)

```ts
export type UsageStatus = 0 | 1 | 2 | 3; // Unknown | OK | Warn | Critical

export interface UsageWindow {
  usedPercent: number;
  windowMinutes: number;
  resetsAt: number; // epoch sec; 0 = bilinmiyor
}

export interface UsageSnapshot {
  sessionID: string;
  cli: CLIType;
  kind: number; // 0 none | 1 percentLimit | 2 tokenCount
  primary?: UsageWindow;
  secondary?: UsageWindow;
  planType?: string;
  inputTokens?: number;
  outputTokens?: number;
  cacheTokens?: number;
  model?: string;
  updatedAt: number;
}

export interface UsageUpdatedEvent {
  snapshot: UsageSnapshot;
  status: UsageStatus;
}
```

- [ ] **Step 2: Wails binding'leri üret**

Run: `command -v wails && GOFLAGS=-mod=readonly wails generate module`
Expected: `App.d.ts`/`App.js` yeni fonksiyonları içerir.

Fallback (wails headless çalışmazsa) — `App.d.ts`'e elle ekle:
```ts
export function SwitchTerminal(arg1:string,arg2:string):Promise<string>;
export function GetUsageThresholds():Promise<usage.Thresholds>;
export function SetUsageThresholds(arg1:number,arg2:number):Promise<void>;
```
ve `App.js`'e mevcut stildeki karşılıkları. (Not: bu build sırasında `make dev`/`make build` ile zaten yeniden üretilir; elle ekleme yalnız ara `npm run build` doğrulaması içindir.)

- [ ] **Step 3: Frontend derle**

Run: `cd frontend && npm run build`
Expected: TS hatası yok.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/types.ts frontend/wailsjs/go/main/App.d.ts frontend/wailsjs/go/main/App.js
git commit -m "feat(frontend): UsageSnapshot types + SwitchTerminal/threshold bindings (#10)"
```

---

### Task 10: `frontend` — `useUsage` store + App.tsx event abonelik

**Files:**
- Create: `frontend/src/store/useUsage.ts`
- Modify: `frontend/src/App.tsx` (`usage:updated` + `usage:limit-hit` aboneliği)

**Interfaces:**
- Consumes: `UsageSnapshot`, `UsageUpdatedEvent` (Task 9)
- Produces:
  - `useUsage` store: `entries: Record<string, UsageUpdatedEvent>`, `limitHits: Record<string, number>`, `applySnapshot(ev)`, `markLimitHit(sessionID)`, `clear(sessionID)`
  - `useUsageFor(sessionID): UsageUpdatedEvent | undefined` selector (stabil boş referans)

- [ ] **Step 1: `useUsage.ts` oluştur** (`useSummaries.ts` deseni)

```ts
import { create } from "zustand";
import { UsageUpdatedEvent } from "../lib/types";

interface UsageState {
  entries: Record<string, UsageUpdatedEvent>;
  limitHits: Record<string, number>; // sessionID → epoch ms (reaktif PTY sinyali)
  applySnapshot: (ev: UsageUpdatedEvent) => void;
  markLimitHit: (sessionID: string) => void;
  clear: (sessionID: string) => void;
}

export const useUsage = create<UsageState>((set) => ({
  entries: {},
  limitHits: {},
  applySnapshot: (ev) =>
    set((s) => ({ entries: { ...s.entries, [ev.snapshot.sessionID]: ev } })),
  markLimitHit: (sessionID) =>
    set((s) => ({ limitHits: { ...s.limitHits, [sessionID]: Date.now() } })),
  clear: (sessionID) =>
    set((s) => {
      const entries = { ...s.entries };
      const limitHits = { ...s.limitHits };
      delete entries[sessionID];
      delete limitHits[sessionID];
      return { entries, limitHits };
    }),
}));

const EMPTY: UsageUpdatedEvent | undefined = undefined;
export function useUsageFor(sessionID: string): UsageUpdatedEvent | undefined {
  return useUsage((s) => s.entries[sessionID] ?? EMPTY);
}
export function useAllUsage(): Record<string, UsageUpdatedEvent> {
  return useUsage((s) => s.entries);
}
```

- [ ] **Step 2: App.tsx event aboneliği** — mevcut `EventsOn` bloğuna (satır ~100-150) ekle:

```ts
    EventsOn("usage:updated", (data: UsageUpdatedEvent) => {
      useUsage.getState().applySnapshot(data);
    });
    EventsOn("usage:limit-hit", (data: { sessionID: string }) => {
      useUsage.getState().markLimitHit(data.sessionID);
    });
```
ve `cleanupFn` içindeki `EventsOff` listesine `"usage:updated"`, `"usage:limit-hit"` ekle. `import { useUsage } from "./store/useUsage"` ve `UsageUpdatedEvent` tipini import et.

- [ ] **Step 3: Derle**

Run: `cd frontend && npm run build`
Expected: TS hatası yok.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/store/useUsage.ts frontend/src/App.tsx
git commit -m "feat(frontend): useUsage store + usage event subscriptions (#10)"
```

---

### Task 11: `frontend` — TerminalPane usage rozeti

**Files:**
- Create: `frontend/src/components/UsageBadge.tsx`
- Modify: `frontend/src/components/TerminalPane.tsx` (başlığa rozet), `frontend/src/styles/globals.css` (rozet stilleri)

**Interfaces:**
- Consumes: `useUsageFor` (Task 10), `usage.MaxUsedPercent` mantığı (frontend'de yeniden)
- Produces: `<UsageBadge sessionID={...} />` — Codex renkli %rozet + hover; token-CLI `142k tok`; sinyal yok → render etmez.

- [ ] **Step 1: `UsageBadge.tsx` oluştur**

```tsx
import { useUsageFor } from "../store/useUsage";
import { UsageWindow } from "../lib/types";

function fmtTokens(n: number): string {
  if (n >= 1000) return `${Math.round(n / 1000)}k`;
  return String(n);
}
function windowLabel(w: UsageWindow): string {
  const m = w.windowMinutes;
  if (m >= 10080) return `${Math.round(m / 10080)}h`; // hafta
  if (m >= 1440) return `${Math.round(m / 1440)}g`;
  if (m >= 60) return `${Math.round(m / 60)}s`;
  return `${m}dk`;
}
function resetLabel(resetsAt: number): string {
  if (!resetsAt) return "";
  const d = new Date(resetsAt * 1000);
  return d.toLocaleString();
}

export default function UsageBadge({ sessionID }: { sessionID: string }) {
  const ev = useUsageFor(sessionID);
  if (!ev) return null;
  const s = ev.snapshot;

  // Codex: renkli yüzde rozeti
  if (s.kind === 1) {
    const wins = [s.primary, s.secondary].filter(Boolean) as UsageWindow[];
    if (wins.length === 0) return null;
    const max = Math.max(...wins.map((w) => w.usedPercent));
    const cls = ev.status === 3 ? "critical" : ev.status === 2 ? "warn" : "ok";
    const title = wins
      .map((w) => `${windowLabel(w)} penceresi: %${w.usedPercent.toFixed(0)}${w.resetsAt ? ` · sıfırlanma ${resetLabel(w.resetsAt)}` : ""}`)
      .join("\n") + (s.model ? `\nModel: ${s.model}` : "") + (s.planType ? `\nPlan: ${s.planType}` : "");
    return (
      <span className={`usage-badge usage-badge-${cls}`} title={title}>
        %{max.toFixed(0)}
      </span>
    );
  }

  // Token-CLI: renksiz tüketim rozeti
  if (s.kind === 2) {
    const total = (s.inputTokens ?? 0) + (s.outputTokens ?? 0);
    if (total === 0) return null;
    const title = `Girdi: ${s.inputTokens ?? 0} · Çıktı: ${s.outputTokens ?? 0} · Cache: ${s.cacheTokens ?? 0}${s.model ? `\nModel: ${s.model}` : ""}`;
    return (
      <span className="usage-badge usage-badge-token" title={title}>
        {fmtTokens(total)} tok
      </span>
    );
  }
  return null;
}
```

- [ ] **Step 2: TerminalPane başlığına ekle** — `cli-badge`'in hemen yanına (satır ~288):

```tsx
{cliType && cliType !== "shell" && (
  <span className={`cli-badge cli-badge-${cliType}`}>{cliType}</span>
)}
<UsageBadge sessionID={sessionID} />
```
(`import UsageBadge from "./UsageBadge";` ekle.)

- [ ] **Step 3: CSS ekle** (`globals.css` sonuna)

```css
.usage-badge { font-size: 11px; padding: 1px 6px; border-radius: 8px; margin-left: 4px; font-variant-numeric: tabular-nums; cursor: default; }
.usage-badge-ok { background: #1f6f3f; color: #d7ffe6; }
.usage-badge-warn { background: #8a6d1a; color: #fff3cf; }
.usage-badge-critical { background: #8a1f1f; color: #ffd7d7; }
.usage-badge-token { background: #333; color: #bbb; }
```

- [ ] **Step 4: Derle**

Run: `cd frontend && npm run build`
Expected: TS hatası yok.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/UsageBadge.tsx frontend/src/components/TerminalPane.tsx frontend/src/styles/globals.css
git commit -m "feat(frontend): usage badge in terminal pane header (#10)"
```

---

### Task 12: `frontend` — `UsagePanelModal` + TabBar 📊

**Files:**
- Create: `frontend/src/components/UsagePanelModal.tsx`
- Modify: `frontend/src/components/TabBar.tsx` (📊 buton + modal render)

**Interfaces:**
- Consumes: `useAllUsage` (Task 10), `useTerminals` (agent/CLI adları)
- Produces: `<UsagePanelModal onClose={...} />` — tablo: Agent · CLI · Durum · Reset · Model.

- [ ] **Step 1: `UsagePanelModal.tsx` oluştur** (SettingsModal iskeleti + tablo)

```tsx
import { useAllUsage } from "../store/useUsage";
import { UsageSnapshot } from "../lib/types";

const STATUS_LABEL = ["—", "🟢 OK", "🟡 Uyarı", "🔴 Kritik"];

function resetCell(s: UsageSnapshot): string {
  const w = s.primary ?? s.secondary;
  if (!w || !w.resetsAt) return "—";
  return new Date(w.resetsAt * 1000).toLocaleString();
}

export default function UsagePanelModal({ onClose }: { onClose: () => void }) {
  const entries = useAllUsage();
  const rows = Object.values(entries);
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="usage-title" onClick={(e) => e.stopPropagation()}>
        <h3 id="usage-title">📊 Kullanım Paneli</h3>
        {rows.length === 0 ? (
          <p className="form-hint">Henüz usage sinyali yok. Agent'lar çalıştıkça burada görünecek.</p>
        ) : (
          <table className="usage-table">
            <thead>
              <tr><th>CLI</th><th>Durum / %</th><th>Reset</th><th>Model</th></tr>
            </thead>
            <tbody>
              {rows.map((ev) => {
                const s = ev.snapshot;
                const pct = s.kind === 1
                  ? `%${Math.max(...[s.primary, s.secondary].filter(Boolean).map((w) => (w as any).usedPercent)).toFixed(0)}`
                  : ((s.inputTokens ?? 0) + (s.outputTokens ?? 0)) + " tok";
                return (
                  <tr key={s.sessionID}>
                    <td>{s.cli}</td>
                    <td>{s.kind === 1 ? `${STATUS_LABEL[ev.status]} ${pct}` : pct}</td>
                    <td>{resetCell(s)}</td>
                    <td>{s.model || "—"}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
        <div className="modal-actions">
          <button className="btn btn-secondary" onClick={onClose}>Kapat</button>
        </div>
      </div>
    </div>
  );
}
```

`globals.css`'e: `.usage-table { width:100%; border-collapse:collapse; font-size:13px; } .usage-table th,.usage-table td { text-align:left; padding:4px 8px; border-bottom:1px solid #333; }`

- [ ] **Step 2: TabBar'a 📊 buton + modal** — state ekle (`const [showUsage, setShowUsage] = useState(false)`), toolbar'a buton, koşullu render:

```tsx
<button className="tab-add" title="Kullanım paneli" aria-label="Kullanım paneli" onClick={() => setShowUsage(true)}>📊</button>
...
{showUsage && <UsagePanelModal onClose={() => setShowUsage(false)} />}
```
(`import UsagePanelModal from "./UsagePanelModal";` + `useState` import.)

- [ ] **Step 3: Derle**

Run: `cd frontend && npm run build`
Expected: TS hatası yok.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/UsagePanelModal.tsx frontend/src/components/TabBar.tsx frontend/src/styles/globals.css
git commit -m "feat(frontend): usage panel modal + TabBar button (#10)"
```

---

### Task 13: `frontend` — geçiş diyaloğu + SettingsModal eşik alanı

**Files:**
- Create: `frontend/src/components/SwitchDialog.tsx`
- Modify: `frontend/src/components/TerminalPane.tsx` (warn/critical/limit-hit → diyalog tetik + buton), `frontend/src/components/SettingsModal.tsx` (eşik alanı)

**Interfaces:**
- Consumes: `useUsageFor`, `useUsage` limitHits (Task 10), `SwitchTerminal`, `GetUsageThresholds`, `SetUsageThresholds` (Task 9), `useTerminals` (kurulu CLI'lar)
- Produces: `<SwitchDialog sessionID currentCLI onClose />` — hedef dropdown + onay → `SwitchTerminal`.

- [ ] **Step 1: `SwitchDialog.tsx` oluştur** (SettingsModal iskeleti)

```tsx
import { useState } from "react";
import { SwitchTerminal } from "../../wailsjs/go/main/App";
import { CLIType } from "../lib/types";

const ALL_TARGETS: CLIType[] = ["codex", "claude", "copilot", "gemini"];

export default function SwitchDialog({
  sessionID, currentCLI, onClose,
}: { sessionID: string; currentCLI: CLIType; onClose: () => void }) {
  const targets = ALL_TARGETS.filter((c) => c !== currentCLI);
  const [target, setTarget] = useState<CLIType>(targets[0]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const doSwitch = async () => {
    setBusy(true); setErr("");
    try { await SwitchTerminal(sessionID, target); onClose(); }
    catch (e) { setErr(String(e)); setBusy(false); }
  };
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <h3>⚠️ CLI Geçişi</h3>
        <p className="form-hint">
          '{currentCLI}' limitine yaklaşıyor. Aynı slotta yeni CLI ile devam et — oda geçmişi ve devralma notu yeni agent'a aktarılır (cross-CLI resume mümkün değil, yeni oturum başlar).
        </p>
        <div className="form-group">
          <label htmlFor="switch-target">Hedef CLI</label>
          <select id="switch-target" value={target} onChange={(e) => setTarget(e.target.value as CLIType)}>
            {targets.map((c) => <option key={c} value={c}>{c}</option>)}
          </select>
        </div>
        {err && <p className="form-error">{err}</p>}
        <div className="modal-actions">
          <button className="btn" onClick={doSwitch} disabled={busy}>{busy ? "Geçiliyor…" : "Geçişi onayla"}</button>
          <button className="btn btn-secondary" onClick={onClose} disabled={busy}>Yoksay</button>
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: TerminalPane — geçiş butonu + otomatik tetik**

`terminal-header-actions` içine (resume/restart yanına) bir buton ekle; usage kritik veya limit-hit olduğunda görünür kıl:

```tsx
const usageEv = useUsageFor(sessionID);
const limitHit = useUsage((s) => s.limitHits[sessionID]);
const showSwitchSuggest = usageEv?.status === 3 || !!limitHit;
const [switching, setSwitching] = useState(false);
...
{cliType && cliType !== "shell" && (
  <button
    className={`term-action-btn ${showSwitchSuggest ? "attention" : ""}`}
    title="CLI geçişi (limit dolduğunda)"
    onClick={() => setSwitching(true)}
  >🔄</button>
)}
...
{switching && (
  <SwitchDialog sessionID={sessionID} currentCLI={cliType as CLIType} onClose={() => setSwitching(false)} />
)}
```
(`import SwitchDialog from "./SwitchDialog"; import { useUsage, useUsageFor } from "../store/useUsage"; import { CLIType } from "../lib/types";` + `useState`.) `.term-action-btn.attention { animation: pulse 1s infinite; }` CSS'i opsiyonel; kritik durumda rozet zaten kırmızı.

- [ ] **Step 3: SettingsModal eşik alanı** — yeni `form-group` (deferral toggle yanına):

```tsx
import { GetUsageThresholds, SetUsageThresholds } from "../../wailsjs/go/main/App";
...
const [warn, setWarn] = useState(85);
const [crit, setCrit] = useState(95);
useEffect(() => {
  GetUsageThresholds().then((t: any) => { setWarn(t.warnPercent ?? 85); setCrit(t.criticalPercent ?? 95); }).catch(() => {});
}, []);
const saveThresholds = async (w: number, c: number) => {
  setWarn(w); setCrit(c);
  try { await SetUsageThresholds(w, c); } catch (e) { setError(String(e)); }
};
...
<div className="form-group">
  <label>Usage uyarı eşikleri (Codex %)</label>
  <div style={{ display: "flex", gap: 8 }}>
    <label>Uyarı <input type="number" min={1} max={100} value={warn} onChange={(e) => saveThresholds(Number(e.target.value), crit)} /></label>
    <label>Kritik <input type="number" min={1} max={100} value={crit} onChange={(e) => saveThresholds(warn, Number(e.target.value))} /></label>
  </div>
  <p className="form-hint">Codex limitin bu yüzdesine ulaşınca rozet sararır/kızarır ve geçiş önerilir.</p>
</div>
```

- [ ] **Step 4: Derle**

Run: `cd frontend && npm run build`
Expected: TS hatası yok.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/SwitchDialog.tsx frontend/src/components/TerminalPane.tsx frontend/src/components/SettingsModal.tsx
git commit -m "feat(frontend): CLI switch dialog + settings thresholds (#10)"
```

---

### Task 14: Tam doğrulama + iç adversarial review

**Files:** (yok — doğrulama)

- [ ] **Step 1: Tam backend doğrulama**

Run:
```bash
GOFLAGS=-mod=readonly make mcp-server && GOFLAGS=-mod=readonly go build ./...
GOFLAGS=-mod=readonly go test ./... && GOFLAGS=-mod=readonly go vet ./... && gofmt -l .
```
Expected: tüm testler PASS, vet temiz, `gofmt -l .` boş, `go.mod` diff yok (`git diff --stat go.mod` boş).

- [ ] **Step 2: Frontend build**

Run: `cd frontend && npm run build`
Expected: hatasız.

- [ ] **Step 3: `go.mod` churn kontrolü**

Run: `git diff --stat go.mod go.sum`
Expected: boş çıktı (readonly sayesinde). Değilse `git checkout go.mod go.sum`.

- [ ] **Step 4: İç adversarial review** — diff'i taze bağlamda §2 hedefi + §3 kısıtları + §6 kabul kriterlerine karşı denetlet (correctness + gereksinim boşlukları; stil/over-engineering DEĞİL). Bulguları koddan doğrula, gerçek olanları düzelt.

- [ ] **Step 5: PR öncesi hazırlık** — spec+plan güncel mi, beklenmeyen dosya/patch farkı yok mu (özellikle `go.mod`), son commit yeşil mi.

---

## Self-Review Notları

- **Spec kapsamı:** A(hibrit sinyal)=Task 3/4/8-reaktif; B1(handoff)=Task 6/8; B2(hedef seçimi)=Task 13; C(eşik-uyarı+tek-tık)=Task 8/13; D(rozet+modal)=Task 11/12; E(tek PR)=tüm task'lar tek branch. Kalan-açık: eşikler=Task 7/13, in-slot replace=Task 8, PTY best-effort=Task 2/8.
- **Tip tutarlılığı:** `usage.Snapshot`/`Window`/`Kind`/`Status`/`Thresholds` Task 1'de tanımlı; Task 3-8 aynı isimleri kullanır; frontend `UsageSnapshot` (Task 9) alan adları JSON tag'leriyle (`usedPercent`,`resetsAt`,`inputTokens`…) birebir eşleşir.
- **İmza zinciri:** `ComposeStartupPrompt`(+handoffFrom, Task 6) ← `composeAgentPrompt`(+handoffFrom) ← `sendStartupPrompt`(+handoffFrom) ← `createTerminal`(+handoffFrom) → `SwitchTerminal` (Task 8) `agentName`'i handoffFrom olarak geçer; `CreateTerminal`/`restartInternal`/`openTeamFromConfig` boş geçer.
- **StartSession imzası:** Task 5 `onUsage` ekler; app.go + mevcut watcher testleri güncellenir (nil-safe).
