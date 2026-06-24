# CLI Session Resume Faz-2 — Geçmiş Oturumlar + Çoklu-Agent Oturum Seçici

- **Tarih:** 2026-06-24
- **Issue:** [#40](https://github.com/mytsx/agent-chat/issues/40) (Faz-2 — kapatma bu fazla)
- **Durum:** Tasarım onaylandı (brainstorming + görsel companion) → spec inceleme bekliyor
- **İlişkili:** Faz-1 ✅ MERGED (PR [#67](https://github.com/mytsx/agent-chat/pull/67) `6db640f`), #65 (ingest), #28 (per-session snapshot), #29 (özet)
- **Branch:** `feat/cli-session-resume-phase2-40` (Faz-1 main'e merge edildi)

## 1. Problem & Amaç

Faz-1 yalnız **en son** captured session'ı in-memory tutar ve restart'ta resume eder. Kullanıcı şunları istiyor:

1. **Geçmiş oturumları tarih/saat bazlı yakala** — yalnız son değil, agent'ın TÜM geçmiş oturumları listelenebilsin.
2. **Oturum seçici** — "Config ile Aç" ve "terminal ekle" akışında, agent'ın geçmiş oturumlarından **belirli birini** seçip ondan başlat.
3. **Agent adı geçmişi** — terminal eklerken Agent Name alanında önceki agent adları gelsin.
4. **Çapraz-agent zaman korelasyonu** — bir agent'ta oturum seçince, diğer agent'larda **aynı dönemde açık olan** (zamansal örtüşen) oturumlar farklı renkte vurgulansın.
5. **"Hepsini aynı döneme set et" kısayolu** — bir seçimden sonra diğer agent'ları aynı dönemdeki oturumlarına otomatik ayarla.

**Amaç:** Bir odaya/takıma devam ederken, **hangi geçmiş çalışma dönemini** sürdüreceğini seçebilmek — ve tüm agent'ları o döneme tek tıkla hizalamak.

## 2. Kapsam Kararları (brainstorming + companion'da kilitlendi)

| Karar | Seçim |
|-------|-------|
| **Fazlama** | Faz-1 merge edildi; bu Faz-2 ayrı PR. #40 Faz-2 bitince kapanır |
| **Agent→session eşlemesi** | **Kalıcı session-history log'u** (CLI dosyaları cwd-anahtarlı, agent-adı içermez → kendi log'umuz gerçek adı tutar) |
| **"Aynı dönem" tanımı** | **Zamansal örtüşme** — iki oturumun `[firstSeen, lastSeen]` açık-pencereleri kesişiyorsa aynı dönem |
| **UI layout** | **Variant B** (kompakt dropdown) — companion'da onaylandı |
| **Mod seçici** | ✨ Hepsi taze · ⏯ Son oturumlardan · 🎛 Özel seçim |
| **Oturum satırı** | `tarih saat · süre · mesaj sayısı` (+ ilk-mesaj snippet) |

**Non-goals (bu PR):** Çapraz-tool resume (Faz-3) · Gemini resume **komutu** (yakalama+listeleme çalışır, resume komutu yine yok — Faz-1 ile aynı).

## 3. Mimari

```
[Terminal çalışırken]
  createTerminal onSessionID callback (Faz-1) ──┬─▶ ptyManager.SetCLISessionID (Faz-1, in-memory)
                                                 └─▶ sessionlog.Record(room,agent,cliType,sid,cwd)  ◀── YENİ
  closeTerminalInternal ───────────────────────────▶ sessionlog.Touch(sid)  (lastSeen=now)  ◀── YENİ

[Oturum seçici UI]
  ListKnownAgents / ListAgentSessions  ◀── sessionlog (room+agent filtreli) + CLI-dosyası zenginleştirme
                       │
                       ▼
  OpenTeamModal / SetupWizard  ──(per-agent resumeID)──▶ CreateTerminalResume ──▶ createTerminal(...,resumeID)
                       │
              korelasyon (frontend): [start,last] pencere overlap → 🟢 aynı dönem
```

### Bileşenler

**`internal/sessionlog` (yeni paket):**

- Depo: `~/.agent-chat/session-history.json` — atomik temp+rename (mevcut `teams.json`/`prompts.json` deseni). **sessionID anahtarlı** map:
  ```go
  type Entry struct {
      Room      string  `json:"room"`
      AgentName string  `json:"agent_name"`
      CLIType   string  `json:"cli_type"`
      Cwd       string  `json:"cwd"`
      FirstSeen float64 `json:"first_seen"` // unix; ilk capture (oturum bizce açıldı)
      LastSeen  float64 `json:"last_seen"`  // unix; son görülme (oturum bizce kapandı)
  }
  // store: map[sessionID]Entry
  ```
- API:
  - `Record(sessionID, room, agent, cliType, cwd string)` — yoksa ekler (`FirstSeen=LastSeen=now`), varsa `LastSeen=now` (idempotent capture). Boş sessionID → no-op.
  - `Touch(sessionID string)` — varsa `LastSeen=now` (FirstSeen korunur). Bilinmeyen → no-op.
  - `ListAgents(room string) []string` — o odada görülmüş distinct agent adları (yeniden→eskiye, lastSeen'e göre).
  - `ListSessions(room, agent string) []Entry` — o (room,agent)'ın oturumları, lastSeen yeniden→eskiye.
- `now()` enjekte edilebilir (test). `last_seen`/`first_seen` `float64` unix (proje konvansiyonu).

**App katmanı (`app.go`) — yazım + enumerasyon + resume-from-any:**

- **Yazım:** createTerminal'ın `onSessionID` callback'i (Faz-1) artık `cli.ResumeSupported(ct)` ise `a.sessionLog.Record(id, room, agentName, cliType, ingestCwd)` da çağırır. `closeTerminalInternal` → `a.sessionLog.Touch(s.CLISessionID-or-captured)`. (Capture, resume-destekli CLI'larda olur; Gemini/shell loglanmaz.)
- **Enumerasyon (bound, Wails):**
  - `ListKnownAgents(teamID string) []string` — `sessionlog.ListAgents(room)` ∪ team config agent adları (distinct).
  - `ListAgentSessions(teamID, agentName string) []SessionInfo` — `sessionlog.ListSessions(room,agent)` → her Entry **zenginleştirilir**: `DurationSec = LastSeen-FirstSeen`; `MessageCount`+`Snippet` o anda CLI dosyasından okunur (ingest adapter discovery/parse yeniden kullanılır; cwd+sessionID'den dosya yolu türetilir, `ingest.SessionFilePath` benzeri); dosya yoksa `MessageCount=0, Snippet=""` (satır yine listelenir, "dosya yok" işaretiyle). `SessionInfo{SessionID, CLIType, StartUnix, LastUnix, DurationSec, MessageCount, Snippet, FileMissing}`.
- **Resume-from-any (bound):** `CreateTerminalResume(teamID, agentName, workDir, cliType, promptID string, useWorktree bool, slotIndex int, resumeID string) (string, error)` → `a.createTerminal(...,resumeID)`. (Faz-1 iç imzası zaten `resumeID` alıyor.) `resumeID=""` → taze (taze CreateTerminal'a eşdeğer).

**Frontend — korelasyon + UI:**

- **Korelasyon (saf TS, `frontend/src/lib/sessionCorrelation.ts`):** `overlaps(a, b)` = `a.StartUnix < b.LastUnix && b.StartUnix < a.LastUnix`. Modal, seçili oturumun penceresiyle her diğer agent'ın oturumlarını karşılaştırıp 🟢 işaretler.
- **`OpenTeamModal.tsx` (yeni):** "Config ile Aç" düğmesi artık `handleOpenFromConfig` yerine bu modalı açar.
  - Üst **mod seçici** (segmented): ✨ Hepsi taze (tüm resumeID=""), ⏯ Son oturumlardan (her agent en son oturumuna ön-set), 🎛 Özel (elle).
  - Team agent'ları satır satır; her satır kompakt dropdown (seçili oturum / "✨ Yeni"). Dropdown açılınca o agent'ın `ListAgentSessions` listesi; başka agent'ta seçim varsa **aynı dönem** olanlar 🟢.
  - Bir seçimden sonra **"🔗 Diğerlerini aynı döneme set et"**: her diğer agent'ı seçili pencereyle örtüşen **en iyi** oturumuna ayarlar. "En iyi" = en çok örtüşme süresi (`min(aLast,bLast)-max(aStart,bStart)`); beraberlikte başlangıcı seçili pencereye en yakın olan. Örtüşeni yoksa o agent kendi seçiminde kalır.
  - "Aç (N)" → her agent için `createTerminalResume(teamID, agent, workDir, cliType, promptID, useWorktree, slotIndex, resumeID)` (mevcut `openTeamFromConfig`'in slot/capacity mantığını korur).
- **`SetupWizard.tsx`:** Agent Name input → `ListKnownAgents` ile autocomplete (datalist). Geçmişi olan bir ad seçilince/yazılınca altında oturum dropdown'ı (`ListAgentSessions`); korelasyon o an **çalışan** terminallerin captured session'larına göre (live). Seçilen oturum `addTerminal` yerine `createTerminalResume` ile açılır.
- **`useTerminals.ts`:** `createTerminalResume` action; `listKnownAgents`/`listAgentSessions` fetch wrapper'ları.

### Bileşen haritası

| Dosya | Değişiklik |
|-------|-----------|
| `internal/sessionlog/` (yeni) | store (atomik json) + Record/Touch/ListAgents/ListSessions + enjekte-edilebilir now |
| `internal/sessionlog/*_test.go` (yeni) | table-driven: record/touch idempotent, list sıralama, room/agent filtre |
| `internal/ingest/` | `SessionFilePath(cliType, cwd, sessionID) (string,bool)` helper (enrichment için id→path). Claude `{slug(cwd)}/{id}.jsonl` + Copilot `{id}/events.jsonl` doğrudan Join; **Codex dosya adı timestamp içerir → id'ye göre `~/.codex/sessions/**/rollout-*-{id}.jsonl` glob/scan** (pür Join değil). + `MessageCount`/`Snippet` parse (adapter user-mesaj parse yeniden kullanılır) |
| `app.go` | sessionLog init; onSessionID→Record, close→Touch; `ListKnownAgents`/`ListAgentSessions`/`CreateTerminalResume` bound metotlar; `SessionInfo` tipi |
| `frontend/src/lib/sessionCorrelation.ts` (yeni) | overlap + "en iyi eşleşme" saf fonksiyonlar |
| `frontend/src/components/OpenTeamModal.tsx` (yeni) | mod + dropdown satırları + korelasyon + set-all + aç |
| `frontend/src/components/SetupWizard.tsx` | agent autocomplete + session picker |
| `frontend/src/components/TerminalGrid.tsx` | "Config ile Aç" → OpenTeamModal aç |
| `frontend/src/store/useTerminals.ts` | `createTerminalResume` + list fetch action'ları |
| `frontend/src/lib/types.ts` | `SessionInfo` mirror |
| `frontend/wailsjs/...` | yeni 3 bound metot regen |

## 4. Veri Akışı

1. **Capture:** agent terminali çalışır → ingest watcher session dosyasını keşfeder → `onSessionID(id)` → `SetCLISessionID` (Faz-1, resume düğmesi) **+** `sessionlog.Record(...)` (Faz-2, kalıcı geçmiş). Resume sonrası yeni id de Record edilir (zincir geçmişe eklenir).
2. **Kapanış:** `closeTerminalInternal` → `sessionlog.Touch(capturedID)` → `LastSeen=now` (oturumun açık-penceresi kapanır).
3. **Listeleme:** modal/wizard açılır → `ListAgentSessions(team,agent)` → log + CLI-dosyası zenginleştirme → UI dropdown.
4. **Korelasyon:** kullanıcı bir agent'ta oturum seçer → frontend, seçili `[start,last]` penceresini her diğer agent'ın oturum pencereleriyle karşılaştırır → 🟢.
5. **Aç:** her agent kendi `resumeID`'siyle (ya da "") `CreateTerminalResume` → Faz-1 resume borusu (ResumeSeedFor dahil — geçmiş herhangi bir id için çalışır).

## 5. Edge-Case'ler

1. **Örtüşmeyen agent:** "set et"te kendi seçiminde kalır (sıfırlanmaz). Modal hiçbir agent'ı zorla taze yapmaz.
2. **Aynı cwd (manager/observer):** CLI dosyaları cwd-anahtarlı olduğundan belirsiz olurdu — ama **log gerçek agent adını tutar** → `ListSessions(room,agent)` doğru ayrışır.
3. **Pruned/silinmiş CLI dosyası:** log entry'si kalır ama zenginleştirme `FileMissing=true` döner; satır listelenir (tarih/süre log'dan), resume denenirse CLI kendi "session not found" davranışını verir (savunmacı — düz başlar). Düğme tooltip uyarısı.
4. **Gemini/shell:** capture yok (resume desteklenmiyor) → log'da görünmez → dropdown'da yalnız "✨ Yeni". (Gemini yakalama+listeleme istenirse ayrı; bu turda resume yok.)
5. **Resume id-yeniden-üretme (Claude):** her capture Record edilir → geçmişte birden çok Claude session (zincir) görünebilir; doğru (her biri ayrı çalışma dönemi).
6. **Eşzamanlı yazım:** sessionlog atomik temp+rename + mutex (birden çok terminal aynı anda Record/Touch).

## 6. Test Stratejisi (TDD, table-driven `t.Run`)

- **`internal/sessionlog`:** Record yeni-vs-var (idempotent, FirstSeen korunur), Touch (FirstSeen korunur, bilinmeyen no-op), ListAgents/ListSessions (room+agent filtre, lastSeen sıralama), boş-sessionID no-op, atomik persist + reload, eşzamanlı (`-race`).
- **`internal/ingest`:** `SessionFilePath` her CLI (id→path türetimi) + missing.
- **`app.go`:** enumerasyon zenginleştirme (mock log + fixture dosya → SessionInfo); `CreateTerminalResume` resumeID threading (komut-seçimi Faz-1'de test edildi).
- **`frontend/src/lib/sessionCorrelation`:** overlap (kesişen/kesişmeyen/sınır), "en iyi eşleşme" seçimi — saf fonksiyon, kolay test (varsa vitest yoksa `npm run build` typecheck + native).
- **Native (`make dev`):** geçmiş oturum listele, seç, çapraz-korelasyon yeşil, "set et", aç → doğru oturumdan devam.

## 7. Build & Doğrulama

`make mcp-server` → `go test ./...` + `go vet ./...` + `gofmt -l` → `wails generate module` → `cd frontend && npm run build` → `make dev` native. Agent-facing metinler Türkçe+emoji. PR review döngüsü (Faz-1'deki gibi: Codex+Copilot hook'lar + bağımsız poll).
