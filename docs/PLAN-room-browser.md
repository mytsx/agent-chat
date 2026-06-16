# Oda Tarayıcısı (Room Browser)

## Problem / Bağlam

Kullanıcının kendi sözleriyle: *"daha önce oluşturduğum odaları göremiyorum. ikinci o odada hangi adda agent'lar oluşturmuşum göremiyorum."*

## Denetim Revizyonu (2026-06-16, kod-doğrulamalı)

**Verdict:** `minor-revisions` — plan sağlam, ama disk gerçeğiyle hizalanması ve MVP kapsamının daraltılması gerekti. Aşağıdaki maddeler çok-ajanlı kod denetiminden çıkan ve diske/koda karşı **doğrulanmış** bulgulardır.

**Uygulanan düzeltmeler:**
- **Sayım düzeltmesi (doğrulandı).** Diskte (`~/.agent-chat/hub-state/`) **33 oda**, toplam **3538 mesaj** var. Bunların **13'ü** `agents` map'i boş (0-agent), **20'si** dolu. Plan önceden "10 oda boş agents" diyordu — yanlış; düzeltildi. Ayrıca `Fatura`, `backend`, `takimc` hem **0 mesaj hem 0 agent** (tamamen boş kabuk odalar).
- **`RoomSummary` tipinin yeri (doğrulandı).** Tip `internal/hub` yerine **`internal/types`** içinde tanımlanmalı. Gerekçe: `Message`/`Agent` zaten `internal/types/message.go`'da; Wails binding'i `[]types.RoomSummary` döndürürse `models.ts`'e `internal/hub` namespace'i sızmaz, sınır temiz geçilir. Step-4 ile dosya tablosundaki `hub.RoomSummary` vs `RoomSummary` tutarsızlığı bu yönde çözüldü.
- **MVP daraltma — join-mesajı parse (Açık Soru #2) çıkarıldı.** 0-agent odalar diskte çok sayıda tekrarlı `🟢 ... odaya katıldı` (`room.go:84`) + `🔴 ... odadan ayrıldı` (`room.go:233`) mesajı içeriyor; naif parse zaten **ayrılmış** agent'ları üye gibi gösterir. Onun yerine: persist agents varsa göster, yoksa nötr *"agent kaydı yok (geçmiş odası)"* etiketi; kullanıcı odayı açınca gerçek geçmişi zaten görür.
- **SİLME (Açık Soru #1) MVP dışına alındı.** Eklenirse oda adının canlı/subscribe bir takımla çakışması in-memory ↔ disk desync riski doğurur (aşağıda analiz edildi).
- **Paylaşılan-store netleştirmesi.** `useMessages` oda-adına (`chatDir`) göre key'li; "salt-okuma" çerçevesi yanıltıcı, açıkça yazıldı (aşağıda).
- **`getOrCreateRoom` yan etkisi ve "son 300" etiketi** notları eklendi.

**Kapsam / böl-birleştir kararları:**
- MVP = salt-listeleme + salt-okuma detay. Silme, içe-aktarma ve join-parse **kapsam dışı** (ayrı, sonraki iş).
- Açık Soru #3 (last_activity = son mesaj timestamp'i) ve #4 (orphan ayrımı frontend'de) plandaki öneriyle bırakıldı; #5 (desktop-authorized) onaylandı.

**Çapraz-analiz / bağımlılık notu:**
- **BAĞIMLILIK:** Room Browser, **arşivleme (#13, Faz-1) TAMAMLANDIKTAN SONRA** gelmeli. Aksi halde 500→300 truncate ile (`room.go:135-136`) diskten düşen mesajlar tarayıcıda da kayıp görünür; arşivleme bu mesajları korur. Bu yüzden bu özellik **önerilen sırada 6. sıraya** yerleştirildi.

Mevcut durumda frontend yalnızca **takımlara** (`teams.json`) bağlı odaları gösterebiliyor. Oysa hub diskte çok daha fazla oda tutuyor:

- `~/.agent-chat/hub-state/` altında **33 oda** kayıtlı, toplam **3538 mesaj** (doğrulandı).
- Bu odaların **13 tanesinde** `agents` map'i boş ama (çoğunda) mesaj geçmişi dolu (örn. `ExportGeo` = 415 mesaj, 0 agent). Sebep: stale-agent cleanup (5dk idle) agent'ları siler, mesajlar kalır.
- **20 odada** persist edilmiş `agents` map'i hâlâ dolu (örn. `GeoTeam` = `Coder1`, `Coder2`).
- **3 oda (`Fatura`, `backend`, `takimc`) hem 0 mesaj hem 0 agent** — tamamen boş kabuk. Bunlar muhtemelen `getOrCreateRoom` yan etkisiyle oluşmuş boş odalar (aşağıya bkz.); listede gösterimi "boş oda" rozetiyle yapılmalı.
- Bu odaların büyük kısmı artık bir takıma karşılık gelmiyor ("orphan" odalar). Takım silinse bile `hub-state/{room}.json` diskte kalıyor.

> **Not — `getOrCreateRoom` yan etkisi:** `internal/hub/hub.go:190` mevcut olmayan bir oda adı sorgulandığında odayı **oluşturur**. `handleGetMessagesRaw`/`handleGetAgents` (`protocol.go:636,651`) bu fonksiyonu çağırdığından, var olmayan bir oda adıyla okuma yapmak diske boş bir oda yazılmasına yol açar. Room Browser yalnızca **mevcut** oda adlarıyla (liste sonucundan gelen) okuma yapmalı; rastgele/elle oda adı sorgulanmamalı.

Frontend'de bu geçmiş odaları listeleyen, içlerindeki agent'ları ve mesaj geçmişini gösteren **hiçbir UI yok**. Backend'de `list_rooms` MCP tool'u var ama yalnızca agent'a-dönük metin döndürüyor; desktop binding'i yok.

**Hedef:** Kullanıcı tüm geçmiş odaları (takıma bağlı olsun olmasın) bir UI'da görebilsin — oda adı, mesaj sayısı, son aktivite zamanı, içindeki agent isimleri/rolleri. Bir odaya tıklayınca o odanın geçmiş mesajlarını okuyabilsin.

---

## Mevcut Durum (file:line referansları)

### Backend — oda state ve listeleme

- **Oda state struct'ı:** `internal/hub/room.go:22-29` — `RoomState` (messages, agents, manager bilgileri). In-memory.
- **Persist edilen biçim:** `internal/hub/room.go:40-43` — `PersistedRoom{ Messages, Agents }`. Diske bu yazılıyor; `last_activity` gibi bir alan **yok**.
- **`Info()`:** `internal/hub/room.go:362-366` — yalnızca `(agentCount, messageCount)` döndürür. Zaman damgası **yok**.
- **`RoomInfo` + `ListRoomInfos()`:** `internal/hub/room.go:369-386` — `{Name, Agents, Messages}` döndürür, ada göre sıralı. **Agent isimleri ve son aktivite bilgisi yok.**
- **`GetAgents()` / `GetMessages()`:** `internal/hub/room.go:319-333` — bir odanın ham agent map'ini ve mesaj listesini (cleanup yapmadan) döndürür. **Bunlar oda bazında zaten mevcut.**

### Backend — disk persistence

- **Yükleme:** `internal/hub/persistence.go:13-56` — `loadPersistedState()` startup'ta `hub-state/*.json` dosyalarını okuyup `h.rooms[roomName]`'e yükler. Yani **33 odanın tamamı startup'ta in-memory'ye gelir.**
- **Sonuç:** `ListRoomInfos(h.rooms)` zaten 33 odayı kapsar; orphan odalar dahil. Eksik olan tek şey **son aktivite zamanı** ve **agent isimleri** metadata'sı.
- **Persist döngüsü:** `internal/hub/persistence.go:74-131` — dirty odalar 5sn'de bir atomik yazılır.

### Backend — hub protocol (RPC dispatch)

- **Request dispatcher:** `internal/hub/protocol.go:14-47` — `handleRequest`. `list_rooms`, `get_agents`, `get_messages_raw` zaten dispatch ediliyor.
- **`handleListRooms`:** `internal/hub/protocol.go:658-682` — `ListRoomInfos` çağırıp **metin** (`text`) döndürür. Frontend için yapılandırılmış JSON döndürmüyor.
- **`handleGetMessagesRaw`:** `internal/hub/protocol.go:644-656` — `room` parametresiyle ham mesajları döndürür, **ama** `c.isDesktopAuthorized()` gerektirir (satır 645). Desktop client zaten authorize (bkz. `app.go:178`).
- **`handleGetAgents`:** `internal/hub/protocol.go:629-641` — aynı şekilde desktop-authorized, `room` parametresiyle ham agent map'i döndürür.

### Backend — hub client (RPC sarmalayıcıları)

- **`ListRooms()`:** `internal/hubclient/client.go:328-330` — metin yanıtı döndürür.
- **`GetAgentsRaw(room)` / `GetMessagesRaw(room)`:** `internal/hubclient/client.go:333-360` — yapılandırılmış `map[string]types.Agent` ve `[]types.Message` döndürür. **Yeniden kullanılabilir.**

### Backend — MCP tool (agent-facing)

- **`listRooms` handler:** `internal/mcpserver/tools.go:276-289` — `storage.ListRooms()` → metin. (Bu agent içindir, UI için değiştirilmeyecek.)
- **`Storage.ListRooms`:** `internal/mcpserver/storage.go:71-73`.

### Backend — Wails binding'leri (`app.go`)

- **`GetMessages(room)`:** `app.go:937-947` — `hubClient.GetMessagesRaw` sarmalar. **Mevcut binding, oda parametreli.**
- **`GetAgents(room)`:** `app.go:949-960` — `hubClient.GetAgentsRaw` sarmalar. **Mevcut binding, oda parametreli.**
- **`WatchChatDir(room)`:** `app.go:962-968` — odaya subscribe olur.
- **Oda listeleme binding'i YOK.** `ListRooms` Wails'e expose edilmemiş.
- Hub client başlatma: `app.go:160-186` (`connectToHub`), desktop olarak identify: `app.go:178`.

### Frontend — durum

- **Wails binding decl'leri:** `frontend/wailsjs/go/main/App.d.ts:22,28` — `GetAgents(room)`, `GetMessages(room)` zaten var. `ListRooms` **yok** (generate edilmeli).
- **TS tipleri:** `frontend/src/lib/types.ts` — `Team`, `Message`, `Agent` mevcut. **`RoomSummary` benzeri bir oda-özeti tipi yok.**
- **Mesaj store'u:** `frontend/src/store/useMessages.ts:44-64` — `loadMessages(chatDir)` / `loadAgents(chatDir)` herhangi bir oda adıyla mesaj/agent çekebiliyor. **Oda tarayıcı bunu yeniden kullanabilir.** Oda listesi için `useRooms` store'u **yok**.
- **Sidebar:** `frontend/src/components/Sidebar.tsx:11-47` — sabit 3 sekme: `status | messages | prompts`. Tek `chatDir` prop'una bağlı (aktif takım odası).
- **MessageFeed:** `frontend/src/components/MessageFeed.tsx:8-70` — bir `chatDir` alıp o odanın mesajlarını render eder. Yeniden kullanılabilir.
- **App kabuğu:** `frontend/src/App.tsx:127-176` — `activeTeam.name` → `chatDir`. Sidebar bu tek odaya kilitli.
- **TabBar:** `frontend/src/components/TabBar.tsx` — yalnızca takımları sekme olarak gösterir; orphan odalar görünmez.

### Özet: neyin eksik olduğu

| Katman | Var | Eksik |
|--------|-----|-------|
| Disk | 33 oda + agents + messages | `last_activity` metadata'sı |
| Hub | `ListRoomInfos` (name/agents/msgs), `GetAgents(room)`, `GetMessagesRaw(room)` | Yapılandırılmış oda listesi (agent isimleri + son aktivite ile) |
| HubClient | `GetAgentsRaw`, `GetMessagesRaw`, `ListRooms`(text) | `ListRooms` yapılandırılmış |
| app.go binding | `GetMessages`, `GetAgents` | `ListRooms` binding'i |
| Frontend | `useMessages`, `MessageFeed` | `useRooms` store, `RoomBrowser` bileşeni, `RoomSummary` tipi |

---

## Çözüm Tasarımı

### Genel yaklaşım

Mesaj/agent okuma altyapısı **zaten oda-parametreli** ve desktop-authorized (`GetMessages(room)`, `GetAgents(room)` `app.go`'da hazır). Bu yüzden temel iş **(1)** yapılandırılmış bir oda listesi RPC'si eklemek ve **(2)** frontend'e bir tarayıcı UI'ı koymaktır. Tek bir odayı açmak için mevcut binding'ler yeniden kullanılır.

### Backend

Yeni bir hub RPC tipi: **`list_rooms_detailed`** (mevcut metin-tabanlı `list_rooms`'u bozmamak için ayrı). Her oda için yapılandırılmış metadata döndürür:

```go
// internal/types/message.go yanında (Message/Agent ile aynı paket)
type RoomSummary struct {
    Name         string                 `json:"name"`
    MessageCount int                    `json:"message_count"`
    Agents       map[string]Agent       `json:"agents"`        // persist edilmiş agent isimleri + rolleri
    LastActivity string                 `json:"last_activity"` // son mesaj timestamp'i (ISO)
    IsDefault    bool                   `json:"is_default"`
}
```

> **Tip yeri (denetim):** `RoomSummary`, `internal/hub` değil **`internal/types`** içinde tanımlanır. `Message`/`Agent` zaten orada; Wails binding'i `[]types.RoomSummary` döndürdüğünde `models.ts`'e `internal/hub` namespace'i sızmaz. `ListRoomSummaries` fonksiyonu `internal/hub`'da kalabilir ama `[]types.RoomSummary` döndürür.

`LastActivity`'yi türetmenin en ucuz yolu: ek alan persist etmeden, mevcut `messages` dizisinin **son elemanının `Timestamp`'ı** (boşsa `""`). Bu sayede `PersistedRoom` şeması ve disk formatı **değişmez** (geriye dönük uyumlu).

> **"Son 300" etiketi (denetim):** `MessageCount`, in-memory mesaj listesinin uzunluğudur ve **bellek-cap'ini** yansıtır, lifetime'ı değil. Oda 500 mesajı aşınca `room.go:135-136` listeyi son 300'e kırpar (`maxMessagesInRoom=500`, `truncateToMessages=300`). UI'da bu sayı "odanın toplam ömür-boyu mesajı" olarak sunulmamalı; "son ~300 mesaj" gibi etiketlenmeli. Truncate edilen mesajları korumak için **arşivleme (#13) önkoşuldur** (bkz. yukarıdaki bağımlılık notu).

`Agents` alanı **persist edilmiş** map'ten gelir (cleanup'sız `GetAgents`/snapshot), böylece "hangi adda agent oluşturmuşum" sorusu 20 odada doğrudan yanıtlanır. Kalan 13 odada agent map'i boştur.

> **MVP kararı — join-parse YOK (denetim):** Boş-agent odalarda agent isimlerini system join mesajlarından (`"🟢 X odaya katıldı"`, `room.go:84`) parse etmek **MVP'den çıkarıldı**. Bu odalar çok sayıda tekrarlı join + `🔴 X odadan ayrıldı` (`room.go:233`) mesajı içerir; naif parse zaten ayrılmış agent'ları üye gibi gösterir. MVP'de bu odalar nötr **"agent kaydı yok (geçmiş odası)"** etiketi alır; kullanıcı odayı açınca mesaj geçmişinde gerçek katılım/ayrılış kayıtlarını görür.

Yeni method: `room.go` içinde `Summary(name string, isDefault bool) RoomSummary` — kilidi alıp son mesaj timestamp'ini, agent snapshot'ını ve mesaj sayısını okur. `ListRoomSummaries(rooms, defaultRoom)` (mevcut `ListRoomInfos`'un yanına) ada göre değil, **son aktiviteye göre azalan** sıralar (en yeni oda üstte) — UX için daha iyi.

### Frontend

Sidebar'a yeni bir sekme: **"Rooms"** (`status | messages | prompts | rooms`). Yeni bileşen `RoomBrowser.tsx`:

1. Mount'ta `ListRooms()` (yeni binding) çağırır → `RoomSummary[]`.
2. Liste görünümü: her satır oda adı, mesaj sayısı, son aktivite ("3 gün önce"), agent isimleri rozetleri (`Coder1`, `Coder2`...). Aktif takım odaları işaretlenir; orphan odalar "kayıtlı takım yok" etiketi alır.
3. Bir odaya tıklanınca **detay görünümü**: `GetAgents(room)` + `GetMessages(room)` (mevcut binding'ler, `useMessages` store) ile o odanın agent listesi + mesaj geçmişi gösterilir (`MessageFeed` yeniden kullanılır; UI'da gönderme/eylem kontrolü yok).
4. "Geri" ile listeye dönülür.

Yeni store `useRooms.ts`: `rooms: RoomSummary[]`, `loadRooms()`, `selectedRoom: string | null`. Mesaj/agent yükleme için **mevcut `useMessages` store'u yeniden kullanılır** (yeni store gerekmez).

> **Paylaşılan-store / "salt-okuma" netleştirmesi (denetim):** `useMessages` store'u **oda adına (`chatDir`) göre key'li** (`useMessages.ts:10`, `messages: Record<string, Message[]>`). `messages:new` event handler'ı (`App.tsx:83-86`) gelen mesajı koşulsuz olarak `addMessages(data.chatDir, ...)` ile aynı kovaya yazar. Sonuç:
> - **Aktif takım odasını** Room Browser'da açmak **CANLI**'dır — aynı store key, gelen `messages:new` event'leri aynı kovaya düşer, detay görünümü kendiliğinden güncellenir.
> - **Orphan odalar STATİK**'tir — desktop yalnızca takım odalarına subscribe olur (`app.go:234` `subscribeExistingTeams`), dolayısıyla bu odalar için event akmaz.
>
> Bu yüzden detayı "tamamen salt-okuma/donmuş" diye çerçevelemek **yanıltıcı**. UI, açılan odanın canlı (subscribe'lı takım) mı yoksa statik (orphan) mı olduğunu kullanıcıya ayırt ettirmeli; canlı odada akan mesajların beklenir olduğu açık olmalı.

### Alternatifler (ve neden seçilmedi)

- **A) Sadece metin `list_rooms`'u parse et.** Frontend metni parse etmek kırılgan; agent isimleri ve makine-okunur timestamp yok. Reddedildi.
- **B) Orphan odaları otomatik takıma çevir.** İstem dışı yan etki, çok fazla "takım" oluşturur, kullanıcı sadece *görmek/okumak* istiyor. Reddedildi.
- **C) TabBar'a orphan odaları da ekle.** TabBar takım-merkezli (silme/oluşturma takım işlemleri çağırır). Geçmiş gezintisi için ayrı bir tarayıcı daha temiz. Reddedildi.
- **D (seçildi)) Sidebar'da ayrı "Rooms" sekmesi + okuma-odaklı detay.** Mevcut altyapıyı (oda-parametreli binding'ler, `MessageFeed`) maksimum yeniden kullanır; takım modelini bozmaz. (Not: aktif takım odası açıldığında detay **canlı** akar — bkz. paylaşılan-store netleştirmesi.)

---

## Etkilenen / Yeni Dosyalar

| Dosya | Değişiklik |
|-------|-----------|
| `internal/types/message.go` | **Yeni** `RoomSummary` tipi (`Message`/`Agent` ile aynı pakette — Wails sınırı temiz). |
| `internal/hub/room.go` | **Yeni** `Summary()` method; `ListRoomSummaries() []types.RoomSummary` fonksiyonu (son aktiviteye göre sıralı). Mevcut `RoomInfo`/`ListRoomInfos` korunur. |
| `internal/hub/protocol.go` | **Yeni** `list_rooms_detailed` case + `handleListRoomsDetailed` (desktop-authorized, yapılandırılmış JSON döndürür). |
| `internal/hubclient/client.go` | **Yeni** `ListRoomsDetailed() ([]types.RoomSummary, error)` — JSON unmarshal eden sarmalayıcı. |
| `app.go` | **Yeni** Wails binding `ListRooms() []types.RoomSummary` — `hubClient.ListRoomsDetailed()` sarmalar. |
| `frontend/wailsjs/go/main/App.d.ts` + `App.js` + `models.ts` | Wails `generate` ile yeniden üretilir (yeni binding + `RoomSummary` modeli). |
| `frontend/src/lib/types.ts` | **Yeni** `RoomSummary` interface'i. |
| `frontend/src/store/useRooms.ts` | **Yeni** Zustand store: `rooms`, `loadRooms`, `selectedRoom`. |
| `frontend/src/components/RoomBrowser.tsx` | **Yeni** bileşen: oda listesi + detay (agents + `MessageFeed`). |
| `frontend/src/components/Sidebar.tsx` | "Rooms" sekmesi eklenir (`SidebarTab` union + buton + render). |
| `frontend/src/styles/globals.css` | Oda listesi / agent rozetleri / "X gün önce" stilleri. |
| `internal/hub/protocol_test.go` | `handleListRoomsDetailed` için test (opsiyonel ama önerilir). |

---

## Adım Adım İmplementasyon

1. **`internal/types/message.go`:** `RoomSummary` struct'ını ekle (`Message`/`Agent` ile aynı pakette). **`internal/hub/room.go`:** `Summary(name string, isDefault bool) types.RoomSummary` method'unu yaz — `r.mu.RLock()`, `len(r.messages)`, son mesajın `Timestamp`'i, `copyAgentsLocked()`. Ardından `ListRoomSummaries(rooms map[string]*RoomState, defaultRoom string) []types.RoomSummary` — `LastActivity` azalan (boşlar en sonda) sırala. (Boş-agent odalar için join-mesajı parse'ı **yapma** — MVP dışı; agents map'i boş döner.)

2. **`internal/hub/protocol.go`:** `handleRequest` switch'ine `case "list_rooms_detailed": h.handleListRoomsDetailed(c, req)` ekle. Handler: `c.isDesktopAuthorized()` kontrolü (orphan odaların tamamı desktop'a açık olmalı), `h.mu.RLock()` altında `ListRoomSummaries(h.rooms, h.defaultRoom)`, sonuç `{"rooms": [...]}` olarak JSON döndür.

3. **`internal/hubclient/client.go`:** `ListRoomsDetailed() ([]types.RoomSummary, error)` ekle — `Send({Type:"list_rooms_detailed"})`, `resp.Data`'yı `{rooms: []types.RoomSummary}` olarak unmarshal et, hata yönetimi `GetMessagesRaw` (satır 347-360) deseniyle aynı.

4. **`app.go`:** `ListRooms() []types.RoomSummary` binding'ini ekle (`GetMessages` deseni, `app.go:937-947`). `a.hubClient == nil` guard'ı koy. (Tip `internal/types`'ta olduğundan `models.ts`'e temiz yansır; `internal/hub` namespace'i sızmaz.)

5. **Wails binding üret:** `make dev` veya `wails generate module` ile `App.d.ts`/`App.js`/`models.ts` yenilensin. (CLAUDE.md: önce `make mcp-server`, MCP binary embed constraint'i.)

6. **`frontend/src/lib/types.ts`:** `RoomSummary` interface'ini ekle (Go struct'ını birebir yansıt: `name`, `message_count`, `agents: Record<string, Agent>`, `last_activity`, `is_default`).

7. **`frontend/src/store/useRooms.ts`:** Zustand store. `loadRooms()` → `ListRooms()`. `selectedRoom` state'i. Mesaj/agent için **mevcut `useMessages`** store'unun `loadMessages`/`loadAgents`'ını kullan.

8. **`frontend/src/components/RoomBrowser.tsx`:** İki mod — (a) liste: `useRooms` ile satırlar, agent isim rozetleri, "X gün önce" (last_activity), aktif-takım/orphan ayrımı (takım adlarıyla karşılaştır: `useTeams`). Boş-agent odalarda agent rozeti yerine **"agent kaydı yok (geçmiş odası)"** nötr etiketi; tamamen boş odalarda (`Fatura`/`backend`/`takimc`) **"boş oda"** rozeti. Mesaj sayısı "son ~300" olarak etiketlenir (cap notu). (b) detay: seçili odada `loadMessages(room)`+`loadAgents(room)` çağır, agent listesini ve `<MessageFeed chatDir={room} />`'ü göster, "Geri" butonu. Aktif takım odası açıldığında akış **canlı**'dır (aynı `useMessages` key'i + `messages:new`); orphan oda statiktir — UI bunu görsel olarak belli etmeli.

9. **`frontend/src/components/Sidebar.tsx`:** `SidebarTab` union'a `"rooms"` ekle, buton ekle, `{activeTab === "rooms" && <RoomBrowser />}` render et.

10. **`frontend/src/styles/globals.css`:** Liste satırı, agent rozeti, son-aktivite, hover/seçim ve "geri" stilleri.

11. **Test/Build:** `go test ./...`; `make dev` ile uçtan uca: orphan oda (örn. `ExportGeo`, 415 mesaj/0 agent) listede görünüyor mu, tıklayınca mesajlar yükleniyor mu, agent isimleri olan oda (`GeoTeam` → `Coder1/Coder2`) rozetleri gösteriyor mu doğrula.

---

## Açık Sorular / Karar Gerektiren Noktalar

1. **Silme — MVP DIŞI (denetim kararı).** "Bu odayı yeni takım olarak içe aktar" ve "odayı sil" (`hub-state/{room}.json` dosyasını kaldır) **kapsam dışı** bırakıldı. Eklenecekse şu **desync riski** analiz edilmeli: silinen oda adı **canlı/subscribe bir takımla çakışırsa**, in-memory `h.rooms[room]` hâlâ yaşıyorken disk dosyası silinir; bir sonraki persist döngüsü (`persistence.go`, 5sn) dosyayı **yeniden yazar** → silme etkisiz/yanıltıcı. Ayrıca `getOrCreateRoom` (`hub.go:190`) okuma anında odayı yeniden canlandırabilir. Güvenli silme; (a) önce in-memory `h.rooms`'tan kaldırmayı, (b) o oda adına ait subscribe/persist'i durdurmayı, (c) yalnızca **orphan** (hiçbir takıma karşılık gelmeyen) odalara izin vermeyi gerektirir. Bu, ayrı bir iş olarak ele alınmalı.

2. **0-agent odalarında agent isimleri — MVP DIŞI (denetim kararı).** Persist `agents` boşken agent isimlerini system join mesajlarından parse etmek **kapsam dışı**. Gerekçe: bu odalar çok sayıda tekrarlı `🟢 ... katıldı` (`room.go:84`) + `🔴 ... ayrıldı` (`room.go:233`) içerir; naif parse ayrılmış agent'ları üye gibi gösterir. MVP'de nötr **"agent kaydı yok (geçmiş odası)"** etiketi yeterli; kullanıcı odayı açınca gerçek katılım/ayrılış geçmişini mesajlarda görür. İleride doğru çözüm: join/leave çiftlerini eşleştirip "geçmişte bulunmuş agent'lar" listesi türetmek (ayrı iş).

3. **`last_activity` türetimi.** Son mesaj timestamp'i yeterli mi, yoksa agent'ların `last_seen`'i (float64 Unix) ile birleştirilsin mi? Mesaj timestamp'i ISO string, agent `last_seen` float — biçim tutarlılığı için sadece son mesaj timestamp'i önerilir.

4. **Orphan oda tanımı.** Bir oda "orphan" = adı hiçbir `teams.json` takım adıyla eşleşmiyor. Bu karşılaştırma frontend'de mi (mevcut `useTeams`) yoksa backend'de mi (`RoomSummary.IsOrphan`) yapılsın? Önerilen: frontend (backend takımları bilmiyor; `team` paketi `app.go` katmanında).

5. **Yetkilendirme.** `list_rooms_detailed` yalnızca desktop-authorized olmalı (agent'lar başka takımların geçmişini görmemeli). `handleGetMessagesRaw` deseni (`protocol.go:645`) izlenir. Onaylansın.

---

## Doğrulama / Test

- **Birim (Go):** `internal/hub/room_test.go`'a `Summary()`/`ListRoomSummaries()` testleri — son aktivite sıralaması, tamamen boş oda (`Fatura`/`backend`/`takimc`, 0 mesaj) `last_activity == ""`, agent map'i doğru kopyalanıyor mu, boş-agent odanın `agents` map'i boş dönüyor mu (join-parse yapılmadığı doğrulanır).
- **Protocol testi:** `protocol_test.go`'a `handleListRoomsDetailed` — desktop-authorized olmayan client reddediliyor mu; authorized client tüm odaları (mevcut fixture'da kaç oda varsa) döndürüyor mu.
- **Manuel (uçtan uca, `make dev`):**
  - "Rooms" sekmesinde 33 oda görünüyor (orphan dahil: `ExportGeo`, `FaturaEkran` vb.).
  - `GeoTeam`'e tıkla → `Coder1`, `Coder2` agent rozetleri + 14 mesaj.
  - `ExportGeo`'ya tıkla (0 agent, son ~300 mesaj görünür; diskte 415) → mesajlar yükleniyor; agent yerine **"agent kaydı yok (geçmiş odası)"** etiketi görünüyor (join-parse MVP dışı).
  - Tamamen boş oda (`Fatura`/`backend`/`takimc`) → "boş oda" rozeti, tıklayınca boş feed.
  - Aktif takım odası ile orphan oda görsel olarak ayırt ediliyor; aktif takım odası açıldığında akış **canlı** (yeni mesajlar düşüyor), orphan oda **statik**.
  - "Geri" ile listeye dönülüyor; aktif takımın canlı mesaj akışı bozulmuyor (mevcut `messages:new` event'i çalışmaya devam ediyor).
- **Regresyon:** Mevcut `list_rooms` MCP tool'u (agent-facing metin) değişmediğinden agent davranışı aynı kalmalı.

---

## Tahmini Efor (S/M/L)

**M** (orta). Backend tarafı küçük ve düşük riskli — mevcut `GetMessages`/`GetAgents` oda-parametreli binding'leri ve `GetAgentsRaw`/`GetMessagesRaw` yeniden kullanılıyor; yalnızca bir adet yapılandırılmış liste RPC'si + Wails binding ekleniyor, disk şeması değişmiyor. Asıl iş frontend'de: yeni `RoomBrowser` bileşeni (liste + detay), `useRooms` store, Sidebar sekmesi ve stiller. Denetim sonrası silme/içe-aktarma (Açık Soru #1) ve join-parse (Açık Soru #2) MVP'den çıkarıldığı için kapsam **M** olarak sabitlendi; bu iki özellik ileride ayrı işler olarak değerlendirilirse her biri ayrı **S–M** efor getirir.

> **Sıralama (denetim):** Bu özellik, **arşivleme (#13, Faz-1) tamamlandıktan sonra** — önerilen sırada **6. sırada** — ele alınmalı; aksi halde truncate edilen mesajlar (`room.go:135-136`) tarayıcıda kayıp görünür.
