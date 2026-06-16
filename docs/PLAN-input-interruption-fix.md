# Input Bölünme Bug'ı Düzeltmesi

> Kullanıcı bir CLI terminaline elle prompt yazarken, orchestrator başka bir
> agent'tan gelen bildirimi **aynı PTY'ye** enjekte ediyor. Enjeksiyon
> kullanıcının yarım kalan (henüz Enter'lanmamış) girdisinin arasına girip hem
> kullanıcının metnini hem bildirimi bozuyor.

---

## Denetim Revizyonu (2026-06-16, kod-doğrulamalı)

> Çok-ajanlı kod-denetimi sonucu plan **kod tabanına karşı** doğrulandı ve revize
> edildi.

**Verdict:** `minor-revisions` — plandaki **öz-çelişki DOĞRULANDI** ve düzeltildi;
yön doğru, çekirdek önceliği ve eşzamanlılık (concurrency) detayları sağlamlaştırıldı.

**Uygulanan düzeltmeler (kod-doğrulamalı):**
- 🔴 **Öz-çelişki (kritik):** Eski plan hem "ayrı `time.AfterFunc(typingQuietWindow,
  recheck)` kur" hem "tek pending kuyruğu/tek timer'da birleştir" diyordu —
  çelişkili. İkinci timer `pendingTimers[key]` slotunu ezer → kayıp/çift/sızıntılı
  bildirim. Kod tabanında tek-timer invariant'ı zaten var
  (`orchestrator.go:206-211`, varlık-guard altında). "Ayrı AfterFunc" talimatı
  **silindi**; yerine flush-anı **RE-ARM** (mevcut timer'ı `Stop()`+yeniden-ata)
  kondu.
- 🟢 **Deterministik çekirdek:** Seçenek D (koşullu/ertelenmiş trailing-CR) erken
  submit hasarını **deterministik** öldürür → **çekirdek** yapıldı; Seçenek A
  (typing-defer) ikincil/interleaving-azaltma katmanına indirildi.
- 🟢 **Write-mutex:** `pty.Manager.Write`'a per-session write mutex eklendi —
  bildirim, kullanıcı tuşlarına göre tek atomik blok yazılır (heuristik tek başına
  yetmez; tuşlar enjeksiyon ortasında gelebilir).
- 🟢 **Altyapı ayrımı:** `lastUserInputNano` (atomic.Int64) `lastOutputNano`'dan
  (`manager.go:123`, yalnızca CLI çıktısı) **ayrı**; kullanıcı yolunda
  (`manager.go:203` / `app.go:686`) set edilir. (grep: `RegisterUserInput` /
  `lastUserInputNano` kod tabanında **yok** — greenfield.)
- 🟢 **Lock-ordering:** `UserTypingRecently`/`GetSession` sorgusu `o.mu` **dışında**
  yapılır; `o.mu` tutarken `pty.Manager.mu` alınmaz → `Manager.mu`↔`Orchestrator.mu`
  deadlock'u olmaz.

**Kapsam / böl-birleştir kararları:**
- Typing-defer **manager-routed dala** (`orchestrator.go:276-298`) DA uygulanır —
  manager terminali tam repro hedefi. Yalnızca analyze dalı yetmez.
- `maxDeferral` **30s'ten KÜÇÜK** (örn. 10-15s); per-key `deferStartedAt` alanıyla
  sonsuz re-arm engellenir.
- **Zorunlu fallback:** `maxDeferral` dolarken kullanıcı hâlâ yazıyorsa, sahip
  olmadığımız readline'a yarış-suz enjeksiyon yoktur → bildirim PTY yerine UI
  badge/toast'a (`EventsEmit`) yönlendirilir. Bu opsiyonel değil **zorunlu**.
- **Üçüncü enjeksiyon yolu:** `SendPromptToAgent` (`app.go:903`, bracketed-paste'siz
  + auto-submit) da hesaba katıldı; voice (#16) ile ortak `injectText` helper'ında
  uzlaştırılacak.

**Çapraz-analiz notu (ön koşul / sıralama):** Bu input-fix **ÖNCE** gelmeli.
Write-mutex + `injectText` disiplini, voice (#16) transkript enjeksiyonu ve olası
CLI-spawn işleri bunun **üzerine** oturur. Önerilen sırada **1. sıradadır**.

---

## Problem / Bağlam

### Tekrar üretim senaryosu

1. Kullanıcı `manager` agent'ının terminaline elle uzun bir prompt yazmaya başlar
   (örn. "Lütfen şu modülü refactor et ve testleri..."). Henüz Enter'a basmamıştır.
2. Tam o sırada başka bir agent (örn. `backend`) odaya bir mesaj gönderir.
3. Hub `message_new` event'ini yayınlar → `app.go` orchestrator'a iletir
   (`app.go:208`).
4. Orchestrator hedef agent'ı (manager) bulur ve `sendToTerminal` ile **doğrudan
   PTY stdin'ine** bracketed-paste bildirimi + `\r` yazar
   (`orchestrator.go:186-188`).
5. Bu yazma, kullanıcının yarım girdisinin **ortasına** enjekte olur. CLI input
   satırında kullanıcının metni ile bildirim metni iç içe geçer; ayrıca sondaki
   `\r` (CR) kullanıcının yarım cümlesini erkenden **submit** edebilir.

### Neden bozuluyor

Kullanıcı girdisi ve programatik bildirim **aynı tek kanaldan** (PTY master
fd'sinin stdin tarafı) akıyor. CLI (Claude/Gemini/Copilot) için her ikisi de
ayırt edilemeyen klavye girişidir. İki yazma kaynağı arasında ne bir kilit, ne
"kullanıcı şu an yazıyor mu?" kontrolü, ne de bir kuyruk var.

---

## Kök Neden Analizi

### 1. İki yazma kaynağı, tek hedef, koordinasyon yok

**Kullanıcı girdisi yolu:**

- `frontend/src/components/TerminalPane.tsx:79-83` — xterm `onData` her tuş
  basımında `WriteToTerminal(sessionID, data)` çağırır.
- `app.go:675-687` — `WriteToTerminal` veriyi doğrudan `a.ptyManager.Write`'a
  geçirir (copilot için sadece Focus-Out filtresi var, başka bir tamponlama yok).
- `internal/pty/manager.go:203-224` — `Write` → `session.PTY.Write(data)`.

**Bildirim (enjeksiyon) yolu:**

- `app.go:208` — `message_new` event'inde `a.orchestrator.ProcessMessage(...)`.
- `internal/orchestrator/orchestrator.go:229` / `261` — `notifyAgent` /
  `flushPending` → `sendToTerminal`.
- `internal/orchestrator/orchestrator.go:152-190` — `sendToTerminal` **aynı**
  `o.ptyManager.Write(sessionID, ...)`'ı çağırır.

Her iki yol da `manager.go:222`'deki tek `session.PTY.Write` satırında birleşir.
`Manager.Write` (`manager.go:203-224`) yalnızca `m.mu.RLock()` ile session map'ini
korur; **gerçek yazma** (`session.PTY.Write`) için **hiçbir per-session kilit yok**.
Tek bir mantıksal bildirim birden çok `Write` çağrısına bölünür (bracketed-paste +
CR, ya da copilot karakter-karakter), bu çağrılar arasına kullanıcı tuşları
girebilir → iki ayrı "satır" iç içe geçer. Çözümün parçası: `Write`'a per-session
write mutex (aşağıya bakın).

### 2. Bildirim, kullanıcının "yazıyor" durumunu hiç kontrol etmiyor

- `internal/orchestrator/orchestrator.go:194-262` — `notifyAgent` ve
  `flushPending` yalnızca **bildirimler arası** cooldown'a (3sn,
  `NotifyCooldown`, satır 19) bakar. Bu cooldown agent'ın son **bildirildiği**
  ana göre çalışır; **kullanıcının son tuş basımına** bakmaz.
- Yani cooldown, "kullanıcı yazarken bekle" amacı taşımıyor; sadece aynı agent'a
  art arda bildirim spam'ini engelliyor.

### 3. Idle detection var ama bildirim için kullanılmıyor

- `internal/pty/manager.go:226-247` — `WaitForIdle`, `lastOutputNano`'ya
  (PTY **çıktısı**) göre boşta kalmayı ölçer.
- Bu yalnızca `app.go:653`'te **startup prompt** gönderiminde kullanılıyor.
  Bildirim enjeksiyonunda (`sendToTerminal`) hiç çağrılmıyor.
- Ayrıca `lastOutputNano` **CLI çıktısını** izler, kullanıcı **girdisini**
  değil (`manager.go:123`'te yalnızca `PTY.Read` sonrası set ediliyor).
  Dolayısıyla mevcut idle metriği "kullanıcı klavyede yazıyor mu?" sorusuna
  zaten cevap veremez — kullanıcı girdisini ölçen bir alan **hiç yok**
  (grep doğrulaması: `lastInputNano` / `RegisterUserInput` / `UserActive`
  kod tabanında bulunmuyor).

### 4. Sondaki CR (`\r`) erken submit riskini büyütüyor

- `orchestrator.go:188` (Claude/Gemini) ve `orchestrator.go:179` (Copilot)
  bildirimi yazdıktan sonra `\r` gönderir. Kullanıcının yarım girdisi input
  satırındayken bu CR, o yarım satırı (bildirim metniyle birleşmiş halde)
  CLI'a **gönderir**.

**Özet:** Enjeksiyon kullanıcının yarım girdisini korumuyor; idle/typing kontrolü
yapılmadan, kullanıcı tam klavyede yazarken bile enjeksiyon gerçekleşiyor.

---

## Çözüm Seçenekleri

### Seçenek A — "Kullanıcı yazıyor" durumunu izleyip enjeksiyonu ertele/kuyrukla

Her PTY için son kullanıcı tuş basımının zaman damgasını tut. Bildirim
enjekte edilmeden önce, eğer son tuş basımından bu yana `typingQuietWindow`
(örn. 1.5–2sn) geçmediyse, enjeksiyonu o terminal için bir kuyruğa al ve sessizlik
oluşunca (veya bir tavan süre dolunca) bas.

- **Nasıl:**
  - `WriteToTerminal` (`app.go:675`) çağrıldığında PTY session'ında
    `lastUserInputNano` set et (Enter/CR ise "satır temizlendi" say, isteğe bağlı).
  - `sendToTerminal`/`notifyAgent` enjeksiyondan önce
    `pty.Manager` üzerinden son input zamanını sorgula; "yazıyor" ise
    `time.AfterFunc` ile ertele veya pending kuyruğuna ekle (mevcut `pendingMsgs`
    batching altyapısı bunu zaten kısmen barındırıyor).
- **Artı:** Bildirim yine terminalde görünür (mevcut UX korunur). Hedefli,
  davranışsal düzeltme. Mevcut batching koduyla iyi örtüşür.
- **Eksi:** "Yazmayı bıraktı" tespiti sezgiseldir — kullanıcı düşünürken duraksarsa
  enjeksiyon yine araya girebilir. Tavan süreyle (örn. 30sn) yine de eninde sonunda
  basmak gerekir; o anda kullanıcı tekrar yazıyorsa sorun nüksedebilir (azaltılmış
  olsa da). Concurrency dikkatli kurulmalı (input zaman damgası atomik olmalı).

### Seçenek B — Bildirimleri PTY'ye hiç enjekte etmeyip UI tarafında göstermek

Orchestrator'ın PTY'ye yazmasını tamamen kaldır; bunun yerine bildirimleri Wails
event'i ile frontend'e gönderip terminal başlığında/ayrı bir badge veya toast
alanında göster. Agent'ın mesajı okuması zaten `read_messages` MCP çağrısıyla
oluyor; PTY'ye yazmanın tek işlevi agent'ı "dürtmek".

- **Artı:** Kullanıcı girdisi **hiçbir zaman** bozulmaz — iki kanal tamamen ayrılır.
  En temiz mimari ayrım.
- **Eksi:** Davranış değişikliği büyük. Şu an enjeksiyon, agent CLI'ını otomatik
  olarak `read_messages` çağırmaya **tetikliyor** (CLI'a metin + CR gidiyor). Badge'e
  geçersek agent'ı otomatik dürtme kaybolur; agent insan müdahalesi olmadan yeni
  mesajı görmez. Yani çekirdek "agent'lar birbirini otomatik uyarsın" özelliği
  kırılır. Bu seçenek ancak agent'ları dürtmenin başka bir yolu (örn. CLI idle iken
  enjeksiyon, insan terminaline yazmıyorsa) ile birleşirse anlamlı.

### Seçenek C — Enjeksiyondan önce yarım girdiyi koru/geri yaz (kill-line + restore)

Enjeksiyondan hemen önce terminal kontrol dizileriyle mevcut input satırını temizle
(`Ctrl-U` / `ESC` veya `\x1b[2K\r`), bildirimi bas, sonra kullanıcının yarım
metnini geri yaz.

- **Artı:** Teorik olarak kullanıcının yazdığı korunur.
- **Eksi:** Pratikte **kırılgan ve riskli**. Kullanıcının o ana kadar PTY'ye
  gönderdiği ham byte'ları biz tutmuyoruz (frontend her tuşu anında gönderiyor,
  `app.go:675`). Geri yazmak için bunları ayrıca tamponlamamız gerekir. Üstelik
  Claude/Gemini/Copilot'un **TUI** input editörleri (Ink/React) imleç konumu,
  çok satırlılık, otomatik tamamlama, history gibi durumları kendileri yönetir;
  bizim "kill-line + restore" varsayımımız her CLI'da farklı davranır ve kolayca
  bozar. CLI'ya özel, bakımı zor kod gerektirir. **Önerilmez.**

### Seçenek D (tamamlayıcı) — Sondaki CR'i koşullu yapmak

Bağımsız olarak, kullanıcı yakın zamanda yazdıysa enjeksiyon sırasında sondaki `\r`'i
geciktir/atla — en azından "erken submit" hasarını azaltır. Tek başına yeterli
değildir ama Seçenek A ile birlikte güvenlik ağı olur.

---

## Önerilen Çözüm

**Seçenek D (deterministik çekirdek) + Seçenek A (ikincil, interleaving azaltma).**

> **Denetim notu (öncelik düzeltmesi):** Asıl zarar — kullanıcının yarı-satırının
> **erken submit** edilmesi — deterministiktir ve Seçenek D (koşullu/ertelenmiş
> trailing-CR) tarafından **kesin** öldürülür. Bu yüzden Seçenek D çekirdek
> düzeltmedir. Seçenek A (typing-defer) interleaving olasılığını azaltır ama
> sezgiseldir; ikincil katman olarak kalır. Ayrıca tek başına heuristik yetmez:
> tuşlar enjeksiyon **ortasında** gelebilir → `pty.Manager.Write`'a per-session
> write mutex eklenmeli (aşağıya bakın).

Gerekçe:
- Mevcut "agent'lar birbirini otomatik dürtme" davranışını **korur** (Seçenek B'nin
  kırdığı şey).
- Mevcut `pendingMsgs`/`pendingTimers` batching altyapısının doğal bir uzantısıdır
  (`orchestrator.go:60-61, 194-262`) — büyük mimari değişiklik gerektirmez.
- Seçenek C'nin kırılgan, CLI'ya özel "geri yazma" hile'sini tamamen yapmaz.

**Yaklaşımın özü:**
1. PTY session'ına `lastUserInputNano atomic.Int64` ekle (`lastOutputNano`'dan
   **AYRI** — `manager.go:123` yalnızca CLI **çıktısını** izler); `WriteToTerminal`'da
   (kullanıcı yolu, `manager.go:203` / `app.go:686`) her kullanıcı girdisinde
   güncelle.
2. `pty.Manager.Write`'a **per-session write mutex** ekle (Seçenek D'nin güvenlik
   ağı): bildirim, kullanıcı tuşlarına göre **tek atomik blok** olarak yazılsın.
   Aksi halde tuşlar enjeksiyon ortasında araya girer; heuristik tek başına yetmez.
3. Manager'a `UserTypingRecently(sessionID, window)` (ya da ham `LastUserInput`)
   sorgusu ekle. **Lock-ordering kuralı:** bu sorguyu (ve `GetSession`'ı) `o.mu`
   **DIŞINDA** yap (mevcut `sendToTerminal` disiplini); `o.mu` tutarken
   `pty.Manager.mu` **ALMA** → `Manager.mu`↔`Orchestrator.mu` deadlock'u olmaz.
4. `notifyAgent` enjeksiyondan önce typing sorgusu yapar:
   - Kullanıcı son `window` (örn. 1.5sn) içinde yazdıysa → bildirimi pending
     kuyruğuna al; flush anında re-arm et (3a).
   - Aksi halde → şimdiki gibi anında enjekte et.
5. **Çekirdek (Seçenek D):** `sendToTerminal`'da trailing-CR'i koşullu yap —
   enjeksiyon anında kullanıcı hâlâ yazıyorsa son `\r`'i geciktir/atla. Bu erken
   submit hasarını deterministik olarak engeller.
6. **Kapsam:** typing-defer'i hem analyze dalına hem **manager-routed dala**
   (`orchestrator.go:276-298`) uygula — manager terminali tam repro hedefidir.
7. **ZORUNLU fallback:** `maxDeferral` dolarken kullanıcı hâlâ yazıyorsa, sahip
   olmadığımız readline'a yarış-suz enjeksiyon mümkün değildir. Bu durumda
   bildirimi PTY'ye yazmak yerine **UI badge/toast'a** (Wails `EventsEmit`)
   yönlendir. Bu opsiyonel değil **zorunlu** fallback'tir (Seçenek B'nin görsel
   kısmı bu edge-case için gereklidir).

---

## Etkilenen / Yeni Dosyalar

| Dosya | Değişiklik türü | Açıklama |
|-------|-----------------|----------|
| `internal/pty/manager.go` | Değişiklik | `PTYSession`'a `lastUserInputNano atomic.Int64` ekle (`lastOutputNano`'dan ayrı); `RegisterUserInput(sessionID)` ve `UserTypingRecently(sessionID, window)` (ya da `LastUserInput(sessionID)`) metodları. **Ayrıca** `Write`'a per-session write mutex ekle (atomik enjeksiyon bloğu). |
| `app.go` (`WriteToTerminal`, ~675-686; `SendPromptToAgent`, ~903) | Değişiklik | Her kullanıcı yazmasında `RegisterUserInput` çağır. Salt-okuma/escape-only diziler için filtre (aşağıdaki açık sorulara bakın). `SendPromptToAgent` (`app.go:903`, bracketed-paste'siz + auto-submit `\n`) **üçüncü enjeksiyon yolu**dur — ortak `injectText` helper'ında (voice #16 ile) uzlaştırılacak. |
| `internal/orchestrator/orchestrator.go` | Değişiklik | `notifyAgent`/`flushPending` enjeksiyon öncesi typing kontrolü + deferral (mevcut tek-timer'ı **RE-ARM** ederek, ikinci timer kurmadan); CR'i koşullu geciktirme; typing-defer'i manager-routed dala (`:276-298`) da uygula. `Orchestrator`'a `typingQuietWindow` / `maxDeferral` (<30s) sabitleri + per-key `deferStartedAt` alanı. |
| `internal/orchestrator/orchestrator_test.go` | Değişiklik | Typing-deferral için yeni testler; mevcut `newTestOrchestrator` harness'ına enjekte edilebilir "son input zamanı" sağlayıcısı. |
| `internal/pty/manager_*_test.go` | Yeni/Değişiklik | `RegisterUserInput` / `UserTypingRecently` için birim testi. |
| `frontend/src/components/TerminalPane.tsx` | (Opsiyonel, faz 2) | Yalnızca badge/toast UI'ı eklenirse — yeni Wails event dinleyicisi. |

---

## Adım Adım İmplementasyon

1. **PTY input zaman damgası altyapısı**
   - `manager.go`: `PTYSession`'a `lastUserInputNano atomic.Int64` ekle.
   - `Manager.RegisterUserInput(sessionID string)`: session'ı bul, `Store(now)`.
   - `Manager.UserTypingRecently(sessionID string, window time.Duration) bool`:
     `now - lastUserInputNano < window` → true. (Veya ham `LastUserInput` döndürüp
     kararı orchestrator'a bırak — test edilebilirlik için tercih edilebilir.)

2. **Kullanıcı girdisini işaretle**
   - `app.go` `WriteToTerminal` (satır 675-686): `a.ptyManager.Write` öncesinde
     `a.ptyManager.RegisterUserInput(sessionID)` çağır. Copilot Focus-Out
     (`\x1b[O`) gibi salt-kontrol dizilerini "gerçek yazım" saymamaya dikkat et.
   - **Üçüncü enjeksiyon yolu:** `SendPromptToAgent` (`app.go:903`) bracketed-paste'siz
     ve auto-submit'li (`rendered+"\n"`) ayrı bir yazma yapar. Bunu da aynı
     write-mutex/`injectText` disiplinine sok (voice #16 transkript enjeksiyonu ile
     ortak helper'da uzlaştırılacak) ki o yol da kullanıcı girdisini bölmesin.

3. **Orchestrator deferral mantığı**
   - `orchestrator.go`: `typingQuietWindow` (örn. 1500ms) ve `maxDeferral`
     (örn. 30s) sabitleri ekle.
   - `notifyAgent` içinde, anında gönderim dalında (satır ~218-229) önce
     "kullanıcı yazıyor mu?" kontrolü yap. Yazıyorsa: mesajı `pendingMsgs`'e ekle.
   - **Kritik (öz-çelişki düzeltildi):** ayrı/ikinci bir `time.AfterFunc` KURMA.
     Kod tabanında zaten tek-timer invariant'ı var
     (`orchestrator.go:206-211`, `if _, exists := o.pendingTimers[key]; !exists`
     varlık-guard'ı altında). İkinci bir timer `pendingTimers[key]` slotunu **ezer**
     → kayıp/çift/sızıntılı bildirim. Bunun yerine **mevcut** `pendingTimers[key]`
     timer'ını yeniden kullan: flush anında kullanıcı hâlâ yazıyorsa ve `maxDeferral`
     aşılmadıysa o timer'ı `timer.Stop()` + yeniden-ata ile `o.mu` altında
     **RE-ARM** et (aşağıdaki 3a maddesi). `pendingMsgs`/`pendingTimers`/
     `lastNotified` için tek-key sözleşmesini ve `UnregisterAgent`
     (`orchestrator.go:102-108`) temizliğini bozma.

3a. **Flush-anı RE-ARM + lock-ordering + kapsam + tavan**
   - `flushPending` içinde, `o.sendToTerminal` çağrısından **önce**, `o.mu`
     **DIŞINDA** `UserTypingRecently(sessionID, typingQuietWindow)` sorgula
     (`GetSession` de `o.mu` dışında). `o.mu` tutulurken `pty.Manager.mu` alınmaz
     → `Manager.mu`↔`Orchestrator.mu` deadlock'u engellenir.
   - Kullanıcı hâlâ yazıyorsa **ve** `now - deferStartedAt[key] < maxDeferral`
     ise: `o.mu` altında **mevcut** timer'ı `timer.Stop()` + yeniden-`time.AfterFunc`
     ile RE-ARM et (yeni slot oluşturma; aynı `pendingTimers[key]` slotunu yaz).
     `pendingMsgs`/`lastNotified` dokunulmaz; mesajlar kuyrukta kalır.
   - Sonsuz re-arm'ı engellemek için per-key `deferStartedAt time.Time` alanı tut:
     ilk erteleme anında set et, flush'ta (gerçek enjeksiyondan sonra) temizle.
     `maxDeferral`'ı **30s'ten KÜÇÜK** seç (örn. 10-15s).
   - **Kapsam:** bu typing-defer'i hem analyze dalına hem manager-routed dala
     (`orchestrator.go:276-298`, `o.notifyAgent(...)`) uygula — manager terminali
     tam repro hedefidir.
   - `maxDeferral` dolarken kullanıcı hâlâ yazıyorsa: PTY'ye enjekte **etme**;
     bunun yerine Wails `EventsEmit` ile UI badge/toast'a yönlendir (zorunlu
     fallback, madde 7).

4. **Koşullu CR (Seçenek D — deterministik çekirdek) + write-mutex**
   - `sendToTerminal`: metni yazdıktan sonra, enjeksiyon anında kullanıcı hâlâ
     `window` içinde yazıyorsa, son `\r`'den önce ek bir kısa bekleme/yeniden
     kontrol ekle (veya enjeksiyonu o tur tamamen ertele). Bu, erken submit
     hasarını deterministik olarak engelleyen **çekirdek** düzeltmedir.
   - `pty.Manager.Write`'a (`manager.go:203`) per-session write mutex ekle ki
     bildirimin tüm byte'ları (bracketed-paste/karakter-karakter copilot + CR)
     kullanıcı tuşlarına göre **tek atomik blok** olarak yazılsın. Heuristik tek
     başına yetmez: tuşlar enjeksiyon ortasında gelebilir.

5. **Testler**
   - Orchestrator: "kullanıcı son 500ms'de yazdı → enjeksiyon ertelendi",
     "kullanıcı 2sn'dir sessiz → anında enjekte", "maxDeferral aşıldı → zorla
     enjekte" senaryoları. `newTestOrchestrator` harness'ına enjekte edilebilir
     bir "typingRecently" fonksiyonu ekle (mevcut `sendFunc` injection deseniyle
     aynı tarzda).
   - PTY: `RegisterUserInput` sonrası `UserTypingRecently` true; window sonrası false.

6. **(Opsiyonel faz 2) UI bildirim göstergesi**
   - Enjeksiyon ertelendiğinde Wails event yay; `TerminalPane.tsx` başlığında badge.

---

## Açık Sorular / Karar Gerektiren Noktalar

1. **`typingQuietWindow` ve `maxDeferral` değerleri.** 1.5sn sessizlik makul
   başlangıç mı? **Denetim kararı:** `maxDeferral` **30s'ten KÜÇÜK** olmalı
   (örn. 10-15s) — 30s çok uzun, mesaj gecikmesi yaratır. Kullanıcı düşünürken
   duraklaması (>1.5sn) enjeksiyona izin verir; bu yüzden write-mutex + koşullu CR
   (Seçenek D) çekirdek güvenlik ağıdır.
2. **"Gerçek yazım" tanımı.** `WriteToTerminal`'a gelen her byte input sayılmalı mı?
   Ok tuşları, salt-kontrol ANSI dizileri, Copilot Focus-In/Out (`\x1b[I` /
   `\x1b[O`) hariç tutulmalı mı? (Focus-Out zaten `app.go:679`'da filtreleniyor.)
3. **Kullanıcı Enter'a bastıktan sonra.** CR gönderildiğinde input satırı "boşaldı"
   sayılmalı; o an enjeksiyon güvenli hale gelir. `RegisterUserInput` CR'i ayrı mı
   ele almalı (typing damgasını sıfırlamalı)?
4. **maxDeferral dolduğunda hasar. (KARARA BAĞLANDI)** Tavan süre dolup kullanıcı
   hâlâ yazıyorsa, sahip olmadığımız readline'a yarış-suz enjeksiyon **mümkün
   değildir**. Bu durumda Seçenek B (UI badge/toast, Wails `EventsEmit`) **zorunlu
   fallback**'tir — opsiyonel değil. PTY'ye o anda enjekte edilmez.
5. **Çoklu hedef/broadcast.** Broadcast'te her hedef terminal ayrı ayrı mı
   değerlendirilmeli (her birinin kendi typing durumu var)? Evet olmalı — kuyruk
   per-session/per-agent tutuluyor (`chatDir:agentName` anahtarı zaten mevcut).
6. **Copilot karakter-karakter enjeksiyonu** (`orchestrator.go:173-179`) uzun
   sürüyor (~5ms/karakter). Enjeksiyon ortasında kullanıcı yazmaya başlarsa?
   **Denetim kararı:** per-session write mutex (madde 4) tüm karakter dizisini
   tek atomik blok yapar; mid-injection kullanıcı tuşu araya **giremez**. Enjeksiyon
   başladıktan sonra durdurmayı (cancel) ilk faz kapsamı dışında bırakmak makul.

---

## Doğrulama / Test

**Manuel tekrar üretim (düzeltme öncesi → kırık):**
1. `make dev` ile uygulamayı başlat, en az 2 agent'lı bir takım kur (örn.
   `manager` + `backend`).
2. `manager` terminaline elle uzun bir cümle yazmaya başla, **Enter'a basma**.
3. `backend` agent'ından `manager`'a bir mesaj gönder (`send_message`) — veya
   ikinci bir terminalden tetikle.
4. **Beklenen kırık davranış:** `manager` input satırında kullanıcı metni ile
   `[agent-chat] New message from backend...` bildirimi iç içe geçer ve/veya yarım
   cümle erken submit olur.

**Düzeltme sonrası beklenen:**
- Adım 2–3 tekrarlandığında, kullanıcı yazmayı bırakana (≥`typingQuietWindow`) kadar
  bildirim enjekte edilmez; kullanıcı durunca bildirim temiz bir satırda görünür.
- Kullanıcı yazmıyorken gelen mesaj eskisi gibi anında enjekte edilir (regresyon yok).

**Otomatik test:**
- `go test ./internal/orchestrator/ -run TestNotify` (yeni typing-deferral testleri).
- `go test ./internal/pty/ -run TestUserInput` (yeni input-timestamp testleri).
- `go test ./...` ile mevcut cooldown/batching/broadcast testlerinin (özellikle
  `orchestrator_test.go` thread-safety testleri) hâlâ geçtiğini doğrula.

**Log doğrulaması:** `~/.agent-chat/mcp-server.log` ve uygulama logunda
`[ORCH] Notification deferred (user typing)` benzeri yeni satırların göründüğünü ve
ertelemenin beklendiği gibi flush edildiğini izle.

---

## Tahmini Efor

**M (Orta).**

- Çekirdek düzeltme (PTY input zaman damgası + orchestrator deferral + CR koşulu +
  testler) tek pakette mantıksal, sınırlı dosya kümesinde (`manager.go`, `app.go`,
  `orchestrator.go` ve testleri). Mevcut batching altyapısı işi kolaylaştırıyor.
- Opsiyonel faz 2 (UI badge/toast) ayrı, küçük (S) bir ek iş olarak ertelenebilir.
- Belirsizlik, "yazmayı bıraktı" sezgiselinin ayarı ve TUI'ye özel davranışlardan
  geliyor; bu nedenle S değil M.
