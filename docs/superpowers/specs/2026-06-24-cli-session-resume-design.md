# CLI Session Resume — Oda Devam Ederken Agent Oturumlarını Sürdürme

- **Tarih:** 2026-06-24
- **Issue:** [#40](https://github.com/mytsx/agent-chat/issues/40)
- **Durum:** Tasarım onaylandı (brainstorming) → spec inceleme bekliyor
- **İlişkili:** #65 (CLI session ingestion — `internal/ingest` discovery'sini **paylaşır**, ana bağımlılık), #29 (user_prompt loglama), #28 (per-session snapshot), #13 (kapanmış özet/arşiv epic'i), #17 (observer)
- **Rota:** ✅#65 → **#40** → #10

## 1. Problem & Amaç

Bir agent terminali restart edildiğinde CLI **sıfırdan boş bir oturumla** açılıyor; önceki konuşma bağlamı kayboluyor. #29 oda özetini yeni agent'lara enjekte ediyor ama bu özet/parafraz; agent'ın **kendi** tam oturumu (tool çağrıları, ara durumlar, model bağlamı) korunmuyor.

**Amaç:** Terminal çalışırken her AI-CLI'ın **kendi session ID'sini deterministik yakala**, sakla; kullanıcı **"Devam Et"** dediğinde terminali `--resume <id>` ile yeniden başlat — böylece agent kaldığı oturumdan devam etsin.

## 2. Kapsam Kararları (brainstorming'de kilitlendi)

| Karar | Seçim | Gerekçe |
|-------|-------|---------|
| **Kapsam** | Faz-1 yalnız (restart, **in-memory**) | En küçük de-risked dilim; disk persistence (Faz-2) ayrı PR |
| **Tetik** | **Opt-in "Devam Et" düğmesi** | Düz restart (taze) ile resume (devam) ayrı eylemler; görünür kullanıcı seçimi |
| **CLI kapsamı** | **Claude / Copilot / Codex** bu turda; Gemini sonra | Üçünün resume-by-id'si ampirik doğrulandı; Gemini'nin tam-UUID kabulü belirsiz |
| **Session-ID kaynağı** | **`internal/ingest` discovery yeniden kullanımı** | #65 zaten her CLI'ın session dosyasını cwd+spawn-zamanı ile deterministik buluyor; PTY-output parse YOK |

**Non-goals (bu PR):** Faz-2 disk persistence / app-restart restore · Gemini resume **komutu** (yakalama implemente, komut sonraki tur) · cross-tool resume (cli-continues markdown handoff).

## 3. M0 Ampirik De-risk (kurulu sürümlere karşı doğrulandı, 2026-06-24)

Resume komutları kurulu CLI'lara karşı `--help` ile doğrulandı:

| CLI | Sürüm | Resume komutu | Yapı | Session ID kaynağı (gerçek dosyada doğrulandı) |
|-----|-------|---------------|------|------------------------------------------------|
| Claude | 2.1.187 | `claude --resume <uuid> --dangerously-skip-permissions` | **flag** | `~/.claude/projects/{slug}/{uuid}.jsonl` → dosya adı kökü |
| Copilot | 1.0.4 | `copilot --resume=<uuid> --yolo` | **flag (`=` sözdizimi)** | `~/.copilot/session-state/{uuid}/events.jsonl` → üst dizin adı |
| Codex | 0.139.0 | `codex resume <uuid> --dangerously-bypass-approvals-and-sandbox` | **subcommand (önce gelir!)** | `rollout-{ts}-{uuid}.jsonl` ilk satır `session_meta.payload.id` |
| Gemini | 0.29.5 | `gemini --resume <?>` — yalnız `latest`/index belgeli | flag | dosyada top-level `sessionId` (dosya adı yalnız 8-hex prefix) |

**Kritik yapısal bulgu:** Codex'te `resume` bir **subcommand** (en başta, positional); diğer üçü flag. Komut kurucusu tek-tip flag-ekleme **yapamaz** — CLI başına ayrı kurulum gerek.

**Codex notu:** `codex resume` subcommand'i `--dangerously-bypass-approvals-and-sandbox` flag'ini kabul ediyor (doğrulandı).

**Gemini belirsizliği:** v0.29.5 `--help` resume için yalnız `"latest"` veya index belgeliyor; tam-UUID kabulü doğrulanamadı (research 125 gün eski). Session dosyasında tam `sessionId` var ama CLI'ın kabulü **canlı doğrulama** ister → Gemini bu turda yalnız **yakalama** (resume komutu yok).

## 4. Mimari

```
[Terminal çalışırken]
  pty.Create ─spawn─▶ CLI ──(kendi session dosyasına yazar)──▶ ~/.<cli>/.../session.*
                                                                    │
                  internal/ingest.Watcher (zaten #65 için var)  ◀──┘ (DiscoverFile + claim)
                       │  dosyayı keşfedince: ad.SessionID(path) → onSessionID(id)
                       ▼
            app: SetCLISessionID(sessionID, id)  +  emit "terminal:resume-available"
                       │                                         │
                       ▼ (PTYSession.CLISessionID, in-memory)    ▼ (frontend: düğme etkin)
            [Kullanıcı "Devam Et" tıklar]
                       │
                       ▼
            app.ResumeTerminal(sessionID): captured id oku → eski PTY kapat →
                       createTerminal(..., resumeID=id) → cli.GetCommandResume(cliType, id)
                       ▼
            CLI `--resume <id>` ile spawn ──▶ önceki oturumdan devam
```

### Bileşenler

**`internal/ingest` — SessionID çıkarımı (#65 discovery'sini paylaş):**

- `SessionAdapter` arayüzüne yeni metot: `SessionID(path string) string` — keşfedilen dosya yolundan/içeriğinden CLI'ın session ID'sini deterministik çıkarır. Çıkaramazsa `""`.
  - `claudeAdapter`: `strings.TrimSuffix(filepath.Base(path), ".jsonl")` (dosya adı kökü = uuid)
  - `copilotAdapter`: `filepath.Base(filepath.Dir(path))` (`{uuid}/events.jsonl` → üst dizin)
  - `codexAdapter`: ilk satır `session_meta.payload.id` (dosya adındaki ts-tireleri filename-parse'ı kırılgan yapar; mevcut `codexFileCwd` deseni yeniden kullanılır → `codexFileID`)
  - `geminiAdapter`: top-level `sessionId` JSON alanı (dosya adı yalnız 8-hex prefix taşır — yetersiz). **Arayüz tekdüzeliği için implemente edilir; bu turda resume komutuna bağlanmaz.**
- Watcher id'yi dışarı verir: `StartSession`'a yeni opsiyonel parametre `onSessionID func(id string)`. Watcher dosyayı keşfedip **claim** ettiği an (`run` içinde `path = p` başarılı olduktan hemen sonra) bir kez `ad.SessionID(path)` çağrılır; sonuç boş değilse `onSessionID(id)` tetiklenir. `nil` callback / boş id → no-op. (Mute durumu çıkarımı etkilemez — observer da yakalanır.)

**`internal/pty` — PTYSession'da sakla:**

- Yeni alan `PTYSession.cliSessionID` — ingest goroutine'inden yazılır, restart goroutine'inden okunur → senkronize erişim gerek. Mevcut `atomic` deseniyle uyumlu: `cliSessionID atomic.Pointer[string]` (veya küçük mutex). Manager metotları: `SetCLISessionID(sessionID, id string)` ve `GetCLISessionID(sessionID string) string` (bilinmeyen session → no-op / `""`).

**`internal/cli` — CLI başına resume komut kurucusu:**

- `ResumeSupported(cliType CLIType) bool` → `claude`/`copilot`/`codex` için `true`, diğerleri `false`.
- `GetCommandResume(cliType CLIType, sessionID string) (cmd string, args []string)` — yapısal farkı izole eder:
  - `claude` → `"claude", ["--resume", id, "--dangerously-skip-permissions"]`
  - `copilot` → `"copilot", ["--resume="+id, "--yolo"]`
  - `codex` → `"codex", ["resume", id, "--dangerously-bypass-approvals-and-sandbox"]`
  - desteklenmeyen → `GetCommand(cliType)`'a düşer (taze) — savunmacı; çağıran zaten `ResumeSupported` ile gate'ler.

**`app.go` — ResumeTerminal + resumeID threading:**

- Refactor: dışa-açık `CreateTerminal(teamID, agentName, workDir, cliType, promptID, useWorktree, slotIndex)` → iç `createTerminal(..., resumeID string)` ince sarmalayıcısı olur. Dışa-açık imza **değişmez** (frontend Wails binding'i bozulmaz); mevcut çağrı `createTerminal(..., "")` yapar.
  - İç `createTerminal`: komut seçim dalı — `resumeID != "" && cli.ResumeSupported(ct)` ise `cli.GetCommandResume(ct, resumeID)`, değilse `cli.GetCommand(ct)`. Geri kalan her şey (worktree, MCP config, ingest start, startup prompt) **aynen** korunur.
- Yeni bound metot `ResumeTerminal(sessionID string) (string, error)`:
  1. Eski session'dan `cliSessionID := a.ptyManager.GetCLISessionID(sessionID)` ve cliType oku.
  2. `cliSessionID == "" || !cli.ResumeSupported(cliType)` → düz restart'a düş (mevcut `RestartTerminal` mantığı) ve resume edilmediğini bildir (log + ileride toast). Kullanıcı yine terminal alır.
  3. Aksi halde: restart yolu (param yakala → eski PTY kapat → `createTerminal(..., cliSessionID)`).
  - Uygulama: `RestartTerminal`'ın gövdesi `restartInternal(sessionID, resumeID string)` helper'ına çıkarılır; `RestartTerminal` = `restartInternal(id, "")`, `ResumeTerminal` = capture id → `restartInternal(id, capturedID)`.
- Watcher id raporlayınca (CreateTerminal'daki `StartSession` çağrısına `onSessionID` callback'i eklenir): yalnız `cli.ResumeSupported(cliType)` ise `a.ptyManager.SetCLISessionID(sessionID, id)` + `runtime.EventsEmit(ctx, "terminal:resume-available", {sessionID, cliSessionID: id})`. Desteklenmeyen (Gemini/shell) → event yok → düğme pasif kalır.

**Frontend:**

- `frontend/src/lib/types.ts`: `TerminalSession.cliSessionID?: string` (opsiyonel — yakalanana dek `undefined`).
- Event dinleyici: `App.tsx`'teki mevcut `EventsOn("messages:new"...)` / `EventsOn("agents:updated"...)` bloğuna `EventsOn("terminal:resume-available", ...)` eklenir → `useTerminals` store'da eşleşen session'ın `cliSessionID`'sini set eden bir action çağrılır (`EventsOff` temizliğiyle birlikte).
- `TerminalPane.tsx`: restart düğmesinin (`terminal-btn-restart`, satır ~319) yanına yeni **"Devam Et"** düğmesi (`onResume` prop). `disabled={!cliSessionID}`; pasifken Türkçe tooltip ("Devam edilebilir oturum henüz yakalanmadı"), etkinken "Oturumdan devam et".
- `TerminalGrid.tsx`: `onResume={() => resumeTerminal(teamID, sessionID)}` (restart'la simetrik 3 çağrı noktası).
- `useTerminals.ts`: `resumeTerminal(teamID, sessionID)` → `ResumeTerminal(sessionID)` (yeni Wails binding) → restart'la aynı session-değişim mantığı.
- Wails binding regen (`ResumeTerminal` için).

## 5. Agent-facing & UX metinleri (Türkçe + emoji)

- Düğme tooltip'leri Türkçe. Resume tetiklendiğinde log: `[RESUME] agent=%s cli=%s id=%s`. Fallback'te (id yok): `[RESUME] agent=%s — yakalı oturum yok, düz restart`.

## 6. Edge-Case'ler

1. **Claude id-yeniden-üretme bug'ı:** `claude --resume <id>` yeni dosya/uuid yaratır. Resume edilen terminalin **yeni** ingest watcher'ı yeni dosyayı keşfeder → yeni id yakalar → `SetCLISessionID` günceller → sonraki "Devam Et" yeni id'yi kullanır. Zincir kendiliğinden doğru.
2. **Same-cwd claim release:** ResumeTerminal eski PTY'yi `closeTerminalInternal` ile kapatır. `closeTerminalInternal` watcher'ı senkron durdurmaz — watcher PTY-ölümü (`SessionDone`) yolundan async durur ve `finish()` claim'i bırakır (#65). Yeni `createTerminal`'ın watcher'ı dosyayı henüz-bırakılmamış claim yüzünden geçici atlarsa bir sonraki tick'te (700ms) yeniden dener → eventual claim. Bu, **mevcut `RestartTerminal`'ın da izlediği** close→create yolu; #40 yeni bir race eklemez.
3. **Yakalanmamış (henüz dosya yok) / desteklenmeyen (Gemini/shell):** event yok → `cliSessionID` undefined → düğme pasif. ResumeTerminal yine de savunmacı: id yoksa düz restart.
4. **Observer:** muted watcher dosyayı yine keşfeder+claim eder → id çıkarımı mute'tan bağımsız → observer da resume edilebilir. Özel-kasa yok (daha az kod, zararsız).
5. **Worktree:** resume edilen terminal aynı cwd'de açılır (`restartInternal` mevcut worktree mantığını korur); CLI session dosyası cwd-keyed olduğundan resume doğru projeyi bulur.
6. **App quit:** in-memory id kaybolur (Faz-1 kabulü). Uygulama yeniden açılınca "Devam Et" pasif başlar; agent çalışıp dosya yazınca yeniden yakalanır. (Faz-2 disk persistence bunu çözecek.)

## 7. Test Stratejisi (TDD, table-driven `t.Run`)

- **`internal/ingest`** (`*_test.go`, mevcut adapter testleriyle aynı fixture deseni):
  - `SessionID` her adapter için gerçek-fixture: claude dosya-adı kökü, copilot üst-dizin, codex `session_meta.payload.id` (geçici dosya), gemini `sessionId` alanı, bozuk/eksik dosya → `""`.
  - watcher: `onSessionID` keşifte bir kez tetiklenir; nil callback no-op.
- **`internal/cli`**: `GetCommandResume`/`ResumeSupported` her cliType — tam cmd+args eşitliği, codex subcommand sıralaması, copilot `=` sözdizimi, gemini/shell unsupported → fallback.
- **`internal/pty`**: `SetCLISessionID`/`GetCLISessionID` round-trip; bilinmeyen session no-op; eşzamanlı erişim (race) testi.
- **`app.go`**: ResumeTerminal fallback (id yok → düz restart); id+supported → resume komutu seçilir (test edilebilirlik için komut-seçimi saf bir helper'a çıkarılabilir).
- **Canlı/native (kullanıcı, `make dev`):** Claude/Copilot/Codex'te gerçek resume — terminal çalıştır, "Devam Et", önceki bağlamın korunduğunu gör.

## 8. Build & Doğrulama

`make mcp-server` (embed şart) → `go test ./...` + `go vet ./...` + `gofmt -l` → Wails binding regen → `make dev` görsel/native test. Push'ta Codex+Copilot review (Gemini genelde kotada); her turda `@codex review` manuel tetik + copilot reviewer botunu poll'la yokla.

## 9. Dosya Değişiklikleri (özet)

| Dosya | Değişiklik |
|-------|-----------|
| `internal/ingest/ingest.go` | `SessionAdapter`'a `SessionID(path) string`; `StartSession` imzasına `onSessionID func(string)` |
| `internal/ingest/adapter_{claude,copilot,codex,gemini}.go` | `SessionID` impl (codex'e `codexFileID` helper) |
| `internal/ingest/watcher.go` | keşif+claim sonrası `onSessionID` tetikle |
| `internal/pty/manager.go` | `PTYSession.cliSessionID` + `Set/GetCLISessionID` |
| `internal/cli/detector.go` | `ResumeSupported` + `GetCommandResume` |
| `app.go` | `createTerminal(...,resumeID)` iç refactor, `restartInternal(...,resumeID)`, `ResumeTerminal` bound metot, `StartSession`'a `onSessionID` + event |
| `frontend/src/lib/types.ts` | `TerminalSession.cliSessionID?` |
| `frontend/src/store/useTerminals.ts` | `resumeTerminal` + event dinleyici |
| `frontend/src/components/{TerminalPane,TerminalGrid}.tsx` | "Devam Et" düğmesi + wiring |
| `frontend/wailsjs/...` | `ResumeTerminal` binding regen |
| `internal/*/_test.go` | yeni testler |
