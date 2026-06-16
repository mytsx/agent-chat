# Sesli Prompt / Mikrofon (STT)

> Durum: Taslak plan (kod yazılmadı). Hedef: her terminal/agent paneline mikrofon butonu ekleyip kullanıcının konuşmasını metne çevirerek (Speech-to-Text) o agent'ın PTY input'una yazmak.

## Denetim Revizyonu (2026-06-16, kod-doğrulamalı)

> ⚠️ Bu bölüm, çok-ajanlı bir kod-denetiminin **kaynak koduyla doğrulanmış** bulgularına göre eklendi. Aşağıdaki bölümlerde yer alan ilgili iddialar yerinde düzeltildi.

- **Verdict:** MAJOR-REVISIONS. Plan teknik olarak sağlam ancak (a) `malgo` (CGO) önerisi build pipeline'ını kırıyor, (b) getUserMedia'nın çalışmadığı iddiası yanlış kaynağa dayanıyor, (c) plan aşırı geniş (5 faz) ve bölünmeli.

- **Uygulanan düzeltmeler:**
  1. **CGO red (doğrulandı):** `malgo`/PortAudio MVP önerisi çıkarıldı. Proje tamamen **CGO-suz** (`grep "import \"C\""` temiz, `Makefile`/`build/`'de `CGO_ENABLED` yok). `scripts/build-universal.sh:30-59` arm64+amd64 cross-compile (`GOOS/GOARCH go build`) + `lipo` ile universal binary üretiyor; CGO bağımlılığı amd64 cross-compile'ı (host arm64'te x86_64 C toolchain yok) ve imzalı/notarize universal release'i **kırar**.
  2. **getUserMedia öncülü düzeltildi:** "getUserMedia macOS WKWebView'da çalışmıyor" **kanıtlanmadı**. WebKit Bug 239816 `webkitSpeechRecognition` (Web Speech API) hakkında ve `[iOS]` etiketli — getUserMedia **değil**. Gerçek engel dar: Wails v2.11.0 (`WailsContext.m:282` `self.webview.UIDelegate = self`) WKUIDelegate'e konform ama `requestMediaCapturePermissionFor:`/`decideMediaCapturePermissionFor:` metodunu **implemente etmiyor** (grep ile doğrulandı — metot yok). Tek eksik, yamanabilir bir delege metodu.
  3. **SPIKE eklendi:** Kod yazmadan önce 1-2 saatlik bir spike ile iki CGO-suz yol arasında karar verilecek (aşağıda Faz 1 öncesi).
  4. **Enjeksiyon uzlaştırması:** 3 enjeksiyon yolu (`sendStartupPrompt` app.go:664 bracketed-paste'li; `SendPromptToAgent` app.go:903 bracketed-paste'siz/auto-submit'li; yeni voice) tek bir `injectText(sessionID, cliType, text, autosubmit)` helper'ında ve input-fix (#15) per-session write-mutex altında birleştirilecek. `codex` CLIType'ı (types.ts:3) dallanmada atlanmış — eklenecek.
  5. **Auto-submit kaldırıldı:** Transkript enjekte edilince otomatik `\r` **gönderilmeyecek** (erken-submit / yanlış agent riski). Kullanıcı review/edit edip kendisi Enter'lar.
  6. **Konfig daraltıldı:** Tek API key + sabit `language=tr` yeterli. OS keychain (precedent'siz), multi-motor seçimi ve autosubmit bayrağı MVP'den çıkarıldı.

- **Kapsam / böl-birleştir kararı:**
  - **(A) Bulut STT MVP** [CGO-suz: spike'tan çıkan yol + bulut Whisper, `language=tr`] → bu issue (#16). Faz 0-4.
  - **(B) Offline whisper.cpp** → **AYRI gelecek-issue**. Projedeki **ilk CGO bağımlılığı** olur; tüm release pipeline'ını (universal cross-compile + notarization) etkiler. Mevcut Faz 5 buradan çıkarıldı.

- **Çapraz-analiz notu (bağımlılık):** Voice özelliği sıralamada **en sona (7.)** konmalı — en yüksek efor/risk. Transkript enjeksiyonu, input-fix (#15) write-mutex'i ve tek `injectText` helper'ı hazır olduğunda güvenle eklenir; aksi halde 3 ayrı enjeksiyon yolu çakışır.

## Problem / Bağlam

Kullanıcının ifadesiyle: *"Bu tarz uygulamalar gelişmeye başladı, bu tarz uygulamalarda genelde mikrofon özelliği de oluyor; her agent'a sesle prompt yazabiliyorsun."*

İstenen davranış: Her terminal panelinde bir mikrofon butonu olacak. Kullanıcı butona basıp konuşacak, konuşma metne dönüştürülecek (STT) ve bu metin **o panele bağlı agent'ın** PTY input'una (terminal stdin) yazılacak. Böylece klavyeyle prompt yazmak yerine sesle prompt verilebilecek.

Kritik kısıtlar:
- Kullanıcı **Türkçe** konuşuyor → seçilen STT motorunun Türkçe desteği iyi olmalı.
- Uygulama bir **Wails v2 masaüstü uygulaması**; frontend **macOS WKWebView** içinde çalışıyor (`go.mod`: `github.com/wailsapp/wails/v2 v2.11.0`). Tarayıcı yerleşik **konuşma tanıma** API'si (`webkitSpeechRecognition`) WKWebView'da çalışmaz; ancak **ham mikrofon yakalama** (`getUserMedia`) için tek engel, Wails'in WKUIDelegate media-capture metodunu implemente etmemesidir (aşağıda doğrulandı).
- Metin enjeksiyonu, mevcut bracketed paste mantığıyla uyumlu olmalı; aksi halde uzun/çok satırlı metinde erken submit (premature submit) veya satır bölünmesi olur.

## Mevcut Durum

### Terminal girdi akışı (kullanıcı → PTY)

Girdi tek yönlü ve basit bir zincir izliyor:

1. **`frontend/src/components/TerminalPane.tsx:79-83`** — xterm.js `term.onData` callback'i kullanıcı tuş vuruşlarını yakalar ve doğrudan Wails binding'i `WriteToTerminal(sessionID, data)` ile backend'e gönderir:
   ```ts
   term.onData((data: string) => {
     WriteToTerminal(sessionID, data).catch(...)
   });
   ```
2. **`frontend/src/components/TerminalPane.tsx:6`** — `WriteToTerminal` ve `ResizeTerminal`, `../../wailsjs/go/main/App` üzerinden import edilir (Wails otomatik üretilen binding).
3. **`frontend/wailsjs/go/main/App.d.ts:58` / `App.js:105-106`** — binding `window['go']['main']['App']['WriteToTerminal']` çağrısına maps eder.
4. **`app.go:675-687`** — `App.WriteToTerminal(sessionID, data string) error`. Copilot için `\x1b[O` (Focus Out) filtresi ve debug log var; sonunda `a.ptyManager.Write(sessionID, []byte(data))` çağrılır.
5. **`internal/pty/manager.go:203-224`** — `Manager.Write(sessionID, data []byte) error` PTY session stdin'ine yazar (`session.PTY.Write(data)`).

### Bracketed paste / startup prompt enjeksiyonu (referans desen)

Sesli prompt enjeksiyonu için kopyalanacak mevcut desen **`app.go:664-671`** (`sendStartupPrompt`):
```go
const (
    bracketOpen  = "\x1b[200~"
    bracketClose = "\x1b[201~"
)
a.ptyManager.Write(sessionID, []byte(bracketOpen+composed+bracketClose))
time.Sleep(200 * time.Millisecond)
a.ptyManager.Write(sessionID, []byte("\r"))
```
Yani Claude/Gemini gibi CLI'lara çok satırlı metin gönderirken metin `ESC[200~ ... ESC[201~` ile sarılır, kısa bir gecikme sonrası `\r` (Enter) gönderilir. STT çıktısı (genelde tek/çok satırlı serbest metin) **aynı yöntemle** enjekte edilmeli. (Not: copilot CLI'da bracketed paste kullanılmıyor; o yüzden CLI tipine göre dallanma gerekir — `sendStartupPrompt` `cliType == "copilot"` durumunda erken return ediyor.)

### CLI tipi bilgisi

- **`frontend/src/lib/types.ts:3`** — `CLIType = "claude" | "gemini" | "copilot" | "codex" | "shell"`.
- **`frontend/src/lib/types.ts:62-69`** — `TerminalSession` arayüzü: `sessionID`, `teamID`, `agentName`, `cliType`, `index`, `slotIndex`. `TerminalPane` props'unda `cliType` zaten mevcut (`TerminalPane.tsx:12`), enjeksiyon davranışını buna göre ayarlamak için yeterli.

### Mevcut frontend bağımlılıkları (`frontend/package.json`)

- Runtime: `@xterm/xterm ^6.0.0`, `@xterm/addon-fit`, `@xterm/addon-web-links`, `react ^18.2.0`, `react-dom`, `react-grid-layout`, `react-resizable-panels`, `zustand ^5.0.11`.
- **STT veya ses kaydı için kütüphane YOK.** Tarayıcı STT'si için ek paket gerekmez (yerleşik API); bulut/yerel STT için yeni bağımlılık/binding gerekir.

### macOS izin/entitlement durumu (mevcut)

- **`build/darwin/Info.plist`** — `NSMicrophoneUsageDescription` **YOK**. `NSSpeechRecognitionUsageDescription` **YOK**.
- **`build/darwin/entitlements.plist`** — yalnızca JIT / unsigned-memory / library-validation / dyld-env anahtarları var. `com.apple.security.device.audio-input` **YOK**. (Bu uygulama App Sandbox kullanmıyor görünüyor; yine de Hardened Runtime + notarization için mikrofon entitlement'ı gerekir.)
- `build/darwin/Info.dev.plist` ayrı dosya; dev build'de de aynı izinlerin eklenmesi gerekir.

## Teknoloji Değerlendirmesi

### Kritik bulgu: Web Speech API WKWebView'da çalışmıyor

Wails frontend'i macOS'ta **WKWebView** içinde çalışır. Doğrulandı (WebKit Bug 239816, "RESOLVED WORKSFORME"): `webkitSpeechRecognition` WKWebView'da `window` üzerinde **görünür ama çalışmaz** — izin istemi hiç gelmez, sonuç `service-not-allowed` hatasıdır. WebKit, Apple'ın Speech framework'üne dayanır ve native `NSSpeechRecognitionUsageDescription` ister; tarayıcı (Safari proper) dışında, gömülü WebView'da bu akış pratikte güvenilir biçimde çalışmaz. **Sonuç: Web Speech API bu uygulamada birincil çözüm OLAMAZ.**

> **Düzeltme (denetim):** WebKit Bug 239816 yalnızca `webkitSpeechRecognition` (Web Speech API) hakkındadır ve `[iOS]` etiketlidir; **`getUserMedia`'yı kapsamaz**. "getUserMedia macOS WKWebView'da çalışmıyor" iddiası **kanıtlanmadı** ve bu plandan çıkarıldı.

`getUserMedia` (ham mikrofon ses yakalama) WKWebView'da **çalışabilir**; gerçek engel dardır: Wails v2.11.0 `WailsContext.m:282`'de `self.webview.UIDelegate = self` ile WKUIDelegate'e konformdur, ancak `requestMediaCapturePermissionFor:`/`decideMediaCapturePermissionFor:` metodunu **implemente etmez** (kod grep'i ile doğrulandı — bu metot dosyada yok). Yani izin isteminin gelmesi için tek eksik, **yamanabilir bu tek delege metodudur**. Gerekli native ön koşullar:
- `Info.plist` → `NSMicrophoneUsageDescription`,
- entitlements → `com.apple.security.device.audio-input` (Hardened Runtime "Audio Input"),
- WKWebView'ın `requestMediaCapturePermissionFor:` delegesinin izni `.grant` ile yanıtlaması (eklenecek metot),
- güvenli (HTTPS/localhost) bağlam.

Bu, yamayla aşılabilir; getUserMedia tabanlı yaklaşım **elenmedi**, aksine spike'ta değerlendirilecek iki CGO-suz yoldan biridir (aşağıda Önerilen Yaklaşım).

### Karşılaştırma tablosu

| Kriter | Web Speech API (yerleşik) | Bulut STT (OpenAI Whisper API / Deepgram / Google STT) | Yerel/Offline STT (whisper.cpp) |
|---|---|---|---|
| **WKWebView uyumu** | ❌ Çalışmıyor (Bug 239816 — exposed ama non-functional) | ✅ Ses yakalama `getUserMedia` ile WebView'da; transkripsiyon Go backend'de → WebView STT'ye bağımlı değil | ✅ Ses Go backend'de yakalanır/işlenir → WebView'a hiç bağlı değil |
| **Türkçe desteği** | (Çalışsaydı `lang="tr-TR"` ile iyi) | ✅ Whisper/Deepgram/Google çok iyi Türkçe | ⚠️ Whisper Türkçe destekler; doğruluk model boyutuna bağlı (small ~Türkçe için orta, medium/large daha iyi) |
| **Gizlilik** | Ses Apple sunucularına gidebilir | ❌ Ses 3. parti buluta gider | ✅ Tamamen offline, ses cihazdan çıkmaz |
| **İnternet** | Gerekebilir | ❌ Gerekli | ✅ Gerekmez |
| **Maliyet** | Ücretsiz | 💲 Kullanım başına (ör. Whisper API ~$0.006/dk; Deepgram benzeri) | Ücretsiz (yalnızca CPU/RAM) |
| **API key / setup** | Yok | API key + güvenli saklama gerekir | Model dosyası indirme (ggml-*.bin; small ~400MB, medium ~1.5GB) |
| **Binary boyutu** | Yok | Küçük (HTTP client) | ⚠️ whisper.cpp CGO + model → uygulama/data boyutu artar |
| **Gecikme** | Düşük (çalışsaydı) | Ağ RTT'ye bağlı | İlk yüklemede model load; sonra CPU'ya bağlı |
| **Build karmaşıklığı** | Düşük | Orta (key yönetimi, HTTP) | ⚠️ Yüksek (CGO, whisper.cpp derleme, embed/indirme, `//go:embed` kısıtı zaten hassas) |

Notlar:
- whisper.cpp Go binding seçenekleri mevcut: `github.com/ggerganov/whisper.cpp/bindings/go` (resmi) ve `github.com/mutablelogic/go-whisper`. Türkçe Whisper'ın desteklediği 100+ dilden biri; doğruluk model boyutuna göre değişir.
- Bulut STT'de bile ses **WKWebView içinde `getUserMedia` ile yakalanmalı** (veya Go tarafında subprocess ile native ses cihazı okunmalı). İki **CGO-suz** seçenek var: (1) Wails WKUIDelegate'i yamala (`requestMediaCapturePermissionFor: .grant`), sesi JS'te `getUserMedia` ile yakala, WAV'ı Go backend'e yükle; (2) `ffmpeg -f avfoundation` subprocess ile Go tarafında yakala (WebView mikrofon iznine gerek kalmaz). **`malgo`/PortAudio gibi CGO bağımlılıkları reddedildi** — bkz. yukarıdaki Denetim Revizyonu (universal cross-compile + notarization'ı kırar).

## Önerilen Yaklaşım

**Aşamalı yaklaşım. MVP olarak Bulut STT (OpenAI Whisper API, `language=tr`), sesi backend'de işleyen CGO-suz mimari.** Offline whisper.cpp bu plandan çıkarıldı → ayrı gelecek-issue (bkz. Denetim Revizyonu).

Gerekçe:
1. Web Speech API (`webkitSpeechRecognition`) WKWebView'da çalışmadığı için elenir; ham `getUserMedia` ise yamayla kullanılabilir (elenmedi).
2. Yerel whisper.cpp en iyi gizliliği verir ama **ilk CGO bağımlılığını** getirir (model dağıtımı + `//go:embed` hassasiyeti + universal cross-compile/notarization etkisi) → **ayrı issue**'ya bırakıldı.
3. Bulut STT (Whisper API) en iyi Türkçe/efor dengesini verir ve hızlı, CGO-suz MVP sağlar.

**Önce SPIKE (1-2 saat, kod yazmadan).** Wails dev build'te `navigator.mediaDevices.getUserMedia({audio:true})` çağrılıp davranış gözlemlenir. Buna göre iki **CGO-suz** yol arasında karar verilir:
- **Yol 1 — WKUIDelegate yaması:** Wails'in `requestMediaCapturePermissionFor:` delegesini `.grant` ile yamala; sesi frontend'de `getUserMedia` ile yakala, WAV'ı Wails binding ile Go backend'e yükle.
- **Yol 2 — ffmpeg subprocess:** Go backend'de `ffmpeg -f avfoundation` subprocess ile yakala (WebView mikrofon iznine gerek yok).

**Her iki yol da CGO-suzdur.** `malgo`/PortAudio reddedildi (bkz. Denetim Revizyonu — universal release'i kırar).

Spike sonrası akış:
- Frontend mikrofon butonu → Wails binding `StartVoiceCapture(sessionID)` çağırır (Yol 2) veya `getUserMedia` başlatır (Yol 1).
- Kullanıcı butona tekrar basınca (push-to-talk) `StopVoiceCapture(sessionID)` → kayıt durur, WAV buffer bulut STT'ye gönderilir → dönen metin **tek `injectText` helper'ı** ile (CLI tipine göre bracketed paste, **autosubmit=false**) o session'a enjekte edilir.

**Etkileşim modeli: Push-to-talk (bas-konuş-bırak).** Sürekli dinleme (continuous) yerine, kullanıcı butona basıp konuşur, bırakınca transkripsiyon başlar. Daha öngörülebilir, yanlış tetiklenme yok, çok-panelli ortamda hangi agent'a konuşulduğu net.

**Enjeksiyon: otomatik submit DEĞİL, önce input'a yaz.** STT çıktısı doğrudan Enter ile gönderilmez; metin agent input satırına yazılır (bracketed paste ile), kullanıcı görüp düzeltir ve kendisi Enter'a basar. Bu, STT hatalarını ve yanlış agent'a gitme riskini azaltır. (Opsiyonel ayar: "transkripsiyon sonrası otomatik gönder".) Bu nokta **multi-manager planı ve startup prompt erken-submit sorunuyla doğrudan ilişkilidir** — aşağıda Açık Sorular'da işaretlendi.

## Etkilenen / Yeni Dosyalar

| Dosya | Tip | Değişiklik |
|---|---|---|
| `frontend/src/components/TerminalPane.tsx` | Değişiklik | Terminal header'a (`terminal-header-actions`, ~satır 145-179) mikrofon butonu ekle; kayıt durumu (idle/recording/transcribing) state'i; `StartVoiceCapture`/`StopVoiceCapture` binding çağrıları; transkript geldiğinde input'a yazma (event ile) |
| `frontend/src/lib/types.ts` | Değişiklik | Ses durumu / transkript event payload tipleri (ör. `VoiceTranscriptEvent { sessionID, text }`, `VoiceStateEvent`) |
| `frontend/wailsjs/go/main/App.*` | Üretilen | Yeni binding'ler (`StartVoiceCapture`, `StopVoiceCapture`, opsiyonel `SetVoiceConfig`) Wails tarafından otomatik üretilir |
| `app.go` | Değişiklik | Yeni metodlar: `StartVoiceCapture(sessionID)`, `StopVoiceCapture(sessionID)`; transkripti enjekte eden **ortak `injectText(sessionID, cliType, text, autosubmit)` helper'ı** (`sendStartupPrompt` app.go:664 + `SendPromptToAgent` app.go:903 + voice birleştirilir; per-session write-mutex altında — input-fix #15 ile); transkript için `EventsEmit("voice:transcript:{sessionID}", ...)` |
| `internal/voice/` (YENİ paket) | Yeni | Ses yakalama (**CGO-suz: ffmpeg subprocess** ya da JS getUserMedia'dan gelen WAV) + STT istemcisi arayüzü `Transcriber` (yalnızca Whisper API impl — whisper.cpp ayrı issue'da); WAV encode |
| `internal/voice/whisper_api.go` (YENİ) | Yeni | OpenAI Whisper API HTTP istemcisi; sabit `language=tr` parametresi; multipart upload |
| `internal/config` veya mevcut config | Değişiklik | **Tek API key** saklama (ör. `~/.agent-chat/voice.json`). Dil sabit `tr`; OS keychain ve autosubmit bayrağı MVP'den çıkarıldı |
| `build/darwin/Info.plist` | Değişiklik | `NSMicrophoneUsageDescription` ekle (TR açıklama). **Her iki yol için de zorunlu** — bu olmadan `navigator.mediaDevices` undefined olur ve imzalı build kırılır |
| `build/darwin/Info.dev.plist` | Değişiklik | Aynı `NSMicrophoneUsageDescription` (dev build'de izin istemi için) |
| `build/darwin/entitlements.plist` | Değişiklik | `com.apple.security.device.audio-input` `<true/>` ekle (Hardened Runtime Audio Input) |
| `app.go` startup / CSS | Değişiklik (küçük) | Mikrofon butonu stilleri (mevcut `terminal-btn-*` desenine uygun); recording animasyonu |
| `CLAUDE.md` | Opsiyonel | Yeni `internal/voice/` paketi ve mikrofon izni notu |

## Adım Adım İmplementasyon

### Faz 0 — Native mikrofon izni altyapısı (her yaklaşımda gerekli)
1. `build/darwin/Info.plist` ve `Info.dev.plist` içine `NSMicrophoneUsageDescription` ekle (TR metin, ör. "Agent Chat sesli prompt için mikrofona erişir").
2. `build/darwin/entitlements.plist` içine `com.apple.security.device.audio-input` ekle.
3. `make build` ile bundle'ı yeniden üret, ilk çalıştırmada macOS mikrofon izin isteminin çıktığını ve System Settings → Privacy → Microphone altında uygulamanın listelendiğini doğrula.

### Faz 1 — Spike + backend ses yakalama + STT (Whisper API)
4a. **SPIKE (kod yazmadan, 1-2 saat):** Wails dev build'te `navigator.mediaDevices.getUserMedia({audio:true})` çağır, davranışı gözle. İki CGO-suz yoldan birine karar ver: **Yol 1** (WKUIDelegate yaması + JS getUserMedia → WAV'ı Go'ya yükle) veya **Yol 2** (`ffmpeg -f avfoundation` subprocess Go'da).
4b. `internal/voice/` paketi oluştur: `Transcriber` arayüzü (`Transcribe(ctx, wav []byte) (string, error)`, dil sabit `tr`) ve seçilen yola göre ses yakalama. Çıktı: 16kHz mono WAV. **CGO yok** (`malgo`/PortAudio reddedildi).
5. `internal/voice/whisper_api.go`: OpenAI `/v1/audio/transcriptions` multipart isteği, `model=whisper-1`, sabit `language=tr`. API key'i config'ten oku.
6. **Tek API key** için config saklama (`~/.agent-chat/voice.json`). Dil sabit `tr`; keychain/autosubmit/multi-motor MVP'de yok.
7. `app.go`: `StartVoiceCapture(sessionID)` → kaydı başlat (session başına state map, mutex'li); `StopVoiceCapture(sessionID)` → kaydı durdur, WAV'ı `Transcriber`'a gönder, sonucu al.

### Faz 2 — Enjeksiyon (tek helper, CLI-aware, input-fix #15 ile birleşik)
8. `app.go`: **3 enjeksiyon yolunu tek helper'da uzlaştır** — `sendStartupPrompt` (app.go:664, bracketed-paste'li), `SendPromptToAgent` (app.go:903, bracketed-paste'siz `rendered+"\n"` ile auto-submit'li) ve yeni voice yolu → `injectText(sessionID, cliType, text string, autosubmit bool)`. CLI tipine göre dallan: claude/gemini/**codex** → bracketed paste; copilot/shell → düz yazım. **`codex` CLIType'ı (types.ts:3) mevcut dallanmada atlanmış — ekle.** Helper, **input-fix (#15) per-session write-mutex** altında çalışmalı (eşzamanlı yazımları serileştir).
9. **Voice enjeksiyonunda `autosubmit=false` (sabit):** transkript enjekte edilince otomatik Enter (`\r`) **GÖNDERME**. Bracketed-paste hassasiyeti nedeniyle erken-submit, yanlış agent/manager'a mesaj riskini doğurur. Kullanıcı review/edit edip kendi Enter'lar. (`sendStartupPrompt`/`SendPromptToAgent` kendi davranışlarını `autosubmit` parametresiyle korur.)
10. Transkripti `EventsEmit("voice:transcript:"+sessionID, text)` ile frontend'e de yolla (kullanıcının görüp düzeltebilmesi için).

### Faz 3 — Frontend UI
11. `TerminalPane.tsx`: header action'larına mikrofon butonu ekle (mevcut `terminal-btn-restart/focus/remove` deseni, satır 149-178). Durumlar: idle / recording (kırmızı yanıp sönen) / transcribing (spinner).
12. Push-to-talk: `onMouseDown` → `StartVoiceCapture(sessionID)`, `onMouseUp`/`onMouseLeave` → `StopVoiceCapture(sessionID)`. (Alternatif: toggle tıklama; karar gerekiyor.)
13. `types.ts`'e event payload tipleri ekle.
14. CSS: recording göstergesi, buton hover stilleri.

### Faz 4 — Doğrulama & yapılandırma
15. Türkçe konuşma ile uçtan uca test; yanlış paneldeki enjeksiyonu önlemek için `sessionID` izolasyonunu doğrula.
16. Ayarlar: yalnızca **tek API key** girişi. Dil sabit `tr`; autosubmit/STT-motoru seçimi MVP'de yok (whisper.cpp ayrı issue).

### (Çıkarıldı) Offline STT (whisper.cpp) → AYRI GELECEK-ISSUE
> **Denetim kararı:** Eski "Faz 5 (yerel whisper.cpp)" bu plandan çıkarıldı. Gerekçe: projedeki **ilk CGO bağımlılığı** olur ve `scripts/build-universal.sh` cross-compile (`GOOS/GOARCH go build` + `lipo`) + notarization pipeline'ını etkiler. Ayrı bir issue olarak ele alınmalı; orada `internal/voice/whispercpp.go` (`github.com/ggerganov/whisper.cpp/bindings/go`), `~/.agent-chat/models/` altına model indirme ve `Makefile` CGO build adımı tasarlanır. MVP (bu issue) tamamen CGO-suz kalır.

## Açık Sorular / Karar Gerektiren Noktalar

1. **Ses yakalama yeri (SPIKE ile karar): WKUIDelegate yaması (JS getUserMedia) mı, `ffmpeg` subprocess mü?** Her ikisi de **CGO-suz**. `malgo`/PortAudio **reddedildi** (universal release'i kırar). Eski "getUserMedia güvenilmez" iddiası geçersiz: Wails WKUIDelegate'e konform (`WailsContext.m:282`), yalnızca `requestMediaCapturePermissionFor:` metodu eksik → yamanabilir. Karar Faz 1 spike'ında verilir.
2. ~~**STT motoru MVP'de hangisi?**~~ **Karar verildi:** MVP = bulut Whisper API (`language=tr`). Offline whisper.cpp ayrı issue'ya alındı (ilk CGO bağımlılığı).
3. ~~**API key yönetimi: düz dosya mı keychain mı?**~~ **Karar verildi:** MVP'de **tek API key, düz dosya** (`~/.agent-chat/voice.json`) — diğer config dosyalarıyla (teams.json, prompts.json) tutarlı; keychain precedent'i yok, MVP'den çıkarıldı.
4. ~~**Autosubmit?**~~ **Karar verildi:** Voice enjeksiyonunda **autosubmit YOK** (`\r` gönderilmez). Bracketed-paste hassasiyeti → erken-submit, yanlış agent/manager'a mesaj riski. Kullanıcı kendi Enter'lar. Multi-manager `to="managers"` routing ile bu sayede çakışma da önlenir.
5. **Push-to-talk vs toggle vs continuous:** Push-to-talk (bas-bırak) öneriliyor. Toggle (bir tık başlat, bir tık bitir) uzun diktasyon için daha rahat olabilir. Continuous (sürekli dinleme) çok-panelli ortamda riskli.
6. **Bracketed paste'in copilot/shell'de davranışı:** `sendStartupPrompt` copilot için bracketed paste kullanmıyor (early return). Sesli prompt enjeksiyonunda copilot/shell için düz yazım mı, farklı mı? Test gerek.
7. **Çok satırlı / noktalama:** Whisper Türkçe noktalama ekler; CLI input satırına çok satırlı metin bracketed paste olmadan bozulur — enjeksiyonun her zaman bracketed paste (uygun CLI'larda) ile yapılması şart.
8. **Eşzamanlılık:** Aynı anda birden fazla panelde kayıt? Tek mikrofon donanımı → muhtemelen aynı anda tek aktif kayda izin verilmeli (global lock).
9. **Windows/Linux taşınabilirliği:** Plan macOS odaklı (Info.plist/entitlement). `ffmpeg` subprocess yolu cross-platform nispeten kolay (girdi aygıtı platforma göre değişir: `avfoundation`/`dshow`/`alsa`); getUserMedia yolu seçilirse delege/izin akışı platform başına farklı.

## Doğrulama / Test

- **İzin akışı:** Temiz makinede ilk mikrofon kullanımı → macOS izin istemi çıkıyor; reddedilince anlamlı hata/uyarı; System Settings'ten geri verince çalışıyor.
- **Türkçe doğruluk:** Standart Türkçe cümleler (özel terimler, kod terimleri dahil) okunup transkript doğruluğu kıyaslanır (whisper-1 vs whisper.cpp small/medium).
- **Panel izolasyonu:** 4 panelli grid'de A panelinde konuşulunca metin **yalnızca** A'nın PTY'sine yazılıyor (`sessionID` doğru). Bu, MEMORY.md'deki "terminal yanlış panelde açılıyor" sınıfı bir hataya benzemesin diye kritik.
- **Enjeksiyon bütünlüğü:** Uzun/çok satırlı transkript bracketed paste ile bozulmadan input'a giriyor; erken submit olmuyor (autosubmit kapalıyken `\r` gönderilmiyor).
- **CLI çeşitliliği:** claude, gemini, copilot, shell session'larında enjeksiyon davranışı doğrulanır.
- **Go testleri:** `internal/voice/` için `Transcriber` arayüzü mock'lanarak enjeksiyon ve session-state yönetimi unit test'lenir (mevcut `internal/orchestrator/orchestrator_test.go` table-driven + injectable func deseni örnek alınır).
- **Eşzamanlılık:** Aynı anda iki panelde kayıt denemesi → global lock davranışı test edilir.

## Tahmini Efor

**L (Large).**

Gerekçe: Tek bir buton değil; (1) native macOS mikrofon izni/entitlement değişikliği + bundle imzalama etkisi, (2) yeni `internal/voice/` paketi + **CGO-suz** ses yakalama (WKUIDelegate yaması veya ffmpeg subprocess — spike ile karar) + STT istemcisi, (3) CLI-aware enjeksiyonun tek `injectText` helper'ında uzlaştırılması (3 yol + codex + input-fix #15 write-mutex; erken-submit / multi-manager routing çakışması hassasiyeti), (4) frontend push-to-talk UI + state, (5) tek API key config.

- **Bu issue: Faz 0-4 (bulut STT, CGO-suz yakalama)** = L. **Bağımlılık:** input-fix (#15) write-mutex ve tek `injectText` helper'ı hazır olunca güvenle eklenir; sıralamada en sona (7.) konmalı.
- **Offline whisper.cpp ayrı issue** = ek L/XL (ilk CGO bağımlılığı + universal cross-compile/notarization etkisi).

---

### Kaynaklar (teknoloji iddiaları doğrulaması)

- [WebKit Bug 239816 — `[iOS]` Web Speech API (`webkitSpeechRecognition`) WKWebView'da çalışmıyor](https://bugs.webkit.org/show_bug.cgi?id=239816) — **Not (denetim):** bu bug yalnızca Web Speech API ile ilgili ve `[iOS]` etiketli; `getUserMedia`'yı kapsamaz.
- Wails v2.11.0 kaynağı: `internal/frontend/desktop/darwin/WailsContext.m:282` (`self.webview.UIDelegate = self`) — WKUIDelegate'e konform; `requestMediaCapturePermissionFor:` metodu **implemente edilmemiş** (grep ile doğrulandı).
- Proje build pipeline: `scripts/build-universal.sh:30-59` — arm64+amd64 cross-compile (`GOOS/GOARCH go build`) + `lipo`; CGO bağımlılığı amd64 cross-compile'ı kırar.
- [Can I WebView — Speech recognition feature support](https://caniwebview.com/features/web-feature-speech-recognition/)
- [Apple Developer — webView(_:requestMediaCapturePermissionFor:...) (WKWebView getUserMedia izin delegesi)](https://developer.apple.com/documentation/webkit/wkuidelegate/webview(_:requestmediacapturepermissionfor:initiatedbyframe:type:decisionhandler:))
- [Apple Developer — Requesting Authorization for Media Capture on macOS](https://developer.apple.com/documentation/bundleresources/requesting-authorization-for-media-capture-on-macos?language=objc)
- [whisper.cpp Go bindings (ggerganov/whisper.cpp/bindings/go)](https://pkg.go.dev/github.com/ggerganov/whisper.cpp/bindings/go)
- [mutablelogic/go-whisper — Speech-to-Text in Go](https://github.com/mutablelogic/go-whisper)
