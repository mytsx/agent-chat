# Gözlemci Oda Agent'ı (Observer)

## Drift Revizyonu (2026-06-22, #29/#12 merge-sonrası)

> Bu plan #29 (session-summary) ve #12 (team-config) merge edilmeden ÖNCE yazıldı.
> O zamandan beri imzalar ve altyapı değişti; aşağıdaki kararlar + drift
> düzeltmeleri, plandaki çelişen eski kararları (§F, §Açık-Soru-#1) **geçersiz kılar**.
> Geri kalan plan (hub cerrahisi, test matrisi) geçerlidir.

### Kullanıcı kararları (2026-06-22)
1. **Observer'ı manager desenini aynalayarak çöz** — `AgentConfig.Role="observer"` team
   store'a yazılır (`setTeamObserver`, `setTeamManager` muadili); mod team store'dan
   çözülür. Bu, **§F'in "team-persistence YOK / saf per-terminal sinyal" ve
   §Açık-Soru-#1'in "CreateTerminal'a explicit `agentMode` param" kararlarını
   GEÇERSİZ kılar.** Gerekçe: #29 zaten broadcast-skip'i **team-store rolüne** bağladı
   (`broadcastRoleLookup` `AgentConfig.Role` okur) ve `composeAgentPrompt` `agentRole`'ü
   `cfg.Role`'den okuyup zincire geçirir; rolü persist etmek bu test edilmiş altyapıyı
   yeniden kullanır + reopen-as-observer'ı bedava verir + **`CreateTerminal`/Wails imzasını
   değiştirmez.**
2. **Observer `list_agents`'ta GÖRÜNÜR** (şeffaf) — §Açık-Soru-#4 varsayımı onaylandı.
   Hub list_agents'a observer-filtresi eklenmez; UI'da `(gözlemci)` etiketi.
3. **Observer'a otomatik PTY bildirimi YOK** (kullanıcı-güdümlü MVP) — §Açık-Soru-#3 /
   §C "orchestrator'a kaydetme" kararı onaylandı.

### Drift düzeltmeleri (gövdedeki/§E-§G'deki satır no & imzalar stale)
- **`cli.ComposeStartupPrompt` GÜNCEL imza (startup.go:20):** `(basePrompt, globalPrompt,
  teamPrompt, roomSummary, selectedPrompt, agentName, agentRole, teamName string,
  isManager bool)` — #29 araya `roomSummary` ekledi. → **son parametre `isManager bool`
  → `agentMode string`** (∈ {"","manager","observer"}). Paralel `isObserver` bool EKLENMEZ.
- **`CreateTerminal` GÜNCEL imza (app.go:566):** `(teamID, agentName, workDir, cliType,
  promptID string, useWorktree bool, slotIndex int)` — #12 trailing param kullandı,
  **opts struct YOK.** Karar-1 sayesinde observer için **yeni param GEREKMEZ** (mod
  team-store'dan çözülür; `resolveAgentMode` `resolveManagerIntent` gibi).
- **`composeAgentPrompt` GÜNCEL imza (app.go:851):** `(teamID, agentName, promptID string,
  isManager bool)` → **`isManager bool` → `agentMode string`.** Gövdede `agentRole`'ü
  `cfg.Role`'den okuyor (app.go:872) ve `ComposeStartupPrompt`'a geçiriyor (app.go:912).
- **#29 forward-wiring (app.go):** `isObserverRole` (1220), `broadcastRoleLookup` (1242,
  `AgentConfig.Role` okur), `broadcastToSessions` observer-skip (1179) ZATEN VAR +
  test edilmiş (`TestIsObserverRole`, `TestBroadcastToSessions_SkipsObservers`,
  `TestBroadcastRoleLookup`). Bugün no-op çünkü hiçbir agent observer rolünde değil;
  Karar-1 ile rol persist edilince **otomatik aktif olur.**
- **`team.UpsertAgent` (store.go:193-218):** boş `Role`'ü KORUR, dolu `Role`'ü EZER →
  `Role="observer"` persist için temiz yol; `CreateTerminal`'ın Role-atlayan upsert'i
  (app.go:690) bozulmaz.
- **`resolveManagerIntent` (app.go:524):** `resolveAgentMode(teamID, agentName, promptID)
  (mode string, err error)`'e genelleştirilir; manager'ı `team.ManagerAgent`+prompt-tag,
  observer'ı `AgentConfig.Role=="observer"`'dan çözer; XOR (ikisi birden → hata).
- **Orchestrator izolasyonu:** İki broadcast yolu var ve İKİSİ de gerekli — (1) app.go
  `broadcastToSessions` (user_prompt log + manuel broadcast) zaten role-skip eder; (2)
  `orchestrator.ProcessMessage` broadcast (agent→all PTY bildirimi) role bilmez → observer'ı
  orchestrator'a **kaydetmeyerek** izole et (Karar-3 / §C MVP). `RegisterAgent` çağrısı
  app.go:724'te koşulsuz; observer dalında atlanır.

## Problem / Bağlam

Kullanıcı, bir odanın gidişatını birlikte değerlendirebileceği, bir görevi vermeden önce hazırlık yapabileceği bir **dış göz** agent'ı istiyor. Bu agent odadaki tüm konuşmayı izleyebilmeli, ama odadaki diğer agent'lara **mesaj gönderememeli**. Sadece **kullanıcı** ile (kendi terminal panelinde) konuşur. Kullanıcı bu agent ile odanın durumunu tartışır, görev metni hazırlar; agent odanın "akıbeti" hakkında yorum yapar.

## Denetim Revizyonu (2026-06-16, kod-doğrulamalı)

> Çok-ajanlı kod denetiminin doğrulanmış bulguları. Verdict ve uygulanan düzeltmeler.

- **Verdict: `minor-revisions`.** Plan mimari olarak sağlam; observer'ın routing/kilit/heartbeat mekanizmasının dışında durması doğru kurgulanmış. Yalnızca bir **kritik heartbeat hatası** ve birkaç netleştirme gerekti.
- **🔴 Heartbeat hatası (DOĞRULANDI, düzeltildi):** Planın eski iddiası — "observer heartbeat'i `read_*` çağrılarında `ReadMessages`/`ReadAllMessages`'ın `last_seen` güncellemesiyle korunur" — **YANLIŞ**. `ReadAllMessages` (`room.go:177-194`) `RLock` altında çalışır ve `last_seen`'e **dokunmaz**; yalnızca `ReadMessages` (`room.go:148-153`) dokunur. `handleGetAllMessages` (`protocol.go:435`) ise sadece `TouchManagerHeartbeat` çağırır — observer manager olmadığından bu **NO-OP**'tur. Sonuç: yalnızca `read_all_messages` ile poll eden bir observer, `cleanupStaleLocked` (`room.go:390-400`, 300s `staleTimeout`) tarafından **evict edilir** ve `read_all` yetkisini kaybeder. § A.3 yerinde düzeltildi.
- **🔧 Düzeltme (önkoşul, ilk yapılacak):** `handleGetAllMessages`'a observer için açık bir `last_seen`-touch ekle. En temiz yol: hub-side yeni bir `TouchAgentHeartbeat(c.agentName)` helper'ı (mutex altında `agents[name].LastSeen = types.Now()`), `protocol.go:435` civarında `if isObserver { ... }`. **Tool imzası değişmez** — `read_all_messages` MCP tool'u `agent_name` göndermez ama hub zaten `c.agentName`'e sahiptir.
- **🔒 Güvenlik (belgelendi):** send-engelleme join-bağlı kimliğe (`c.agentName`) göre çalışmalı. `protocol.go:286-289` zaten `data.From == c.agentName` garantiliyor; yani gate **client-spoof edilemez**. İleride client-sağlanan `from`'a güvenilmemesi için açıkça belgelendi (§ A.2).
- **⚠️ Asimetri (belgelendi):** manager XOR observer dışlaması **desktop app'te** yaşıyor, hub'ta değil (manager'ın hub-seviyesi kilidinin aksine). Düşük risk (desktop tek spawner) ama enforcement sınırı § F'de belirtildi.
- **Kapsam / böl-birleştir kararları:**
  - `agentMode`: Tek bir `agentMode string` (∈ {"", "manager", "observer"}) `CreateTerminal → composeAgentPrompt → ComposeStartupPrompt` boyunca taşınmalı. Paralel `isObserver` bool'ları **eklenmez** (§ E, F).
  - `resolveObserverIntent`'in team-persistence semantiği (manager taklidi) **çıkarıldı** — observer'ın team-store state'ine ihtiyacı yok (oda-kilidi / `team.ManagerAgent` karşılığı yok, çoklu observer serbest); saf per-terminal sinyal yeter (§ F).
  - Açık Soru #1 (mode parametresi): team-config (#12) imza değişikliğiyle **koordine edilir** — aynı `CreateTerminalOpts` / sona-ekleme türünde. Wails binding regen + `useTerminals.addTerminal` çağrı-yeri somut adım olarak yazıldı.
- **Çapraz-analiz notu (imza zinciri):** observer `agentMode`'u, team-config (#12)'nin kurduğu `CreateTerminal` imza-genişletme türüne **alan ekleyerek** girer. Önerilen sırada bu **4.**, ve önkoşulu `read_all` `last_seen`-touch düzeltmesidir (bu da en başa alınır).
- **Yeni regresyon testleri:** (1) observer yalnızca `read_all_messages` ile >300s poll ettiğinde **stale-evict OLMADIĞI**; (2) observer `send_message`'ın manager gateway'inden **önce** reddedildiği ve aktif manager heartbeat'ini **tazelemediği** (§ Doğrulama'ya eklendi).

Kullanıcının kendi ifadesi:
> "Oda agent'ı olsun istiyorum; o odanın akıbetini onunla konuşabileceğim veya bir görevi vermeden önce onunla hazırlayabileceğim bir dış göz agent'ı. Ama o onlarla iletişim kuramaz, sadece izleyebilir — ilk etapta öyle olsun istiyorum. Bu manager modundan FARKLI."

### Manager'dan Farkı (kritik)

| Boyut | Manager | Observer |
|-------|---------|----------|
| Routing'e müdahale | **Evet** — gateway: non-manager `send_message` çağrıları önce manager'a yönlendirilir (`protocol.go:306-316`) | **Hayır** — routing'e hiç karışmaz, mesaj akışını değiştirmez |
| Mesaj gönderme | Evet — `send_message` ile hedefe iletir | **Hayır** — `send_message` hub seviyesinde reddedilir |
| Oda kilidi | Tek aktif manager kilidi tutar (`room.go:70-76`) | Kilit tutmaz; manager ile **aynı anda** var olabilir, birden fazla observer olabilir |
| Tüm mesajları okuma | `read_all_messages` yalnızca aktif manager'a açık (`protocol.go:430-434`) | `read_all_messages` observer'a da açık (read-only izleme) |
| Diğer agent'lara görünürlük | Routing hedefi (mesajlar ona düşer) | **Hedef değil** — kimse ona mesaj atamaz, broadcast ona gitmez |
| Bildirim (orchestrator → PTY) | Manager-routed mesajlar daima manager'a bildirilir (`orchestrator.go:276-298`) | Hiçbir oda mesajı observer terminaline bildirilmez |
| Amaç | Mesaj trafiğini yönetmek/filtrelemek | Kullanıcıya analiz/danışmanlık (insan-agent diyaloğu) |

Özetle: **manager akışın içinde, observer akışın dışında.** Observer "salt-okunur bir gözlem konsolu + kullanıcı sohbeti"dir.

---

## Mevcut Durum

### Rol mimarisi

- `types.Agent.Role` serbest string alanı (`internal/types/message.go:6-10`). Şu an "manager" dışında özel anlamı olan bir rol değeri yok; geri kalan roller yalnızca açıklama amaçlı (örn. "Backend API Developer").
- `RoomState.Join(agentName, role)` (`internal/hub/room.go:60-102`): role `"manager"` ise tek-manager kilidi mantığı (`room.go:70-76`) devreye girer; aksi halde role yalnızca agent kaydına ve sistem mesajına yazılır. **Başka hiçbir rol değeri özel davranış tetiklemez.**
- Hub `join_room` doğrulaması (`internal/hub/protocol.go:201-212`): role `strings.ToLower(strings.TrimSpace(...))` ile normalize edilir; `"manager"` için configured-manager kontrolü yapılır. Diğer tüm roller serbestçe kabul edilir.

### send_message routing (gateway)

- `handleSendMessage` (`internal/hub/protocol.go:257-340`):
  - join zorunluluğu (`protocol.go:273-280`)
  - `from_agent == c.agentName` kimlik zorlaması (`protocol.go:286-289`)
  - aktif manager varsa ve gönderen manager değilse mesaj manager'a reroute edilir (`protocol.go:306-316`)
  - `RoomState.SendMessage` ile kaydedilir (`room.go:104-141`)
- **Şu an `send_message`'ı role'e göre engelleyen hiçbir mekanizma yok.** Join olan herkes mesaj gönderebilir.

### Mesaj okuma

- `read_messages` → `handleGetMessages` (`protocol.go:342-406`) → `RoomState.ReadMessages` (`room.go:144-174`): yalnızca `to == "all"`, `to == agentName` veya `type == "system"` mesajlarını döndürür. Observer için bu zaten yeterli (broadcast + sistem mesajları görünür), ama hedefli (direct) mesajları göremez.
- `read_all_messages` → `handleGetAllMessages` (`protocol.go:408-473`): tüm mesajları döndürür (`room.go:177-194`), ancak **yetki kontrolü** var (`protocol.go:419-436`): yalnızca aktif manager veya yetkili desktop. Observer şu an buradan reddedilir.

### list_agents

- `handleListAgents` (`protocol.go:475-511`) → `RoomState.ListAgents` (`room.go:197-212`): tüm agent'ları role'leriyle döndürür. Observer da listede görünür; özel işaretleme yok. AgentStatus UI yalnızca `role === "manager"` etiketini gösteriyor (`frontend/src/components/AgentStatus.tsx:26-28,42-45`).

### Orchestrator (PTY bildirimleri)

- `ProcessMessage` (`internal/orchestrator/orchestrator.go:265-339`):
  - manager-routed mesajlar daima manager session'ına bildirilir (`orchestrator.go:276-298`)
  - broadcast → gönderen hariç tüm kayıtlı session'lara bildirilir (`orchestrator.go:326-332`)
  - direct → yalnızca hedef session'a bildirilir (`orchestrator.go:333-336`)
- `RegisterAgent(chatDir, agentName, sessionID)` (`orchestrator.go:84-92`): role bilgisi **tutulmaz**. `CreateTerminal` her agent için çağırır (`app.go:536-538`). Yani observer da kaydedilirse broadcast bildirimleri ona da gider — istenmeyen davranış.

### Lifecycle (app.go)

- `CreateTerminal` (`app.go:415-543`): `resolveManagerIntent` ile manager olup olmadığı belirlenir (`app.go:432`, `app.go:376-411`); manager ise worktree kapatılır (`app.go:438-440`), `syncHubManager` çağrılır (`app.go:472`). Startup prompt `composeAgentPrompt(..., isManager)` ile kurulur (`app.go:497, 590-638`) ve orchestrator'a kaydedilir (`app.go:536-538`).
- `composeAgentPrompt` (`app.go:590-638`): `isManager` ise `manager_prompt.md` eklenir; `cli.ComposeStartupPrompt(..., isManager)` çağrılır.
- `cli.ComposeStartupPrompt` (`internal/cli/startup.go:9-56`): `isManager` ise role `"manager"`, join talimatı `read_all_messages` ve join çağrısı `join_room(name, "manager")` olur. Aksi halde role agent açıklaması, talimat `read_messages` olur.

### Team / Frontend

- `team.Team.ManagerAgent string` (`internal/team/store.go:32`); `team.AgentConfig` (`store.go:17-23`) role içerir.
- `SetupWizard.tsx`: "Set as manager" checkbox'ı (`SetupWizard.tsx:140-149`), manager seçilince worktree devre dışı (`SetupWizard.tsx:158-164`). `setTeamManager` çağrısı (`SetupWizard.tsx:66-68`).
- `frontend/src/lib/types.ts`: `Team.manager_agent`, `Agent.role` alanları mevcut.

### Sonuç (boşluk analizi)

Observer için gereken davranışların hiçbiri mevcut değil:
1. `send_message` role-tabanlı engellemesi → **yok**.
2. `read_all_messages` observer'a izin → **yok** (sadece manager).
3. Observer'ın routing/notification'dan izolasyonu → **yok** (broadcast herkese, orchestrator role bilmiyor).
4. UI'da observer rol seçimi → **yok**.
5. Observer için startup prompt → **yok**.

---

## Çözüm Tasarımı

Yeni bir özel rol değeri: **`observer`** (normalize edilmiş, küçük harf — `manager` ile aynı pattern).

### Tasarım ilkeleri

1. **Observer routing'e hiç dokunmaz.** Manager gateway mantığı, tek-manager kilidi, heartbeat mekanizması observer'dan tamamen bağımsız kalır. Observer join olduğunda `managerAgent` alanına dokunulmaz.
2. **Observer salt-okunur izleyicidir.** `read_messages` ve `read_all_messages` serbest; `send_message` reddedilir.
3. **Observer bir routing hedefi değildir.** Broadcast ve direct mesajlar observer'a *yönlendirilmez*; orchestrator observer terminaline bildirim göndermez.
4. **Observer manager ile birlikte ve çoğul olabilir.** Kilit yok → aynı odada manager + N observer mümkün.
5. **MVP kapsamı:** yalnızca izleme + kullanıcıyla sohbet. Observer'ın odaya müdahalesi (örn. kullanıcı onayıyla mesaj enjekte etme) bu fazda **yok**, ileriye bırakılır.

### A. Hub: rol tanımı ve send engellemesi

**1. Role normalizasyonu (`internal/hub/protocol.go:handleJoinRoom`)**
- `role := strings.ToLower(strings.TrimSpace(data.Role))` zaten var (`protocol.go:201`).
- `role == "observer"` için **özel kontrol gerekmez** — configured-manager kontrolü yapılmaz, kilit denemesi olmaz. `RoomState.Join` mevcut haliyle role'ü agent kaydına yazar; `manager` dalına girmediği için kilit mantığı tetiklenmez (`room.go:70-76`). Yani join tarafında değişiklik minimaldir: yalnızca opsiyonel bir geçerli-rol doğrulaması eklenebilir (observer adının manager ile çakışmadığını garanti etmek için ekstra koda gerek yok).
- Net karar: **`RoomState.Join` observer'ı otomatik olarak doğru kaydeder.** Tek eklenecek şey, observer rolünü "bilinen özel rol" olarak tanıyan bir sabit/helper.

**2. send_message reddi (`internal/hub/protocol.go:handleSendMessage`)**
- Join + kimlik kontrollerinden sonra (`protocol.go:286-289`'dan sonra), gönderenin odadaki rolünü sorgula ve observer ise reddet:
  ```
  if roomState.IsObserver(data.From) {
      c.sendError(req.ID, req.Type,
          "observer rolündeki agent mesaj gönderemez; yalnızca odayı izleyebilir")
      return
  }
  ```
- Bunun için `RoomState`'e yeni helper: `IsObserver(agentName string) bool` (agent kaydındaki role'ü `observer` ile karşılaştırır, mutex altında).
- **Kritik:** Bu kontrol manager gateway'inden (`protocol.go:306-316`) **önce** olmalı ki observer mesajı manager'a bile reroute edilmesin — tamamen reddedilir. (Aktif manager varken bile observer'ın mesajı manager heartbeat'ini **tazelemez**, çünkü reddedilme `GetActiveManagerAndTouch`'tan önce gerçekleşir.)
- **🔒 Güvenlik (denetim notu):** Engelleme `data.From` üzerinden değil, **join-bağlı kimlik** `c.agentName` üzerinden çalışmalı. `protocol.go:286-289` zaten `data.From == c.agentName` zorlaması yaptığından gate **client-spoof edilemez** — bir client kendini observer-olmayan bir `from` ile göstererek engeli atlayamaz. İleride client-sağlanan `from`'a güvenilmemesi için bu sınır açıkça korunmalı; `IsObserver` çağrısı `c.agentName` (veya doğrulanmış `data.From`) ile yapılır.

**3. read_all_messages izni + observer heartbeat (`internal/hub/protocol.go:handleGetAllMessages`)**
- Mevcut yetki bloğu (`protocol.go:425-435`) yalnızca aktif manager'a izin veriyor. Observer'ı da izinli yap **ve heartbeat'ini tazele:**
  ```
  isManager := activeManager != "" && c.agentName == activeManager
  isObserver := roomState.IsObserver(c.agentName)
  if !isManager && !isObserver {
      c.sendError(... "yalnızca aktif manager veya observer tüm mesajları okuyabilir")
      return
  }
  if isManager {
      roomState.TouchManagerHeartbeat(c.agentName)
  } else if isObserver {
      roomState.TouchAgentHeartbeat(c.agentName)
  }
  ```
- **🔴 KRİTİK (denetim düzeltmesi):** Observer heartbeat'i `read_*` çağrılarında **otomatik korunmaz**. `ReadAllMessages` (`room.go:177-194`) `RLock` altında çalışır ve `last_seen`'e **dokunmaz** — yalnızca `ReadMessages` (`room.go:148-153`) dokunur. `handleGetAllMessages`'ın mevcut `TouchManagerHeartbeat` çağrısı (`protocol.go:435`) ise observer manager olmadığından **NO-OP**'tur. Bu yüzden yalnızca `read_all_messages` ile poll eden observer, `cleanupStaleLocked` (`room.go:390-400`, `staleTimeout` 300s) tarafından **evict edilir** ve `read_all` yetkisini kaybeder.
- **Çözüm:** Yeni hub helper `TouchAgentHeartbeat(agentName string)` — mutex altında `agents[name].LastSeen = types.Now()` (`TouchManagerHeartbeat` paterni, ama manager-kilidi koşulu olmadan; § B'ye ekle). Observer dalında çağrılır. Manager heartbeat'ine dokunulmaz. Tool imzası **değişmez** (MCP `read_all_messages` `agent_name` göndermez; hub zaten `c.agentName`'e sahiptir).

**4. clear_room (`handleClearRoom`, `protocol.go:557-588`)**
- Observer'a clear yetkisi **verilmez** — salt-okunur ilkesi. Mevcut kod observer'ı zaten reddeder (manager değil) — değişiklik gerekmez. (Karar: observer hiçbir mutasyon yapamaz.)

**5. list_agents görünümü**
- `RoomState.ListAgents` observer'ı role'üyle döndürür (değişiklik gerekmez). Hub metin çıktısında (`protocol.go:496-507`) role zaten basılıyor; observer için ek olarak `(gözlemci)` etiketi eklemek opsiyonel kozmetik iyileştirme. **MVP'de metin çıktısı yeterli; özel işaretleme UI tarafında yapılır (aşağıda).**

### B. RoomState değişiklikleri (`internal/hub/room.go`)

- Yeni sabit: `roleObserver = "observer"`.
- Yeni metot:
  ```
  func (r *RoomState) IsObserver(agentName string) bool {
      r.mu.RLock(); defer r.mu.RUnlock()
      a, ok := r.agents[agentName]
      return ok && strings.EqualFold(strings.TrimSpace(a.Role), roleObserver)
  }
  ```
- Yeni metot (denetim önkoşulu — observer stale-evict düzeltmesi):
  ```
  // TouchAgentHeartbeat, yalnızca read_all_messages ile poll eden
  // observer'ın stale-evict olmasını önlemek için last_seen'i tazeler.
  func (r *RoomState) TouchAgentHeartbeat(agentName string) {
      r.mu.Lock(); defer r.mu.Unlock()
      if a, ok := r.agents[agentName]; ok {
          a.LastSeen = types.Now()
          r.agents[agentName] = a
          r.dirty = true
      }
  }
  ```
  - `TouchManagerHeartbeat` (`room.go:262-271`) paterni; manager-kilidi koşulu yok. Manager için `managerLastSeen`'e dokunmaz, yalnızca agent kaydının `last_seen`'ini günceller (`ReadMessages`'ın `room.go:148-152`'deki davranışıyla aynı).
- `Join`, `Leave`, `Clear`, manager kilidi mantığı **değişmez** — observer bu yollara hiç girmez. (Doğrulama: `Join` yalnızca `role == "manager"` için kilit dener; observer kayıt edilir, kilit denemesi olmaz.)

### C. Orchestrator: bildirim izolasyonu (`internal/orchestrator/orchestrator.go`)

İki problem: (a) observer broadcast bildirimleri almamalı, (b) kimse observer'a direct mesaj atamayacağı için direct yolu zaten tetiklenmez ama gönderici olarak observer da olmaz (hub reddediyor).

Çözüm — orchestrator'a observer farkındalığı ekle:
- `RegisterAgent` imzasına rol bilgisi ekle veya ayrı bir `observers map[chatDir]map[agentName]bool` set'i tut. Minimal yaklaşım: `agentSessions`'a paralel `observerAgents map[string]map[string]bool` ekle.
- `app.go:CreateTerminal` observer ise `o.RegisterObserver(teamName, agentName)` (veya `RegisterAgent`'a `isObserver` parametresi) çağırsın. **Alternatif (daha basit) MVP yaklaşımı:** observer'ı orchestrator'a session olarak **hiç kaydetme** (`app.go:536-538`'de observer ise `RegisterAgent` çağrılmaz). Böylece broadcast döngüsü (`orchestrator.go:328-332`) observer'ı zaten görmez, hiç bildirim gitmez.
  - **Önerilen MVP kararı:** Observer'ı orchestrator'a kaydetme. Bu en az kod ve en net izolasyon. (Trade-off: observer terminaline otomatik "yeni mesaj var" bildirimi gitmez; kullanıcı isterse observer'a kendisi "odayı kontrol et" der. MVP için kabul edilebilir — observer zaten kullanıcı güdümlü çalışır.)
- Not: Manager-routed yol (`orchestrator.go:276-298`) ve direct yol (`333-336`) observer'ı zaten hedeflemez (observer hiç mesaj göndermez, kimse observer'a `to=observer` atamaz; atsa bile observer kaydı olmadığından session bulunamaz ve `Target agent not found` loglanır — zararsız).

### D. Startup prompt (`prompts/observer_prompt.md` — YENİ)

Manager prompt'una paralel yeni bir gömülü prompt. İçerik (Türkçe, base_prompt tonu):
- "Sen bu odanın **GÖZLEMCİSİ**sin (dış göz). Odadaki agent'larla **iletişim kuramazsın**; yalnızca izlersin ve **kullanıcıyla** konuşursun."
- Araç kullanımı: `join_room(name, "observer")`, ardından `read_all_messages(since_id=0)` ile tüm trafiği oku. Periyodik olarak `read_all_messages` / `list_agents` ile durumu izle.
- **Yasak:** `send_message` çağırma (hub reddeder; deneme yapma). `clear_room` çağırma.
- Görev: kullanıcıya odanın gidişatı hakkında özet/analiz sun, görev taslağı hazırlamasına yardım et.

### E. CLI startup composition (`internal/cli/startup.go`)

- `ComposeStartupPrompt` imzası `isManager bool` içeriyor (`startup.go:9`).
- **Karar (denetim):** `isManager bool` yerine **tek bir `agentMode string`** (∈ {"", "manager", "observer"}) parametresine genişlet. Paralel bir `isObserver bool` **EKLENMEZ** — iki ayrı bool {manager, observer} kombinasyonu geçersiz durumlar (ikisi de true) üretir ve `CreateTerminal → composeAgentPrompt → ComposeStartupPrompt` zincirinde imza şişmesine yol açar. Tek `agentMode` string'i bu zincir boyunca taşınır; geçersiz kombinasyon en başta (`resolveAgentMode`) elenir.
- Observer dalında:
  - `join_room(name, "observer")`
  - read talimatı: `read_all_messages(since_id=0) ile odayı izle; agent'lara mesaj GÖNDERME, yalnızca kullanıcıyla konuş.`

### F. app.go lifecycle (`app.go`)

- **Rol çözümü:** **tek bir** `resolveAgentMode(...) (mode string, err error)` (mode ∈ {"", "manager", "observer"}). Kaynaklar:
  - Prompt tag'i `"observer"` (manager için `hasPromptTag(... "manager")` paterni, `app.go:365-373`).
  - Frontend'den gelen açık `agentMode`/observer sinyali (aşağıya bak) → en temiz kaynak.
- **Karar (denetim) — team-persistence YOK:** `resolveManagerIntent`'in team-store semantiğini (`team.ManagerAgent` okuma/yazma) **taklit ETME**. Observer'ın team-store state'ine **ihtiyacı yok**: oda-kilidi / `team.ManagerAgent` karşılığı yoktur, çoklu observer serbesttir, kalıcı "team manager'ı kim" sorusunun observer karşılığı yoktur. Observer **saf per-terminal sinyaldir** — `resolveAgentMode` yalnızca prompt-tag + frontend sinyalini okur, teams.json'a yazmaz. (Bu yüzden ayrı bir `resolveObserverIntent` team-persistence helper'ı **eklenmez**.)
- **Karşılıklı dışlama (enforcement sınırı):** Bir agent aynı anda hem manager hem observer olamaz; `resolveAgentMode` çakışmada hata döner. **⚠️ Denetim notu:** bu XOR dışlaması **desktop app'te** yaşar, hub'ta değil — manager'ın hub-seviyesi tek-kilidinin (`room.go:70-76`) aksine, hub observer için bir invariant zorlamaz. Risk düşük (desktop tek spawner'dır, manuel hub bağlantısı senaryo dışıdır), ama enforcement'ın desktop katmanında olduğu bilinçli bir tercih olarak belirtilir.
- **Worktree:** Observer da kod yazmaz/izler — worktree gereksiz. Manager gibi `useWorktree = false` zorlanabilir (`app.go:438-440` paterni). (Karar: observer için worktree kapat.)
- **syncHubManager:** Observer manager **değildir** → `syncHubManager` observer agent adıyla **çağrılmaz**; mevcut manager ataması bozulmaz (`app.go:463-472` observer dalında atlanır).
- **composeAgentPrompt:** `isManager` yerine mode'a göre observer prompt'unu ekle (`app.go:625-635` paterni; `observer_prompt.md` oku).
- **Orchestrator kaydı:** observer ise `RegisterAgent` çağırma (C bölümü kararı, `app.go:536-538`).
- `join_room` rolü: observer ise `"observer"` (`composeAgentPrompt` → `ComposeStartupPrompt` zinciri üzerinden).

### G. Frontend (`SetupWizard.tsx`, `types.ts`, `AgentStatus.tsx`)

- **SetupWizard:** "Set as manager" checkbox'ının yanına/yerine bir rol seçimi. MVP için ikinci bir checkbox: **"Set as observer (read-only)"**. Manager checkbox'ı ile karşılıklı dışlayıcı (biri seçilince diğeri disable). Observer seçilince worktree disable (manager paterni, `SetupWizard.tsx:158-164`).
  - `handleCreate`: observer seçiliyse `agentMode: "observer"` `CreateTerminal`'a iletilir. **Karar (denetim):** Bu sinyal `CreateTerminal` imzasına **team-config (#12) ile aynı imza-genişletme türünde** (örn. `CreateTerminalOpts` struct'ı veya sona-eklenen alan) taşınır — iki ayrı sinyal kanalı kurma. Somut çağrı-yeri adımları § Açık Soru #1'de.
- **types.ts:** `Agent.role` zaten var; özel tip değişikliği gerekmez. İstenirse `AgentMode = "manager" | "observer" | ""` yardımcı tipi.
- **AgentStatus.tsx:** `isManager` paternine (`AgentStatus.tsx:26-28`) paralel `isObserver = agent.role?.toLowerCase() === "observer"` ekle; etiket `" (gözlemci)"` / göz ikonu göster (`AgentStatus.tsx:42-45`).

---

## Etkilenen / Yeni Dosyalar

| Dosya | Değişiklik | Tip |
|-------|-----------|-----|
| `internal/hub/room.go` | `roleObserver` sabiti + `IsObserver()` metodu + **`TouchAgentHeartbeat()` (observer stale-evict düzeltmesi)** | Değişiklik |
| `internal/hub/protocol.go` | `handleSendMessage`: observer reddi (gateway'den **önce**, `c.agentName` kimliğine göre); `handleGetAllMessages`: observer'a okuma izni + **observer dalında `TouchAgentHeartbeat`** | Değişiklik |
| `internal/orchestrator/orchestrator.go` | Observer'ı kaydetmeme / bildirimden izole etme (app.go ile birlikte) | Değişiklik (minimal/yok) |
| `app.go` | `resolveAgentMode` (tek `agentMode` string, **team-persistence YOK** — saf per-terminal sinyal), `CreateTerminal` imzasına `agentMode` (team-config #12 ile koordineli), observer'ı orchestrator'a kaydetmeme, observer prompt'unu composeAgentPrompt'a ekleme, worktree kapatma, syncHubManager'ı atlama | Değişiklik |
| `internal/cli/startup.go` | `ComposeStartupPrompt` imzasını `agentMode string`'e genişlet (paralel `isObserver` bool **yok**); observer dalı: join_room rolü + read talimatı | Değişiklik |
| `frontend/wailsjs/...` (regen) | `CreateTerminal` yeni `agentMode` imzası — `make dev`/`make build` ile regen | Doğrulama |
| `frontend/src/store/useTerminals.ts` | `addTerminal` → `CreateTerminal` çağrısına `agentMode` ilet | Değişiklik |
| `prompts/observer_prompt.md` | Observer rol prompt'u | **Yeni** |
| `app.go` (embed) | `//go:embed prompts/*.md` zaten tüm md'leri kapsıyor — yeni dosya otomatik gömülür | Doğrulama |
| `frontend/src/components/SetupWizard.tsx` | "Set as observer" checkbox'ı (manager ile karşılıklı dışlayıcı), worktree disable | Değişiklik |
| `frontend/src/components/AgentStatus.tsx` | Observer etiketi/ikonu | Değişiklik |
| `frontend/src/lib/types.ts` | (Opsiyonel) `AgentMode` yardımcı tipi | Opsiyonel |
| `internal/mcpserver/server.go` | (Opsiyonel) `join_room` ve `send_message` tool açıklamalarına observer semantiği notu | Opsiyonel |
| `internal/hub/room_test.go` | Observer join + `IsObserver` + **`TouchAgentHeartbeat` stale-evict regresyon** testleri | Test |
| `internal/hub/protocol_test.go` | Observer send reddi (gateway öncesi, manager heartbeat tazelenmez) + read_all izni + **read_all-only >300s no-evict** testleri | Test |
| `CLAUDE.md` | "Manager + Orchestrator Routing" bölümüne observer rolü notu | Doküman |

---

## Adım Adım İmplementasyon

0. **🔴 Önkoşul — observer heartbeat (room.go):** `TouchAgentHeartbeat(agentName)` helper'ını ekle (§ B). Bu, observer'ın yalnızca `read_all_messages` ile poll ederken `staleTimeout` (300s) sonrası evict olmasını önler — diğer adımlar bu düzeltme olmadan çalışır gibi görünüp 5 dk sonra observer'ı düşürür.
1. **Hub çekirdeği (room.go):** `roleObserver` sabiti ve `IsObserver()` metodunu ekle. `Join`/`Leave`/`Clear` ve manager kilidinin observer'a dokunmadığını doğrula (kod değişmez).
2. **Hub protocol (protocol.go):**
   - `handleSendMessage`: kimlik kontrolünden (`protocol.go:286-289`, join-bağlı `c.agentName`) sonra, manager gateway'inden (`GetActiveManagerAndTouch`, `protocol.go:306`) **önce** observer reddi ekle.
   - `handleGetAllMessages`: yetki bloğunu observer'ı da kapsayacak şekilde genişlet **ve** observer dalında `TouchAgentHeartbeat` çağır.
3. **Hub testleri:** `room_test.go` + `protocol_test.go` — observer join, send reddi (gateway'den önce + manager heartbeat tazelenmez), read_all izni, **read_all-only >300s poll'de stale-evict OLMAMASI**, manager kilidiyle çakışmama (manager + observer aynı odada).
4. **Observer prompt (prompts/observer_prompt.md):** yeni dosyayı yaz. `make mcp-server` sonrası embed'in çalıştığını doğrula.
5. **CLI startup (startup.go):** `ComposeStartupPrompt`'a observer modu ekle (join_room rolü `"observer"`, read talimatı `read_all_messages` + "mesaj gönderme" uyarısı). İmza değişikliğini (mode parametresi) tüm çağıranlara yansıt.
6. **app.go:** `resolveAgentMode` (manager/observer/none, karşılıklı dışlama), observer dalında: worktree kapat, `syncHubManager` atla, `composeAgentPrompt`'a observer prompt'u ekle, orchestrator'a **kaydetme**.
7. **Frontend:** SetupWizard observer checkbox'ı + manager ile dışlama; observer sinyalini App binding'ine ilet (Açık Sorular'daki karara göre). AgentStatus observer etiketi.
8. **MCP tool açıklamaları (opsiyonel):** `send_message` / `join_room` açıklamalarına observer notu.
9. **Dokümantasyon:** CLAUDE.md ve gerekirse README'ye observer rolü; manager'dan farkı.
10. **Uçtan uca manuel test** (Doğrulama bölümü).

---

## Açık Sorular / Karar Gerektiren Noktalar

1. **Observer sinyalinin App'e taşınması (en kritik karar):** Observer modu hangi kanaldan belirlenecek?
   - (a) Yalnızca **prompt tag'i** `"observer"` (manager'daki `hasPromptTag` paterni) — UI checkbox'ı observer-tag'li bir prompt seçtirir. Kod değişikliği en az; ama kullanıcı "observer" prompt'u oluşturmak zorunda.
   - (b) `CreateTerminal` imzasına açık bir **`agentMode string` parametresi** eklemek — en açık ama imza ve Wails binding değişikliği gerektirir.
   - **Karar (denetim): (b).** Açık `agentMode string` en az sürprizli ve manager için de ileride birleştirilebilir. **team-config (#12) ile koordine et:** #12 zaten `CreateTerminal` imzasını genişletiyorsa, observer'ın `agentMode` alanını **aynı `CreateTerminalOpts` struct'ına / aynı sona-ekleme türüne** kat — ayrı bir imza turu açma. Somut çağrı-yeri adımları:
     1. `app.go`: `CreateTerminal` imzasına `agentMode string` ekle (#12'nin opts struct'ı varsa oraya alan olarak).
     2. **Wails binding regen:** `make dev`/`make build` Wails binding'lerini yeniden üretir; `frontend/wailsjs/go/main/App.d.ts` ve `.js`'te `CreateTerminal` yeni imzayı yansıtmalı.
     3. `frontend/src/store/useTerminals.ts`: `addTerminal` çağrı-zinciri yeni `agentMode` argümanını `CreateTerminal`'a iletecek şekilde güncellenir.
     4. `SetupWizard.tsx`: observer checkbox'ı → `addTerminal({ ..., agentMode: "observer" })`.
   - **Çapraz-analiz sırası:** Bu adım, önerilen implementasyon sırasında **4.**'tür; **önkoşulu** `read_all` `last_seen`-touch düzeltmesidir (§ A.3 / B), o yüzden o düzeltme **en başa** alınır.
2. **read_messages mi read_all_messages mi?** Observer'ın direct mesajları da görmesi için `read_all_messages` (tüm trafik) doğru seçim. `read_messages` filtresi (`room.go:163`) direct mesajları gizler. **Karar:** observer prompt'u `read_all_messages` kullanmalı; `handleGetAllMessages` observer'a açılmalı (yukarıda kararlaştırıldı).
3. **Observer'a otomatik PTY bildirimi gitsin mi?** MVP kararı: **hayır** (orchestrator'a kaydetme). İleride "yeni mesaj geldikçe observer'ı uyandır" opsiyonu eklenebilir; bu durumda observer için `notifyAgent`'ı broadcast'ten ayrı, gönderici-bağımsız bir kanal olarak eklemek gerekir.
4. **list_agents'ta observer gizlensin mi?** Hayır — observer odada görünür olmalı (şeffaflık). Diğer agent'lar observer'ın izlediğini görebilir; bu istenen davranış mı, yoksa "görünmez gözlemci" mi tercih edilir? **Varsayım:** görünür. Onay gerekli.
5. **Çoklu observer ve manager+observer aynı agent adı:** Aynı odada birden çok observer serbest (kilit yok). Aynı anda manager+observer **farklı agent'lar** olabilir. Aynı agent ikisini birden olamaz (resolveAgentMode dışlar).
6. **İleri faz (kapsam dışı):** Observer'ın kullanıcı onayıyla odaya mesaj enjekte etmesi (örn. hazırlanan görevi manager'a/agent'lara gönderme). MVP'de yok; ayrı plan.

---

## Doğrulama / Test

### Birim testleri

- `internal/hub/room_test.go`:
  - `TestObserverJoin_DoesNotClaimManagerLock`: observer join → `GetActiveManager()` boş kalır.
  - `TestIsObserver`: observer/non-observer/manager için doğru bool.
  - `TestManagerAndObserverCoexist`: manager + observer aynı odada; manager kilidi observer'dan etkilenmez.
  - **🔴 `TestObserverHeartbeat_ReadAllKeepsAlive` (denetim, YENİ):** observer join → `last_seen`'i geçmişe it (>300s) → `TouchAgentHeartbeat` çağrılır → `cleanupStaleLocked` sonrası agent **hâlâ kayıtlı**. Düzeltme olmadan bu test başarısız olmalı (regresyon koruması). Tamamlayıcı: `TouchAgentHeartbeat`'in `managerLastSeen`'e dokunmadığını assert et.
- `internal/hub/protocol_test.go`:
  - `TestSendMessage_ObserverRejected`: observer `send_message` → hata, mesaj kaydedilmez, manager'a reroute edilmez.
  - `TestReadAllMessages_ObserverAllowed`: observer `read_all_messages` → başarı.
  - `TestReadAllMessages_NonManagerNonObserverRejected`: normal agent hâlâ reddedilir (regresyon).
  - `TestSendMessage_ObserverRejectedBeforeManagerGateway`: aktif manager varken observer'ın mesajı manager'a bile düşmez.
  - **🔴 `TestSendMessage_ObserverDoesNotTouchManagerHeartbeat` (denetim, YENİ):** aktif manager varken observer `send_message` denemesi → reddedilir **ve** aktif manager'ın `managerLastSeen`'i **değişmez** (reddetme `GetActiveManagerAndTouch`'tan önce gerçekleştiği için).
  - **🔴 `TestObserverReadAll_Over300sNoEvict` (denetim, YENİ):** observer yalnızca `handleGetAllMessages` ile poll → >300s simüle et → observer evict **OLMAZ**, `read_all` yetkisi korunur (uçtan uca, protocol seviyesinde).
- `internal/orchestrator/orchestrator_test.go`:
  - Observer kaydedilmediğinde broadcast'in observer'a bildirim göndermediği (manager/agent regresyonları korunur).

### Komutlar

```bash
go test ./...
go test ./internal/hub/ -run Observer
make mcp-server   # embed: observer_prompt.md gömülmeli
make dev
```

### Manuel senaryolar

1. Bir team'de iki AI terminal (agent A, agent B) + bir observer terminal aç.
2. Observer odanın tüm trafiğini `read_all_messages` ile görür.
3. Observer `send_message` denerse hub reddeder ("observer mesaj gönderemez").
4. A → B broadcast/direct mesaj: observer'a otomatik PTY bildirimi **gitmez** (terminali sessiz kalır); A ve B normal akışta çalışır.
5. Aynı odada manager varken observer eklenir: manager gateway bozulmaz, manager kilidi observer'dan etkilenmez.
6. Kullanıcı observer terminalinde sohbet eder, observer odanın durumunu özetler ve görev taslağı hazırlar.
7. Observer `clear_room` denerse reddedilir.
8. **🔴 Uzun-poll (denetim, YENİ):** Observer açık bırakılır, 5 dk (>300s) boyunca yalnızca `read_all_messages` ile poll eder, kullanıcı arada mesaj göndermez → observer **stale-evict OLMAZ**, `read_all` yetkisini korur. (`TouchAgentHeartbeat` olmadan bu senaryoda observer 5 dk sonra düşer.)

---

## Tahmini Efor

**M (Orta).**

- Hub değişiklikleri küçük ve cerrahi (yeni rol özel davranış tetiklemediği için `Join`/kilit/routing dokunulmaz; iki handler'a koşul + iki helper: `IsObserver`, `TouchAgentHeartbeat`).
- Asıl iş frontend rol seçimi + `CreateTerminal` `agentMode` kanalı (Açık Soru #1, team-config #12 ile koordineli) ve yeni prompt + startup composition imza değişikliğinin tüm çağıranlara yayılması.
- Risk düşük: observer routing'in dışında olduğundan mevcut manager/orchestrator davranışlarını bozma yüzeyi minimaldir; ana riskler (1) imza değişikliklerinin (`ComposeStartupPrompt`, `CreateTerminal`) eksiksiz yansıtılması ve (2) **denetimde yakalanan observer heartbeat'i** — `read_all`-only poll eden observer'ın `TouchAgentHeartbeat` olmadan 300s sonra evict olması (önkoşul adım 0 ile giderildi).
