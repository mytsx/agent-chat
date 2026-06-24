# CLI Session Resume Faz-2 (#40) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agent'ların TÜM geçmiş CLI oturumlarını kalıcı bir log'da tarih/saat ile yakala; "Config ile Aç" ve "terminal ekle" akışlarında bu oturumlardan birini seç, agent'lar-arası zamansal korelasyonu (aynı dönem) göster ve "hepsini aynı döneme set et" ile hizala.

**Architecture:** Yeni `internal/sessionlog` paketi `~/.agent-chat/session-history.json`'a (atomik, `teams.json` deseni) `[firstSeen,lastSeen]` pencereli kayıt tutar; Faz-1'in `onSessionID` callback'i Record, `closeTerminalInternal` Touch eder. App enumerasyon metotları (`ListKnownAgents`/`ListAgentSessions` + CLI-dosyası zenginleştirme) ve `CreateTerminalResume` sunar. Frontend `OpenTeamModal` + `SetupWizard` picker'ı + saf `sessionCorrelation` overlap matematiğini kullanır.

**Tech Stack:** Go 1.25.5 (modül `desktop`), Wails v2.11.0, React 18 + TS + Zustand + Vite.

## Global Constraints

- Go modül `desktop`; import `desktop/internal/...`.
- **Embed şartı:** `go build/test/vet` öncesi `make mcp-server` (app.go `//go:embed`).
- Agent-facing/UX metinleri **Türkçe + emoji**.
- Testler **table-driven** `t.Run()`.
- `last_seen`/`first_seen` alanları **`float64` unix** (proje konvansiyonu).
- Atomik dosya yazımı: temp+rename + mutex (`internal/team/store.go` `save()` deseni).
- Frontend test runner yok → doğrulama `cd frontend && npm run build` + native.
- Build sırası: `make mcp-server` → `go test ./...` → `go vet ./...` → `gofmt -l` → `wails generate module` → `npm run build` → `make dev`.
- Branch: `feat/cli-session-resume-phase2-40` (Faz-1 main'e merged `6db640f`).
- "Aynı dönem" = `[firstSeen,lastSeen]` örtüşmesi: `a.start < b.last && b.start < a.last`. "En iyi eşleşme" = en çok örtüşme süresi, beraberlikte başlangıcı en yakın.
- Non-goals: çapraz-tool resume; Gemini resume **komutu** (yalnız claude/copilot/codex Record edilir).

---

## File Structure

| Dosya | Sorumluluk | Create/Modify |
|-------|-----------|---------------|
| `internal/sessionlog/store.go` | session-history map store + Record/Touch/ListAgents/ListSessions | Create |
| `internal/sessionlog/store_test.go` | table-driven testler | Create |
| `internal/ingest/sessionfile.go` | `SessionFilePath` (id→path) + `SessionStats` (msgCount/snippet) | Create |
| `internal/ingest/sessionfile_test.go` | testler | Create |
| `app.go` | sessionLog init + Record/Touch wiring + `SessionInfo`/`ListKnownAgents`/`ListAgentSessions`/`CreateTerminalResume` | Modify |
| `frontend/src/lib/types.ts` | `SessionInfo` | Modify |
| `frontend/src/lib/sessionCorrelation.ts` | overlap/overlapSeconds/bestMatch saf fonksiyonlar | Create |
| `frontend/src/store/useTerminals.ts` | `createTerminalResume` + `listKnownAgents`/`listAgentSessions` | Modify |
| `frontend/src/components/OpenTeamModal.tsx` | çoklu-agent oturum seçici modalı | Create |
| `frontend/src/components/TerminalGrid.tsx` | "Config ile Aç" → OpenTeamModal | Modify |
| `frontend/src/components/SetupWizard.tsx` | agent autocomplete + session picker | Modify |
| `frontend/src/styles/globals.css` | modal + dropdown + korelasyon stilleri | Modify |
| `frontend/wailsjs/...` | yeni 3 metot regen | Modify |

---

## Task 1: internal/sessionlog — kalıcı session-history store

**Files:**
- Create: `internal/sessionlog/store.go`, `internal/sessionlog/store_test.go`

**Interfaces:**
- Produces: `sessionlog.Record{SessionID,Room,AgentName,CLIType,Cwd,FirstSeen,LastSeen float64}`; `(*Store)` with `New(dataDir string) (*Store, error)`, `Record(sessionID,room,agent,cliType,cwd string)`, `Touch(sessionID string)`, `ListAgents(room string) []string`, `ListSessions(room,agent string) []Record`. Injectable `now func() float64` field for tests.

- [ ] **Step 1: Failing test — `internal/sessionlog/store_test.go`**

```go
package sessionlog

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecordAndListSessions(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }

	s.Record("sid-1", "room-a", "agent-1", "claude", "/cwd/1")
	clock = 200
	s.Record("sid-2", "room-a", "agent-1", "codex", "/cwd/1")
	s.Record("sid-x", "room-b", "agent-1", "claude", "/cwd/1") // başka oda

	got := s.ListSessions("room-a", "agent-1")
	if len(got) != 2 {
		t.Fatalf("ListSessions len = %d, want 2", len(got))
	}
	// lastSeen yeniden→eskiye: sid-2 (200) önce
	if got[0].SessionID != "sid-2" || got[1].SessionID != "sid-1" {
		t.Fatalf("order = %s,%s, want sid-2,sid-1", got[0].SessionID, got[1].SessionID)
	}
	if got[1].FirstSeen != 100 || got[1].CLIType != "claude" {
		t.Fatalf("sid-1 entry = %+v", got[1])
	}
}

func TestRecordIdempotentPreservesFirstSeen(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("sid", "r", "a", "claude", "/c")
	clock = 500
	s.Record("sid", "r", "a", "claude", "/c") // tekrar capture
	got := s.ListSessions("r", "a")
	if len(got) != 1 || got[0].FirstSeen != 100 || got[0].LastSeen != 500 {
		t.Fatalf("entry = %+v, want firstSeen=100 lastSeen=500", got[0])
	}
}

func TestTouch(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("sid", "r", "a", "claude", "/c")
	clock = 300
	s.Touch("sid")
	s.Touch("unknown") // no-op, panik yok
	got := s.ListSessions("r", "a")
	if got[0].LastSeen != 300 || got[0].FirstSeen != 100 {
		t.Fatalf("after touch = %+v", got[0])
	}
}

func TestListAgents(t *testing.T) {
	s := newTestStore(t)
	clock := 100.0
	s.now = func() float64 { return clock }
	s.Record("s1", "r", "alice", "claude", "/c")
	clock = 200
	s.Record("s2", "r", "bob", "codex", "/c")
	clock = 300
	s.Record("s3", "r", "alice", "claude", "/c")
	got := s.ListAgents("r")
	// son-görülme yeniden→eskiye distinct: alice(300), bob(200)
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("ListAgents = %v, want [alice bob]", got)
	}
}

func TestEmptySessionIDNoOp(t *testing.T) {
	s := newTestStore(t)
	s.Record("", "r", "a", "claude", "/c")
	if len(s.ListSessions("r", "a")) != 0 {
		t.Fatal("empty sessionID must not be recorded")
	}
}

func TestPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	s1, _ := New(dir)
	s1.Record("sid", "r", "a", "claude", "/c")
	s2, err := New(dir) // aynı dizinden yeniden yükle
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.ListSessions("r", "a")) != 1 {
		t.Fatal("reload must restore persisted record")
	}
	_ = filepath.Join(dir, "session-history.json")
}
```

- [ ] **Step 2: Test'i çalıştır — başarısız (paket yok)**

Run: `go test ./internal/sessionlog/`
Expected: FAIL — paket/derleme.

- [ ] **Step 3: Implement — `internal/sessionlog/store.go`**

```go
// Package sessionlog persists a per-(room,agent) history of CLI sessions a
// terminal has run, so the user can later resume a SPECIFIC past session and
// correlate sessions across agents by the time window they were open (#40 Faz-2).
package sessionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Record is one logged session. SessionID is the map key, flattened in for List
// results. FirstSeen/LastSeen are unix seconds (float64, project convention): the
// window the terminal was open, which is what "same period" correlation compares.
type Record struct {
	SessionID string  `json:"session_id"`
	Room      string  `json:"room"`
	AgentName string  `json:"agent_name"`
	CLIType   string  `json:"cli_type"`
	Cwd       string  `json:"cwd"`
	FirstSeen float64 `json:"first_seen"`
	LastSeen  float64 `json:"last_seen"`
}

// Store is the atomic JSON-backed session-history index. Safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	filePath string
	records  map[string]Record // keyed by SessionID
	now      func() float64
}

func nowUnix() float64 { return float64(time.Now().Unix()) }

// New loads (or creates) the store under dataDir/session-history.json.
func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	s := &Store{
		filePath: filepath.Join(dataDir, "session-history.json"),
		records:  make(map[string]Record),
		now:      nowUnix,
	}
	if data, err := os.ReadFile(s.filePath); err == nil {
		_ = json.Unmarshal(data, &s.records) // corrupt/empty → start empty
		if s.records == nil {
			s.records = make(map[string]Record)
		}
	}
	return s, nil
}

// Record adds a session (FirstSeen=LastSeen=now) or, if already present, only
// advances LastSeen (FirstSeen preserved). Empty sessionID is a no-op.
func (s *Store) Record(sessionID, room, agent, cliType, cwd string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.now()
	if r, ok := s.records[sessionID]; ok {
		r.LastSeen = t
		s.records[sessionID] = r
	} else {
		s.records[sessionID] = Record{
			SessionID: sessionID, Room: room, AgentName: agent,
			CLIType: cliType, Cwd: cwd, FirstSeen: t, LastSeen: t,
		}
	}
	s.save()
}

// Touch advances LastSeen for a known session (FirstSeen preserved). Unknown → no-op.
func (s *Store) Touch(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[sessionID]; ok {
		r.LastSeen = s.now()
		s.records[sessionID] = r
		s.save()
	}
}

// ListSessions returns a room+agent's sessions, newest LastSeen first.
func (s *Store) ListSessions(room, agent string) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, r := range s.records {
		if r.Room == room && r.AgentName == agent {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out
}

// ListAgents returns distinct agent names seen in a room, newest activity first.
func (s *Store) ListAgents(room string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := map[string]float64{}
	for _, r := range s.records {
		if r.Room != room {
			continue
		}
		if r.LastSeen > last[r.AgentName] {
			last[r.AgentName] = r.LastSeen
		}
	}
	names := make([]string, 0, len(last))
	for n := range last {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return last[names[i]] > last[names[j]] })
	return names
}

// save writes records atomically (temp+rename). Called under mu.
func (s *Store) save() {
	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, s.filePath); err != nil {
		os.Remove(tmp)
	}
}
```

- [ ] **Step 4: Testleri çalıştır — geç + `-race`**

Run: `go test ./internal/sessionlog/ -race`
Expected: PASS.

- [ ] **Step 5: vet + gofmt + commit**

```bash
go vet ./internal/sessionlog/ && gofmt -l internal/sessionlog/
git add internal/sessionlog/
git commit -m "feat: #40 Faz-2 internal/sessionlog — kalıcı session-history store

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: internal/ingest — SessionFilePath + SessionStats (enrichment)

**Files:**
- Create: `internal/ingest/sessionfile.go`, `internal/ingest/sessionfile_test.go`

**Interfaces:**
- Consumes: `claudeSlug` (unexported, same package), `AdapterFor`, `Cursor`.
- Produces: `SessionFilePath(cliType, cwd, sessionID string) (string, bool)`; `SessionStats(cliType, path string) (count int, snippet string)`.

- [ ] **Step 1: Failing test — `internal/ingest/sessionfile_test.go`**

```go
package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionFilePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	tests := []struct {
		name, cliType, cwd, id, wantSuffix string
		wantOK                             bool
	}{
		{"claude", "claude", "/x/y", "uuid-1", filepath.Join(".claude", "projects", "-x-y", "uuid-1.jsonl"), true},
		{"copilot", "copilot", "/ignored", "uuid-2", filepath.Join(".copilot", "session-state", "uuid-2", "events.jsonl"), true},
		{"gemini unsupported", "gemini", "/x", "uuid", "", false},
		{"shell", "shell", "/x", "uuid", "", false},
		{"empty id", "claude", "/x", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SessionFilePath(tt.cliType, tt.cwd, tt.id)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && got != filepath.Join(home, tt.wantSuffix) {
				t.Fatalf("path = %q, want suffix %q", got, tt.wantSuffix)
			}
		})
	}
}

func TestSessionStats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"ilk mesaj burada"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"ikinci"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	count, snippet := SessionStats("claude", path)
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if snippet != "ilk mesaj burada" {
		t.Fatalf("snippet = %q", snippet)
	}
}

func TestSessionStatsUnknownCLI(t *testing.T) {
	if c, s := SessionStats("shell", "/nope"); c != 0 || s != "" {
		t.Fatalf("unknown cli = %d,%q", c, s)
	}
}
```

- [ ] **Step 2: Test'i çalıştır — başarısız**

Run: `make mcp-server && go test ./internal/ingest/ -run 'SessionFilePath|SessionStats'`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement — `internal/ingest/sessionfile.go`**

```go
package ingest

import (
	"os"
	"path/filepath"
)

// SessionFilePath returns the on-disk transcript path for a captured session, used
// to enrich the session-history list with message count + snippet (#40 Faz-2).
// Claude/Copilot paths are derivable from cwd+id; Codex's filename embeds a
// timestamp before the uuid, so it is found by globbing for the uuid suffix. Only
// resume-captured CLIs (claude/copilot/codex) are supported — others return false.
func SessionFilePath(cliType, cwd, sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	switch cliType {
	case "claude":
		return filepath.Join(home, ".claude", "projects", claudeSlug(cwd), sessionID+".jsonl"), true
	case "copilot":
		return filepath.Join(home, ".copilot", "session-state", sessionID, "events.jsonl"), true
	case "codex":
		matches, _ := filepath.Glob(filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*-"+sessionID+".jsonl"))
		if len(matches) > 0 {
			return matches[0], true
		}
		return "", false
	default:
		return "", false
	}
}

// SessionStats returns the human user-message count and a short first-message
// snippet (≤80 runes) for a session file, reusing the CLI adapter's parser. An
// unknown CLI or unreadable file yields (0, "").
func SessionStats(cliType, path string) (int, string) {
	ad := AdapterFor(cliType)
	if ad == nil {
		return 0, ""
	}
	msgs, _, _ := ad.ParseNewUserMessages(path, Cursor{})
	if len(msgs) == 0 {
		return 0, ""
	}
	snippet := msgs[0].Content
	if r := []rune(snippet); len(r) > 80 {
		snippet = string(r[:80])
	}
	return len(msgs), snippet
}
```

- [ ] **Step 4: Testleri çalıştır — geç**

Run: `go test ./internal/ingest/ -run 'SessionFilePath|SessionStats'`
Expected: PASS.

- [ ] **Step 5: vet + gofmt + commit**

```bash
go vet ./internal/ingest/ && gofmt -l internal/ingest/
git add internal/ingest/sessionfile.go internal/ingest/sessionfile_test.go
git commit -m "feat: #40 Faz-2 ingest.SessionFilePath + SessionStats — enrichment

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: app.go — sessionLog init + Record/Touch wiring

**Files:**
- Modify: `app.go`

**Interfaces:**
- Consumes: `sessionlog.New/Record/Touch` (Task 1), `(*pty.Manager).GetCLISessionID` (Faz-1).
- Produces: `a.sessionLog *sessionlog.Store` field, wired writes.

- [ ] **Step 1: Import + struct field — `app.go`**

Import bloğuna ekle (diğer `desktop/internal/...` importlarının yanına):

```go
	"desktop/internal/sessionlog"
```

`App` struct'ında `ingestMgr *ingest.Manager` (≈satır 69) satırından sonra ekle:

```go
	sessionLog    *sessionlog.Store
```

- [ ] **Step 2: Init — `app.go`**

`a.ingestMgr = ingest.New()` (≈satır 125) satırından sonra ekle:

```go
	// #40 Faz-2: persistent per-agent session history for the resume picker.
	if sl, err := sessionlog.New(a.dataDir); err != nil {
		log.Printf("[SESSIONLOG] init failed: %v", err)
	} else {
		a.sessionLog = sl
	}
```

- [ ] **Step 3: Record on capture — `app.go`**

onSessionID callback'inde (≈satır 989-997), `a.ptyManager.SetCLISessionID(sessionID, id)` satırından **sonra** ekle:

```go
			// #40 Faz-2: also log to the persistent history so this session can be
			// resumed/correlated later (room/agent/cliType/cwd from the enclosing
			// createTerminal scope).
			a.sessionLog.Record(id, room, agentName, cliType, ingestCwd)
```

(`room`, `agentName`, `cliType`, `ingestCwd` bu callback'in kapsamında zaten tanımlı.)

- [ ] **Step 4: Touch on close — `app.go`**

`closeTerminalInternal` (≈satır 1662) içinde, `session` alındıktan ve metadata kopyalandıktan sonra ama `ptyManager.Close`'dan ÖNCE ekle (captured id'yi close eviction'dan önce oku):

```go
	// #40 Faz-2: close the session's open-window (lastSeen) in the history log.
	if cid := a.ptyManager.GetCLISessionID(sessionID); cid != "" {
		a.sessionLog.Touch(cid)
	}
```

> **Not:** `closeTerminalInternal`'ın gerçek gövdesini oku; bu bloğu `session := a.ptyManager.GetSession(sessionID)` nil-guard'ından SONRA, `a.ptyManager.Close(sessionID)` çağrısından ÖNCE yerleştir.

- [ ] **Step 5: Derle + tüm testler + vet + gofmt**

Run: `make mcp-server && go build ./... && go test ./... && go vet ./... && gofmt -l app.go`
Expected: PASS, gofmt boş.

- [ ] **Step 6: Commit**

```bash
git add app.go
git commit -m "feat: #40 Faz-2 app sessionLog init + Record/Touch wiring

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: app.go — enumeration + CreateTerminalResume bound methods

**Files:**
- Modify: `app.go`

**Interfaces:**
- Consumes: `sessionlog.ListAgents/ListSessions/Record` (Task 1), `ingest.SessionFilePath/SessionStats` (Task 2), `(*App).createTerminal(...,resumeID)` (Faz-1), `teamStore.Get`.
- Produces: bound `SessionInfo` struct + `ListKnownAgents(teamID string) []string`, `ListAgentSessions(teamID, agentName string) []SessionInfo`, `CreateTerminalResume(teamID, agentName, workDir, cliType, promptID string, useWorktree bool, slotIndex int, resumeID string) (string, error)`.

- [ ] **Step 1: Add SessionInfo + methods — `app.go`**

Dosya sonuna (uygun bir bölüme, örn. GetTerminalSessions yakınına) ekle:

```go
// SessionInfo is one past CLI session of an agent, enriched for the resume picker
// (#40 Faz-2). Times are unix seconds. Wails generates the TS interface from this.
type SessionInfo struct {
	SessionID    string  `json:"sessionID"`
	CLIType      string  `json:"cliType"`
	StartUnix    float64 `json:"startUnix"`
	LastUnix     float64 `json:"lastUnix"`
	DurationSec  float64 `json:"durationSec"`
	MessageCount int     `json:"messageCount"`
	Snippet      string  `json:"snippet"`
	FileMissing  bool    `json:"fileMissing"`
}

// roomNameForTeam resolves a team's room name (its Name, or "default"), matching
// CreateTerminal's derivation.
func (a *App) roomNameForTeam(teamID string) string {
	if teamID != "" {
		if t, err := a.teamStore.Get(teamID); err == nil && t.Name != "" {
			return t.Name
		}
	}
	return "default"
}

// ListKnownAgents returns agent names previously seen in the team's room (session
// history) unioned with the team's configured agents — newest-activity first, then
// any config-only names (#40 Faz-2).
func (a *App) ListKnownAgents(teamID string) []string {
	room := a.roomNameForTeam(teamID)
	seen := map[string]bool{}
	var out []string
	for _, n := range a.sessionLog.ListAgents(room) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if t, err := a.teamStore.Get(teamID); err == nil {
		for _, ag := range t.Agents {
			if ag.Name != "" && !seen[ag.Name] {
				seen[ag.Name] = true
				out = append(out, ag.Name)
			}
		}
	}
	return out
}

// ListAgentSessions returns an agent's past sessions in the team's room, newest
// first, enriched with duration + message count + first-message snippet read live
// from each CLI transcript (#40 Faz-2). A pruned transcript yields FileMissing.
func (a *App) ListAgentSessions(teamID, agentName string) []SessionInfo {
	room := a.roomNameForTeam(teamID)
	recs := a.sessionLog.ListSessions(room, agentName)
	out := make([]SessionInfo, 0, len(recs))
	for _, r := range recs {
		si := SessionInfo{
			SessionID:   r.SessionID,
			CLIType:     r.CLIType,
			StartUnix:   r.FirstSeen,
			LastUnix:    r.LastSeen,
			DurationSec: r.LastSeen - r.FirstSeen,
		}
		if path, ok := ingest.SessionFilePath(r.CLIType, r.Cwd, r.SessionID); ok {
			if _, statErr := os.Stat(path); statErr == nil {
				si.MessageCount, si.Snippet = ingest.SessionStats(r.CLIType, path)
			} else {
				si.FileMissing = true
			}
		} else {
			si.FileMissing = true
		}
		out = append(out, si)
	}
	return out
}

// CreateTerminalResume creates a terminal resuming a SPECIFIC past session
// (resumeID), or fresh when resumeID is empty. Thin exported wrapper over the
// Faz-1 internal createTerminal (#40 Faz-2). The resume picker calls this per agent.
func (a *App) CreateTerminalResume(teamID, agentName, workDir, cliType, promptID string, useWorktree bool, slotIndex int, resumeID string) (string, error) {
	return a.createTerminal(teamID, agentName, workDir, cliType, promptID, useWorktree, slotIndex, resumeID)
}
```

(`os`, `ingest`, `a.teamStore`, `a.sessionLog`, `a.createTerminal` hepsi mevcut/import'lu.)

- [ ] **Step 2: Derle + testler + vet + gofmt**

Run: `make mcp-server && go build ./... && go test ./... && go vet ./... && gofmt -l app.go`
Expected: PASS.

- [ ] **Step 3: Wails binding regen**

Run: `wails generate module`
Doğrula: `grep -nE "ListKnownAgents|ListAgentSessions|CreateTerminalResume" frontend/wailsjs/go/main/App.d.ts`
Expected: üç metot da görünür; `SessionInfo` `frontend/wailsjs/go/models.ts`'e eklenir.

- [ ] **Step 4: Commit**

```bash
git add app.go frontend/wailsjs/
git commit -m "feat: #40 Faz-2 enumeration + CreateTerminalResume bound metotlar

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: frontend — types + korelasyon lib + store action'ları

**Files:**
- Modify: `frontend/src/lib/types.ts`, `frontend/src/store/useTerminals.ts`
- Create: `frontend/src/lib/sessionCorrelation.ts`

**Interfaces:**
- Consumes: Wails `ListKnownAgents`/`ListAgentSessions`/`CreateTerminalResume` + `main.SessionInfo` model (Task 4).
- Produces: `SessionInfo` type; `overlaps`/`overlapSeconds`/`bestMatch`; store `createTerminalResume`, `listKnownAgents`, `listAgentSessions`.

- [ ] **Step 1: `SessionInfo` type — `frontend/src/lib/types.ts`**

`TerminalSession` interface'inin yanına ekle:

```ts
export interface SessionInfo {
  sessionID: string;
  cliType: CLIType;
  startUnix: number;
  lastUnix: number;
  durationSec: number;
  messageCount: number;
  snippet: string;
  fileMissing: boolean;
}
```

- [ ] **Step 2: Korelasyon lib — `frontend/src/lib/sessionCorrelation.ts`**

```ts
import { SessionInfo } from "./types";

// Two sessions are "same period" when their [start,last] open-windows intersect.
export function overlaps(a: SessionInfo, b: SessionInfo): boolean {
  return a.startUnix < b.lastUnix && b.startUnix < a.lastUnix;
}

// overlapSeconds is the length of the intersection (0 when disjoint).
export function overlapSeconds(a: SessionInfo, b: SessionInfo): number {
  return Math.max(0, Math.min(a.lastUnix, b.lastUnix) - Math.max(a.startUnix, b.startUnix));
}

// bestMatch picks the candidate most overlapping `sel`; ties broken by nearest
// start. Returns null when nothing overlaps (caller leaves that agent unchanged).
export function bestMatch(sel: SessionInfo, candidates: SessionInfo[]): SessionInfo | null {
  let best: SessionInfo | null = null;
  let bestOv = 0;
  for (const c of candidates) {
    const ov = overlapSeconds(sel, c);
    if (ov <= 0) continue;
    const closer = best !== null && Math.abs(c.startUnix - sel.startUnix) < Math.abs(best.startUnix - sel.startUnix);
    if (ov > bestOv || (ov === bestOv && closer)) {
      best = c;
      bestOv = ov;
    }
  }
  return best;
}
```

- [ ] **Step 3: Store action'ları — `frontend/src/store/useTerminals.ts`**

Import bloğuna (Wails App importları) ekle:

```ts
  CreateTerminalResume,
  ListKnownAgents,
  ListAgentSessions,
```

`SessionInfo`'yu types importuna ekle: `import { CLIInfo, CLIType, TerminalSession, SessionInfo } from "../lib/types";`

Store tipine ekle (`createTerminalResume` vb.):

```ts
  createTerminalResume: (teamID: string, agentName: string, workDir: string, cliType: CLIType, promptId: string, slotIndex: number, useWorktree: boolean, resumeID: string) => Promise<string>;
  listKnownAgents: (teamID: string) => Promise<string[]>;
  listAgentSessions: (teamID: string, agentName: string) => Promise<SessionInfo[]>;
```

İmplementasyonları (mevcut `addTerminal`'ın session-ekleme deseniyle) ekle:

```ts
  createTerminalResume: async (teamID, agentName, workDir, cliType, promptId, slotIndex, useWorktree, resumeID) => {
    const currentSessions = get().sessions[teamID] ?? [];
    const sessionID = await CreateTerminalResume(teamID, agentName, workDir, cliType, promptId, useWorktree, slotIndex, resumeID);
    set((s) => {
      const pendingID = s.pendingCLISessionIDs[sessionID];
      const session: TerminalSession = {
        sessionID, teamID, agentName, cliType,
        index: currentSessions.length, slotIndex,
        ...(pendingID !== undefined ? { cliSessionID: pendingID } : {}),
      };
      const pending = { ...s.pendingCLISessionIDs };
      delete pending[sessionID];
      return {
        sessions: { ...s.sessions, [teamID]: [...(s.sessions[teamID] ?? []), session] },
        pendingCLISessionIDs: pending,
      };
    });
    try { await useTeams.getState().refreshTeam(teamID); } catch (e) { console.error("[createTerminalResume] refreshTeam:", e); }
    return sessionID;
  },
  listKnownAgents: async (teamID) => {
    try { return await ListKnownAgents(teamID); } catch (e) { console.error("[listKnownAgents]", e); return []; }
  },
  listAgentSessions: async (teamID, agentName) => {
    try { return (await ListAgentSessions(teamID, agentName)) as unknown as SessionInfo[]; } catch (e) { console.error("[listAgentSessions]", e); return []; }
  },
```

> **Not:** Gerçek `addTerminal` gövdesini oku ve `createTerminalResume`'u onun set-state şekline birebir uydur (slotIndex/index/refreshTeam). `useTeams` zaten import'lu.

- [ ] **Step 4: Frontend derle**

Run: `cd frontend && npm run build`
Expected: TS hatası yok.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/types.ts frontend/src/lib/sessionCorrelation.ts frontend/src/store/useTerminals.ts
git commit -m "feat: #40 Faz-2 frontend types + korelasyon lib + store action'ları

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 6: frontend — OpenTeamModal (çoklu-agent oturum seçici)

**Files:**
- Create: `frontend/src/components/OpenTeamModal.tsx`
- Modify: `frontend/src/components/TerminalGrid.tsx`, `frontend/src/styles/globals.css`

**Interfaces:**
- Consumes: `listKnownAgents`/`listAgentSessions`/`createTerminalResume` (Task 5), `overlaps`/`bestMatch` (Task 5), team config (`useTeams`).
- Produces: `OpenTeamModal` component (props `{ teamID: string; onClose: () => void }`).

- [ ] **Step 1: OpenTeamModal — `frontend/src/components/OpenTeamModal.tsx`**

Onaylanan Variant B akışı: mod seçici + agent satırları (kompakt dropdown) + açık-dropdown korelasyonu + "set et" + "Aç". Mevcut modal deseni (`RoomCharterModal.tsx`/`ImportRoomModal.tsx`) ve CSS sınıflarını taban al. Bileşen iskeleti:

```tsx
import { useEffect, useMemo, useState } from "react";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";
import { SessionInfo, CLIType } from "../lib/types";
import { overlaps, bestMatch } from "../lib/sessionCorrelation";

interface Props { teamID: string; onClose: () => void; }
type Mode = "fresh" | "last" | "custom";

// Per-agent row state: the agent config + its fetched history + the chosen session
// (undefined = fresh "✨ Yeni").
interface Row {
  agentName: string;
  cliType: CLIType;
  workDir: string;
  promptID: string;
  useWorktree: boolean;
  slotIndex: number;
  sessions: SessionInfo[];
  selected?: SessionInfo;
  open: boolean;
}

function fmt(unix: number): string {
  const d = new Date(unix * 1000);
  return d.toLocaleString("tr-TR", { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" });
}
function dur(sec: number): string {
  const m = Math.round(sec / 60);
  return m >= 60 ? `${Math.floor(m / 60)}s ${m % 60}dk` : `${m}dk`;
}

export default function OpenTeamModal({ teamID, onClose }: Props) {
  const team = useTeams((s) => s.teams.find((t) => t.id === teamID));
  const { listAgentSessions, createTerminalResume } = useTerminals();
  const [rows, setRows] = useState<Row[]>([]);
  const [mode, setMode] = useState<Mode>("custom");
  const [opening, setOpening] = useState(false);

  // Load each configured agent's session history once.
  useEffect(() => {
    if (!team) return;
    let alive = true;
    (async () => {
      const loaded: Row[] = [];
      for (const ag of team.agents ?? []) {
        const sessions = await listAgentSessions(teamID, ag.name);
        loaded.push({
          agentName: ag.name,
          cliType: (ag.cli_type || "shell") as CLIType,
          workDir: ag.work_dir || "",
          promptID: ag.prompt_id || "",
          useWorktree: !!ag.use_worktree,
          slotIndex: ag.slot_index ?? loaded.length,
          sessions, open: false,
        });
      }
      if (alive) setRows(loaded);
    })();
    return () => { alive = false; };
  }, [teamID]); // eslint-disable-line

  // The currently-selected session that drives correlation (first row with a pick).
  const driver = useMemo(() => rows.find((r) => r.selected)?.selected, [rows]);

  const applyMode = (m: Mode) => {
    setMode(m);
    setRows((rs) => rs.map((r) => ({
      ...r,
      selected: m === "fresh" ? undefined : m === "last" ? r.sessions[0] : r.selected,
    })));
  };

  const pick = (i: number, s?: SessionInfo) => {
    setMode("custom");
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, selected: s, open: false } : r)));
  };
  const toggleOpen = (i: number) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, open: !r.open } : { ...r, open: false })));

  // Set every OTHER agent to its best same-period session relative to `driver`.
  const setAllSamePeriod = () => {
    if (!driver) return;
    setRows((rs) => rs.map((r) => {
      if (r.selected === driver) return r;
      const m = bestMatch(driver, r.sessions);
      return m ? { ...r, selected: m } : r;
    }));
  };

  const open = async () => {
    if (opening) return;
    setOpening(true);
    try {
      for (const r of rows) {
        await createTerminalResume(teamID, r.agentName, r.workDir, r.cliType, r.promptID, r.slotIndex, r.useWorktree, r.selected?.sessionID ?? "");
      }
      onClose();
    } catch (e) {
      console.error("[OpenTeamModal] open failed:", e);
      alert(`Açılırken hata: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setOpening(false);
    }
  };

  if (!team) return null;
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal open-team-modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">Takımı Aç — {team.name}</div>

        <div className="open-team-modes">
          {([["fresh", "✨ Hepsi taze"], ["last", "⏯ Son oturumlardan"], ["custom", "🎛 Özel seçim"]] as [Mode, string][]).map(([m, label]) => (
            <button key={m} className={`mode-seg ${mode === m ? "active" : ""}`} onClick={() => applyMode(m)}>{label}</button>
          ))}
        </div>

        <div className="open-team-rows">
          {rows.map((r, i) => (
            <div key={r.agentName} className="open-team-row">
              <div className="ot-agent">{r.agentName}<span className="ot-cli">{r.cliType}</span></div>
              <div className="ot-picker">
                <button className="ot-current" onClick={() => toggleOpen(i)}>
                  <span>{r.selected ? `${fmt(r.selected.startUnix)} · ${dur(r.selected.durationSec)} · ${r.selected.messageCount} mesaj` : "✨ Yeni (taze)"}</span>
                  <span>{r.open ? "▴" : "▾"}</span>
                </button>
                {r.open && (
                  <div className="ot-dropdown">
                    <div className="ot-opt" onClick={() => pick(i, undefined)}>✨ Yeni (taze başlat)</div>
                    {r.sessions.length === 0 && <div className="ot-empty">Geçmiş oturum yok</div>}
                    {r.sessions.map((s) => {
                      const same = driver && s !== driver && overlaps(driver, s);
                      return (
                        <div key={s.sessionID} className={`ot-opt ${r.selected === s ? "sel" : ""} ${same ? "same-period" : ""}`} onClick={() => pick(i, s)}>
                          <div>{fmt(s.startUnix)} · {dur(s.durationSec)} · {s.messageCount} mesaj{same ? " 🟢" : ""}{s.fileMissing ? " ⚠️" : ""}</div>
                          {s.snippet && <div className="ot-snippet">{s.snippet}</div>}
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            </div>
          ))}
        </div>

        {driver && <button className="ot-setall" onClick={setAllSamePeriod}>🔗 Diğerlerini aynı döneme set et</button>}

        <div className="modal-actions">
          <button className="btn-secondary" onClick={onClose}>İptal</button>
          <button className="btn" onClick={open} disabled={opening}>{opening ? "Açılıyor..." : `Aç (${rows.length})`}</button>
        </div>
      </div>
    </div>
  );
}
```

> **Not:** `useTeams` Team tipindeki agent alanı adlarını (`agents`/`cli_type`/`work_dir`/`prompt_id`/`use_worktree`/`slot_index`) `frontend/src/lib/types.ts` Team/AgentConfig mirror'ından DOĞRULA ve uydur; modal-overlay/modal/modal-header/modal-actions sınıfları için `RoomCharterModal.tsx`'i taban al (varsa farklı sınıf adları onunkiyle eşle).

- [ ] **Step 2: "Config ile Aç" → modal — `frontend/src/components/TerminalGrid.tsx`**

`handleOpenFromConfig`'i (≈satır 204) modal-açmaya çevir: bir state ekle (`const [openModal, setOpenModal] = useState(false)`), "Config ile Aç" butonunun `onClick`'ini `() => setOpenModal(true)` yap, ve render'a ekle:

```tsx
{openModal && team && <OpenTeamModal teamID={team.id} onClose={() => setOpenModal(false)} />}
```

import: `import OpenTeamModal from "./OpenTeamModal";`

> **Not:** Mevcut `handleOpenFromConfig`/`openingFromConfig`/`openTeamFromConfig` tek-atış yolu KALDIRILMAZ — buton artık modalı açar; modal "Aç"ında `createTerminalResume` döngüsü çalışır. Eski `openTeamFromConfig` store action'ı dursun (başka kullanım yoksa kullanılmaz olur — bırak, kaldırma kapsamı bu task değil).

- [ ] **Step 3: Stiller — `frontend/src/styles/globals.css`**

`.open-team-modal`, `.open-team-modes`/`.mode-seg`(+`.active`), `.open-team-row`/`.ot-agent`/`.ot-cli`/`.ot-picker`/`.ot-current`/`.ot-dropdown`/`.ot-opt`(+`.sel`/`.same-period`)/`.ot-snippet`/`.ot-empty`/`.ot-setall` ekle. Onaylanan mockup renkleri: seçili `#1f6feb`, aynı-dönem `#3fb950`, panel `#161b22`/`#0d1117`, border `#30363d`. `.same-period` = `background:#3fb95022;border-color:#3fb950;color:#3fb950`. Mevcut modal/buton değişkenlerini (`var(--...)`) tutarlı kullan.

- [ ] **Step 4: Frontend derle**

Run: `cd frontend && npm run build`
Expected: TS hatası yok.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/OpenTeamModal.tsx frontend/src/components/TerminalGrid.tsx frontend/src/styles/globals.css
git commit -m "feat: #40 Faz-2 OpenTeamModal — çoklu-agent oturum seçici + korelasyon

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 7: frontend — SetupWizard agent autocomplete + session picker

**Files:**
- Modify: `frontend/src/components/SetupWizard.tsx`

**Interfaces:**
- Consumes: `listKnownAgents`/`listAgentSessions`/`createTerminalResume` (Task 5).

- [ ] **Step 1: Agent autocomplete + picker — `frontend/src/components/SetupWizard.tsx`**

State ekle: `const [knownAgents, setKnownAgents] = useState<string[]>([])`, `const [agentSessions, setAgentSessions] = useState<SessionInfo[]>([])`, `const [resumeID, setResumeID] = useState("")`. Import: `SessionInfo` types'tan; store'dan `listKnownAgents`/`listAgentSessions`/`createTerminalResume`.

`useEffect` mount'ta: `listKnownAgents(teamID).then(setKnownAgents)`.

Agent Name `<input>`'una `list="agent-history"` ekle + ardına `<datalist id="agent-history">{knownAgents.map(n => <option key={n} value={n} />)}</datalist>`.

`agentName` değişince (debounce/onBlur) o agent'ın oturumlarını çek: `listAgentSessions(teamID, agentName.trim()).then(setAgentSessions)`; liste boş değilse Agent Name altında bir `<select>` (oturum picker) göster:

```tsx
{agentSessions.length > 0 && (
  <div className="wizard-field">
    <label>Oturum <span className="wizard-optional">(geçmişten devam)</span></label>
    <select value={resumeID} onChange={(e) => setResumeID(e.target.value)}>
      <option value="">✨ Yeni (taze)</option>
      {agentSessions.map((s) => (
        <option key={s.sessionID} value={s.sessionID}>
          {new Date(s.startUnix * 1000).toLocaleString("tr-TR", { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" })} · {s.messageCount} mesaj
        </option>
      ))}
    </select>
  </div>
)}
```

`handleCreate`'i resume-aware yap: `resumeID` doluysa `addTerminal` yerine `createTerminalResume(teamID, name, workDir, selectedCLI, promptID, slotIndex, useWorktree, resumeID)`; boşsa mevcut `addTerminal` yolu.

> **Not:** Cross-agent korelasyon SetupWizard'da (tek-agent) zorunlu değil — picker yeterli. Mevcut `handleCreate` gövdesini koru, yalnız resume dalını ekle.

- [ ] **Step 2: Frontend derle**

Run: `cd frontend && npm run build`
Expected: TS hatası yok.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/components/SetupWizard.tsx
git commit -m "feat: #40 Faz-2 SetupWizard — agent autocomplete + oturum picker

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 8: Uçtan-uca doğrulama + native + PR

- [ ] **Step 1: Tam gate**

Run: `make mcp-server && go build ./... && go test ./... && go vet ./... && gofmt -l . && cd frontend && npm run build`
Expected: hepsi temiz.

- [ ] **Step 2: Native (`make dev`, kullanıcı)**

1. Birkaç agent'la oda çalıştır, mesajlaş, kapat → tekrar aç. 2. "Config ile Aç" → modal: her agent geçmiş oturumları (tarih·süre·mesaj+snippet) listeler. 3. Bir agent'ta oturum seç → diğerlerinde aynı-dönem 🟢. 4. "🔗 set et" → diğerleri hizalanır. 5. "Aç" → her agent doğru oturumdan devam eder. 6. "Terminal ekle" → Agent Name autocomplete + seçilince oturum picker. 7. Mod seçici (taze/son/özel) doğru.

- [ ] **Step 3: PR + review döngüsü**

```bash
git push -u origin feat/cli-session-resume-phase2-40
gh pr create --title "feat: #40 Faz-2 — geçmiş oturumlar + çoklu-agent oturum seçici" --body "..."
```

`Closes #40` (Faz-2 #40'ı kapatır). Push sonrası Codex+Copilot review; Faz-1'deki bağımsız poll + adversarial değerlendirme.

---

## Self-Review

**Spec coverage:** §3 sessionlog → T1 ✅ · SessionFilePath/SessionStats → T2 ✅ · Record/Touch wiring → T3 ✅ · enumeration + CreateTerminalResume → T4 ✅ · korelasyon + store → T5 ✅ · OpenTeamModal + mod + set-all → T6 ✅ · SetupWizard picker → T7 ✅ · native → T8 ✅. Edge-case'ler: örtüşmeyen agent (bestMatch null → değişmez, T5/T6) ✅ · aynı-cwd (log agent-adı tutar, T1) ✅ · pruned dosya (FileMissing, T4) ✅ · Gemini/shell (Record edilmez → listede yok, T3) ✅.

**Placeholder scan:** Backend adımları tam kod; frontend "Not"ları mevcut-desen-uyumu (gerçek alan adları/CSS sınıfları oku-uydur), placeholder değil. ✅

**Type consistency:** `Record{FirstSeen,LastSeen}` (T1) ↔ `SessionInfo{StartUnix,LastUnix}` (T4 dönüşüm) ✅ · `SessionFilePath`/`SessionStats` (T2) ↔ ListAgentSessions (T4) ✅ · `SessionInfo` (T4 Go) ↔ types.ts (T5) ↔ overlaps/bestMatch (T5) ↔ OpenTeamModal (T6) ✅ · `CreateTerminalResume(...,resumeID)` (T4) ↔ store (T5) ↔ modal/wizard (T6/T7) ✅ · overlap formülü Go-yok/TS-`sessionCorrelation` tutarlı (`a.start<b.last && b.start<a.last`) ✅.
