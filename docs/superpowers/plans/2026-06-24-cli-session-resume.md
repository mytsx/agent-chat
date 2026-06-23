# CLI Session Resume (#40) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Terminal çalışırken her AI-CLI'ın kendi session ID'sini ingest discovery ile deterministik yakala, PTYSession'da in-memory sakla; kullanıcı opt-in "Devam Et" düğmesine basınca terminali `--resume <id>` ile yeniden başlat (Claude/Copilot/Codex).

**Architecture:** `internal/ingest`'in (#65) per-CLI session-dosyası keşfini paylaş; keşfedilen yoldan/içerikten session ID'yi çıkar (`SessionAdapter.SessionID`). Watcher id'yi bir callback ile app'e iter; app PTYSession'da saklar + frontend'e event yayar. "Devam Et" → `ResumeTerminal` → `createTerminal(..., resumeID)` → `cli.GetCommandResume` ile CLI'a özel resume komutu (Codex subcommand, diğerleri flag).

**Tech Stack:** Go 1.25.5 (modül adı `desktop`), Wails v2.11.0, React 18 + TS + Zustand + Vite, xterm.js.

## Global Constraints

- Go modül adı `desktop` (URL-tabanlı değil); import: `desktop/internal/...`.
- **Embed şartı:** `go build`/`go vet`/`go test` öncesi `make mcp-server` çalışmış olmalı (`app.go` `//go:embed build/mcp-server-bin`). Her Go-test adımından önce binary mevcut olmalı.
- Agent-facing/UX metinleri **Türkçe + emoji**.
- Testler **table-driven** `t.Run()` alt-testleri.
- `last_seen` alanları `float64` Unix (bu PR'da dokunulmuyor).
- Frontend'de test runner yok → frontend doğrulaması `cd frontend && npm run build` (typecheck) + native test.
- Build doğrulama sırası: `make mcp-server` → `go test ./...` → `go vet ./...` → `gofmt -l` (boş çıktı) → frontend `npm run build` → `make dev` (native, kullanıcı).
- Branch: `feat/cli-session-resume-40` (zaten açık; spec orada commit'li).
- Kapsam: Faz-1 yalnız (in-memory, restart). Gemini bu turda yalnız **yakalama** (resume komutu yok). Disk persistence / app-restart restore = Faz-2 (bu PR'da YOK).

---

## File Structure

| Dosya | Sorumluluk | Create/Modify |
|-------|-----------|---------------|
| `internal/ingest/ingest.go` | `SessionAdapter`'a `SessionID` metodu | Modify |
| `internal/ingest/adapter_claude.go` | claude SessionID (dosya adı kökü) | Modify |
| `internal/ingest/adapter_copilot.go` | copilot SessionID (üst dizin) | Modify |
| `internal/ingest/adapter_codex.go` | codex SessionID (`codexFileID`) | Modify |
| `internal/ingest/adapter_gemini.go` | gemini SessionID (`sessionId` alanı) | Modify |
| `internal/ingest/sessionid_test.go` | SessionID table-driven testleri | Create |
| `internal/ingest/watcher.go` | `StartSession`/`run` `onSessionID` param + fire | Modify |
| `internal/ingest/watcher_test.go` | `fakeAdapter.SessionID` + onSessionID testi | Modify |
| `internal/pty/manager.go` | `PTYSession.cliSessionID` + `Set/GetCLISessionID` | Modify |
| `internal/pty/manager_resume_test.go` | Set/Get round-trip + race testi | Create |
| `internal/cli/detector.go` | `ResumeSupported` + `GetCommandResume` | Modify |
| `internal/cli/detector_resume_test.go` | resume komut kurucusu testleri | Create |
| `app.go` | `createTerminal(resumeID)` refactor, `ResumeTerminal`, `restartInternal`, StartSession callback + event | Modify |
| `frontend/src/lib/types.ts` | `TerminalSession.cliSessionID?` | Modify |
| `frontend/src/store/useTerminals.ts` | `resumeTerminal` + `setCLISessionID` action | Modify |
| `frontend/src/App.tsx` | `terminal:resume-available` event dinleyici | Modify |
| `frontend/src/components/TerminalPane.tsx` | "Devam Et" düğmesi + `onResume` prop | Modify |
| `frontend/src/components/TerminalGrid.tsx` | `onResume` wiring (3 çağrı noktası) | Modify |
| `frontend/src/styles/globals.css` | `.terminal-btn-resume` stili | Modify |
| `frontend/wailsjs/go/main/App.{d.ts,js}` | `ResumeTerminal` binding (regen) | Modify |

---

## Task 1: ingest — SessionID çıkarımı (4 adapter + interface)

**Files:**
- Create: `internal/ingest/sessionid_test.go`
- Modify: `internal/ingest/ingest.go`, `internal/ingest/adapter_claude.go`, `internal/ingest/adapter_copilot.go`, `internal/ingest/adapter_codex.go`, `internal/ingest/adapter_gemini.go`, `internal/ingest/watcher_test.go`

**Interfaces:**
- Produces: `SessionAdapter.SessionID(path string) string` (interface metodu). Adapter implementasyonları: `claudeAdapter`, `copilotAdapter`, `codexAdapter`, `geminiAdapter`. Helper `codexFileID(path string) string`.

- [ ] **Step 1: Failing test yaz — `internal/ingest/sessionid_test.go`**

```go
package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionID(t *testing.T) {
	dir := t.TempDir()

	// codex: session_meta first line with payload.id
	codexPath := filepath.Join(dir, "rollout-2026-06-23T20-34-23-019ef58c-27d5-7e43-9902-8a02b5517bf1.jsonl")
	codexLine := `{"timestamp":"2026-06-23T20:34:23.000Z","type":"session_meta","payload":{"id":"019ef58c-27d5-7e43-9902-8a02b5517bf1","cwd":"/x"}}` + "\n"
	if err := os.WriteFile(codexPath, []byte(codexLine), 0644); err != nil {
		t.Fatal(err)
	}

	// gemini: monolithic JSON with top-level sessionId
	geminiPath := filepath.Join(dir, "session-2026-02-22T10-49-65a26031.json")
	geminiBody := `{"sessionId":"65a26031-dcf1-4a40-aff0-42b7d84dc7b4","messages":[]}`
	if err := os.WriteFile(geminiPath, []byte(geminiBody), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		ad   SessionAdapter
		path string
		want string
	}{
		{"claude filename stem", claudeAdapter{}, "/home/u/.claude/projects/-x/5c6e4e64-3305-45b3-a70e-f0a97a974ab2.jsonl", "5c6e4e64-3305-45b3-a70e-f0a97a974ab2"},
		{"claude empty path", claudeAdapter{}, "", ""},
		{"copilot parent dir", copilotAdapter{}, "/home/u/.copilot/session-state/c96cde26-3b35-4a82-b1e9-c40747f9346e/events.jsonl", "c96cde26-3b35-4a82-b1e9-c40747f9346e"},
		{"copilot empty path", copilotAdapter{}, "", ""},
		{"codex session_meta id", codexAdapter{}, codexPath, "019ef58c-27d5-7e43-9902-8a02b5517bf1"},
		{"codex missing file", codexAdapter{}, filepath.Join(dir, "nope.jsonl"), ""},
		{"gemini sessionId field", geminiAdapter{}, geminiPath, "65a26031-dcf1-4a40-aff0-42b7d84dc7b4"},
		{"gemini missing file", geminiAdapter{}, filepath.Join(dir, "nope.json"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ad.SessionID(tt.path); got != tt.want {
				t.Errorf("SessionID(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Test'i çalıştır, derleme hatasıyla başarısız olduğunu doğrula**

Run: `make mcp-server && go test ./internal/ingest/ -run TestSessionID`
Expected: FAIL — `claudeAdapter.SessionID undefined` (ve diğerleri).

- [ ] **Step 3: `claudeAdapter.SessionID` ekle — `internal/ingest/adapter_claude.go`**

Dosyanın sonuna (son `}`'tan sonra) ekle:

```go

// SessionID extracts Claude's session UUID from a discovered file path
// ({uuid}.jsonl → uuid), or "" for an empty path (#40).
func (claudeAdapter) SessionID(path string) string {
	if path == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}
```

(`strings` ve `path/filepath` zaten import'lu.)

- [ ] **Step 4: `copilotAdapter.SessionID` ekle — `internal/ingest/adapter_copilot.go`**

`copilotWorkspaceCwd`'den önce (veya dosya sonuna) ekle:

```go

// SessionID extracts Copilot's session UUID from a discovered events.jsonl path
// ({uuid}/events.jsonl → {uuid}), or "" for an empty path (#40).
func (copilotAdapter) SessionID(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(filepath.Dir(path))
}
```

(`path/filepath` zaten import'lu.)

- [ ] **Step 5: `codexAdapter.SessionID` + `codexFileID` ekle — `internal/ingest/adapter_codex.go`**

`codexFileCwd`'den sonra ekle:

```go

// SessionID extracts Codex's session UUID from a rollout's session_meta first
// line (payload.id) (#40). The filename embeds the uuid too, but its leading
// timestamp also contains '-', so reading session_meta is robust where
// filename-splitting is fragile.
func (codexAdapter) SessionID(path string) string {
	return codexFileID(path)
}

// codexFileID returns the session id recorded in a rollout's first line
// (session_meta.payload.id), or "" if it can't be read.
func codexFileID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadBytes('\n')
	var meta struct {
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(line, &meta)
	return meta.Payload.ID
}
```

(`bufio`, `encoding/json`, `os` zaten import'lu.)

- [ ] **Step 6: `geminiAdapter.SessionID` ekle — `internal/ingest/adapter_gemini.go`**

Dosya sonuna ekle:

```go

// SessionID extracts Gemini's session UUID from its monolithic JSON record's
// top-level sessionId field (#40). The filename carries only an 8-hex prefix, so
// the file must be read. Not wired to a resume command this round (Gemini
// resume-by-id unverified) — capture-only.
func (geminiAdapter) SessionID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var gf struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(data, &gf) != nil {
		return ""
	}
	return gf.SessionID
}
```

(`encoding/json`, `os` zaten import'lu.)

- [ ] **Step 7: Interface'e `SessionID` ekle — `internal/ingest/ingest.go`**

`SessionAdapter` interface'inde `ParseNewUserMessages` satırından sonra ekle:

```go
	// SessionID extracts the CLI's own session/conversation ID from a discovered
	// session-file path, or "" if it can't be determined. Used to resume the CLI
	// from this session on a later restart (#40).
	SessionID(path string) string
```

- [ ] **Step 8: `fakeAdapter`'a `SessionID` ekle — `internal/ingest/watcher_test.go`**

`fakeAdapter` struct'ına `sessID` alanı ekle ve metot ekle. Struct'ı:

```go
type fakeAdapter struct {
	batches [][]ParsedMessage
	calls   int
	sessID  string
}
```

`DiscoverFile` metodundan sonra ekle:

```go
func (f *fakeAdapter) SessionID(string) string { return f.sessID }
```

- [ ] **Step 9: Testleri çalıştır, geçtiğini doğrula**

Run: `make mcp-server && go test ./internal/ingest/`
Expected: PASS (TestSessionID dahil tüm ingest testleri).

- [ ] **Step 10: vet + gofmt + commit**

```bash
go vet ./internal/ingest/ && gofmt -l internal/ingest/
git add internal/ingest/
git commit -m "feat: #40 ingest SessionAdapter.SessionID — CLI session ID çıkarımı

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

Expected: `gofmt -l` boş çıktı; commit başarılı.

---

## Task 2: pty — PTYSession.cliSessionID + Set/Get

**Files:**
- Modify: `internal/pty/manager.go`
- Create: `internal/pty/manager_resume_test.go`

**Interfaces:**
- Produces: `(*Manager).SetCLISessionID(sessionID, id string)`, `(*Manager).GetCLISessionID(sessionID string) string`. Yeni alan `PTYSession.cliSessionID atomic.Pointer[string]` (unexported).

- [ ] **Step 1: Failing test yaz — `internal/pty/manager_resume_test.go`**

```go
package pty

import (
	"sync"
	"testing"
)

func TestCLISessionID_RoundTrip(t *testing.T) {
	m := NewManager(nil)
	// Inject a session directly (no real PTY needed for the field).
	m.sessions["s1"] = &PTYSession{ID: "s1"}

	if got := m.GetCLISessionID("s1"); got != "" {
		t.Fatalf("initial GetCLISessionID = %q, want empty", got)
	}
	m.SetCLISessionID("s1", "uuid-123")
	if got := m.GetCLISessionID("s1"); got != "uuid-123" {
		t.Fatalf("GetCLISessionID = %q, want uuid-123", got)
	}
}

func TestCLISessionID_UnknownSession(t *testing.T) {
	m := NewManager(nil)
	m.SetCLISessionID("ghost", "x") // must not panic
	if got := m.GetCLISessionID("ghost"); got != "" {
		t.Fatalf("unknown session GetCLISessionID = %q, want empty", got)
	}
}

func TestCLISessionID_ConcurrentAccess(t *testing.T) {
	m := NewManager(nil)
	m.sessions["s1"] = &PTYSession{ID: "s1"}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); m.SetCLISessionID("s1", "uuid-x") }()
		go func() { defer wg.Done(); _ = m.GetCLISessionID("s1") }()
	}
	wg.Wait()
	if got := m.GetCLISessionID("s1"); got != "uuid-x" {
		t.Fatalf("final GetCLISessionID = %q, want uuid-x", got)
	}
}
```

- [ ] **Step 2: Test'i çalıştır, başarısız olduğunu doğrula**

Run: `go test ./internal/pty/ -run TestCLISessionID -race`
Expected: FAIL — `m.SetCLISessionID undefined`.

- [ ] **Step 3: `cliSessionID` alanını ekle — `internal/pty/manager.go`**

`PTYSession` struct'ında `lastUserInputNano atomic.Int64` satırından sonra (writeMu'dan önce) ekle:

```go
	// cliSessionID holds the CLI's own session/conversation ID captured by the
	// ingest watcher, used to resume this terminal on restart (#40). Set from the
	// ingest goroutine, read from the restart goroutine — hence atomic.
	cliSessionID atomic.Pointer[string]
```

- [ ] **Step 4: Set/Get metotlarını ekle — `internal/pty/manager.go`**

Dosya sonuna (uygun bir yere, örn. `GetSession` yakınına) ekle:

```go

// SetCLISessionID records the CLI session ID for a terminal (#40). No-op for an
// unknown session.
func (m *Manager) SetCLISessionID(sessionID, id string) {
	m.mu.RLock()
	s := m.sessions[sessionID]
	m.mu.RUnlock()
	if s != nil {
		s.cliSessionID.Store(&id)
	}
}

// GetCLISessionID returns the captured CLI session ID for a terminal, or "" if
// none was captured or the session is unknown (#40).
func (m *Manager) GetCLISessionID(sessionID string) string {
	m.mu.RLock()
	s := m.sessions[sessionID]
	m.mu.RUnlock()
	if s == nil {
		return ""
	}
	if p := s.cliSessionID.Load(); p != nil {
		return *p
	}
	return ""
}
```

- [ ] **Step 5: Testleri -race ile çalıştır, geçtiğini doğrula**

Run: `go test ./internal/pty/ -run TestCLISessionID -race`
Expected: PASS, race yok.

- [ ] **Step 6: vet + gofmt + commit**

```bash
go vet ./internal/pty/ && gofmt -l internal/pty/
git add internal/pty/
git commit -m "feat: #40 PTYSession.cliSessionID + Set/GetCLISessionID

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: cli — ResumeSupported + GetCommandResume

**Files:**
- Modify: `internal/cli/detector.go`
- Create: `internal/cli/detector_resume_test.go`

**Interfaces:**
- Consumes: `GetCommand(cliType CLIType) (string, []string)`, sabitler `CLIClaude/CLICopilot/CLICodex/CLIGemini/CLIShell`.
- Produces: `ResumeSupported(cliType CLIType) bool`, `GetCommandResume(cliType CLIType, sessionID string) (string, []string)`.

- [ ] **Step 1: Failing test yaz — `internal/cli/detector_resume_test.go`**

```go
package cli

import (
	"reflect"
	"testing"
)

func TestResumeSupported(t *testing.T) {
	tests := []struct {
		cliType CLIType
		want    bool
	}{
		{CLIClaude, true},
		{CLICopilot, true},
		{CLICodex, true},
		{CLIGemini, false},
		{CLIShell, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.cliType), func(t *testing.T) {
			if got := ResumeSupported(tt.cliType); got != tt.want {
				t.Errorf("ResumeSupported(%s) = %v, want %v", tt.cliType, got, tt.want)
			}
		})
	}
}

func TestGetCommandResume(t *testing.T) {
	const id = "7f32dcf3-11c6-4ca1-9461-fe8590e164e0"
	tests := []struct {
		name     string
		cliType  CLIType
		id       string
		wantCmd  string
		wantArgs []string
	}{
		{"claude flag", CLIClaude, id, "claude", []string{"--resume", id, "--dangerously-skip-permissions"}},
		{"copilot eq-syntax", CLICopilot, id, "copilot", []string{"--resume=" + id, "--yolo"}},
		{"codex subcommand-first", CLICodex, id, "codex", []string{"resume", id, "--dangerously-bypass-approvals-and-sandbox"}},
		// Unsupported CLI → fresh GetCommand (Gemini stays a normal launch this round).
		{"gemini falls back to fresh", CLIGemini, id, "gemini", []string{"--approval-mode", "yolo"}},
		// Empty id → fresh GetCommand even for a supported CLI.
		{"claude empty id falls back", CLIClaude, "", "claude", []string{"--dangerously-skip-permissions"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCmd, gotArgs := GetCommandResume(tt.cliType, tt.id)
			if gotCmd != tt.wantCmd {
				t.Errorf("cmd = %q, want %q", gotCmd, tt.wantCmd)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}
```

- [ ] **Step 2: Test'i çalıştır, başarısız olduğunu doğrula**

Run: `go test ./internal/cli/ -run 'TestResumeSupported|TestGetCommandResume'`
Expected: FAIL — `ResumeSupported undefined`.

- [ ] **Step 3: Fonksiyonları ekle — `internal/cli/detector.go`**

`GetCommand` fonksiyonundan sonra dosya sonuna ekle:

```go

// ResumeSupported reports whether GetCommandResume can build a native resume
// invocation for cliType. Claude/Copilot/Codex are supported (resume-by-id
// empirically verified, 2026-06-24); Gemini/shell are not this round (#40).
func ResumeSupported(cliType CLIType) bool {
	switch cliType {
	case CLIClaude, CLICopilot, CLICodex:
		return true
	default:
		return false
	}
}

// GetCommandResume returns the command and args to resume cliType from sessionID.
// Codex's `resume` is a SUBCOMMAND (positional, first); the others use a flag
// (Copilot needs the `=` form). An unsupported cliType or empty sessionID falls
// back to a fresh GetCommand so callers never accidentally launch a broken
// resume (#40).
func GetCommandResume(cliType CLIType, sessionID string) (string, []string) {
	if sessionID == "" || !ResumeSupported(cliType) {
		return GetCommand(cliType)
	}
	switch cliType {
	case CLIClaude:
		return "claude", []string{"--resume", sessionID, "--dangerously-skip-permissions"}
	case CLICopilot:
		return "copilot", []string{"--resume=" + sessionID, "--yolo"}
	case CLICodex:
		return "codex", []string{"resume", sessionID, "--dangerously-bypass-approvals-and-sandbox"}
	default:
		return GetCommand(cliType)
	}
}
```

- [ ] **Step 4: Testleri çalıştır, geçtiğini doğrula**

Run: `go test ./internal/cli/ -run 'TestResumeSupported|TestGetCommandResume'`
Expected: PASS.

- [ ] **Step 5: vet + gofmt + commit**

```bash
go vet ./internal/cli/ && gofmt -l internal/cli/
git add internal/cli/
git commit -m "feat: #40 cli.ResumeSupported + GetCommandResume — CLI başına resume komutu

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: ingest watcher — onSessionID callback + app wiring

**Files:**
- Modify: `internal/ingest/watcher.go`, `internal/ingest/watcher_test.go`, `app.go`

**Interfaces:**
- Consumes: `SessionAdapter.SessionID` (Task 1), `(*Manager).SetCLISessionID` (Task 2), `cli.ResumeSupported` (Task 3).
- Produces: `(*Manager).StartSession(..., emit EmitFunc, onSessionID func(id string))` — yeni son parametre. Watcher dosyayı keşfedip claim ettiği an `onSessionID(ad.SessionID(path))`'i (boş değilse) bir kez çağırır.

- [ ] **Step 1: Failing test yaz — `internal/ingest/watcher_test.go`**

Import bloğuna `"time"` ekle (mevcut: `sync`, `testing`). Dosya sonuna ekle:

```go
func TestStartSession_FiresOnSessionID(t *testing.T) {
	m := New()
	ad := &fakeAdapter{sessID: "fake-id"}
	got := make(chan string, 1)
	m.StartSession("s1", ad, "cwd", 0, nil, nil,
		func(string, string) bool { return true },
		func(id string) {
			select {
			case got <- id:
			default:
			}
		})
	defer m.StopSession("s1")

	select {
	case id := <-got:
		if id != "fake-id" {
			t.Fatalf("onSessionID = %q, want fake-id", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onSessionID not fired within 2s")
	}
}
```

- [ ] **Step 2: Test'i çalıştır, başarısız olduğunu doğrula**

Run: `make mcp-server && go test ./internal/ingest/ -run TestStartSession_FiresOnSessionID`
Expected: FAIL — `StartSession` çağrısında argüman sayısı uyuşmuyor (derleme hatası).

- [ ] **Step 3: `StartSession` imzasına `onSessionID` ekle — `internal/ingest/watcher.go`**

`StartSession` fonksiyon imzasını değiştir:

```go
func (m *Manager) StartSession(sessionID string, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, ready func() bool, exited <-chan struct{}, emit EmitFunc, onSessionID func(id string)) {
```

Aynı fonksiyonun sonundaki `go m.run(...)` çağrısını güncelle:

```go
	go m.run(s, ad, cwd, spawnedAtUnixNano, ready, exited, emit, onSessionID)
```

- [ ] **Step 4: `run` imzasına `onSessionID` ekle ve keşifte tetikle — `internal/ingest/watcher.go`**

`run` imzasını değiştir:

```go
func (m *Manager) run(s *session, ad SessionAdapter, cwd string, spawnedAtUnixNano int64, ready func() bool, exited <-chan struct{}, emit EmitFunc, onSessionID func(id string)) {
```

`discoverAndPoll` içinde `path = p` satırından **hemen sonra** tetikleme ekle:

```go
			path = p
			// #40: surface the CLI's own session ID once, right after the file is
			// discovered+claimed, so the app can store it for opt-in resume.
			if onSessionID != nil {
				if id := ad.SessionID(path); id != "" {
					onSessionID(id)
				}
			}
```

- [ ] **Step 5: Testi çalıştır, geçtiğini doğrula**

Run: `go test ./internal/ingest/ -run TestStartSession_FiresOnSessionID`
Expected: PASS.

- [ ] **Step 6: app.go StartSession çağrısına callback ekle — `app.go`**

`app.go`'da `a.ingestMgr.StartSession(...)` çağrısının (≈satır 950) son `})`'ini, emit callback'inden sonra yeni callback ekleyecek şekilde güncelle. Mevcut çağrının kapanışı:

```go
			return true
		})
```

şu hale gelsin:

```go
			return true
		}, func(id string) {
			// #40: capture the CLI's session ID for opt-in resume. Only for CLIs
			// whose native resume we support — others (Gemini/shell) never enable
			// the "Devam Et" button.
			if !cli.ResumeSupported(ct) {
				return
			}
			a.ptyManager.SetCLISessionID(sessionID, id)
			runtime.EventsEmit(a.ctx, "terminal:resume-available", map[string]string{
				"sessionID":    sessionID,
				"cliSessionID": id,
			})
		})
```

(`ct` = `cli.CLIType(cliType)` zaten bu fonksiyonda tanımlı; `cli`, `runtime`, `a.ctx` zaten kullanımda.)

- [ ] **Step 7: Tüm Go testlerini + vet çalıştır**

Run: `make mcp-server && go test ./... && go vet ./...`
Expected: PASS, vet temiz.

- [ ] **Step 8: gofmt + commit**

```bash
gofmt -l internal/ingest/ app.go
git add internal/ingest/ app.go
git commit -m "feat: #40 watcher onSessionID callback — yakalanan ID'yi app'e ilet + event

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: app — createTerminal(resumeID) refactor + ResumeTerminal

**Files:**
- Modify: `app.go`

**Interfaces:**
- Consumes: `cli.GetCommandResume`, `cli.ResumeSupported` (Task 3), `(*Manager).GetCLISessionID` (Task 2).
- Produces: bound metot `(*App).ResumeTerminal(sessionID string) (string, error)`; iç `(*App).createTerminal(..., resumeID string)`, `(*App).restartInternal(sessionID, resumeID string)`.

- [ ] **Step 1: `CreateTerminal`'ı ince sarmalayıcı yap + iç `createTerminal` — `app.go`**

Mevcut imza satırını (≈742):

```go
func (a *App) CreateTerminal(teamID, agentName, workDir, cliType, promptID string, useWorktree bool, slotIndex int) (string, error) {
```

şununla değiştir (yeni wrapper + iç fonksiyon başlığı):

```go
// CreateTerminal creates a new terminal and returns its session ID. Exported
// signature is unchanged (Wails binding stable); it delegates to createTerminal
// with no resume ID (fresh launch).
func (a *App) CreateTerminal(teamID, agentName, workDir, cliType, promptID string, useWorktree bool, slotIndex int) (string, error) {
	return a.createTerminal(teamID, agentName, workDir, cliType, promptID, useWorktree, slotIndex, "")
}

// createTerminal is the implementation. A non-empty resumeID with a resume-capable
// CLI launches via cli.GetCommandResume (--resume <id>); otherwise a fresh
// cli.GetCommand. Everything else (worktree, MCP config, ingest, startup prompt)
// is identical to a fresh create (#40).
func (a *App) createTerminal(teamID, agentName, workDir, cliType, promptID string, useWorktree bool, slotIndex int, resumeID string) (string, error) {
```

- [ ] **Step 2: Komut seçim dalını resume-aware yap — `app.go`**

Mevcut satırı (≈828):

```go
	// Get command for CLI type
	cmdName, cmdArgs := cli.GetCommand(ct)
```

şununla değiştir:

```go
	// Get command for CLI type. #40: when resuming (resumeID set + CLI supports it)
	// build the resume invocation instead of a fresh launch. Everything downstream
	// (Copilot -i, startup prompt, ingest) is unchanged so the resumed agent still
	// re-joins the room.
	var cmdName string
	var cmdArgs []string
	if resumeID != "" && cli.ResumeSupported(ct) {
		cmdName, cmdArgs = cli.GetCommandResume(ct, resumeID)
		log.Printf("[RESUME] agent=%s cli=%s id=%s", agentName, cliType, resumeID)
	} else {
		cmdName, cmdArgs = cli.GetCommand(ct)
	}
```

- [ ] **Step 3: `RestartTerminal`'ı `restartInternal`'a çıkar + `ResumeTerminal` ekle — `app.go`**

Mevcut `RestartTerminal` imzasını (≈984):

```go
func (a *App) RestartTerminal(sessionID string) (string, error) {
```

şununla değiştir (wrapper + ResumeTerminal + iç fonksiyon başlığı):

```go
// RestartTerminal closes a terminal and creates a fresh one with the same
// parameters (no resume).
func (a *App) RestartTerminal(sessionID string) (string, error) {
	return a.restartInternal(sessionID, "")
}

// ResumeTerminal restarts a terminal resuming its captured CLI session (#40). If
// nothing was captured (the CLI hasn't written its session file yet, or it is an
// unsupported CLI), it falls back to a fresh restart so the user still gets a
// working terminal.
func (a *App) ResumeTerminal(sessionID string) (string, error) {
	resumeID := a.ptyManager.GetCLISessionID(sessionID)
	cliType := ""
	if s := a.ptyManager.GetSession(sessionID); s != nil {
		cliType = s.CLIType
	}
	if resumeID == "" || !cli.ResumeSupported(cli.CLIType(cliType)) {
		log.Printf("[RESUME] session=%s — yakalı oturum yok, düz restart", ptymgr.ShortID(sessionID))
		return a.restartInternal(sessionID, "")
	}
	return a.restartInternal(sessionID, resumeID)
}

// restartInternal closes a terminal and recreates it, optionally resuming from
// resumeID. If the terminal was using a worktree, the worktree is preserved.
func (a *App) restartInternal(sessionID, resumeID string) (string, error) {
```

- [ ] **Step 4: `restartInternal` içinde recreate çağrısını resume-aware yap — `app.go`**

`restartInternal` (eski RestartTerminal) gövdesindeki recreate satırını (≈1028):

```go
	newSessionID, err := a.CreateTerminal(teamID, agentName, workDir, cliType, promptID, false, slotIndex)
```

şununla değiştir:

```go
	newSessionID, err := a.createTerminal(teamID, agentName, workDir, cliType, promptID, false, slotIndex, resumeID)
```

- [ ] **Step 5: Derle, tüm testleri + vet çalıştır**

Run: `make mcp-server && go build ./... && go test ./... && go vet ./...`
Expected: PASS. (Bu Wails-bound orchestration metodu PTY/Wails gerektirdiğinden birim-test edilmez; komut seçimi Task 3'te saf `GetCommandResume` ile test edilir, uçtan-uca davranış `make dev` native testte doğrulanır.)

- [ ] **Step 6: Wails binding'lerini regen et**

Run: `wails generate module`
Expected: `frontend/wailsjs/go/main/App.d.ts` ve `App.js` içinde `ResumeTerminal` görünür.

Doğrula: `grep -n "ResumeTerminal" frontend/wailsjs/go/main/App.d.ts`
Expected: `export function ResumeTerminal(arg1:string):Promise<string>;`

- [ ] **Step 7: gofmt + commit**

```bash
gofmt -l app.go
git add app.go frontend/wailsjs/
git commit -m "feat: #40 ResumeTerminal + createTerminal(resumeID) — opt-in resume backend

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 6: frontend — types + store + event listener

**Files:**
- Modify: `frontend/src/lib/types.ts`, `frontend/src/store/useTerminals.ts`, `frontend/src/App.tsx`

**Interfaces:**
- Consumes: Wails `ResumeTerminal` binding (Task 5), event `terminal:resume-available` {sessionID, cliSessionID} (Task 4).
- Produces: `TerminalSession.cliSessionID?: string`; store actions `resumeTerminal(teamID, sessionID) => Promise<string>`, `setCLISessionID(sessionID, cliSessionID) => void`.

- [ ] **Step 1: `TerminalSession`'a `cliSessionID` ekle — `frontend/src/lib/types.ts`**

`TerminalSession` interface'ine (≈satır 83) alan ekle:

```ts
export interface TerminalSession {
  sessionID: string;
  teamID: string;
  agentName: string;
  cliType: CLIType;
  index: number;
  slotIndex: number;
  cliSessionID?: string; // captured CLI session ID for opt-in resume (#40)
}
```

- [ ] **Step 2: `resumeTerminal` + `setCLISessionID` action'larını ekle — `frontend/src/store/useTerminals.ts`**

Önce import bloğuna `ResumeTerminal`'ı ekle (mevcut `RestartTerminal` import'unun yanına, ≈satır 8):

```ts
  ResumeTerminal,
```

Store tip tanımına (`restartTerminal` satırının yanına, ≈satır 46-47) ekle:

```ts
  resumeTerminal: (teamID: string, sessionID: string) => Promise<string>;
  setCLISessionID: (sessionID: string, cliSessionID: string) => void;
```

`restartTerminal` implementasyonunun (≈satır 201) hemen ardından, aynı session-değişim mantığını paylaşan implementasyonu ekle. Önce mevcut `restartTerminal`'ın gövdesini incele; `resumeTerminal` onunla aynıdır, yalnız `RestartTerminal` yerine `ResumeTerminal` çağırır:

```ts
  resumeTerminal: async (teamID, sessionID) => {
    const sessions = get().sessions[teamID] || [];
    const session = sessions.find((s) => s.sessionID === sessionID);
    if (!session) {
      console.error(`[resumeTerminal] session ${sessionID} not found in team ${teamID}`);
      return "";
    }
    const newSessionID = await ResumeTerminal(sessionID);
    set((state) => ({
      sessions: {
        ...state.sessions,
        [teamID]: (state.sessions[teamID] || []).map((s) =>
          s.sessionID === sessionID ? { ...s, sessionID: newSessionID, cliSessionID: undefined } : s
        ),
      },
    }));
    return newSessionID;
  },
  setCLISessionID: (sessionID, cliSessionID) => {
    set((state) => {
      const next: Record<string, TerminalSession[]> = {};
      for (const [tid, list] of Object.entries(state.sessions)) {
        next[tid] = list.map((s) =>
          s.sessionID === sessionID ? { ...s, cliSessionID } : s
        );
      }
      return { sessions: next };
    });
  },
```

> **Not:** `restartTerminal`'ın gerçek gövdesini (set-state şekli) Step öncesi oku ve `resumeTerminal`'ı ona birebir uydur; yukarıdaki şekil mevcut store deseninin tipik hali — sapma varsa store'un kendi desenini izle. `resumeTerminal` ek olarak yeni session'da `cliSessionID`'yi sıfırlar (yeni oturum henüz yakalanmadı).

- [ ] **Step 3: Event dinleyici ekle — `frontend/src/App.tsx`**

Mevcut `EventsOn(...)` bloğuna (≈satır 97-122, `agents:updated` yanına) ekle:

```ts
      EventsOn("terminal:resume-available", (data: { sessionID: string; cliSessionID: string }) => {
        useTerminals.getState().setCLISessionID(data.sessionID, data.cliSessionID);
      });
```

Ve `EventsOff` temizlik bloğuna (≈satır 134-135) ekle:

```ts
          EventsOff("terminal:resume-available");
```

> **Not:** `useTerminals` import'unun App.tsx'te mevcut olduğunu doğrula; yoksa `import { useTerminals } from "./store/useTerminals";` ekle.

- [ ] **Step 4: Frontend'i derle (typecheck)**

Run: `cd frontend && npm run build`
Expected: TypeScript hatası yok; build başarılı.

- [ ] **Step 5: commit**

```bash
git add frontend/src/lib/types.ts frontend/src/store/useTerminals.ts frontend/src/App.tsx
git commit -m "feat: #40 frontend resume store + event — cliSessionID mirror + resumeTerminal

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 7: frontend — "Devam Et" düğmesi

**Files:**
- Modify: `frontend/src/components/TerminalPane.tsx`, `frontend/src/components/TerminalGrid.tsx`, `frontend/src/styles/globals.css`

**Interfaces:**
- Consumes: `resumeTerminal` (Task 6), `TerminalSession.cliSessionID` (Task 6).
- Produces: `TerminalPane` yeni props `onResume?: () => void` + `canResume?: boolean`.

- [ ] **Step 1: `TerminalPane`'e props ekle — `frontend/src/components/TerminalPane.tsx`**

Props tipine (`onRestart?: () => void;` satırının yanına, ≈satır 21) ekle:

```ts
  onResume?: () => void;
  canResume?: boolean;
```

Fonksiyon parametre destructuring'ine (≈satır 24) ekle:

```ts
export default function TerminalPane({ sessionID, agentName, cliType, isFocused, onToggleFocus, onRemove, onRestart, onResume, canResume }: Props) {
```

- [ ] **Step 2: "Devam Et" düğmesini ekle — `frontend/src/components/TerminalPane.tsx`**

Restart düğmesi bloğunun (`{onRestart && (...)}`, ≈satır 319-328) **hemen öncesine** ekle:

```tsx
          {onResume && (
            <button
              type="button"
              className="terminal-btn-resume"
              onClick={onResume}
              disabled={!canResume}
              title={canResume ? "Oturumdan devam et (--resume)" : "Devam edilebilir oturum henüz yakalanmadı"}
            >
              {"⏯"}
            </button>
          )}
```

- [ ] **Step 3: `TerminalGrid`'de `onResume`/`canResume` wiring — `frontend/src/components/TerminalGrid.tsx`**

`restartTerminal`'ı store'dan çeken satıra (≈satır 116) `resumeTerminal` ekle:

```ts
  const { sessions, focusedSessionID, toggleFocusSession, setFocusedSession, loadCLIs, removeTerminal, restartTerminal, resumeTerminal, openTeamFromConfig } = useTerminals();
```

Üç `onRestart={...}` çağrı noktasının (≈satır 273, 290, 352) **her birinin yanına**, o noktadaki session değişkenini (`s` veya `slot.session`) kullanarak `onResume` + `canResume` ekle. Örnek (satır 273 bağlamı, session değişkeni `s`):

```tsx
              onRestart={() => restartTerminal(s.teamID, s.sessionID).catch(err => console.error("[restart] failed:", err))}
              onResume={() => resumeTerminal(s.teamID, s.sessionID).catch(err => console.error("[resume] failed:", err))}
              canResume={!!s.cliSessionID}
```

Satır 290 bağlamında session değişkeni `slot.session`:

```tsx
              onRestart={() => restartTerminal(slot.session.teamID, slot.session.sessionID).catch(err => console.error("[restart] failed:", err))}
              onResume={() => resumeTerminal(slot.session.teamID, slot.session.sessionID).catch(err => console.error("[resume] failed:", err))}
              canResume={!!slot.session.cliSessionID}
```

Satır 352 bağlamında session değişkeni `s`, team `team.id`:

```tsx
                  onRestart={() => restartTerminal(team.id, s.sessionID).catch(err => console.error("[restart] failed:", err))}
                  onResume={() => resumeTerminal(team.id, s.sessionID).catch(err => console.error("[resume] failed:", err))}
                  canResume={!!s.cliSessionID}
```

> **Not:** Her üç çağrı noktasındaki gerçek değişken adlarını (session ve teamID kaynağı) Step öncesi oku ve ona göre uyarla; `onRestart`'ın kullandığı aynı değişkenleri kullan.

- [ ] **Step 4: Düğme stilini ekle — `frontend/src/styles/globals.css`**

`.terminal-btn-restart` blokunun (≈satır 1281-1292) ardından ekle (restart stilini taban al; pasif durum için opacity):

```css
/* ============ Terminal Resume (Devam Et) Button ============ */
.terminal-btn-resume {
  /* Inherit the restart button's sizing/colors; only state styling differs. */
  composes: terminal-btn-restart;
}
.terminal-btn-resume:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}
```

> **Not:** Eğer projede CSS Modules kullanılmıyorsa (`composes` çalışmazsa — bu `globals.css` global stil), `composes` yerine `.terminal-btn-restart` blokunun kurallarını `.terminal-btn-resume`'a kopyala (selector'ı `.terminal-btn-restart, .terminal-btn-resume { ... }` yaparak DRY tut), ve `:disabled` kuralını ayrı bırak. Step öncesi `.terminal-btn-restart` blokunu oku ve global-CSS uyumlu şekli uygula.

- [ ] **Step 5: Frontend'i derle (typecheck)**

Run: `cd frontend && npm run build`
Expected: TypeScript hatası yok; build başarılı.

- [ ] **Step 6: commit**

```bash
git add frontend/src/components/TerminalPane.tsx frontend/src/components/TerminalGrid.tsx frontend/src/styles/globals.css
git commit -m "feat: #40 'Devam Et' düğmesi — opt-in resume UI (cliSessionID ile etkin)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 8: Uçtan-uca doğrulama + native test (kullanıcı)

**Files:** (yok — doğrulama)

- [ ] **Step 1: Tam build + test paketi**

Run: `make mcp-server && go build ./... && go test ./... && go vet ./... && gofmt -l . && cd frontend && npm run build`
Expected: Hepsi temiz (`gofmt -l` boş, testler PASS, build başarılı).

- [ ] **Step 2: Native test — `make dev` (kullanıcı)**

`make dev` ile uygulamayı aç. Her CLI için (Claude, Copilot, Codex):
1. Bir takım/oda aç, agent terminali oluştur.
2. Agent'a birkaç mesaj yaz; konuşma bağlamı oluştur.
3. Birkaç saniye sonra "Devam Et" düğmesinin **etkinleştiğini** doğrula (cliSessionID yakalandı).
4. "Devam Et"e bas → terminal kapanıp `--resume` ile yeniden açılmalı; agent **önceki bağlamı hatırlamalı**.
5. Düz "Restart" (↻) ile karşılaştır → o **taze** başlamalı (bağlam yok).
6. Gemini terminalinde "Devam Et" **pasif** kalmalı (bu turda desteklenmiyor).

Expected: Claude/Copilot/Codex resume bağlamı korur; Gemini düğmesi pasif; shell'de de pasif.

- [ ] **Step 3: PR aç ve review döngüsü**

```bash
git push -u origin feat/cli-session-resume-40
gh pr create --title "feat: #40 CLI session resume — opt-in 'Devam Et' (Claude/Copilot/Codex)" --body "..."
```

PR gövdesinde `Closes #40` yerine `Refs #40` (Faz-1; Gemini + Faz-2 takip). Push sonrası Codex+Copilot review; `@codex review` manuel tetikle; copilot-pull-request-reviewer botunu poll'la yokla. Bulguları analiz/test/araştır — körü körüne kabul etme.

---

## Self-Review (yazım sonrası kontrol)

**Spec coverage:**
- §4 ingest SessionID → Task 1 ✅ · watcher onSessionID → Task 4 ✅
- §4 pty cliSessionID/Set-Get → Task 2 ✅
- §4 cli ResumeSupported/GetCommandResume → Task 3 ✅
- §4 app createTerminal(resumeID)/ResumeTerminal/event → Task 4 (event) + Task 5 (resume) ✅
- §4 frontend types/store/event/button → Task 6 + Task 7 ✅
- §6 edge-case'ler: Claude id-regen (createTerminal yeni watcher → yeni capture, Task 4+5) ✅ · same-cwd claim (mevcut close→create, Task 5) ✅ · unsupported pasif (ResumeSupported gate, Task 4) ✅ · observer (mute'tan bağımsız capture, Task 4 doğal) ✅
- §7 testler → Task 1/2/3 birim, Task 8 native ✅

**Placeholder scan:** Kod adımlarında gerçek kod var; "Not" blokları mevcut-desen-uyumu içindir (gerçek değişken adlarını oku-ve-uyarla), placeholder değil. ✅

**Type consistency:** `SessionID(path string) string` (T1) ↔ watcher `ad.SessionID(path)` (T4) ✅ · `SetCLISessionID/GetCLISessionID` (T2) ↔ app çağrıları (T4/T5) ✅ · `GetCommandResume(CLIType, string)` (T3) ↔ createTerminal (T5) ✅ · `cliSessionID?` (T6 types) ↔ `canResume={!!...cliSessionID}` (T7) ✅ · `resumeTerminal`/`setCLISessionID` (T6 store) ↔ App.tsx/TerminalGrid (T6/T7) ✅ · event payload `{sessionID, cliSessionID}` (T4 emit) ↔ App.tsx listener (T6) ✅
