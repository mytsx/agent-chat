# Takım Konfigürasyonu Kalıcılığı

> Agent adı + provider (CLIType) + proje yolu (workDir) + rol/prompt + slot pozisyonunu takım şablonu olarak kaydet ve tek tıkla aynı config ile yeniden aç.

## Denetim Revizyonu (2026-06-16, kod-doğrulamalı)

> Çok-ajanlı kod denetiminden çıkan ✅ **DOĞRULANMIŞ** bulgular. Bu bölüm en güncel kaynaktır; aşağıdaki gövde de yerinde düzeltildi.

- **Verdict:** `minor-revisions` — planın temel premisi (**"migration gerekmez, altyapı zaten hazır"**) kodla TEYİT EDİLDİ. `AgentConfig` (store.go:17-23) ve `Team.Agents` (store.go:29) mevcut; `Create` (store.go:99) / `Update` (store.go:132) `agents` parametresini `save()` ile teams.json'a yazıyor; frontend `SetupWizard.handleCreate` (SetupWizard.tsx:61-76) yalnızca `addTerminal` çağırıp PTY açıyor, `Team.Agents`'a hiç yazmıyor. Eski `"agents": []` kayıtları temiz deserialize olur → migration scripti gerekmez.
- **İmza doğrulaması:** `CreateTerminal` (app.go:415) imzası `(teamID, agentName, workDir, cliType, promptID string, useWorktree bool)` — 6 parametre. `useTerminals.addTerminal` (useTerminals.ts:61-62) `slotIndex` argümanını alıyor ama Go'ya **GEÇİRMİYOR** (sadece 6 arg gönderiyor; slot yalnızca frontend store'da kalıyor). Yeni slot parametresi eklenince bu çağrı yeri de güncellenmeli.
- **Uygulanan düzeltmeler (yerinde):**
  1. **A1 vs A2 → A1 (tek-alan `UpsertAgent`) kesinleşti.** `SetManager` (store.go:156) deseni izlenir; pozisyonel `Store.Update` genişletilmez. Çelişkili "şablona kaydet onay kutusu + otomatik-upsert" ikilemi A1 lehine çözüldü — onay kutusu KALDIRILDI (Açık Soru #1 sadeleşti).
  2. **`UpsertAgent` INVARIANT:** update'te mevcut `Role` KORUNUR. `CreateTerminal`/wizard `Role` sağlamıyor; naif upsert kullanıcının set ettiği `Role`'ü boş string'le ezerdi. (`composeAgentPrompt` app.go:610 bu `Role`'ü okuduğu için davranış kritik.)
  3. **`composeAgentPrompt` uyarısı:** app.go:610 ZATEN `Agents[].Role` okuyor → `agents` dizisi dolunca startup-prompt davranışı değişir. Bu BİLİNÇLİ yapılıp test edilmeli (regresyon maddesine eklendi).
  4. **`PromptID`/`WorkDir`/`CLIType` okuyucusu:** Bu alanların şu an OKUYUCUSU YOK (sadece prompt için `Role` okunuyor). Session-restore için yazma yetmez; `OpenTeamFromConfig` read/respawn yolunu da kurmalı — bu plan bunu kapsıyor (B akışı).
  5. **`origWorkDir`:** config'e PTY'nin worktree dizini DEĞİL, kullanıcının seçtiği ORİJİNAL proje dizini yazılır (app.go:443-460; `workDir` app.go:460'ta worktree'ye reassign ediliyor). `git.CreateWorktree` (git.go:37-68) eşleşen worktree'yi idempotent reuse ediyor, mismatch'te hata veriyor → **Açık Soru #6 KAPANDI**.
  6. **Durabilite (IN-SCOPE):** `save()` (store.go:68-74) `os.WriteFile` ile NON-ATOMİK. `OpenTeamFromConfig` 4-6 agent açarken 4-6 ardışık yazım yapar → crash mid-batch'te kısmi dosya. Bu özellik batch paterni yarattığı için kapsam içi: `save()` temp-file+rename'e (hub-state deseni) çevrilir VEYA batch tek `Update`'te toplanır.
  7. **`SlotIndex`:** `omitempty` YOK (0 geçerli slot); eski kayıtlar için array-order fallback. Bu, MEMORY'deki "terminal yanlış panelde açılıyor" bug'ını da çözer.
- **Kapsam / böl-birleştir kararları:**
  - **ÇIKARILDI (over-engineering):** Şablon yönetim UI (Adım 10), "eksik prompt uyarısı" (Açık Soru #5 — `composeAgentPrompt` zaten sessizce degrade ediyor, app.go:619-623), spekülatif `RemoveAgent` (MVP'de tetikleyicisi yok).
  - **İMZA:** Önce **sona-ekleme** (düşük risk, JS pozisyonel çağrı geriye-uyumlu). `CreateTerminalOpts` struct'ı Wails'te TEMİZ üretildiği bu repoda DOĞRULANMADI (value-struct input precedent'i yok). Struct istenirse önce küçük spike: `make build` → `App.d.ts`/`models.ts` gözle. `RestartTerminal` (app.go:548) ve `useTerminals.addTerminal` çağrı yerleri aynı anda güncellenir.
  - **Batch partial-success:** `OpenTeamFromConfig`'te bir agent worktree hatası verirse abort mu skip mi açıkça tanımlanır (öneri: skip + hatalı agent listesini döndür).
- **Çapraz-analiz notu (İMZA ÇAKIŞMASI):** team-config + observer-agent (`docs/PLAN-observer-agent.md`) ikisi de `CreateTerminal` imzasını değiştirir → TEK refactor turunda birleştir. **Sıra:** team-config ÖNCE girer; observer `agentMode`'u aynı yapıya alan ekleyerek sonra girer. Bu özellik önerilen sırada **2. sırada** konumlanmalı.

## Problem / Bağlam

Kullanıcının kendi sözleriyle:

> "aynı configurasyonlarda tekrardan açmanın bir yolu yok şuanda. bazı odadaki agent teamlarım standarttır ve hangi agent'a ne ad vermişim, hangi provider ile oluşturmuşum, hangi projeye yerleştirmişim o agent'ı sabittir; tekrar açmak için tek tek yine seçmem gerekiyor, baya uğraştırıcı."

Standart (tekrar eden) takımlar için her uygulama açılışında her agent'ı `SetupWizard` üzerinden tek tek elle kurmak gerekiyor: ad yaz, CLI seç, klasör seç, prompt seç, "Create Terminal". Bu, 4-6 agent'lı bir takım için açılış başına dakikalarca süren manuel bir iş. Elle giriş yüzünden agent isimlerinde typo'lar da oluşuyor (örn. "Fontend", "Backned").

**Beklenen davranış:** Bir takımın agent kompozisyonu (her agent'ın ad/provider/proje/rol/slot bilgisi) diske kaydedilsin; kullanıcı takımı seçip "Bu config ile aç" dediğinde tüm terminaller otomatik, doğru ad/provider/projeyle, doğru slotlarda açılsın.

> Not: Bu özellik **CLI session resume** (`memory/session-persistence-plan.md`) DEĞİLDİR. O plan CLI'ın kendi konuşma geçmişini `--resume` ile geri yüklemekle ilgilenir. Bu plan ise yalnızca **takım şablonunun** (agent kurulum parametrelerinin) kalıcılığıyla ilgilenir — terminaller temiz/yeni CLI oturumu olarak açılır, sadece kurulum adımları otomatikleşir. İki özellik birbirini tamamlar ama bağımsızdır.

## Mevcut Durum

### Kök neden: Agent config'leri toplanıyor ama hiçbir zaman takıma yazılmıyor

Veri modeli aslında **zaten hazır**. Eksik olan tek şey: frontend, `SetupWizard`'da topladığı agent parametrelerini takımın `agents` dizisine geri yazmıyor.

> ✅ **Denetim doğrulaması (kod-teyitli):** `AgentConfig` (store.go:17-23) ve `Team.Agents` (store.go:29) mevcut; `Create`/`Update` `agents` parametresini `save()` ile teams.json'a yazıyor; `SetupWizard.handleCreate` (SetupWizard.tsx:61-76) yalnızca `addTerminal(...)` çağırıp PTY açıyor, `Team.Agents`'a hiç yazmıyor. Eski `"agents": []` kayıtları zero-value'larla temiz deserialize olur → **migration scripti gerekmez** premisi DOĞRULANDI.

**1) Backend veri modeli agent config'i destekliyor (zaten var):**

`internal/team/store.go:16-23` — `AgentConfig` struct'ı tam olarak ihtiyacımız olan alanlara sahip:

```go
type AgentConfig struct {
    Name     string `json:"name"`
    Role     string `json:"role"`
    PromptID string `json:"prompt_id"`
    WorkDir  string `json:"work_dir"`
    CLIType  string `json:"cli_type"`
}
```

`internal/team/store.go:26-35` — `Team.Agents []AgentConfig` alanı var ve teams.json'a serialize ediliyor. `Create` (`store.go:99`) ve `Update` (`store.go:132`) fonksiyonları `agents` parametresini alıp **diske yazıyor** (`store.go:120`, `store.go:144`).

**2) App binding'leri agent config'i geçiriyor (zaten var):**

`app.go:789` `CreateTeam(name, gridLayout string, agents []team.AgentConfig)` ve `app.go:807` `UpdateTeam(id, name, gridLayout string, agents []team.AgentConfig)` — her ikisi de `agents` dizisini olduğu gibi store'a iletiyor. Yani backend tarafında dolu bir `agents` dizisi gelse sorunsuz kaydedilir.

**3) Frontend ASLA dolu bir agents dizisi göndermiyor — kök neden burada:**

- `frontend/src/App.tsx:65` — varsayılan takım `createTeam("Default", "2x2", [])` ile **boş** agents'la oluşturuluyor.
- `frontend/src/components/TabBar.tsx:14` — yeni takım `createTeam(newName.trim(), "2x2", [])` ile **boş** agents'la oluşturuluyor.
- `frontend/src/components/TerminalGrid.tsx:176` — grid layout değişince `updateTeam(team.id, team.name, layout, team.agents)` çağrılıyor; burada `team.agents` **mevcut (boş) diziyi olduğu gibi** geri gönderiyor. Yani agents hiçbir zaman güncellenmiyor, sadece grid layout değişiyor.
- `frontend/src/components/SetupWizard.tsx:61-76` — `handleCreate` agent parametrelerini (ad, CLI, workDir, promptID, slotIndex, useWorktree) toplayıp **yalnızca** `addTerminal(...)` çağırıyor (`SetupWizard.tsx:69`). `addTerminal` → `CreateTerminal` (`useTerminals.ts:61-62`) sadece **geçici bir PTY** açıyor; takımın `agents` dizisine **hiçbir yazma yapmıyor**.

Sonuç: agent kurulum bilgisi yalnızca çalışma anındaki `TerminalSession` (`frontend/src/lib/types.ts:62-69`, RAM'de Zustand store) içinde yaşıyor ve uygulama kapanınca kayboluyor.

**4) Diskteki kanıt:**

`~/.agent-chat/teams.json` içindeki **her** takımda `"agents": []` boş:

```json
{ "id": "...", "name": "ServisGuzergah", "agents": [], "grid_layout": "2x3",
  "manager_agent": "Pilot", ... }
```

`manager_agent` doluyken bile `agents` boş — çünkü `SetManager` (`store.go:156`) ayrı bir akış; agent listesini doldurmuyor.

### Çalışma anı terminal oluşturma akışı (referans)

`SetupWizard` (`SetupWizard.tsx:69`) → `useTerminals.addTerminal` (`useTerminals.ts:61`) → `CreateTerminal(teamID, agentName, workDir, cliType, promptID, useWorktree)` binding'i (`app.go:415`).

`app.go:415-544` `CreateTerminal` akışı şu parametreleri kullanıyor: `teamID`, `agentName`, `workDir`, `cliType`, `promptID`, `useWorktree`. Bunlar **tam olarak** kalıcı kılmak istediğimiz alanlar (`useWorktree` hariç — şu an `AgentConfig`'te yok). PTY oluşturulurken (`pty/manager.go:54` `Create`) `WorkDir` ve `PromptID` `PTYSession`'a restart için zaten saklanıyor (`manager.go:27-28`, `app.go:527-533`), ama bu sadece RAM'de — diske gitmiyor.

Ayrıca dikkat: `agentRole` (`AgentConfig.Role`) startup prompt'unda zaten okunuyor — `app.go:606-616` takımın `t.Agents` dizisinde isme göre eşleşen `cfg.Role`'ü arıyor. Yani `agents` dizisi dolu olsaydı rol bilgisi otomatik prompt'a girecekti; şu an boş olduğu için rol her zaman boş kalıyor. Bu, dolu `agents` dizisinin halihazırda backend'de bir tüketicisi olduğunu gösteriyor.

> ⚠️ **Denetim uyarısı (bilinçli davranış değişimi):** `agents` dizisi dolmaya başlayınca `composeAgentPrompt` (app.go:610) artık boş olmayan `Role` okuyabilir → startup-prompt çıktısı değişir. Bu, istenen ama **bilinçli** bir davranış değişimidir; round-trip'te prompt'un doğru oluştuğu test edilmeli. Üstelik upsert akışı `Role` SAĞLAMADIĞI için (wizard/CreateTerminal'da Role alanı yok), `UpsertAgent` update'te mevcut `Role`'ü KORUMALI — aksi halde her terminal oluşturma kullanıcının elle set ettiği `Role`'ü boş string'le ezer (bkz. "(A) Yakalama akışı" invariant).

### Frontend tip aynası (zaten hazır)

`frontend/src/lib/types.ts:13-19` `AgentConfig` ve `types.ts:21-30` `Team.agents: AgentConfig[]` Go struct'larını birebir yansıtıyor. `useTeams.ts:54-68` `createTeam`/`updateTeam` zaten `agents: AgentConfig[]` parametresi alıyor. Yani frontend altyapısı da hazır — sadece doldurma/çağırma mantığı eksik.

## Çözüm Tasarımı

### Genel yaklaşım

İki ayrı sorunu çözüyoruz:

- **(A) Yakalama:** Agent kurulduğunda config'i takımın `agents` dizisine yaz (persist).
- **(B) Tekrar açma:** Kayıtlı `agents` dizisinden tüm terminalleri tek tıkla otomatik aç.

Mevcut `AgentConfig`/`Team.Agents` altyapısı yeniden kullanılacak; teams.json şeması zaten `agents` alanını içerdiği için **geriye dönük uyumluluk doğal** (eski kayıtlarda `agents: []`, sadece boş — migration'sız çalışır).

### Veri modeli değişikliği

`AgentConfig`'e iki alan eklenir (`internal/team/store.go:16-23` ve aynası `frontend/src/lib/types.ts:13-19`):

| Alan | Tip | Neden |
|------|-----|-------|
| `SlotIndex` / `slot_index` | `int` | Hangi grid slotunda açılacağı; `TerminalSession.slotIndex` (`types.ts:69`) ve `TerminalGrid` slot eşlemesi (`TerminalGrid.tsx:199-210`) bunu kullanıyor. Pozisyon korunmazsa agent'lar yanlış slotta açılır (MEMORY'deki bilinen "terminal yanlış panelde açılıyor" sorunuyla da örtüşür). |
| `UseWorktree` / `use_worktree` | `bool` | `CreateTerminal`'ın 6. parametresi (`app.go:415`). Worktree tercihi config'in parçası; yeniden açışta korunmalı. |

`SlotIndex` için JSON `omitempty` **kullanılmaz** (0 geçerli bir slot). Eski kayıtlarda alan yoksa Go `0`, TS `undefined` döner; `undefined` durumunda dizideki sıraya göre fallback uygulanır (aşağıda "Geriye dönük uyumluluk").

Manager bilgisi ayrıca `AgentConfig`'e konabilir (`IsManager bool`) ya da mevcut `Team.ManagerAgent` (`store.go:32`) ile isim eşleşmesinden türetilebilir. **Tercih:** mevcut `ManagerAgent` alanını tek doğruluk kaynağı olarak korumak (çift kaynak/çelişki riskini önler); yeniden açışta `agent.Name === team.manager_agent` ise manager olarak işaretlenir. (Açık soru #3'e bakınız.)

### teams.json şema değişimi (örnek)

Öncesi:
```json
{ "name": "ServisGuzergah", "agents": [], "grid_layout": "2x3", "manager_agent": "Pilot" }
```

Sonrası (agent'lar artık dolu):
```json
{
  "name": "ServisGuzergah",
  "grid_layout": "2x3",
  "manager_agent": "Pilot",
  "agents": [
    { "name": "Pilot",    "role": "", "prompt_id": "abc", "work_dir": "/Users/yerli/proj/api",  "cli_type": "claude",  "slot_index": 0, "use_worktree": false },
    { "name": "Frontend", "role": "", "prompt_id": "",    "work_dir": "/Users/yerli/proj/web",  "cli_type": "gemini",  "slot_index": 1, "use_worktree": true  },
    { "name": "Backend",  "role": "", "prompt_id": "",    "work_dir": "/Users/yerli/proj/api",  "cli_type": "claude",  "slot_index": 2, "use_worktree": true  }
  ]
}
```

### (A) Yakalama akışı — agent kurulunca config'i yaz

İki seçenek var:

**Seçenek A1 (Önerilen): Backend `CreateTerminal` içinde otomatik upsert.**
`CreateTerminal` (`app.go:415`) zaten tüm parametrelere (`teamID, agentName, workDir, cliType, promptID, useWorktree` + slotIndex'i frontend'den ek parametre olarak alarak) sahip. PTY başarıyla oluşturulduktan sonra (`app.go:543` öncesi) takımın `agents` dizisini bu agent için upsert eden yeni bir store metodu çağrılır:

```
teamStore.UpsertAgent(teamID, AgentConfig{Name, PromptID, WorkDir, CLIType, SlotIndex, UseWorktree})
```

> ⚠️ **INVARIANT (denetim, zorunlu):** `UpsertAgent`, ad eşleşen mevcut bir agent'ı güncellerken kaydın **`Role`'ünü KORUMALI**. `CreateTerminal`/wizard `Role` sağlamadığı için yukarıdaki çağrıda `Role` alanı YOKTUR (boş bırakılır); naif bir upsert kullanıcının daha önce set ettiği `Role`'ü boş string ile ezer ve `composeAgentPrompt` (app.go:610) davranışını bozar. Upsert mantığı: ad eşleşirse `Role` hariç alanları güncelle, eşleşmezse yeni kayıt ekle.

Avantaj: Frontend'de neredeyse hiç değişiklik yok; her terminal oluşturulduğunda config otomatik kalıcılaşır. Kullanıcı ekstra bir "kaydet" adımı yapmaz — istediği davranış bu.

Dezavantaj: `CreateTerminal` imzasına yeni bir slot parametresi eklenmeli (Wails binding regenerate). **İmza yaklaşımı (denetim):** parametre imzanın SONUNA eklenir (`...promptID string, useWorktree bool, slotIndex int`) — JS pozisyonel çağrı geriye-uyumlu kalır, en düşük risk. `CreateTerminalOpts` value-struct'ı bu repoda Wails'le temiz üretildiği DOĞRULANMADIĞI için varsayılan tercih sona-ekleme; struct istenirse önce `make build` + `App.d.ts`/`models.ts` gözlemiyle küçük spike. Geçici/deneme terminalleri de kaydedilir (bkz. Açık soru #1).

**Seçenek A2: Frontend `SetupWizard.handleCreate` içinde explicit `updateTeam`.**
`addTerminal` sonrası (`SetupWizard.tsx:69`) frontend, mevcut `team.agents`'a yeni `AgentConfig`'i ekleyip/güncelleyip `updateTeam(team.id, team.name, team.grid_layout, nextAgents)` çağırır.

Avantaj: Backend imza değişikliği yok. Dezavantaj: Slot/agent senkronu frontend'de elle yönetilir; `removeTerminal`/`restartTerminal` durumlarında `agents` dizisini güncel tutma sorumluluğu da frontend'e kayar (tutarsızlık riski).

> **KARAR (denetim, kesinleşti):** A1 (backend tek-alan upsert) seçildi — tek doğruluk kaynağı backend'de, frontend ince kalır. `UpsertAgent`, `SetManager` (store.go:156) tek-alan desenini izler; pozisyonel `Store.Update` genişletilmez. Önceden Açık Soru #1'de geçen "şablona kaydet onay kutusu + otomatik-upsert" ikilemi A1 lehine ÇÖZÜLDÜ: onay kutusu eklenmez (otomatik-upsert sürtünmesiz, kullanıcının istediği "tek tıkla" deneyimi). Bu plan A1'i baz alır.

Ayrıca: agent **silindiğinde** (`removeTerminal` → `CloseTerminal`, `app.go:758`) config'in takımda kalıp kalmayacağına karar verilmeli. **Öneri:** Terminal kapatmak config'i **silmez** (şablon kalıcı olmalı). Config silme yalnızca kullanıcının açık bir "şablondan çıkar" eylemiyle yapılır (Açık soru #2).

### (B) Tekrar açma akışı — "Bu config ile aç"

**Yeni binding:** `app.go`'ya `OpenTeamFromConfig(teamID string) ([]string, error)` eklenir. İçeride:
1. `teamStore.Get(teamID)` ile takımı al.
2. `team.Agents`'ı `SlotIndex`'e göre sırala (boşsa hata/uyarı döndür: "kayıtlı config yok").
3. Her `AgentConfig` için mevcut `CreateTerminal(teamID, a.Name, a.WorkDir, a.CLIType, a.PromptID, a.UseWorktree)` akışını çağır (bu akış manager çözümü, worktree, MCP config, startup prompt'u zaten hallediyor — `app.go:432-541`).
4. Oluşan `sessionID`'leri döndür.

Manager agent zaten `team.ManagerAgent` üzerinden `resolveManagerIntent` (`app.go:378`) ile çözülüyor, ekstra iş yok.

**Frontend:**
- `useTerminals`'a `openTeamFromConfig(teamID)` aksiyonu: backend binding'ini çağırır, dönen her `(sessionID, agentConfig)` için store'a `TerminalSession` ekler (slotIndex = `agent.slot_index`).
- `TabBar.tsx` (takım sekmesi) veya `TerminalGrid` toolbar'ına (`TerminalGrid.tsx:361`) "Config ile Aç" / "Takımı Başlat" butonu. Buton yalnızca `team.agents.length > 0` ve o takımda aktif session yoksa görünür/etkin olur.
- Akış sırasında her CLI'a sıralı startup prompt gönderildiği için (idle bekleme `app.go:646-653`), terminaller arka planda kademeli açılır; UI "Açılıyor..." durumu gösterebilir.

**İdempotans / çakışma:** Açma sırasında o takımda zaten session varsa, ya (a) sadece eksik slotları doldur, ya (b) kullanıcıyı uyar. Öneri: sadece boş slotları doldur (mevcut terminallere dokunma).

### Geriye dönük uyumluluk / migration

- **Şema:** `agents` alanı zaten teams.json'da var ve boş. Yeni alanlar (`slot_index`, `use_worktree`) eski kayıtlarda yok → Go unmarshal'da zero value (`0`, `false`), TS'te `undefined`. **Migration scripti gerekmez.**
- **Boş şablon:** Mevcut tüm takımların `agents`'ı `[]`. Yeni "config ile aç" butonu bu takımlar için pasif/gizli olur; kullanıcı bir kez normal `SetupWizard` ile agent kurunca (A1 sayesinde) config otomatik dolar ve sonraki açılışlarda buton aktifleşir.
- **`SlotIndex` fallback:** Eski/eksik `slot_index` için açma sırasında dizinin indeksini (0,1,2…) slot olarak kullan.
- **Save metodu (durabilite — denetim, IN-SCOPE):** `store.go:68-74` `save()` `os.WriteFile` ile yazıyor (atomik DEĞİL). `OpenTeamFromConfig` 4-6 agent açarken her `CreateTerminal` → `UpsertAgent` → `save()` zinciri **4-6 ardışık non-atomik yazım** yapar; crash mid-batch'te kısmi/bozuk teams.json riski doğar. Bu özellik batch yazım paternini İLK KEZ yarattığı için bu iyileştirme **kapsam içidir** (kapsam dışı değil). İki seçenek: (a) `save()`'i temp-file+rename atomik yazıma çevir (hub-state `hub-state/{room}.json` deseni; CLAUDE.md "Persistence: atomic write"), VEYA (b) `OpenTeamFromConfig` içinde tüm agent'ları toplayıp tek `Update` çağrısıyla bir kez yaz. Tercih: (a) — tüm yazım yollarını korur.

## Etkilenen / Yeni Dosyalar

| Dosya | Değişiklik türü | Açıklama |
|-------|-----------------|----------|
| `internal/team/store.go` | Değişiklik | `AgentConfig`'e `SlotIndex int` + `UseWorktree bool` ekle; yeni `UpsertAgent(teamID string, cfg AgentConfig) (Team, error)` (**`Role`-koruyan** invariant). `save()`'i temp-file+rename atomik yazıma çevir (batch durabilite). `RemoveAgent` ÇIKARILDI (MVP'de tetikleyicisi yok — over-engineering). |
| `app.go` | Değişiklik | `CreateTerminal` imzasının SONUNA `slotIndex int` ekle; PTY başarısından sonra `teamStore.UpsertAgent(...)` (`origWorkDir` ile — worktree dizini değil). `RestartTerminal` (app.go:548) çağrısını yeni imzaya güncelle. Yeni binding `OpenTeamFromConfig(teamID string) ([]map[string]string, error)` (partial-success: hatalı agent'ı skip + döndür). |
| `frontend/src/lib/types.ts` | Değişiklik | `AgentConfig`'e `slot_index: number` + `use_worktree: boolean`; `TerminalSession` ile alan paritesi. |
| `frontend/src/store/useTerminals.ts` | Değişiklik | `addTerminal` `CreateTerminal` çağrısına `slotIndex` GEÇİR (şu an slotIndex'i alıyor ama Go'ya geçirmiyor, useTerminals.ts:62); yeni `openTeamFromConfig(teamID)` aksiyonu. |
| `frontend/src/components/SetupWizard.tsx` | Değişiklik (A2 seçilirse) / minimal (A1) | A1'de değişiklik yok; A2'de `updateTeam` ile agents persist. |
| `frontend/src/components/TerminalGrid.tsx` veya `TabBar.tsx` | Değişiklik | Toolbar'a "Config ile Aç / Takımı Başlat" butonu; `team.agents` boşsa devre dışı. |
| `wailsjs/go/main/App.d.ts` + `App.js` | Otomatik üretim | `wails generate module` ile yeni binding/imza üretilecek (elle düzenlenmez). |
| `docs/PLAN-team-config-persistence.md` | Yeni | Bu doküman. |

> Yeni Go paketi/dosyası gerekmiyor — mevcut `team` ve `app` katmanları yeterli.

## Adım Adım İmplementasyon

1. **Backend veri modeli:** `internal/team/store.go` — `AgentConfig`'e `SlotIndex int` ve `UseWorktree bool` ekle (JSON tag'leri `slot_index`, `use_worktree`).
2. **Backend upsert metodu:** `store.go`'ya `UpsertAgent(teamID string, cfg AgentConfig) (Team, error)` ekle — mutex altında ismi eşleşen agent'ı güncelle (**`Role`'ü KORU** — `cfg.Role` boşsa mevcut değeri ezme), yoksa ekle, `save()` çağır. Aynı turda `save()`'i temp-file+rename atomik yazıma çevir (batch durabilite, hub-state deseni). `RemoveAgent` EKLENMEZ (over-engineering, MVP'de tetikleyici yok).
3. **CreateTerminal entegrasyonu:** `app.go:415` `CreateTerminal` imzasının SONUNA `slotIndex int` ekle (sona-ekleme = JS pozisyonel çağrı geriye-uyumlu); başarılı PTY oluşturma sonrası (yaklaşık `app.go:534` civarı, return'den önce) `if teamID != "" { a.teamStore.UpsertAgent(teamID, team.AgentConfig{Name: agentName, PromptID: promptID, WorkDir: cfgWorkDir, CLIType: cliType, SlotIndex: slotIndex, UseWorktree: useWorktree}) }`. **Dikkat:** worktree açıldıysa `workDir` PTY için worktree'ye reassign edilir (`app.go:460`); config'e **kullanıcının seçtiği orijinal** dizi yazılmalı — `cfgWorkDir := workDir; if origWorkDir != "" { cfgWorkDir = origWorkDir }` (origWorkDir app.go:446'da set ediliyor). Yoksa yeniden açışta worktree-içinden-worktree hatası olur (git.go:55 mismatch). `Role` alanı verilmez (upsert mevcut Role'ü korur).
4. **Yeni binding:** `app.go`'ya `OpenTeamFromConfig(teamID string)` ekle — `team.Agents`'ı slot'a göre sırala, her biri için `CreateTerminal(...)` çağır, dönen sessionID + agentName + slotIndex listesini döndür. **Partial-success:** bir agent worktree/PTY hatası verirse abort etme; o agent'ı SKIP edip hata mesajını sonuç listesinde döndür (kullanıcı kalan terminallerle çalışmaya devam eder).
5. **Wails binding üret:** `wails generate module` (veya `make dev`/`make build` ile otomatik) ile `wailsjs` güncelle.
6. **Frontend tipler:** `frontend/src/lib/types.ts` `AgentConfig`'e `slot_index`, `use_worktree` ekle.
7. **useTerminals.addTerminal:** `CreateTerminal` çağrısına `slotIndex` parametresini GEÇİR (`useTerminals.ts:62` — şu an `addTerminal` slotIndex'i alıyor ama `CreateTerminal`'a iletmiyor, yalnızca store'da `resolvedSlotIndex` olarak tutuyor). Aynı anda `RestartTerminal`'ın çağrı yolunu da yeni imzaya uyumla.
8. **useTerminals.openTeamFromConfig:** Yeni aksiyon — `OpenTeamFromConfig(teamID)` çağır, dönen her kayıt için store'a `TerminalSession` ekle (`slotIndex` = config'ten). Hata/boş-config durumlarını yönet.
9. **UI butonu:** `TerminalGrid.tsx` toolbar'ına (`TerminalGrid.tsx:361-379`) "Takımı Config ile Aç" butonu; `team.agents.length === 0` ya da o takımda zaten session varsa devre dışı/gizli.
10. ~~**(Opsiyonel) Şablon yönetimi UI**~~ — **ÇIKARILDI (denetim, over-engineering):** Kayıtlı config görüntüleme/düzenleme/silme paneli MVP kapsamı dışı. A1 otomatik upsert + "Config ile Aç" butonu temel değeri zaten sağlıyor; yönetim UI'sı ayrı bir özellik olarak ertelenir.
11. **Doğrulama:** Aşağıdaki test/doğrulama bölümünü uygula.

## Açık Sorular / Karar Gerektiren Noktalar

1. ~~**Her terminal otomatik mi kaydedilsin (A1), yoksa explicit "şablona kaydet" butonu mu?**~~ **ÇÖZÜLDÜ (denetim):** A1 (otomatik upsert), onay kutusu YOK. Çelişkili "A1 + onay kutusu" önerisi sadeleştirildi — onay kutusu A1'in sürtünmesizlik avantajını iptal ediyordu. Deneme/geçici terminallerin de yazılması kabul edilir; kullanıcı ileride şablondan çıkarabilir.
2. **Terminal kapatınca config silinsin mi?** Öneri: **hayır** — şablon kalıcı olmalı, aksi halde "kapat → kayıp" yaşanır. Silme yalnızca açık "şablondan çıkar" eylemiyle. Onaylanmalı.
3. **Manager bilgisi nerede tutulsun?** Mevcut `Team.ManagerAgent` (tek isim) korunsun mu, yoksa `AgentConfig.IsManager` mu? Multi-manager planı (`docs/PLAN-multi-manager.md`) varsa, manager'ı `AgentConfig` üzerinde flag'lemek o planla daha uyumlu olabilir. **Öneri: şimdilik `ManagerAgent`'ı koru, türetme yap; multi-manager gelince `AgentConfig.IsManager`'a geç.**
4. **Aynı agent adı birden çok slotta?** `UpsertAgent` ismi anahtar kabul ediyor. Aynı ad iki slotta olursa? Validation `ValidateName` var ama benzersizlik yok. **Öneri: takım içinde agent adı benzersiz olmalı (upsert anahtarı = ad).**
5. ~~**`prompt_id` kalıcılığı ve prompt silinmesi.**~~ **KAPANDI (denetim, over-engineering çıkarıldı):** `composeAgentPrompt` (app.go:619-623) silinmiş `prompt_id` için ZATEN sessizce boş prompt'a degrade ediyor — kabul edilebilir davranış. Spekülatif "eksik prompt uyarısı" UI'sı MVP kapsamından ÇIKARILDI.
6. ~~**Worktree yeniden açış davranışı.**~~ **KAPANDI (denetim, kod-teyitli):** `git.CreateWorktree` (git.go:37-68) eşleşen worktree'yi (aynı repo + aynı branch) idempotent **yeniden kullanıyor** (`created=false`); path başka repo/branch'e aitse HATA veriyor. Dolayısıyla `use_worktree=true` agent yeniden açılınca mevcut worktree güvenle reuse edilir — **`origWorkDir` (kullanıcının seçtiği ana repo) config'e yazıldığı sürece** (madde 3). Madde 6 (origWorkDir) bu nedenle zorunlu.
7. **Açış sırası ve manager önceliği.** Manager terminalinin diğerlerinden önce açılması gerekir mi (routing lock için)? Slot sırası 0'dan başladığı için manager genelde slot 0'da; garanti için açış öncesi manager'ı öne almak düşünülebilir.

## Doğrulama / Test

**Go birim testleri (`internal/team/store_test.go` — yeni):**
- `UpsertAgent` yeni agent ekliyor; aynı ad tekrar gelince güncelliyor (eklemiyor).
- **`UpsertAgent` `Role`-koruma invariantı:** `Role="Backend"` set edilmiş agent için `cfg.Role=""` ile upsert çağrılınca mevcut `Role` KORUNUYOR (boşalmıyor).
- `slot_index` / `use_worktree` serialize/deserialize round-trip doğru. `slot_index=0` `omitempty` olmadan diske yazılıyor (kaybolmuyor).
- Eski (alan içermeyen) teams.json yüklenince zero-value'larla bozulmadan açılıyor (geriye dönük uyumluluk).
- Eşzamanlılık: 50+ goroutine'le `UpsertAgent` çağrısı race-free (mevcut orchestrator test deseni — CLAUDE.md "Testing Patterns" — taklit edilir).

**Manuel / entegrasyon doğrulaması:**
1. Boş bir takım oluştur → `SetupWizard` ile 2-3 agent kur (farklı CLI/dizin/slot) → `~/.agent-chat/teams.json`'da ilgili takımın `agents` dizisinin **dolduğunu** doğrula.
2. Uygulamayı kapat-aç → "Takımı Config ile Aç" → tüm terminallerin **doğru ad/CLI/dizin/slotta** açıldığını doğrula.
3. Worktree'li agent için açışta worktree'nin doğru ana repodan türetildiğini, "worktree içinden worktree" hatası olmadığını doğrula (madde 3'teki origWorkDir kararı).
4. Manager'lı takımda (örn. `ServisGuzergah`/`Pilot`) açışta manager routing'in (`syncHubManager`, `app.go:472`) doğru kurulduğunu doğrula.
5. Kayıtlı `prompt_id`'li agent'ta startup prompt'unun beklenen şekilde gönderildiğini terminalde gözlemle.

**Regresyon:**
- `go test ./...` (özellikle `internal/orchestrator`, `internal/team`) yeşil.
- Mevcut `CreateTerminal` çağrı yollarının (`RestartTerminal` fonksiyonu app.go:548; içindeki `CreateTerminal` çağrısı app.go:576) yeni `slotIndex` parametresiyle kırılmadığını doğrula — `RestartTerminal` da slotIndex geçirmeli (session'dan türetilebilir; gerekirse `PTYSession`'a slotIndex eklenmeli).
- **`Role`-koruma regresyonu:** Kullanıcı `Role` set edilmiş bir agent'ı yeniden açıp/restart edince (her ikisi de `CreateTerminal` → `UpsertAgent` tetikler, `Role` sağlamadan), teams.json'daki mevcut `Role`'ün BOŞALMADIĞINI doğrula. Ayrıca `composeAgentPrompt` (app.go:610) `agents` dizisi dolunca beklenen rol metnini prompt'a kattığını gözlemle (bilinçli davranış değişimi).

## Tahmini Efor (S/M/L)

**M (Orta).** Veri modeli ve teams.json şeması büyük ölçüde hazır olduğu için risk düşük; iş esas olarak (a) `CreateTerminal` imza değişikliği + `UpsertAgent` upsert mantığı, (b) yeni `OpenTeamFromConfig` orkestrasyon binding'i, (c) frontend buton/aksiyon ve Wails binding regenerate'ten ibaret. Worktree origWorkDir kararı ve RestartTerminal'ın slotIndex'i en olası tökezleme noktaları. Tahmini 1-2 günlük geliştirme + test.
