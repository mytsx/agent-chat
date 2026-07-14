# Usage Takibi + Limite Yaklaşınca CLI Geçişi (+ Usage Paneli)

**Issue:** [#10](https://github.com/mytsx/agent-chat/issues/10) — oda-özellikleri roadmap'inin son halkası.
**Branch:** `feat/usage-tracking-cli-switch-10`
**Tasarım kaynağı:** issue #10'a parklanmış "🧭 Tasarım & Analiz Notu" (A–E kararları) + bu spec'teki M0 düzeltmesi.

## 1. Problem & Amaç

Her AI agent terminali bir CLI (Codex/Claude/Copilot/Gemini) çalıştırıyor ve her CLI'ın bir usage/limit
bütçesi var. Kullanıcı şu an "hangi agent limite yaklaşıyor" bilgisini göremiyor ve limit dolunca agent
sessizce tıkanıyor. Amaç:

1. **Pasif usage takibi** — her terminalin CLI session dosyasından usage sinyalini topla (ingest tick'ine piggyback).
2. **Usage paneli** — panel-başı mini rozet + TabBar `📊` → tüm agent'ları gösteren modal.
3. **Eşik-uyarı + tek-tık CLI geçişi** — limit dolmaya yakınken (kullanıcı onaylı) agent'ı başka bir CLI'a devret.

## 2. Kapsam Kararları (parklama-yorumunda kilitlendi — A–E)

- **A — Sinyal kaynağı:** **Hibrit.** Codex authoritative limit-yüzdesi verir (dosyadan); Claude/Copilot/Gemini
  yalnız token tüketimi (payda yok) + opsiyonel PTY reaktif "limit reached" metni.
- **B1 — Handoff:** **Oda-tabanlı + kısa primer.** Yeni CLI aynı odaya katılır (`read_all_messages` ile geçmişi
  görür) + startup prompt'a handoff primeri enjekte edilir. Cross-CLI resume İMKANSIZ → geçiş = yeni session.
- **B2 — Hedef CLI seçimi:** **Öneri + dropdown override.** Kurulu+müsait CLI'lardan makul hedef önerilir
  (varsayılan sıra Codex→Claude→Copilot→Gemini), kullanıcı değiştirebilir.
- **C — Geçiş otonomisi:** **Eşik-uyarı + tek-tık geçiş** (kullanıcı onaylı; tam otomatik DEĞİL).
- **D — Panel:** **Panel-başı mini rozet + TabBar `📊` detaylı modal.**
- **E — Kapsam/faz:** **Tek spec/PR** (takip+panel+uyarı+geçiş birlikte; kod içeride modüler).

### Kalan-açık kararlar (default ile ilerleniyor)

1. **Eşikler:** Codex warn `≥%85` / critical `≥%95`; `SettingsModal`'da konfigüre; 5s VE haftalık pencerelerden
   önce aşan tetikler. Token CLI'ları default'ta sessiz (yalnız gösterim).
2. **Eski terminal akıbeti:** geçişte eski terminal aynı slotta **kapatılıp** hedef CLI ile değiştirilir (in-slot replace) — `restartInternal` deseni.
3. **PTY-parse kapsamı:** ANSI-strip + dar regex, best-effort (Codex authoritative kaldıkça yeterli).

## 3. M0 DÜZELTMESİ — gerçek Codex `rate_limits` şeması (2026-07-14 teyitli)

Parklama-yorumundaki M0 örnekleri eskimişti. Gerçek güncel rollout (`~/.codex/sessions/.../rollout-*.jsonl`),
481 dosya tarandı:

```json
{"timestamp":"...","type":"event_msg","payload":{
  "type":"token_count",
  "info":{"total_token_usage":{"input_tokens":...,"cached_input_tokens":...,"output_tokens":...,"total_tokens":...},
          "model_context_window":258400},
  "rate_limits":{
    "limit_id":"codex","limit_name":null,
    "primary":{"used_percent":43.0,"window_minutes":10080,"resets_at":1784487700},
    "secondary":null,"credits":null,"individual_limit":null,
    "plan_type":"pro","rate_limit_reached_type":null
  }}}
```

**Kritik farklar (tasarımı bunlara göre kur):**
- `rate_limits` bağımsız satır DEĞİL → `type=="event_msg"` **ve** `payload.type=="token_count"` satırının içinde `payload.rate_limits`.
- Bu makinedeki 481 rollout'un tamamında yalnız `primary` dolu (`window_minutes=10080`=haftalık), `secondary` **null**.
  → "primary=5s / secondary=haftalık" varsayımı YANLIŞ. Pencere kimliği **`window_minutes`'ten türetilir**, slot adına anlam yüklenmez. `secondary` null olabileceğinden parse null-güvenli olmalı.
- **Model:** `type=="event_msg"` & `payload.type=="thread_settings_applied"` → `payload.thread_settings.model` (örn. `gpt-5.6-sol`). Son görüleni tut. Yoksa boş.
- Codex `payload.info.total_token_usage` de verir → Codex token da taşır (ikincil; authoritative olan yüzde).

**Token-CLI şemaları (teyitli):**
- **Claude** `~/.claude/projects/{slug}/{uuid}.jsonl`: assistant satırlarında `message.usage`
  (`input_tokens`, `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`) + `message.model` (`claude-opus-4-8`).
- **Copilot** `~/.copilot/session-state/{id}/events.jsonl`: `session.shutdown` içinde
  `usage:{inputTokens,outputTokens,cacheReadTokens,...}` + `currentModel` (`gpt-5.3-codex`). Canlıyken sınırlı.
- **Gemini** `~/.gemini/tmp/{sha}/chats/session-*.json`: mesaj başına `tokens`+`cached` (monolitik JSON); model alanı yoksa boş.

Token CLI'ları payda vermez → **renkli eşik/auto-switch YOK**, yalnız gösterim + opsiyonel PTY reaktif.

## 4. Mimari

`internal/ingest` watcher'ını **genişlet** (sıfırdan yazma). Usage = aynı dosyadan farklı alanları oku.

### Bileşen haritası

| Bileşen | Değişiklik |
|---|---|
| `internal/usage/` (**yeni leaf paket**, `internal/sanitize` gibi std-only) | `Snapshot`, `Window`, `Kind`, `Thresholds`, `Status` tipleri + `Evaluate(Snapshot, Thresholds) Status` (saf, CLI-bağımsız). |
| `internal/ingest/` | Her adapter'a `ParseUsage(path) (*usage.Snapshot, error)` (yeni opsiyonel arayüz). Watcher'a `onUsage func(*usage.Snapshot)` callback — poll tick'ine piggyback. `SessionAdapter` arayüzü genişlemez; ayrı `UsageParser` arayüzü + type-assert. Reaktif PTY sinyali için `usage.ScanRateLimitHit(text) bool` (ANSI-strip + dar regex). |
| `app.go` | `StartSession`'a `onUsage` bağla → Wails `usage:updated` emit. `onOutput` (app.go:119) handler'ında reaktif tarama. Yeni `SwitchTerminal(sessionID, targetCLI)` (`restartInternal` deseni, resumeID YOK, handoff primeri). Yeni `appSettings` alanları + `GetUsageThresholds`/`SetUsageThresholds`. |
| `internal/cli/startup.go` | `ComposeStartupPrompt`'a opsiyonel **handoff segment'i** (yeni parametre; mevcut segment sırası korunur). |
| `frontend/` | `useUsage` store + `TerminalPane` rozeti + `UsagePanelModal` + geçiş diyaloğu + `SettingsModal` eşik alanı + `UsageSnapshot` tipi. |

**Hub'a DOKUNULMAZ** — usage app-local PTY/ingest durumu, Wails event ile frontend'e gider. Oda paylaşımı zaten hub'da; handoff için yeni hub işi yok. **`prompts/` embed'ine dokunulmaz** — handoff/uyarı metni kod içinde.

### `usage.Snapshot` şeması (M0-düzeltilmiş)

```go
type Kind int
const ( KindNone Kind = iota; KindPercentLimit; KindTokenCount )

// Window is one Codex rate-limit window. WindowMinutes identifies it (10080=haftalık,
// 300=5s pencere vb.); no semantic meaning is attached to primary/secondary slot order.
type Window struct {
    UsedPercent   float64 `json:"usedPercent"`
    WindowMinutes int     `json:"windowMinutes"`
    ResetsAt      int64   `json:"resetsAt"` // epoch sec; 0 = bilinmiyor
}

type Snapshot struct {
    SessionID string  `json:"sessionID"`
    CLI       string  `json:"cli"`   // "codex" | "claude" | "copilot" | "gemini"
    Kind      Kind    `json:"kind"`
    // Codex authoritative (nil = o slot dosyada null/yok):
    Primary   *Window `json:"primary,omitempty"`
    Secondary *Window `json:"secondary,omitempty"`
    PlanType  string  `json:"planType,omitempty"`
    // Token-tüketim CLI'ları (ve Codex ikincil):
    InputTokens  int64 `json:"inputTokens,omitempty"`
    OutputTokens int64 `json:"outputTokens,omitempty"`
    CacheTokens  int64 `json:"cacheTokens,omitempty"`
    // Ortak:
    Model     string `json:"model,omitempty"`
    UpdatedAt int64  `json:"updatedAt"` // epoch sec, snapshot üretim anı (app tarafından damgalanır)
}
```

`UpdatedAt`, `Math.random`/`Date.now` script kısıtına takılmaz — Go tarafında `time.Now()` app'te damgalanır (usage paketi saf; UpdatedAt'i çağıran doldurur veya adapter geçer).

### `Evaluate` (saf, test edilebilir)

```go
type Thresholds struct{ WarnPercent, CriticalPercent float64 } // default 85 / 95
type Status int
const ( StatusUnknown Status = iota; StatusOK; StatusWarn; StatusCritical )

// Evaluate: yalnız KindPercentLimit için renkli status üretir. Primary VE Secondary'nin
// (nil olmayanların) used_percent'lerinin MAX'ı alınır; >=critical→Critical, >=warn→Warn,
// aksi→OK. KindTokenCount / KindNone → StatusUnknown (renksiz gösterim, auto-switch yok).
func Evaluate(s Snapshot, t Thresholds) Status
```

### Veri akışı

```
CLI session dosyası                    PTY çıktısı (onOutput)
   │ ingest poll tick (700ms)             │
   ▼                                      ▼
adapter.ParseUsage(path)            usage.ScanRateLimitHit(ANSI-strip)
   │ *usage.Snapshot                      │ bool (reaktif limit-hit)
   └──► onUsage(snapshot) ◄───────────────┘
            ▼ app.go: UpdatedAt damgala + usage.Evaluate(snapshot, thresholds)
   Wails "usage:updated" {snapshot, status}
            ▼
   frontend useUsage store → rozet + modal + (warn/critical → geçiş diyaloğu)
```

## 5. Usage Paneli (D)

- **Panel-başı rozet** (`TerminalPane.tsx` başlığı, `cli-badge` yanına — satır ~288):
  - Codex: `max(primary,secondary)` → `🟢 <warn` / `🟡 warn` / `🔴 ≥critical` + `%NN`. Hover: her pencereyi
    `window_minutes`'ten türetilen etiket + reset saati (`primary(7g): %43 · sıfırlanma 14:30`).
  - Token CLI: `Claude · 142k tok` (renksiz — payda yok). Hover: input/output/cache + model.
  - Sinyal yok → gizli / `—`.
- **TabBar `📊` → `UsagePanelModal`** (tüm agent'lar tablo: Agent · CLI · Durum · Reset · Model). Canlı
  `usage:updated`. Token satırları Durum/Reset'te "—".

## 6. Geçiş Akışı + Handoff (B + C)

1. **Tetik:** Codex `Evaluate ≥ Warn`, VEYA PTY `ScanRateLimitHit`, VEYA (opsiyonel) token eşiği.
2. **Uyarı:** rozet kırmızıya döner + geçiş diyaloğu (`SettingsModal` iskeleti): `⚠️ Codex limit doluyor (%91)` +
   hedef CLI dropdown (öneri: ilk müsait kurulu CLI) + `[Geçişi onayla] [Yoksay]`.
3. **Onayda `SwitchTerminal(sessionID, targetCLI)`:**
   - Eski session'ın team/agent/workDir/slot/room'u okunur (`restartInternal` gibi).
   - Eski terminal aynı slotta kapatılır; hedef CLI ile **yeni terminal** (`createTerminal`, **resumeID YOK**).
   - Startup prompt'a handoff primeri (yeni segment): `⚠️ {eski CLI} limit doldu, devralıyorsun. Detay için read_all_messages çağır.`
   - Yeni terminal aynı odaya katılır (`AGENT_CHAT_ROOM`) → geçmişi görür.

### `ComposeStartupPrompt` handoff segmenti

Yeni `handoffFrom string` parametresi (boş = segment yok). Segment sırası: base→global→charter→summary→**handoff**→selected→join.
Handoff, özet ile aynı "labelli bağlam" deseninde (kendi başlığı, charter'ı ezmez):
```
## Devralma Notu (bağlam)
⚠️ '{handoffFrom}' agent'ının CLI limiti doldu; görevi sen devralıyorsun. Oda geçmişini read_all_messages ile oku.
```

## 7. Eşikler + Settings

`appSettings`'e alanlar (default 85/95; 0 → default'a düş):
```go
type appSettings struct {
    DeferralEnabled  bool    `json:"deferral_enabled"`
    UsageWarnPercent float64 `json:"usage_warn_percent"` // default 85
    UsageCritPercent float64 `json:"usage_crit_percent"` // default 95
}
```
`GetUsageThresholds() usage.Thresholds` / `SetUsageThresholds(warn, crit float64) error` (Deferral getter/setter deseni). `SettingsModal`'a yeni `form-group` + optimistic rollback.

## 8. Edge-Case'ler

- **Codex `secondary=null`:** parse nil bırakır; Evaluate yalnız primary'i sayar. (Bu makinede daima böyle.)
- **`rate_limits` hiç yok** (yeni session, ilk API yanıtı gelmeden): `ParseUsage` → `(nil, nil)`; rozet `—`.
- **Token-only CLI:** `Kind=TokenCount`, `Evaluate→Unknown`; auto-switch tetiklemez; rozet renksiz.
- **Model bulunamadı:** boş string; rozet model'siz gösterir.
- **Bozuk/kısmi JSON satırı:** adapter skip (mevcut `readCompleteJSONLines` partial-line koruması geçerli).
- **`SwitchTerminal` hedef CLI kurulu değil:** hata döndür, eski terminali kapatma (frontend diyaloğu yalnız kurulu CLI'ları listeler; backend defense-in-depth kontrol eder).
- **PTY reaktif yanlış-pozitif:** dar regex (`rate limit|429|usage limit reached|limit reached`), ANSI-strip sonrası; normal çıktıda tetiklenmemeli (test ile kanıtla).

## 9. Test Stratejisi (TDD, table-driven `t.Run`)

**`internal/usage/` (saf):**
1. `Evaluate` sınırları: ok/warn/critical, primary-only, primary+secondary max, nil-slot, TokenCount→Unknown, eşik 0→default.
2. `ScanRateLimitHit`: ANSI-gürültülü "limit reached" yakalar; normal çıktı (kod, "rate" kelimesi geçen prose) yanlış-pozitif VERMEZ.

**`internal/ingest/` (fixture'lı adapter testleri):**
3. `codex ParseUsage`: gerçek-şekilli rollout → primary %+window+reset+plan+model; `secondary=null` → nil; token toplamı; `rate_limits` yok → nil.
4. `claude/copilot/gemini ParseUsage`: token toplamı + model; Kind=TokenCount; limit-% YOK.
5. Usage-sinyali olmayan dosya → `(nil, nil)`.

**`app.go` / `internal/cli/`:**
6. `ComposeStartupPrompt` handoff parametresi: handoffFrom dolu → segment doğru konumda; boş → segment yok; mevcut sıra korunur.
7. `SwitchTerminal` (mevcut restart testleri deseni): aynı slot+oda, resumeID yok, handoff enjekte, eski terminal kapatıldı.

**Frontend:** `npm run build` derlenir (TS tip kontrolü). Rozet/modal davranışı `make dev` görsel (kullanıcıda).

## 10. Build & Doğrulama

```bash
GOFLAGS=-mod=readonly make mcp-server && GOFLAGS=-mod=readonly go build ./...
GOFLAGS=-mod=readonly go test ./... && GOFLAGS=-mod=readonly go vet ./... && gofmt -l .
(cd frontend && npm run build)
```
`-mod=readonly` ŞART (lokal wails CLI go.mod'u 2.12→2.11 düşürüyor). `go.mod` diff'siz kalmalı.

## 11. Referanslar (kod)

- Ingest: `internal/ingest/watcher.go` (poll tick), `adapter_codex.go` / `adapter_claude.go` / `adapter_copilot.go` / `adapter_gemini.go`, `jsonl.go` (`parseCompleteJSONLUserMessages`), `adapterfor.go`.
- Startup: `internal/cli/startup.go` (`ComposeStartupPrompt`), `app.go:1416` (`composeAgentPrompt`), `app.go:1491` (`sendStartupPrompt`).
- Terminal yaşam döngüsü: `app.go:840` (`createTerminal`), `app.go:1252` (`restartInternal`), `app.go:2007` (`CloseTerminal`).
- Settings: `app.go:2747` (`appSettings` + load/save + Get/SetDeferralEnabled).
- Leaf paket deseni: `internal/sanitize/sanitize.go`.
- Frontend: `store/useSummaries.ts` (store deseni), `App.tsx:95-156` (event abonelik), `components/TerminalPane.tsx:284` (başlık/rozet), `components/TabBar.tsx:120` (buton→modal), `components/SettingsModal.tsx` (modal+settings), `components/RoomSummaryModal.tsx` (tablo+onay), `lib/types.ts` (tipler), `wailsjs/go/main/App.d.ts` (bindings).
