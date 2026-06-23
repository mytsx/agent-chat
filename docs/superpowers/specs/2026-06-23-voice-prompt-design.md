# Sesli Prompt / Mikrofon (STT) — Tasarım (#16)

> Durum: Onaylanmış tasarım (kod yazılmadı). Tarih: 2026-06-23. Branch: `feat/voice-prompt-16`.
> Otoriter önceki belge: `docs/PLAN-voice-prompt.md` (2026-06-16, denetim-revizyonlu). Bu spec o planı
> güncel kodla (HEAD `955b16a`) doğrular, staleness'i düzeltir ve kilitlenen kararları kayda geçirir.

## Hedef

Her terminal/agent paneline bir mikrofon butonu eklemek. Kullanıcı butona basılı tutarken Türkçe
konuşur; bırakınca konuşma Speech-to-Text ile metne çevrilip **o panele bağlı agent'ın** PTY input
satırına **autosubmit olmadan** yazılır. Kullanıcı metni görür, gerekirse düzeltir ve kendi Enter'lar.

## Kilitlenen kararlar

| Karar | Seçim | Gerekçe |
|---|---|---|
| STT motoru | Bulut **OpenAI Whisper API** (`model=whisper-1`, sabit `language=tr`) | CGO-suz, iyi Türkçe, hızlı MVP. Offline whisper.cpp ayrı gelecek-issue (ilk CGO bağımlılığı). |
| Ses yakalama | **Yol 2 — ffmpeg subprocess** (`ffmpeg -f avfoundation`) | Saf Go `os/exec`, Wails cerrahisi yok, cross-platform kolay. Yol 1 (WKUIDelegate) Wails-fork gerektirir → reddedildi. |
| Etkileşim | **Push-to-talk** (bas-konuş-bırak) | Öngörülebilir, yanlış-panele konuşma riski düşük. |
| Eşzamanlılık | **Global tek-kayıt kilidi** | Tek mikrofon donanımı; aynı anda yalnız bir panel kayıtta. |
| API key girişi | **Kalıcı Ayarlar paneli** | Maskeli key girişi; JSON elle düzenlemeden kullanılabilir. |
| Key depolama | Düz dosya `~/.agent-chat/voice.json` (atomic) | teams.json/prompts.json deseni; keychain MVP'den çıkarıldı. |
| ffmpeg bağımlılığı | **Sistemde varsay + `exec.LookPath` kontrol & Türkçe uyarı** | Sıfır bundling; bundle'lama gelecek iyileştirme. |
| Autosubmit | **YOK** — transcript enjekte edilince `\r` gönderilmez | Erken-submit / yanlış agent-manager riski; kullanıcı kendi Enter'lar. |

## Staleness doğrulaması (güncel kodla, HEAD 955b16a)

Plan 2026-06-16 tarihliydi; o tarihten sonra #27/#14/#28/#29/#17/#18 merge oldu. Doğrulanan farklar:

- **`pty.InjectText(sessionID, text string, submit bool)` HAZIR** — `internal/pty/manager.go:343`.
  - copilot → char-by-char (control/format flatten); **claude/gemini/codex/shell → bracketed-paste**
    (`else` dalı). codex özel-durumlanmamış, `else`'e düşüyor → **bracketed-paste alır, doğru.**
  - `internal/sanitize` ile control/format/bidi strip + bracketed-paste-close neutralize + boş-sonrası
    no-op guard built-in → transcript otomatik temizlenir, **ek sanitize işi yok.**
  - Tüm dizi `session.writeMu` (per-session write-mutex, #15) altında → eşzamanlı yazımlar serileşir.
  - `submit=false` → `lastUserInputNano` set edilir (pending), `\r` gönderilmez. **Voice tam olarak bunu çağırır.**
- **Stale satır no'ları düzeltildi:** `sendStartupPrompt` → `app.go:1060` (plan 664 diyordu);
  `SendPromptToAgent` → `app.go:1893` (plan 903 diyordu). İkisi de artık `agentMode` param'lı (#17).
- **YENİ `injectText` helper'ı YAZILMAYACAK** — `a.ptyManager.InjectText(...)` doğrudan çağrılır
  (broadcast `app.go:1166` aynısını yapıyor).
- **Faz 0 dosyaları temiz:** `build/darwin/Info.plist`, `Info.dev.plist`, `entitlements.plist` —
  mikrofon izni/entitlement **yok** (eklenecek).
- **`internal/voice/` ve `~/.agent-chat/voice.json` yok** (oluşturulacak).
- **Wails sürümü:** `go.mod` artık `v2.12.0` (plan v2.11.0 diyordu). v2.12.0'da da `WailsContext`
  `<WKUIDelegate>`'e konform ama `requestMediaCapturePermissionFor:` **implemente edilmemiş**
  (tek WKUIDelegate metodu `runOpenPanelWithParameters`) → Yol 1 önkoşulu hâlâ eksik, Yol 2 seçildi.
- **ffmpeg dev makinede kurulu** (v8.0.1) ve avfoundation mikrofonu görüyor (`[0] MacBook Pro Mikrofonu`).

## Mimari akış

```
[Mic butonu / panel A]
  mousedown → App.StartVoiceCapture(sessionID)
     → global kayıt kilidi al (aynı anda tek aktif kayıt; ikinci panel reddedilir)
     → exec.LookPath("ffmpeg") + API key var mı? (yoksa hata state'i)
     → ffmpeg -f avfoundation -i ":0" -ac 1 -ar 16000 -acodec pcm_s16le -y <tmp.wav>
     → EventsEmit("voice:state:"+sessionID, "recording")
  mouseup/mouseleave → App.StopVoiceCapture(sessionID)
     → ffmpeg stdin'e "q" (graceful finalize; fallback SIGINT → timeout → SIGKILL)
     → WAV oku → EventsEmit("voice:state:"+sessionID, "transcribing")
     → voice.Transcriber.Transcribe(ctx, wav)        // Whisper API, language=tr, ctx ~30s
     → a.ptyManager.InjectText(sessionID, transcript, false)   // ZATEN HAZIR; autosubmit YOK
     → EventsEmit("voice:transcript:"+sessionID, transcript)   // yalnız UI durum/feedback
     → EventsEmit("voice:state:"+sessionID, "idle"); kilidi bırak; tmp.wav sil
```

**Çift-yazım önlemi:** transcript `InjectText` ile PTY input satırına yazılır; CLI bunu echo'lar →
xterm'de zaten görünür. `voice:transcript` event'i terminale **ikinci kez yazmaz**, yalnız durum
göstergesi/toast içindir.

## Bileşenler

### `internal/voice/` (YENİ paket — saf Go, CGO YOK)

| Dosya | İçerik |
|---|---|
| `transcriber.go` | `type Transcriber interface { Transcribe(ctx context.Context, wav []byte) (string, error) }`. Mock'lanabilir → app.go testleri ve voice testleri bunu enjekte eder. |
| `whisper_api.go` | `WhisperClient` `Transcriber`'ı implemente eder. `POST https://api.openai.com/v1/audio/transcriptions`, multipart: `file` (wav), `model=whisper-1`, `language=tr`. Key parametre/config'ten. `httptest` ile test edilir. |
| `capture.go` | `Recorder`: ffmpeg subprocess. `Start(ctx)` → `ffmpeg -f avfoundation -i ":0" -ac 1 -ar 16000 -acodec pcm_s16le -y <tmp>`; `Stop()` → stdin'e `q`, bekle, WAV oku & dön, tmp sil. `exec.LookPath("ffmpeg")` yoksa Türkçe hata. Komut çalıştırıcı **enjekte edilebilir** (interface/func) → gerçek ffmpeg olmadan Start/Stop state'i unit-test edilir. avfoundation girdi string'i (`:0`) platforma göre soyutlanır (sonraki: dshow/alsa). |
| `config.go` | `VoiceConfig{ OpenAIAPIKey string }`. `Load()`/`Save()` → `~/.agent-chat/voice.json`, atomic write (temp + rename, teams.json deseni). |

### `app.go` — yaşam döngüsü + orkestrasyon

- Voice state: `map[sessionID]*activeRecording` + `sync.Mutex`; **global guard** (en fazla 1 aktif kayıt).
- `StartVoiceCapture(sessionID string) error` — kilit al, ffmpeg+key kontrol, `Recorder.Start`, state event.
- `StopVoiceCapture(sessionID string) error` — `Recorder.Stop`, WAV → `Transcriber.Transcribe`,
  `InjectText(sessionID, transcript, false)`, transcript+state event, kilit bırak. Hatalar error state'i.
- `GetVoiceStatus() (VoiceStatus, error)` / `SetVoiceConfig(apiKey string) error` — Ayarlar bindings.
  **Gerçek key frontend'e hiç sızmaz:** `GetVoiceStatus` storage struct'ını değil bir görünüm tipi
  döndürür: `VoiceStatus{ HasKey bool, KeyHint string /* son 4 hane, ör. "...sk9f" */, FFmpegFound bool }`.
  `SetVoiceConfig` tam key'i alıp `voice.json`'a yazar (set-only).
- `Transcriber` ve `Recorder` factory'leri **enjekte edilebilir** tutulur (orchestrator `SendFunc` deseni) → app.go voice testleri gerçek ağ/ffmpeg olmadan koşar.

### Frontend (React + TS + Zustand)

- `frontend/src/components/TerminalPane.tsx` — header action'larına mikrofon butonu (`terminal-btn-*`
  deseni). Push-to-talk: `onMouseDown→StartVoiceCapture`, `onMouseUp`/`onMouseLeave→StopVoiceCapture`.
  Durum makinesi: `idle | recording | transcribing | error`. `voice:state:{sessionID}` +
  `voice:transcript:{sessionID}` event'lerini dinler (yalnız bu panelin sessionID'si).
- **Ayarlar paneli** (yeni bileşen, ör. `SettingsModal.tsx`) — OpenAI key girişi (maskeli),
  `GetVoiceConfig`/`SetVoiceConfig`. Giriş noktası: sidebar/header'da bir ⚙️ buton.
- `frontend/src/lib/types.ts` — `VoiceStatus{hasKey,keyHint,ffmpegFound}`, voice state enum,
  `VoiceTranscriptEvent{sessionID,text}`, `VoiceStateEvent{sessionID,state,message?}`.
- CSS — recording pulse animasyonu, buton hover/durum stilleri.

### Faz 0 — native mikrofon izni

- `build/darwin/Info.plist` + `build/darwin/Info.dev.plist`: `NSMicrophoneUsageDescription`
  (TR, ör. "Agent Chat sesli prompt için mikrofona erişir").
- `build/darwin/entitlements.plist`: `com.apple.security.device.audio-input` `<true/>`.

## Hata yönetimi (hepsi `voice:state` error event'i → frontend toast)

| Durum | Davranış |
|---|---|
| ffmpeg yok | `LookPath` hatası → "ffmpeg bulunamadı — `brew install ffmpeg`". |
| API key yok | "OpenAI key gerekli — Ayarlar'dan girin". |
| Whisper hata/timeout | ctx ~30s; enjeksiyon yok, hata mesajı. |
| Kilit çakışması | İkinci panel reddedilir: "Zaten kayıt var". |
| Boş/sessiz transcript | Enjeksiyon yok (InjectText boş'ta zaten no-op). |
| Panel izolasyonu | sessionID uçtan uca; transcript **yalnız** kendi sessionID'sine (MEMORY "yanlış panel" bug sınıfı). |

## Test

- `internal/voice`: Transcriber mock; `whisper_api` multipart alanları + `language=tr` (`httptest`);
  `config` load/save round-trip + atomic; `capture` LookPath-hata yolu (enjekte edilebilir runner ile
  gerçek ffmpeg'siz Start/Stop state).
- `app.go`: global kilit (eşzamanlı Start → tek kazanan, 50+ goroutine), sessionID izolasyonu (doğru
  session'a enjekte), hata propagasyonu. `internal/orchestrator/orchestrator_test.go` table-driven +
  injectable-func deseni.
- `go test ./...` yeşil; **CGO yok** (MCP server saf-Go cross-compile korunur).

## Fazlar (TDD sırası)

1. **Faz 0** — native izin (Info.plist ×2 + entitlements). `make build` → ilk çalıştırmada izin istemi.
2. **Faz 1** — `internal/voice/`: Transcriber arayüzü + whisper_api + config + capture (+ unit testler).
3. **Faz 2** — app.go: Start/Stop + global kilit + `InjectText` enjeksiyon + events + Settings bindings (+ testler).
4. **Faz 3** — frontend: mic buton + push-to-talk + Ayarlar paneli + types + CSS.
5. **Faz 4** — native uçtan-uca doğrulama (kullanıcıda): izin istemi, Türkçe doğruluk, panel izolasyonu.

## Birincil rezidüel risk (Faz 4'te native doğrulanacak)

ffmpeg **subprocess** olarak çalışınca macOS TCC mikrofon iznini **app bundle'ımıza** atfetmeli.
Faz 0 (app üzerinde Info.plist + entitlement) bunu hedefler; ama subprocess-TCC davranışı yalnızca
imzalı/native build'de kesinleşir. İlk mikrofon erişiminde izin isteminin app adına çıktığı kullanıcının
native testiyle teyit edilecek — bu, planın "spike"inin gerçek karşılığıdır. Eğer TCC subprocess'e izin
vermezse yedek plan: ffmpeg yerine küçük bir saf-Go avfoundation köprüsü değil (CGO), `getUserMedia`
(Yol 1) yeniden değerlendirilir; ancak bu MVP kapsamı dışıdır.

## Kapsam dışı (gelecek-issue)

- Offline whisper.cpp (ilk CGO bağımlılığı; universal cross-compile + notarization etkisi).
- ffmpeg'in .app içine bundle'lanması.
- Autosubmit ayar bayrağı, çoklu STT motoru, dil seçimi (sabit `tr`).
- Windows/Linux ses aygıtı (avfoundation → dshow/alsa) — capture.go soyutlaması hazırlar, MVP macOS.
