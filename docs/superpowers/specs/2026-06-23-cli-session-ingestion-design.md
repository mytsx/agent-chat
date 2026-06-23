# CLI Session Ingestion — Terminale Yazılan Mesajları Yakalama

- **Tarih:** 2026-06-23
- **Issue:** [#65](https://github.com/mytsx/agent-chat/issues/65)
- **Durum:** Tasarım onaylandı (brainstorming) → spec inceleme bekliyor
- **İlişkili:** #29 (user_prompt logla altyapısı — yeniden kullanılır), #58 (timestamp/oda/dedup düzeltmeleri, PR #64), #40 (CLI resume — session-dosyası keşfini paylaşır), #13 (kapanmış özet/arşiv epic'i), #17 (observer gizliliği)

## 1. Problem & Amaç

Kullanıcının **doğrudan agent terminaline (xterm) yazdığı** mesajlar oda transcript'ine/özetine girmiyor. Bugün yalnız iki yol `user_prompt` olarak loglanıyor:

- **Broadcast** (#27 — `BroadcastToTeam` → `logUserPrompt`)
- **Prompt-send** (`SendPromptToAgent` → `logUserPrompt`)

Terminale doğrudan yazılanlar (`WriteToTerminal` → ham PTY) hiç loglanmıyor. Bu, oda özetinin (#29 `ReadFullTranscript`) gerçek konuşmayı eksik yansıtmasına yol açıyor.

**Amaç:** Kullanıcının agent'a gönderdiği **her** mesaj — hangi yolla olursa olsun (doğrudan yazma, broadcast, sesli dikte) — oda transcript'ine tam bir kez `user_prompt` olarak işlensin; böylece özet eksiksiz olsun.

## 2. Neden Composer/Keystroke Değil → Session-File Ingestion

Giriş yüzeyi **mükemmel bir ayna** olmalı: agent TUI'lerinin tuş kombinasyonları (Shift+Tab mod-döngüsü, Esc, slash-komutlar, vim-modları, geçmiş gezinme, bracketed-paste, `@`-dosya-bahsi, image-paste…) **sınırsız ve her sürümde değişiyor** — Claude Code artık kullanıcı-özelleştirmeli `keybindings.json` bile destekliyor. Değerlendirilen alternatifler:

- **Composer (temiz metin kutusu):** mükemmel ayna OLAMAZ — keyfi kombinasyon geçişi yapamaz. ❌
- **Keystroke yakalama (ham byte tampon, Enter'da):** ayna korunur ama yakalanan metin düzenleme/geçmiş/TUI'de bozuk/eksik (özellikle Claude). Doğruluk düşük. ❌
- **Session-file ingestion:** terminali olduğu gibi (ham passthrough) bırak, mesajı CLI'ın **kendi session dosyasından** oku — temiz, rol-etiketli, birebir. ✅

→ Karar: **terminali hiç değiştirme**, mesajları CLI session dosyalarından oku.

## 3. Mimari

```
[xterm — DEĞİŞMEZ, mükemmel ayna]  ──onData──▶  WriteToTerminal ──▶ PTY ──▶ CLI
                                                                          │ (CLI kendi session dosyasına yazar)
                                                                          ▼
                          internal/ingest.Watcher(perAITerminal)  ◀── tail/parse ── ~/.<cli>/.../session.*
                                   │  ham user mesajları, fingerprint-süzülmüş
                                   ▼
                          hubClient.LogMessage(room, agent, content, ts)  → user_prompt  (#29/#58 altyapısı)
```

### Bileşenler

**`internal/ingest` (yeni paket):**

- **`SessionAdapter` interface** (CLI-başına bir implementasyon):
  - `DiscoverFile(cwd string, spawnedAt time.Time) (path string, err error)` — spawn edilen CLI'ın session dosyasını bul.
  - `ParseNewUserMessages(path string, cursor Cursor) (msgs []UserMessage, next Cursor, err error)` — cursor'dan sonraki yeni `role:user` mesajları çıkar; `version` alanına göre şema-gate.
  - Adapter'lar: `claudeAdapter`, `copilotAdapter`, `codexAdapter`, `geminiAdapter`.
- **`UserMessage{ Content string; Timestamp string }`** — birebir kullanıcı metni + CLI'ın kendi ISO timestamp'i.
- **`Cursor`** — JSONL için byte-offset (veya son-uuid/satır-index); Gemini monolitik JSON için işlenmiş-mesaj-sayısı.
- **`Watcher`** (her AI terminali için bir tane):
  - Discovery → tail (JSONL: offset'ten yeni satırlar; Gemini: mtime değişince re-parse, cursor'dan sonrasını al) → parse → **fingerprint-süzme** → her gerçek mesaj için emit.
  - Tetik: fsnotify (varsa) veya kısa-aralık poll (basit, bağımlılıksız MVP). Karar: **poll (ör. 500ms–1s)** — fsnotify ek bağımlılık; poll yeterli ve test-edilebilir. (İstenirse sonra fsnotify.)

**App wiring (`app.go`):**

- `CreateTerminal` içinde, terminal **AI-CLI ve observer-değil** ise spawn'dan sonra `ingest.StartWatcher(adapter, cwd, spawnedAt, room, agentName, emitFn)`.
- `closeTerminalInternal` / `RestartTerminal` → ilgili watcher'ı durdur. Restart → yeni session dosyası → yeni watcher.
- `emitFn` = `hubClient.LogMessage(room, agentName, content, ts)` (mevcut #29/#58 yolu; dosyanın kendi timestamp'i geçilir → sıralama dosyadan otoriter).

> **Bağımlılık:** Bu özellik #58'in (PR #64) **timestamp-taşıyan** `LogMessage(room,to,content,ts)` imzasını kullanır — ingestion'da mesaj, gerçekte gönderilmesinden **saniyeler sonra** (poll/tail gecikmesi) loglanabilir; hub'ın RPC-varış anında damgalaması mesajı yanlış sıralar. Dosyanın kendi timestamp'i şart. → İmplementasyon #58 üstüne kurulur (rebase) ya da #64 merge'ünden sonra başlar.

## 4. Self-Injection Fingerprint Modeli (kritik)

Uygulama bir agent'a spawn'da **startup prompt** (`ComposeStartupPrompt`) enjekte ediyor; ayrıca broadcast/prompt-send/sesli-dikte metinleri de PTY'ye enjekte ediliyor. CLI bunların **hepsini** kendi session dosyasına `role:user` mesajı olarak kaydeder. Süzmezsek kendi bootstrap'ımızı "kullanıcının mesajı" diye loglarız.

**Çözüm:** App, bir session'a enjekte ettiği metinlerin **fingerprint'ini** (normalize edilmiş içerik) per-session bir kümede tutar:

- **Startup prompt** → fingerprint'lenir → ingestion atlar → **asla loglanmaz** (zaten bizim bootstrap'ımız).
- **Broadcast / prompt-send** → mevcut `logUserPrompt` ile **anında + güvenilir** loglanır; fingerprint'lenir → ingestion atlar → **çift-log yok**.
- **Sesli dikte** → kullanıcı genelde düzenleyip gönderir → son metin enjekte edilenden farklı → fingerprint'e uymaz → **loglanır** (doğru: kullanıcının nihai mesajı). (Düzenlemeden gönderirse fingerprint'e uyar ve atlanır — kabul edilebilir; nadir.)
- **Doğrudan yazma** → hiçbir fingerprint'e uymaz → **loglanır** (asıl hedef).

**Sonuç:** Her user→agent mesajı tam bir kez loglanır; startup prompt sızmaz; doğrudan-yazma yakalanır.

**Fingerprint detayları:**
- Anahtar = enjekte edilen metnin normalize hali (trim + iç-boşluk sadeleştirme; bracketed-paste/CR strip). CLI metni reformatlayabilir (ör. trailing newline) → normalize tolerans sağlar.
- Küme per-session, **bellekte**, sınırlı (birkaç enjeksiyon). Eşleşen bir mesaj tüketildiğinde fingerprint **bir kez** kullanılır (aynı metni kullanıcı sonra gerçekten yazarsa ikincisi loglanır). → küçük bir "tüketilebilir sayaç" map'i.
- **Sıralama güvenliği:** fingerprint, metnin PTY'ye **enjekte edildiği anda** (CreateTerminal startup prompt / BroadcastToTeam / SendPromptToAgent içinde) kaydedilir — watcher'ın o satırı dosyada görüp işlemesinden önce. Böylece enjeksiyon hep fingerprint'lenmiş olur (watcher poll'u her zaman enjeksiyondan sonra gelir).
- **Alternatif (reddedildi):** ingestion'ı tek-kaynak yapıp `logUserPrompt`'u emekli etmek — broadcast loglamasının anındalığını ve CLI-kaydından bağımsızlığını kaybederdik; startup-prompt süzmesi yine fingerprint gerektirirdi. Bu yüzden mevcut model (logUserPrompt korunur + fingerprint) daha dayanıklı.

## 5. Per-CLI Adapter Detayları (bu makinede doğrulanmış)

| CLI | Konum | Format | User mesajı | Timestamp | Discovery | Append |
|---|---|---|---|---|---|---|
| **Claude Code** | `~/.claude/projects/{slug(cwd)}/{uuid}.jsonl` | JSONL | `type:"user"` + `message.content` **string** (content array ise tool_result → atla) | per-satır ISO+ms+Z | cwd→slug klasör (her non-alnum→`-`), spawn-zamanına en yakın yeni `.jsonl`; `CLAUDE_CODE_SESSION_ID` env de var | canlı satır-append |
| **Copilot** | `~/.copilot/session-state/{uuid}/events.jsonl` | JSONL | `type:"user.message"` + `data.content` (ham; `transformedContent` DEĞİL) | per-event ISO+offset | yeni `{uuid}/` dizini (spawn-zamanı); cwd `workspace.yaml`'da | canlı append |
| **Codex** | `~/.codex/sessions/Y/M/D/rollout-{ts}-{uuid}.jsonl` | JSONL | **yeni (2026):** `type:"event_msg"`+`payload.type:"user_message"`+`payload.message`; **eski (2025):** `type:"message",role:"user"`. `response_item` kopyasını alma (dedup) | per-satır RFC3339 | spawn-günü klasöründe yeni `rollout-*`; cwd `session_meta.payload.cwd`'de | canlı (buffer'lı) |
| **Gemini** | `~/.gemini/tmp/{sha256(cwd)}/chats/session-*.json` | **tek JSON** | `type:"user"` (NOT `role`) + `content[].text` | per-mesaj ISO | `sha256(cwd)`→klasör, yeni `session-*.json` | canlı ama monolitik → mtime'da re-parse |

**Ortak süzme:** her adapter yalnız **insan** user mesajını döndürür — tool_result, asistan çıktısı, sistem/MCP-preamble enjeksiyonu hariç. Enjekte edilen MCP/startup preamble fingerprint ile de süzülür (§4).

**Version-gate:** her adapter, kaydın `version`/`cli_version` alanına bakar; tanımadığı şemada parse-hatası fırlatmak yerine **zarif atlar** (loglamaz, watcher ölmez) + bir kez uyarı loglar.

## 6. Yaşam Döngüsü & Eşleme

- **Keşif:** cwd→klasör (Claude slug / Gemini sha256) ya da spawn-günü klasörü (Codex) / yeni-dizin (Copilot) + **spawn-zamanına en yakın** dosya; dosyanın iç-alanıyla (`cwd`/`workspace.yaml`/`session_meta`) doğrula, sonra o dosyaya **kilitle**.
- **Worktree agent'ları** farklı cwd → farklı klasör → doğal ayrışır. **Edge:** aynı cwd'de eşzamanlı agent (manager+observer ana repoda) → spawn-zamanı + ilk-satır timestamp'iyle en yakını seç; kilitlendikten sonra sabit. (Nadir; başarısızlıkta o terminal için ingestion sessizce devre-dışı + log.)
- **Cursor** session-ömrü boyunca bellekte. CLI ömrü ⊆ app ömrü (PTY app'in çocuğu) → app-restart-arası kalıcılık/backfill gerekmez. Watcher dosyayı baştan tail eder.
- **Observer terminalleri atlanır** (#17 — özel taslak alanı, loglanmaz).

## 7. Riskler & De-risk

- **M0 — ampirik de-risk (İLK İŞ, kodlamadan önce):** Uygulamanın spawn ettiği Claude gerçekten `~/.claude/projects/.../*.jsonl` yazıyor mu? PTY env `CLAUDECODE`'u strip ediyor (bilinen transcript-suppress bug'ını tetikleyen değişken — strip etmek *iyi*), ama doğrulanmalı: `make dev` → Claude terminali aç → birkaç mesaj yaz → dosya oluşuyor + büyüyor mu? Yazmıyorsa Claude adapter'ı yeniden düşünülür (ör. başka konum/flag).
- **Format drift:** 3/4 şema reverse-engineered ve zaten kaymış (Codex sürüm-drift; Gemini JSON→JSONL göçü) → version-gate + zarif-atlama; CLI güncellemelerinde **ara sıra adapter bakımı** (kabul edilen maliyet).
- **Eşleme yarışı:** aynı cwd'de eşzamanlı spawn (§6 edge).
- **Gizlilik:** observer atlanır; ayrıca ingestion yalnız `role:user` okur (agent çıktısı/araç sonuçları loglanmaz).
- **Performans:** poll aralığı makul (≥500ms); Gemini monolitik re-parse yalnız mtime değişince.

## 8. Test Stratejisi (TDD, backend-ağırlıklı)

- **Adapter parse testleri** (table-driven, gerçek satır fixture'ları): her CLI için user-mesajı çıkarımı, asistan/araç/sistem ayrımı, timestamp, version-gate (bilinmeyen sürüm → boş+uyarı), Codex `event_msg` vs `response_item` dedup, Gemini `content[].text` birleştirme. **Saf fonksiyonlar → kolay test.**
- **Discovery testleri:** temp-dir'de sahte session ağaçları kurup cwd→dosya eşleme (slug, sha256, spawn-zamanı en-yakın, eşzamanlı-edge).
- **Watcher testleri:** cursor ilerleme/dedup (aynı satırı iki kez okumaz), fingerprint-süzme (startup/broadcast atlanır, doğrudan-yazma loglanır), observer-atlama, dosya-yok/bozuk-satır zarif-atlama.
- **Emit:** mevcut `LogMessage` zinciri zaten test-kapsamlı; watcher'ın doğru `(room, agent, content, ts)` ürettiği assert edilir (fake emitFn).
- **Frontend:** değişiklik yok (terminal dokunulmuyor) → frontend testi gerekmez.

## 9. Kapsam / Non-Goals

**Kapsam:** 4 adapter (Claude, Copilot, Codex, Gemini); `internal/ingest` paketi; app lifecycle wiring; self-injection fingerprint; observer-atlama; version-gate.

**Non-goals:** cross-tool resume (#40); agent ÇIKTISINI loglamak (yalnız user mesajları); app-restart-arası backfill; fsnotify (poll yeterli — sonra opsiyonel); `ValidateName` ASCII kısıtı (mevcut, kapsam-dışı).

## 10. Milestone'lar

- **M0 — De-risk:** Claude transcript-yazımını app-spawn altında ampirik doğrula. (Geçemezse tasarım revize.)
- **M1 — `internal/ingest` iskelet + Claude adapter:** interface, Cursor, claudeAdapter (discovery+parse) + testler.
- **M2 — Watcher + fingerprint + app wiring (Claude):** uçtan uca Claude için çalışır; native görsel test.
- **M3 — Copilot, Codex, Gemini adapter'ları:** her biri parse+discovery+test; version-gate.
- **M4 — Cila:** observer-atlama teyit, edge'ler, dokümantasyon, `go test ./...`+vet+build.

## 11. Açık Sorular

- Poll aralığı (varsayılan 500ms–1s) — performans/anındalık dengesi; M2'de ayarlanır.
- Fingerprint normalize'ın CLI-reformat toleransı (ör. Claude/Copilot trailing-newline) — M2'de gerçek dosyalarla kalibre edilir.
