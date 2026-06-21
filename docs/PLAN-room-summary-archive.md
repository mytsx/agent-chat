# Oda Özeti + Arşivleme + Fresh Session'a Özet Enjeksiyonu

## Problem / Bağlam

Agent'lar bir odaya katıldıklarında geçmişi `read_messages` / `read_all_messages` ile okuyor. Asıl risk **token şişmesi değil**, oda yaşam döngüsündeki **geri-dönülmez veri kaybı**:

- **Truncate kaynaklı veri kaybı (asıl gerekçe):** Oda mesajları 500'de cap'lenip 300'e truncate ediliyor (`internal/hub/room.go:13-15, 135-137`). Truncate tetiklendiğinde düşen ~200 mesaj **hiçbir yere yedeklenmeden** bellekten siliniyor; persistence sadece truncate sonrası snapshot'ı yazıyor (`persistence.go:104-131`), eski mesajları kurtarmıyor.
- **`clear_room` geri-dönülmezliği:** `Clear()` tüm mesajları ve agent kayıtlarını tek seferde siliyor (`internal/hub/room.go:289-298`), geri dönüşü yok.
- **Not — "token şişmesi" iddiası kodla çürütüldü:** `read_all_messages(since_id=0)` çağrısı **tüm geçmişi çekmez**. Default `limit=15` (`mcpserver/tools.go:222`, `hub/protocol.go:413`); ayrıca `room.go:177-194` (`ReadAllMessages`) `since_id=0` olsa BİLE sonucu son 15 mesajla sınırlar. Yani fresh agent yüzlerce mesaj okuyamaz; bu öncül yanlıştı (detay aşağıdaki Denetim Revizyonu bölümünde).

Kullanıcının isteği: Oda kapanmadan/temizlenmeden önce konuşmanın **özeti çıkarılmalı**, tam geçmiş **arşivlenmeli** (istenince yine okunabilsin), ve aynı odada **fresh bir takım** kurulduğunda agent'ların karşısına **tüm geçmiş yerine özet** gelmeli.

## Denetim Revizyonu (2026-06-16, kod-doğrulamalı)

> Çok-ajanlı kod denetimi sonucu. **Verdict: MAJOR-REVISIONS + BÖL.** Aşağıdaki bulgular kaynak kodla (file:line) doğrulandı.

**Düzeltilen yanlış/eskimiş iddialar:**

- **Temel öncül hatası (kritik):** "Fresh agent `read_all_messages(since_id=0)` ile tüm geçmişi (400-500 mesaj) çeker → token şişmesi" iddiası **YANLIŞ**. `read_all_messages` default `limit=15` (`tools.go:222`, `protocol.go:413`) ve `ReadAllMessages` (`room.go:189-191`) `since_id=0` olsa bile sonucu son 15 mesajla cap'ler. Problem/Bağlam (eski satır 5-7) buna göre yeniden yazıldı.
- **Yeniden temellendirme:** Planın gerçek gerekçesi token değil: **(a)** truncate kaynaklı veri kaybı (`room.go:135-137`, ~200 mesaj yedeklenmeden siliniyor) + **(b)** `clear_room` geri-dönülmezliği (`room.go:289-298`). İkisi de kodla doğrulandı.
- **Gerekçe metni düzeltmesi:** "her mesajda" ve "fsync bekletir" ifadeleri çıkarıldı. Truncate ~200 mesajda bir tetiklenir (her mesajda değil); kod tabanı **hiç fsync yapmıyor** (`persistence.go` `WriteFile`+`Rename`, `Sync()` çağrısı yok).
- **Öz-çelişkili arşiv tasarımı düzeltmesi:** A1 hem "mutex altında senkron yaz" hem "persistLoop'a bağla" diyordu. Doğru tasarım netleştirildi (aşağıya bakın — Clear() belleği anında boşalttığı için persistLoop 5s sonra boş odayı okur, hiçbir şey arşivlenmez).

**Bonus bug (✅ ÇÖZÜLDÜ):** `startup.go:40` manager'a "tüm mesajları oku" diyor ama `read_all_messages(since_id=0)` çağrısına `limit` geçmediği için **sessizce yalnızca son 15 mesaj** geliyordu. Bu "token şişmesi" değil, **sessiz bağlam eksikliği**ydi — manager eksik bağlamla çalışıyordu. Çözüm: startup talimatı artık `read_all_messages(since_id=0, limit=1000)` çağırıyor (oda en çok 500 mesaj tuttuğu için tüm geçmişi kapsar). TDD ile düzeltildi (`startup_test.go` → `TestComposeStartupPrompt_ManagerReadInstructionPassesExplicitLimit`).

**Kapsam / böl-birleştir kararları:** Tek plan 14 dosya + yeni `summarizer` paketine dokunuyordu; aşırı geniş. Plan 3 bağımsız alt-plana bölündü:

- **(A) ARŞİVLEME** — Faz-1, HEMEN. 2-3 dosya (`room.go`, `persistence.go`, küçük dispatch). Tek başına değer üretir ve veri kaybını durdurur.
- **(B) ÖZET ÜRETİMİ** — ayrı gelecek-plan. İhtiyaç kanıtlanınca (özetin ölçülebilir fayda sağladığı gösterilince) ele alınır. CLI one-shot (`summarizer`) ertelendi.
- **(C) STARTUP ENJEKSİYONU** — ayrı plan. `room-intro-story` (#14) ile **aynı enjeksiyon noktasında** (`ComposeStartupPrompt`) birleşir; o yüzden #14 ile koordineli yapılmalı.

**Güvenlik:** `archive/{room}.jsonl` ve `summary/{room}.md` için `validation.ValidateName` **zorunlu** (path-traversal). `read_archive` için `limit` **zorunlu** (sınırsız okuma yok). `DeleteTeam` (`app.go:852`) erken-return tuzağı: arşiv flush'u terminal-kapatma hatasından **önce** ve **hata-toleranslı** olmalı (mevcut kod `closeErrors` varsa flush'a varmadan return ediyor).

**Çapraz analiz notu (bağımlılık):** Faz-1 arşivleme `room-browser` (#11) den **önce** gelmeli (truncate edilen mesajlar kaybolmadan önce yakalanmalı). Enjeksiyon fazı (C) `room-intro` (#14) ile `ComposeStartupPrompt`'ta `[charter] → [özet] → [son N mesaj]` sırasında birleşir. **Önerilen sıradaki yer: 5. (yalnızca arşivleme).**

## Mevcut Durum (file:line)

### Mesaj cap / truncate / kayıp (asıl problem)
- `internal/hub/room.go:13-15` — sabitler: `maxMessagesInRoom = 500`, `truncateToMessages = 300`.
- `internal/hub/room.go:135-137` — `SendMessage` içinde `len > 500` olunca `r.messages = r.messages[len-300:]`. **Bu noktada en eski ~200 mesaj bellekten düşüyor ve hiçbir yere yedeklenmiyor.** (Truncate ~200 mesajda bir tetiklenir, her mesajda değil.)
- `internal/hub/persistence.go:104-131` — `persistRoom`, `Snapshot()` (truncate sonrası hali) `hub-state/{room}.json`'a atomik yazıyor (temp + rename). **fsync yok** (`WriteFile`+`Rename`, `Sync()` çağrılmıyor). Diskteki dosya da truncate edilmiş hali tutuyor; arşiv yok.
- `internal/hub/room.go:289-298` — `Clear()` her şeyi sıfırlıyor (geri dönüşsüz).

### Geçmiş okuma yolları (NOT: token şişmesi kaynağı DEĞİL)
- `internal/hub/room.go:143-194` — `ReadMessages` (agent'a özel filtre) ve `ReadAllMessages` (manager/admin). **`ReadAllMessages` (`:189-191`) `since_id=0` olsa bile sonucu son `limit` mesajla cap'ler.**
- `internal/hub/protocol.go:342-473` — `handleGetMessages` / `handleGetAllMessages`. `handleGetAllMessages` default `limit=15` (`:413`). `read_all_messages` yalnızca aktif manager veya yetkili desktop'a açık (`protocol.go:419-436`).
- `internal/mcpserver/tools.go:220-241` — `readAllMessages` handler'ı default `limit=15` (`:222`). Yani fresh agent **tüm geçmişi okuyamaz**; "yüzlerce mesaj context'e girer" öncülü geçersiz.
- `internal/mcpserver/tools.go:114-144` (`readMessages`), `:220-241` (`readAllMessages`) — ince RPC sarmalayıcıları.
- `internal/mcpserver/server.go:127-235` — MCP tool tanımları/description'ları (özet için yeni tool buraya eklenecek).

### Fresh session'a başlangıçta ne enjekte ediliyor?
- `internal/cli/startup.go:9-56` — `ComposeStartupPrompt`. Sıra: base → global → team → selected/manager prompt → **join talimatı**. Join talimatı (`startup.go:37-41`) agent'a şunu söylüyor:
  - normal agent: `read_messages("<agent>")` ile mesajları oku
  - manager: `read_all_messages(since_id=0, limit=1000)` ile "tüm mesajları" oku — **BONUS BUG (✅ ÇÖZÜLDÜ):** önceden `limit` geçilmediği için handler default `limit=15` uyguluyordu (`tools.go:222`), yani manager sessizce yalnızca son 15 mesajı görüyordu. Startup talimatına açık `limit=1000` eklenerek düzeltildi.
- `app.go:591-638` — `composeAgentPrompt`: embedded `base_prompt.md` + `~/.agent-chat/global_prompt.md` + team prompt + library/manager prompt'u birleştirip `ComposeStartupPrompt`'a veriyor.
- `app.go:640-673` — `sendStartupPrompt`: CLI idle olunca bracketed-paste ile prompt'u terminale yolluyor.
- `prompts/base_prompt.md:1-45`, `prompts/manager_prompt.md:1-44` — agent'a verilen başlangıç bağlamı (yeni "özet okuma" talimatı buralara da işlenebilir).

### Oda yaşam döngüsü & CLI entegrasyonu (özet üretimi için)
- `app.go:415-544` — `CreateTerminal`: terminal/PTY oluşturma, room subscribe, MCP config, startup prompt.
- `app.go:557-588` — `RestartTerminal`.
- `app.go:851-873` — `DeleteTeam`: takımın tüm terminallerini kapatıyor + `syncHubManager(name,"")`. (Oda state'ini silmiyor, sadece manager lock temizliyor.) **Arşiv tetikleme için doğal nokta — AMA erken-return tuzağı var:** `:862-864` terminal kapatma hatasında (`closeErrors`) fonksiyon hemen return ediyor; arşiv flush'u bu kontrolden **önce** ve hata-toleranslı yapılmalı, yoksa bir terminal kapanmazsa arşiv hiç çalışmaz.
- `internal/cli/detector.go:124-142` — `GetCommand`: `claude --dangerously-skip-permissions`, `gemini --approval-mode yolo`, `copilot --yolo`, `codex ...`. Bu binary'ler tek-atımlık (one-shot) özet çağrısı için yeniden kullanılabilir (aşağıda değerlendirildi).
- `app.go:937-967` — `GetMessages` / `GetAgents` (desktop, `GetMessagesRaw`/`GetAgentsRaw` üzerinden ham veri okuyor). Özet üretimi için ham mesajları desktop tarafına çekmenin hazır yolu.
- `internal/hubclient/client.go:347-360` — `GetMessagesRaw` (yetkili desktop → `get_messages_raw`).

### Tipler
- `internal/types/message.go:12-24` — `Message` (ID, From, To, Content, Timestamp, Type, ...). Özet, normal bir `Message` (örn. `Type:"summary"`) olarak tutulabilir veya ayrı dosyada.

## Çözüm Tasarımı

Üç bağımsız ama birbirini besleyen alt-mekanizma: **(A) Arşivleme**, **(B) Özet üretimi**, **(C) Fresh session enjeksiyonu**. Önerilen yol her birinde işaretlendi; alternatifler de listelendi.

### A. Arşivleme (tam geçmişi koru)

Amaç: hiçbir mesaj kaybolmasın; istenince tam geçmiş okunabilsin.

**A1 — Truncate ve Clear noktalarında düşen mesajları senkron yakala, ayrı goroutine'le arşivle (ÖNERİLEN).**

> **DÜZELTME (denetim):** Planın ilk hâli (a) arşiv yazımını truncate noktasında mutex altında **senkron disk I/O** olarak yapıyor (yanlış — disk I/O'yu kilit altında tutar), (b) alternatif olarak "persistLoop'a bağla" diyordu — bu da **bug**: `Clear()` (`room.go:290`) belleği **anında** boşaltır, persistLoop ise 5s sonra `Snapshot()` ile artık **boş** odayı okur → hiçbir şey arşivlenmez. İkisi de öz-çelişkili.

**Doğru tasarım:**
1. Truncate (`room.go:135`) ve `Clear()` (`room.go:290`) noktalarında, düşen mesaj slice'ını **lock altında senkron yakala** — ama bu yalnızca ucuz bir **bellek kopyası** (`append([]Message{}, dropped...)`), disk I/O değil. Kilit hızlıca bırakılır.
2. Yakalanan slice'ı **buffered channel** ile, `archive/{room}.jsonl` dosyasının sahibi olan ayrı bir **goroutine**'e ver. Tüm dosya I/O (JSONL append) o goroutine'de, kilit dışında yapılır. I/O hatası loglanır, akışı bloklamaz.
3. `RoomState`'in `dataDir`/`Hub` referansı **yok**; bu yüzden enjekte edilmiş bir callback (`onTruncate(msgs []Message)`) veya hub-seviyesi bir metot kullanılmalı. RoomState dosya yolu bilmemeli — sadece "şu mesajlar düştü" sinyalini iletmeli.

Böylece:
- Bellek/aktif dosya 300-500 arası kalır (mevcut davranış korunur).
- Tam geçmiş `archive/{room}.jsonl` + güncel `hub-state/{room}.json` birleşimi olarak her zaman elde edilebilir.
- `Clear()` de düşen mesajları (temizlemeden önce) aynı kanala iletir; veri kaybı olmaz.

Notlar:
- Append-only JSONL, atomik tam-dosya yazımına göre büyük arşivlerde daha ucuz.
- `~/.agent-chat/hub-state/archive/` dizini `0700` ile oluşturulur (mevcut `os.MkdirAll(stateDir, 0700)` paterni — `persistence.go:106`).
- `archive/{room}.jsonl` yolu için `validation.ValidateName(room)` **zorunlu** (path-traversal).

**A2 — Snapshot dosyaları (Alternatif).** Truncate yerine, `Clear()`/arşivleme anında o anki tüm mesajları `hub-state/archive/{room}-{epoch}.json` olarak yazmak. Daha basit ama truncate sırasında kaybolan mesajları yakalamaz; yalnızca açık arşivleme anlarını kurtarır. A1 ile birlikte kullanılmaz; A1 üstün.

**Okuma:** Yeni `read_archive(room, since_id, limit)` MCP tool'u veya mevcut `read_all_messages`'a `include_archive=true` parametresi. Manager/yetkili desktop'a kısıtlı kalır (mevcut yetki kontrolü `protocol.go:419-436` korunur). **Güvenlik (zorunlu):** `room` için `validation.ValidateName`; `limit` **zorunlu** ve sınırlı (sınırsız/tüm-arşiv okuma yok — arşiv süresiz büyüyebileceği için tek çağrıda tüm dosyayı belleğe almak yasak).

### B. Özet Üretimi

Soru: özeti **kim/ne** üretecek? Üç gerçekçi seçenek:

> **DENETİM KARARI: B (Özet Üretimi) bu plandan ERTELENDİ.** Aşağıdaki seçenekler ileride ayrı bir planda değerlendirilecek. Eklenirse B1 için kritik kısıt: **spawn edilen süreç İZOLE env ile çalıştırılmalı** — `AGENT_CHAT_DATA_DIR` ve `AGENT_CHAT_ROOM` env var'ları **GEÇİRİLMEMELİ**, yoksa one-shot süreç yanlışlıkla hub'a bağlanıp mesaj üretir/oda durumunu bozar. Ayrıca prompt'a "sadece düz metin döndür, hiçbir tool çağırma" talimatı eklenmeli.

**B1 — Mevcut CLI binary'siyle one-shot özet (ERTELENDİ).**
Projede zaten `claude` / `gemini` / `copilot` binary'leri PATH'te ve `GetCommand` ile çağrılıyor (`detector.go:124-142`). Desktop tarafında, arşivlenecek mesajları (`GetMessagesRaw`) bir prompt'a derleyip CLI'yı **etkileşimsiz/one-shot** modda çağırmak:
- Claude: `claude -p "<prompt>"` (print mode, non-interactive).
- Gemini: `gemini -p "<prompt>"` benzeri non-interactive flag.
- Copilot: `copilot -i "<prompt>"` (zaten startup'ta `-i` kullanılıyor — `app.go:496-501`).
- Çıktı stdout'tan okunur, özet metni olarak saklanır.

Avantaj: ek API anahtarı/SDK gerekmez, kullanıcının zaten kurulu/oturum açtığı CLI kullanılır. Dezavantaj: her CLI'nın non-interactive bayrağı farklı; çıktının "saf metin" gelmesi garanti değil (TUI artefaktları temizlenmeli — mevcut `sanitize()` `room.go:433` benzeri). Hangi CLI'nın kullanılacağı ayar/parametre: varsayılan olarak takımın bir agent'ının cliType'ı veya kullanıcı seçimi.

**B2 — Manager agent'a özet ürettirme (Alternatif, en az kod).**
Oda kapanırken aktif manager'a (varsa) bir sistem mesajı/notification: "Bu odanın 8-10 maddelik özetini çıkar ve `submit_summary(...)` ile gönder." Manager zaten `read_all_messages`'a erişebiliyor (`protocol.go:419-436`). Yeni `submit_summary` MCP tool'u özeti `hub-state/summary/{room}.md`'ye yazar.
Avantaj: ayrı CLI süreci yok, mevcut akışa oturur. Dezavantaj: manager her zaman aktif/uygun durumda olmayabilir; kapanış anında manager'ın yanıt vermesini beklemek kırılgan.

**B3 — Kullanıcı manuel yazar / düzenler (Alternatif, her zaman fallback).**
UI'da odanın özetini gösteren/düzenleten bir alan; kullanıcı özeti elle girer veya B1/B2 ile üretilen taslağı düzeltir.
Avantaj: deterministik, sıfır LLM bağımlılığı. Dezavantaj: emek gerektirir.

**Önerilen kombinasyon:** B1'i otomatik taslak üreten varsayılan yap, B3'ü her zaman fallback/düzenleme yolu olarak bırak. B2'yi ileride opsiyon olarak ekle.

**Tetikleme noktaları (hepsi desteklenebilir):**
1. **Manuel buton (öncelik):** UI'da "Odayı Özetle & Arşivle" → desktop ham mesajları çeker, özet üretir, arşivler.
2. **Takım/oda kapanışı:** `DeleteTeam` (`app.go:851-873`) içinde terminaller kapanmadan önce özet+arşiv tetikle. (Opsiyonel; kullanıcı onayıyla.)
3. **`clear_room` öncesi otomatik:** `Clear()` çağrılmadan önce arşive flush + (varsa) özet üret. Böylece yıkıcı temizlik bilgi kaybetmez.
4. **N mesaj eşiği (opsiyonel, ileride):** Mesaj sayısı cap'e yaklaşınca (örn. 450) arka planda özet tazele.

### C. Fresh Session'a Özet Enjeksiyonu

Amaç: aynı odaya yeni takım/agent katıldığında, tüm geçmiş yerine **özet** gelsin.

**C1 — Startup prompt'a özet enjeksiyonu (ÖNERİLEN).**
`composeAgentPrompt` (`app.go:591-638`) odanın kayıtlı özetini (`hub-state/summary/{room}.md`) okuyup, varsa `ComposeStartupPrompt`'a yeni bir `roomSummary` parçası olarak geçirir. `ComposeStartupPrompt` (`startup.go:9-56`) join talimatından **önce** "Bu odanın geçmiş özeti:" bloğunu ekler.
Ayrıca join talimatı değişir:
- normal agent: "Geçmiş özetini yukarıda okudun; detay gerekirse `read_messages` ile son N mesaja bak."
- manager: `read_all_messages(since_id=0)` → **`read_messages(limit=N)` veya özet + `read_archive`** olarak değiştirilir; fresh manager artık tüm geçmişi çekmez.

Avantaj: agent daha ilk turda özete sahip; ekstra tool çağrısı gerekmez. Dezavantaj: özet startup prompt'unu büyütür (ama tüm geçmişten çok daha küçük).

**C2 — Yeni `read_summary` MCP tool'u (ÖNERİLEN tamamlayıcı).**
Agent istediğinde özeti çekebilsin: `read_summary(room)` → `hub-state/summary/{room}.md` içeriğini döner. `read_all_messages`'ın description'ı güncellenir: "Önce `read_summary` çağır; tüm mesajları yalnızca gerektiğinde oku." `base_prompt.md` / `manager_prompt.md`'ye de bu talimat işlenir.
C1 + C2 birlikte: enjeksiyon (push) + isteğe bağlı erişim (pull).

**C3 — `join_room` yanıtına özet gömme (Alternatif).**
`handleJoinRoom` (`protocol.go:176-255`) yanıt metnine ("✅ ... odasına katıldın") özetin ilk K karakterini ekler. Avantaj: agent'lar prompt'tan bağımsız, her join'de özeti görür. Dezavantaj: yanıt metni büyür; uzun özetlerde kırpma gerekir. C1 ile çakışmaz ama gereksiz tekrar olur; C1+C2 tercih edilir.

### Veri Yerleşimi (özet)
```
~/.agent-chat/hub-state/
  {room}.json                 # mevcut: aktif (truncate edilmiş) state
  archive/{room}.jsonl        # YENİ: append-only tam geçmiş
  summary/{room}.md           # YENİ: odanın güncel özeti (markdown)
```

## Etkilenen / Yeni Dosyalar

| Dosya | Değişiklik | Açıklama |
|-------|-----------|----------|
| `internal/hub/room.go` | düzenle | Truncate noktasında (`:135-137`) ve `Clear()` (`:289-298`) öncesi düşen mesajları arşive flush et; özet alanı/erişimi için yardımcılar. |
| `internal/hub/persistence.go` | düzenle | `archive/` ve `summary/` dizinlerini yönet; arşiv JSONL append fonksiyonu; özet dosyası oku/yaz. |
| `internal/hub/protocol.go` | düzenle | Yeni request tipleri: `read_summary`, `submit_summary`, `read_archive`; `handleClearRoom`'da arşiv flush; yetki kontrolleri. |
| `internal/hub/hub.go` | düzenle | `handleRequest` switch'ine yeni tip dispatch'leri (`:14-47` benzeri). |
| `internal/types/message.go` | düzenle | (Opsiyonel) `Type:"summary"` sabiti / `RoomSummary` tipi. |
| `internal/types/protocol.go` | düzenle | (Gerekirse) yeni request/response payload yapıları. |
| `internal/hubclient/client.go` | düzenle | `ReadSummary`, `SubmitSummary`, `ReadArchive`, `WriteSummary` convenience metodları. |
| `internal/mcpserver/storage.go` | düzenle | Yeni RPC sarmalayıcıları. |
| `internal/mcpserver/tools.go` | düzenle | `read_summary` (ve gerekiyorsa `submit_summary`, `read_archive`) handler'ları. |
| `internal/mcpserver/server.go` | düzenle | Yeni MCP tool tanımları + `read_all_messages` description güncellemesi ("önce read_summary"). |
| `internal/cli/startup.go` | düzenle | `ComposeStartupPrompt`'a `roomSummary` parametresi; join talimatında `read_all_messages(since_id=0)` yerine özet-temelli talimat. |
| `app.go` | düzenle | `composeAgentPrompt`'a özet okuma; `DeleteTeam`/clear akışında özet+arşiv tetikleme; özet üretimi (CLI one-shot çağrısı); yeni Wails-bound metotlar (`SummarizeRoom`, `GetRoomSummary`, `ArchiveRoom`). |
| `prompts/base_prompt.md` | düzenle | "Önce odanın özetini oku (`read_summary`), tüm geçmişi okuma" talimatı. |
| `prompts/manager_prompt.md` | düzenle | Manager için fresh-start'ta tüm mesaj okuma yerine özet + arşiv talimatı. |
| `internal/summarizer/` (YENİ paket) | yeni | (B1 için) Ham mesajları prompt'a derleyip CLI'yı one-shot çağıran, çıktıyı temizleyen yardımcı. CLI bağımsız (claude/gemini/copilot). |
| `frontend/src/...` | düzenle | "Özetle & Arşivle" butonu, özet görüntüleme/düzenleme paneli (Wails binding çağrıları). |

## Adım Adım İmplementasyon

1. **Arşiv altyapısı (hub) — Faz-1, HEMEN:** `persistence.go`'ya `appendArchive(room, msgs []Message)` (JSONL append, `archive/` dir, `ValidateName`) ve `readArchive(room, sinceID, limit)` (limit zorunlu) ekle. `room.go`: truncate (`:135`) ve `Clear()` (`:290`) noktalarında düşen mesaj slice'ını **lock altında ucuz bellek kopyası** olarak yakala; `onTruncate(msgs)` callback'i (veya hub-seviyesi metot) ile **buffered channel** üzerinden arşiv goroutine'ine ilet. Disk I/O kilit dışında. RoomState dosya yolu bilmemeli. `Clear()` temizlemeden önce mesajları aynı kanala verir.
2. **Özet depolama (hub):** `writeSummary(room, md)` / `readSummary(room)` (atomik temp+rename, `summary/` dir). Yetki: `submit_summary` yalnızca aktif manager/yetkili desktop; `read_summary` odadaki herhangi bir join'li agent.
3. **Protokol & RPC:** `hub.go` switch + `protocol.go` handler'ları (`handleReadSummary`, `handleSubmitSummary`, `handleReadArchive`) ekle. `hubclient/client.go`'ya convenience metotları ekle.
4. **MCP tool'ları:** `storage.go` + `tools.go` + `server.go`'da `read_summary` (her agent), opsiyonel `submit_summary` (manager). `read_all_messages` description'ına "önce read_summary'i dene" notu.
5. **Özet üreteci (B1):** `internal/summarizer/` paketi — `Summarize(cliType, messages) (string, error)`: mesajları sıralı transkripte çevir, CLI'yı one-shot (`-p`/`-i`) çağır, çıktıyı `sanitize` benzeri temizle. Hata/zaman aşımında boş döner (fallback: kullanıcı manuel).
6. **Desktop tetikleme:** `app.go`'ya `SummarizeRoom(room, cliType)` (üret + `writeSummary`), `ArchiveRoom(room)` (flush) ve `GetRoomSummary(room)`/`SetRoomSummary(room, md)` Wails-bound metotları. `DeleteTeam` ve `clear` akışlarında (kullanıcı onayıyla) bunları çağır.
7. **Enjeksiyon (C1):** `composeAgentPrompt`'ta `readSummary` çek; `ComposeStartupPrompt`'a `roomSummary` parametresi ekleyip join talimatından önce blok ekle; manager talimatını özet-temelli yap.
8. **Prompt güncellemeleri:** `base_prompt.md` / `manager_prompt.md` metinlerini güncelle (Türkçe + emoji konvansiyonu).
9. **Frontend:** "Özetle & Arşivle" butonu + özet paneli (görüntüle/düzenle/kaydet).
10. **Geri uyumluluk:** Özet/arşiv dosyaları yoksa tüm akışlar eski davranışa düşmeli (özet boşsa enjeksiyon atlanır, tool "henüz özet yok" döner).

## Açık Sorular / Karar Gerektiren Noktalar

1. **Özeti kim/ne üretecek? (en kritik)** — B1 (mevcut CLI one-shot), B2 (manager agent), B3 (manuel)? Öneri: B1 varsayılan + B3 fallback. Karar gerekiyor: hangi CLI varsayılan (takımın manager cliType'ı mı, kullanıcı seçimi mi)? Non-interactive bayraklar (`claude -p`, `gemini -p`, `copilot -i`) projede test edilmeli — bazıları oturum/onay isteyebilir.
2. **Otomatik mi, manuel mi tetikleme?** — `DeleteTeam`/`clear_room` içinde sessiz-otomatik özet, kullanıcının beklemediği bir gecikme/CLI süreci yaratır. Öncelik manuel buton + opsiyonel "kapanışta özetle" ayarı mı?
3. **Özet formatı/uzunluğu:** sabit şablon mu (kararlar, açık görevler, agent rolleri, son durum) yoksa serbest mi? Startup prompt'a sığması için bir karakter/satır üst sınırı (örn. ≤ 2-3 KB).
4. **Arşiv formatı:** JSONL append (A1) vs. zaman damgalı snapshot (A2)? A1 truncate kayıplarını da yakalar; tercih A1.
5. **Arşiv büyümesi/rotasyon:** `archive/{room}.jsonl` sınırsız büyür. Rotasyon/sıkıştırma gerekir mi, yoksa şimdilik sınırsız mı?
6. **Özet dili:** Agent-facing metinler Türkçe (CLAUDE.md konvansiyonu). Özet de Türkçe mi üretilecek, yoksa konuşma diline mi uyacak?
7. **Yetki:** `read_summary` join olmuş her agent'a açık mı, yoksa manager'a mı kısıtlı? (Öneri: her agent okuyabilir; yalnızca manager/desktop yazabilir.)
8. **Çoklu özet versiyonu:** Oda zamanla değişir; özet üstüne mi yazılır yoksa versiyonlanır mı (`summary/{room}-{epoch}.md`)?

## Doğrulama / Test

- **Birim (hub):** truncate sırasında düşen mesajların `archive/{room}.jsonl`'a tam yazıldığı; `readArchive` ile geri okunduğu; `Clear()`'ın önce arşivlediği. (`internal/hub/room_test.go` / yeni `persistence_test.go`.)
- **Birim (özet enjeksiyonu):** `ComposeStartupPrompt`'ın özet varken bloğu eklediği, yokken eski çıktıyı koruduğu (`internal/cli/startup_test.go` tarzı table-driven).
- **Birim (summarizer) — (B, ertelendi):** CLI çağrısı mock'lanarak (injectable exec func) çıktının temizlendiği; hata halinde boş+fallback; spawn env'inde `AGENT_CHAT_DATA_DIR`/`AGENT_CHAT_ROOM` **bulunmadığı**.
- **MCP/RPC — (B, ertelendi):** `read_summary`/`submit_summary` yetki kuralları (`protocol_test.go` paterni) — manager olmayan `submit_summary` yapamaz; özet yoksa "henüz özet yok".
- **Entegrasyon (manuel) — Faz-1 (A):** Bir odayı cap'e kadar doldur (`SendMessage` ile 500+ mesaj), truncate tetiklensin → düşen ~200 mesajın `archive/{room}.jsonl`'da tam ve sırasıyla bulunduğunu doğrula. `Clear()` çağır → temizlik öncesi mesajların da arşivde olduğunu doğrula. `read_archive` ile tam geçmişin (limit sınırı içinde) erişilebilir kaldığını doğrula. (Not: "read_all_messages tüm geçmişi çekmez" zaten kodda cap'li; bu test artık enjeksiyon/özet fazına ait.)
- **Regresyon:** Özet/arşiv dosyaları yokken mevcut akışların (join, send, read, clear) bozulmadığı.

## Tahmini Efor (S/M/L) — denetim sonrası bölünmüş

Tek "L" plan, denetimle 3 bağımsız alt-plana bölündü:

- **(A) ARŞİVLEME — S/M, HEMEN yapılacak.** 2-3 dosya (`room.go`, `persistence.go`, küçük dispatch + `read_archive`). Veri kaybını tek başına durdurur; `room-browser` (#11) öncesi sıra-5 önceliği. Diğerlerinden bağımsız sevk edilebilir.
- **(B) ÖZET ÜRETİMİ — M/L, ERTELENDİ.** `summarizer` paketi, CLI one-shot (izole env zorunlu), summary depolama/yetki, `submit_summary`/`read_summary`. İhtiyaç kanıtlanınca ayrı planda.
- **(C) STARTUP ENJEKSİYONU — S/M, ERTELENDİ.** `ComposeStartupPrompt` + `composeAgentPrompt` + prompt güncellemeleri. `room-intro-story` (#14) ile aynı enjeksiyon noktasında birleştiği için onunla koordineli yapılmalı.

Eski tahmin tüm kapsam için **L** idi; yalnızca Faz-1 (A) **S/M**.
