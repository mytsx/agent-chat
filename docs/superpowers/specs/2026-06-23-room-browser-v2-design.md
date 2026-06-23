# Room Browser v2 (#18) — Tasarım Spec'i

**Tarih:** 2026-06-23
**Issue:** [#18](https://github.com/mytsx/agent-chat/issues/18) — Room Browser v2: oda silme + orphan→takım içe aktarma + geçmiş-oda agent isimleri
**Önkoşul:** #11 (Room Browser v1, merged), #13 (arşiv, merged), #29 (session snapshot, merged), #17 (observer, merged)
**Branch:** `feat/room-browser-v2-18`

## Problem / Bağlam

Room Browser v1 (#11) MVP olarak sevk edilirken üç dilim bilinçli olarak ertelendi
(`docs/PLAN-room-browser.md` Açık Soru #1 ve #2). #18 bu üç dilimi tamamlar:

- **A — Oda silme:** Orphan (takıma bağlı olmayan) bir odanın `hub-state/{room}.json`
  dosyasını kaldırma. Plan, in-memory↔disk **desync riskini** uyardı: silinen oda canlı bir
  takımla çakışırsa persist döngüsü dosyayı geri yazar; `getOrCreateRoom` okuma anında odayı
  diriltir.
- **B — Orphan → takım içe aktarma:** Geçmiş bir odayı yeni bir takım olarak içe aktarıp
  konuşmayı takım bağlamında sürdürme.
- **C — Geçmiş-oda agent isimleri:** Persist `agents` map'i boş odalarda (stale-cleanup
  agent'ları silmiş) agent isimlerini join mesajlarından türetme. Plan uyardı: naif parse
  ayrılmış agent'ları üye gösterir; doğru çözüm join/leave'i "geçmişte bulunmuş" listesi
  olarak sunmaktır.

## Kapsam Kararları (kullanıcı ile kilitlendi)

| Karar | Seçim |
|---|---|
| **v2 kapsamı** | A + B + C (üçü de) |
| **A — Arşiv** | Korunur; yalnızca `hub-state/{room}.json` silinir. `archive/{room}.jsonl` **ve** `sessions/{room}/` (#29) korunur. |
| **B — Import agent kaynağı** | persist `agents` map → C-türetimi (`historical_agents`) fallback → boş takım |
| **C — Türetim katmanı** | Backend (`RoomSummary`'ye yeni alan, join/leave eşleştirme Go'da) |
| **Okuma-path sertleştirme** | Dahil (getOrCreateRoom→getRoom okuma handler'larında; phantom-room bug'ını da çözer) |

## Doğrulanmış Mevcut Durum (kod referansları, 2026-06-23)

Plan (2026-06-16) satır no'ları stale; aşağıdakiler güncel kodla doğrulandı.

### Hub çekirdeği
- `internal/hub/hub.go:294-305` — `getOrCreateRoom`: var-olmayan odayı **materialize eder**
  (revival side-effect). `internal/hub/hub.go:310-314` — `getRoom`: materialize-etmeyen
  variant (nil döndürür), zaten mevcut ama okuma handler'larında kullanılmıyor.
- `internal/hub/hub.go:24-34` — `Hub` struct: `rooms`, `subs`, `roomManager`, `roomObservers`
  map'leri + `mu sync.RWMutex`. **`deletedRooms` guard yok** (eklenecek).
- `internal/hub/persistence.go:59-93` — `persistLoop` (5sn) → `persistDirtyRooms`: oda
  adlarını RLock altında snapshot'lar, her biri için RLock altında re-check eder, sonra
  `persistRoom`'u **lock'suz** çağırır.
- `internal/hub/persistence.go:105-132` — `persistRoom`: atomik yazım (temp + rename),
  IO sırasında **lock tutmaz** → re-check ile rename arası mid-IO resurrection race penceresi.
- `internal/hub/protocol.go:934-947` — `handleGetAgents`: `getOrCreateRoom` kullanır (reviving).
- `internal/hub/protocol.go:949-987` — `handleGetMessagesRaw`: `getOrCreateRoom` kullanır.
- `internal/hub/protocol.go:166-198` — `handleSetObservers` (+ dispatch `:21-22`): yeni
  `delete_room` için **kopyalanacak desen**. `isDesktopAuthorized()` `:125-127`.
- `internal/hub/protocol.go:675-727` — `handleClearRoom`: mesajları arşivler + roster temizler
  ama **oda adını bırakır** (silme değil).
- `internal/hub/archive.go:75-78` — `archiveDir()` = `~/.agent-chat/hub-state/archive`;
  dosya `archive/{room}.jsonl`. **Hiçbir silme mekanizması yok** (yalnız append).

### Hub client / app / tip
- `internal/hubclient/client.go:277-289` — `SetObservers`: yeni `DeleteRoom` deseni.
- `app.go:1514-1529` — `CreateTeam`: `teamStore.Create` + hub `Subscribe` + `syncHubManager`.
- `app.go:265-290` — `subscribeExistingTeams`; `app.go:303-312` — `syncHubManager`.
- `app.go:1504-1506` — `ListTeams() []team.Team` → `teamStore.List()` (orphan kontrolü için).
- `internal/team/store.go:136-167` — `Create(name, gridLayout, agents)`: **duplicate ad
  kontrolü YOK** (yalnız append) → import için defansif guard eklenecek.
- `internal/validation/validate.go` — `ValidateName`: `^[a-zA-Z0-9._\- ]{1,50}$`, leading-dot
  ve `..` yasak. **Türkçe karakterler reddedilir** (import'ta bilinen kısıt).
- `internal/types/message.go:12-19` — `RoomSummary{Name, MessageCount, Agents, LastActivity,
  IsDefault}`. `:34-46` — `Message{ID, From, To, Content, Timestamp, Type, ...}`. `:24-29` —
  `MsgTypeSystem = "system"`. `:5-10` — `Agent{Role, JoinedAt, LastSeen}`.
- `internal/hub/room.go:107-110` — join mesajı: `"\U0001f7e2 %s odaya katıldı"` (+ `" (Rol: %s)"`).
  `internal/hub/room.go:346` — leave: `"\U0001f534 %s odadan ayrıldı"`. İkisi de `Type=system`.
- `internal/hub/room.go:554-573` — `Summary(name, isDefault)`; `:575-596` — `ListRoomSummaries`
  (LastActivity DESC). `:642-648` — `copyAgentsLocked`.

### Frontend (v1 mevcut)
- `frontend/src/components/RoomBrowser.tsx` — liste+detay; orphan ayrımı `teamNames.has(name)`
  (`:176`); detay görünümü `:88-161`.
- `frontend/src/store/useRooms.ts` — `rooms`, `loading`, `error`, `selectedRoom`, `loadRooms`,
  `selectRoom`.
- `frontend/src/lib/types.ts:65-71` — `RoomSummary` interface; `Team`/`AgentConfig` interface'leri.
- `frontend/wailsjs/go/main/App.d.ts` — `ListRooms`, `GetMessages`, `GetAgents`, `CreateTeam`,
  `ListTeams`, `DeleteTeam`, `SetTeamObserver` var; **`DeleteRoom` yok** (regen edilecek).

---

## Çözüm Tasarımı

Uygulama sırası: **C → B → A** (C, B'nin import fallback'ini besler; A en riskli, sona).

### DİLİM C — Geçmiş-oda agent isim türetimi

**Tip:** `internal/types/message.go` `RoomSummary`'ye alan eklenir:

```go
type RoomSummary struct {
    Name             string           `json:"name"`
    MessageCount     int              `json:"message_count"`
    Agents           map[string]Agent `json:"agents"`
    HistoricalAgents []string         `json:"historical_agents"` // geçmişte bulunmuş (join'lerden türetildi); roster boşsa dolu
    LastActivity     string           `json:"last_activity"`
    IsDefault        bool             `json:"is_default"`
}
```

**Türetim (`internal/hub/room.go`):** `Summary()` içinde **yalnızca `len(r.agents)==0`** ise
mesajlar taranır. Yeni yardımcı `parseHistoricalAgentsLocked(messages) []string`:
- `m.Type == MsgTypeSystem` olan mesajlarda join prefix'i (`"\U0001f7e2 "`) + infix
  (`" odaya katıldı"`) eşleşmesi → aradan agent adını çıkar (opsiyonel ` (Rol: …)` ekini at).
- **Distinct** join adları, sıralı (`sort.Strings`). Leave mesajları yok sayılır (yalnız
  join'den türetiriz; "geçmişte bulunmuş" tam buna karşılık gelir).
- Roster doluysa `HistoricalAgents = nil` (mevcut roster yeterli).

**Plan caveat'ının çözümü:** Bu liste "current member" değil — alan adı (`historical_agents`)
ve frontend label ("geçmişte bulunmuş agent'lar") açıkça ayırır. "Ayrılmış agent üye görünür"
sorunu doğru etiketleme ile çözülür; kullanıcının asıl sorusu ("hangi adda agent oluşturmuşum")
tam bu listeyle yanıtlanır.

**Frontend:** `RoomBrowser` listesinde roster boş olan odalarda, mevcut "agent kaydı yok"
nötr etiketinin yerine (veya yanında) `historical_agents` rozetleri "geçmişte bulunmuş"
başlığıyla gösterilir. `types.ts` `RoomSummary`'ye `historical_agents: string[]` eklenir.

### DİLİM B — Orphan → takım içe aktarma

**Backend yeni binding gerekmez** — mevcut `CreateTeam(room, layout, agents)` yeniden
kullanılır. Takım adı = oda adı olduğundan yeni takım **mevcut orphan odaya subscribe olur →
geçmiş otomatik görünür**.

**Frontend import diyaloğu** (RoomBrowser detayında "Takıma Aktar" aksiyonu, yalnız orphan odada):
1. **Agent ön-doldurma (fallback zinciri):** `summary.agents` doluysa `[{name, role}, …]` →
   boşsa `summary.historical_agents` doluysa `[{name}, …]` → o da boşsa boş liste.
2. Kullanıcı her agent'a **CLIType atar** (claude/gemini/copilot — terminal spawn için şart;
   türetilen config'lerde yok), ad/rol düzenler/siler; `SlotIndex` sıralı atanır.
3. **Takım adı = oda adı**, `ValidateName`'den geçmeli. Oda adı Türkçe karakter içeriyorsa
   import **bloklanır** (süreklilik takım adı=oda adı gerektirir; ASCII-only kısıt) → net
   Türkçe hata ("Oda adı geçersiz karakter içeriyor; içe aktarılamaz").
4. Onayda → `CreateTeam(room, defaultGridLayout, agents)`.

**Backend sertleştirme (`internal/team/store.go`):** `Create`'e **duplicate-ad guard'ı**
eklenir (orphan zaten çakışmaz ama doğruluk + import güvenliği için): aynı adda takım varsa
hata döndür. Bu, mevcut çağrıları bozmayan defansif bir iyileştirme.

### DİLİM A — Oda silme (orphan-only, güvenli)

**Üç katmanlı güvenlik:**

1. **Okuma-path sertleştirme** (`internal/hub/protocol.go`): `handleGetAgents` ve
   `handleGetMessagesRaw` → `getOrCreateRoom` yerine `getRoom`; nil ise boş roster/mesaj
   döndür. Sonuç: **okuma artık oda diriltmez**; ayrıca var-olmayan oda okuması diske phantom
   dosya yazmaz (plan-flagged side-effect çözülür).

2. **Tombstone guard** (`internal/hub/hub.go`): `deletedRooms map[string]bool` (h.mu altında).
   - `persistRoom` (`persistence.go`): yazımdan önce tombstone kontrolü → tombstoned odayı
     **atla** (mid-IO resurrection race'ini kapatır).
   - `getOrCreateRoom`: create-branch'inde `delete(h.deletedRooms, room)` — **meşru recreation
     tombstone'u kaldırır** (yeni takım aynı adla kurulabilir). Okuma artık getRoom kullandığı
     için stray okuma bu branch'i tetiklemez; yalnız yazma (join/send) tombstone'u temizler.

3. **`delete_room` RPC** (`internal/hub/protocol.go`, `set_observers` deseni):
   - `isDesktopAuthorized()` gate.
   - `room == defaultRoom` → reddet.
   - **Savunma:** odanın canlı agent'ı (in-memory roster non-empty) veya aktif subscriber'ı
     (`h.subs[room]`) varsa reddet (takım odasını ek güvenlikle yakalar).
   - `h.mu.Lock()` altında: `deletedRooms[room]=true`; `rooms`, `subs`, `roomManager`,
     `roomObservers` (+ varsa session/observer config map'leri) içinden sil.
   - `hub-state/{room}.json` (ve `.json.tmp`) sil. **`archive/{room}.jsonl` ve `sessions/{room}/`
     korunur.**
   - `room_deleted` event broadcast.

**Otoriter orphan kontrolü (`app.go`):** Hub takımları bilmez. Yeni binding `DeleteRoom(room
string) error`: `teamStore.List()`'te ad eşleşirse reddet ("Bu oda bir takıma bağlı; önce
takımı silin"); default oda reddi; sonra `hubClient.DeleteRoom(room)`. Yeni client wrapper
`hubclient.DeleteRoom(room)` (SetObservers deseni).

**Frontend:** `RoomBrowser` listesinde **yalnız orphan** odalara 🗑 butonu → onay diyaloğu →
`DeleteRoom` → başarıda `loadRooms()` + açık detay seçimi temizlenir. `useRooms`'a `deleteRoom`
aksiyonu. Takım odalarında buton yok (mevcut takım-silme akışı).

---

## Etkilenen / Yeni Dosyalar

| Dosya | Değişiklik | Dilim |
|---|---|---|
| `internal/types/message.go` | `RoomSummary`'ye `HistoricalAgents []string` | C |
| `internal/hub/room.go` | `Summary()` historical türetimi + `parseHistoricalAgentsLocked` | C |
| `internal/hub/hub.go` | `deletedRooms` guard; `getOrCreateRoom` tombstone-clear | A |
| `internal/hub/persistence.go` | `persistRoom` tombstone-skip | A |
| `internal/hub/protocol.go` | `delete_room` dispatch+handler; read-path `getRoom` | A |
| `internal/hubclient/client.go` | `DeleteRoom(room) error` | A |
| `app.go` | `DeleteRoom` binding + orphan/default kontrolü | A |
| `internal/team/store.go` | `Create` duplicate-ad guard | B |
| `frontend/src/lib/types.ts` | `RoomSummary.historical_agents: string[]` | C |
| `frontend/src/store/useRooms.ts` | `deleteRoom` aksiyonu | A |
| `frontend/src/components/RoomBrowser.tsx` | historical rozetleri; 🗑 + onay; "Takıma Aktar" diyaloğu | A/B/C |
| `frontend/wailsjs/...` | Wails regen (`DeleteRoom` + `RoomSummary` modeli) | — |

## Test Stratejisi (TDD, table-driven `t.Run`)

- **`internal/hub/room_test.go`:** `parseHistoricalAgentsLocked` / `Summary` historical türetimi
  — join+leave karışık, tekrarlı join, rol'lü/rol'süz, yalnız-leave gürültüsü, roster doluyken
  nil; distinct + sıralı.
- **`internal/hub/protocol_test.go`:** `handleDeleteRoom` — desktop-auth olmayan red; default
  oda red; canlı-agent/subscriber red; başarılı silmede `rooms`/`subs`/`roomManager`/
  `roomObservers`'tan kalkıyor + dosya siliniyor + arşiv/sessions duruyor.
- **Revival regresyonu:** delete sonrası `handleGetMessagesRaw`/`handleGetAgents` odayı
  **diriltmiyor** (getRoom → boş, dosya yeniden oluşmuyor); persist döngüsü tombstoned odayı
  yazmıyor; `getOrCreateRoom` ile meşru recreation tombstone'u kaldırıyor.
- **`internal/team/store_test.go`:** duplicate-ad `Create` reddi.
- **Manuel (uçtan uca, `make dev`):** orphan sil → dirilmiyor, listeden düşüyor; takım odası
  silinemez (buton yok / app.go red); orphan→import sonrası yeni takım odasında geçmiş görünür;
  geçmiş-oda agent isimleri "geçmişte bulunmuş" etiketiyle; Türkçe-adlı oda import'u net hata.

## Riskler / Notlar

- **Mid-IO persist race** tombstone ile kapatılır; tombstone meşru recreation'da temizlenir
  (getOrCreateRoom create-branch). Tombstone map'i silme adlarıyla büyür ama silme manuel/seyrek
  bir kullanıcı aksiyonu — kabul edilebilir; recreation'da temizlenir.
- **Türkçe oda adı kısıtı (B):** süreklilik takım adı=oda adı gerektirdiğinden Türkçe-adlı
  orphan oda import edilemez. Bilinen kısıt; net hata ile iletilir.
- **Okuma-path değişikliği** mevcut davranışı etkiler: var-olmayan oda okuması artık phantom
  dosya yazmaz (istenen düzeltme). Mevcut testlerde regresyon kontrol edilir.
- **MCP `list_rooms` (agent-facing metin)** değişmez → agent davranışı aynı.

## Build & Doğrulama

```bash
make mcp-server          # binary embed şart (CLAUDE.md)
go test ./...            # yeni + regresyon testleri
make dev                 # localhost:34115 görsel uçtan uca
```

Go modül adı `desktop`. Agent-facing metinler Türkçe + emoji.
