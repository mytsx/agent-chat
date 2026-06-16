# Oda Başlangıç Açıklaması (Intro / Charter)

## Problem / Bağlam

Kullanıcının kendi sözleri:

> "Yeni bir oda kurmak istediğimde o odanın da bir başlangıç hikayesini bir yere yazabilmeliyim, başlangıç açıklaması olmalı."

Bugün yeni bir oda/takım kurulduğunda odanın amacı, bağlamı, kuralları hiçbir yere yazılamıyor. Odaya katılan her agent yalnızca base prompt + global prompt + (varsa) seçilen kütüphane prompt'u ile başlıyor; "bu oda ne için kuruldu, hangi projeyi/görevi konuşuyoruz, agent'lardan ne bekleniyor" bilgisini içermiyor. Kullanıcı her seferinde bunu agent'lara elle anlatmak zorunda kalıyor.

İstenen: Oda kurulurken serbest metin bir **başlangıç açıklaması / oda misyonu / bağlam** yazılabilsin; bu metin kalıcı olarak saklansın ve odaya katılan **her agent'a başlangıç prompt'unda otomatik enjekte** edilsin. Böylece bütün agent'lar daha ilk mesajdan itibaren ortak bağlamı bilir.

**Kritik gerçek (altyapı kısmen MEVCUT):** `teams.json`'daki `Team.CustomPrompt` (`custom_prompt`) alanı zaten tanımlı ve startup prompt akışında zaten **okunup enjekte ediliyor** — fakat hiçbir yerde **yazılmıyor**, dolayısıyla her zaman boş. Yani enjeksiyon borusunun yarısı (okuma + enjekte) hazır; eksik olan tek şey alanı UI'dan doldurup persist etmek.

## Denetim Revizyonu (2026-06-16, kod-doğrulamalı)

**Verdict:** `minor-revisions`. Plan mimari olarak sağlam; aşağıdaki düzeltmeler kod-doğrulamasıyla yerine işlendi.

**Uygulanan düzeltmeler:**

- **Persist yolu netleşti — pozisyonel `Store.Update` genişletme REDDEDİLDİ.** Bunun yerine `SetManager` desenini (tek alan, hedefli endpoint — `internal/team/store.go:155-176`) izleyen ayrı bir `SetCustomPrompt(id, text)` endpoint'i kullanılır. Gerekçe (doğrulandı): tek `updateTeam` çağrı-yeri `frontend/src/components/TerminalGrid.tsx:176` (`handleLayoutChange`'ten tetiklenir) `custom_prompt` geçmiyor — eğer `Store.Update` imzası pozisyonel olarak genişletilirse **her grid-layout değişiminde charter `""`'a SIFIRLANIR**. Nüans: bugün `Update` `s.teams[i]`'yi in-place mutate ettiği için `CustomPrompt`'a hiç dokunmaz; bu yüzden charter şu an **korunuyor** — bug YALNIZCA reddedilen "imzayı genişlet" yolunda ortaya çıkar.
- **Alan: `custom_prompt` yeniden kullanılır (yeni `charter`/`intro` alanı EKLENMEZ).** Enjeksiyon borusu zaten bu alana bağlı (`app.go:608` → `internal/cli/startup.go:23`); migration yok, eski `teams.json` (`""`) geri uyumlu.
- **Sanitizasyon zorunlu hâle getirildi.** Charter bracketed-paste ile aynen PTY'ye gidiyor; ESC ve bracketed-paste sonlandırıcısı (`ESC[201~`) + kontrol baytları strip/escape edilmeli ve yumuşak bir uzunluk sınırı konmalı. `ValidateName` charter'a **uygulanmaz** (serbest metin), ama minimal kontrol-karakteri temizliği şarttır.
- **Ölü-veri uyarısı.** `team.Agents` tüm takımlarda bugün boş; `agentRole` çözümü (`app.go:610` döngüsü) şu an etkin değil/ölü. Charter mantığı `t.Agents`'in dolu olduğu varsayımına KURULMAZ.
- **Enjeksiyon sırası sabitlendi:** `ComposeStartupPrompt`'ta `[charter (değişmez core)] → [oda özeti (türetilmiş)] → [son N ham mesaj]`. Bu sıra #13-C (oda özeti) ile **aynı refactor'da** sabitlenmeli.
- **UX netleştirildi:** düzenlenen charter yalnızca **yeni başlayan** agent'lara etki eder — UI bunu açıkça belirtmeli (yoksa kullanıcı bug sanar).
- **MVP daraltıldı:** yalnızca yazılır alan + create-time textarea + tek düzenleme yolu. Değişken substitution (`{{TEAM_NAME}}`) ve `SetupWizard` salt-okunur gösterimi **ertelenir**.

**Çapraz-analiz notu:** BİRLEŞME — room-intro (charter) + room-summary-archive (#13) enjeksiyon fazı **aynı `ComposeStartupPrompt` noktasında** birleşir. team-config (#12) ile aynı `SetManager`-deseni "tek-alan-endpoint" disiplinini paylaşır. Bu özelliğin önerilen uygulama sırası: **3. sıra**.

## Mevcut Durum

### Veri modeli — alan var ama hiç yazılmıyor

- `internal/team/store.go:33` — `Team` struct'ında alan tanımlı:
  ```go
  CustomPrompt string `json:"custom_prompt"`
  ```
- `internal/team/store.go:99-129` — `Create(name, gridLayout, agents)` yeni `Team` oluştururken `CustomPrompt`'u **set etmiyor** (parametre olarak da almıyor). Yani her yeni oda `custom_prompt: ""` ile kaydediliyor.
- `internal/team/store.go:132-153` — `Update(id, name, gridLayout, agents)` yalnızca `Name`, `GridLayout`, `Agents` alanlarını güncelliyor; `CustomPrompt`'a dokunmuyor — `s.teams[i]`'yi **in-place mutate** ettiği için `CustomPrompt` mevcut değerini **korur** (sıfırlamaz). Yani charter sonradan düzenleme imkânı yok ama mevcut layout/agent güncellemeleri charter'ı bozmaz. **Önemli:** Bu güvenli davranış yalnızca imza pozisyonel genişletilmediği sürece geçerlidir (bkz. Denetim Revizyonu).
- `internal/team/store.go:155-176` — `SetManager` ayrı bir hedefli update örneği (tek alan güncelleme deseni); charter için **seçilen desen budur** (`SetCustomPrompt`), pozisyonel `Update` genişletmesi değil.

### Enjeksiyon borusu — ZATEN BAĞLI

- `app.go:603-616` — `composeAgentPrompt()` takımı `teamStore.Get(teamID)` ile çekip:
  ```go
  teamPrompt = t.CustomPrompt
  ```
  ataması yapıyor. Yani `custom_prompt` dolu olsaydı bağlam zaten agent'a giderdi.
- **Ölü-veri uyarısı:** `app.go:610` aynı blokta `t.Agents` üzerinde dönerek `agentRole` çözüyor; ancak `team.Agents` bugün tüm takımlarda boş olduğundan bu döngü etkin değil (ölü). Charter mantığı `t.Agents`'in dolu olduğu varsayımına KURULMAMALI.
- `app.go:637` — `cli.ComposeStartupPrompt(... teamPrompt ...)` çağrısıyla bu metin startup prompt'a aktarılıyor.
- `internal/cli/startup.go:9-56` — `ComposeStartupPrompt`, `teamPrompt`'u **3. parça** olarak ekliyor (sıra: base → global → **team (custom_prompt)** → selected/library → join talimatı). Boşsa `TrimSpace` ile atlanıyor (startup.go:23-25). Yani dolu olduğu anda doğru sırada, doğru yerde devreye girer; ek kod gerekmez.
- `app.go:640+` — `sendStartupPrompt()` bu metni PTY'ye gönderiyor (CLI idle olunca, bracketed-paste ile). Burada da değişiklik gerekmez.

### Backend bindings (Wails) — charter parametresi yok

- `app.go:788-804` — `CreateTeam(name, gridLayout, agents)` → `teamStore.Create(...)`. Charter parametresi yok.
- `app.go:806-824` — `UpdateTeam(id, name, gridLayout, agents)` → `teamStore.Update(...)`. Charter parametresi yok.
- `frontend/wailsjs/go/main/App.d.ts:12` — `CreateTeam(arg1, arg2, arg3)` (üç parametre, charter yok).
- `frontend/wailsjs/go/main/App.d.ts:54` — `UpdateTeam(arg1..arg4)` (charter yok).

### Frontend — giriş alanı YOK

- `frontend/src/components/TabBar.tsx:12-17` — odalar **gerçekte burada** oluşturuluyor: yalnızca takım adı (`newName`) alınıp `createTeam(newName.trim(), "2x2", [])` çağrılıyor. Charter / açıklama girişi yok.
- `frontend/src/components/SetupWizard.tsx` — bu bileşen **oda değil, oda içindeki tek bir terminali (agent)** kuruyor (agent adı, CLI tipi, workdir, startup prompt seçimi, manager checkbox). Oda misyonu için doğru yer **değil**; yine de kullanıcı oda bağlamını burada da görebilir (salt-okunur) düşünülebilir.
- `frontend/src/store/useTeams.ts:54-68` — `createTeam` / `updateTeam` Zustand action'ları sırasıyla `CreateTeam` / `UpdateTeam` binding'lerini sarıyor; charter geçmiyor.
- `frontend/src/lib/types.ts:21-30` — `Team` arayüzünde `custom_prompt: string` zaten mevcut (Go struct'ı yansıtıyor). Yani frontend tipinde alan hazır.

### Prompt/değişken altyapısı (opsiyonel zenginleştirme için)

- `internal/prompt/store.go:151-157` — `RenderPrompt(content, vars)` ve `internal/prompt/store.go:197-219` — `extractVariables` `{{VAR}}` substitution destekliyor. Charter metninde `{{TEAM_NAME}}` gibi değişkenler kullanılmak istenirse bu mekanizma yeniden kullanılabilir (zorunlu değil).

### Prompt dosyaları

- `prompts/base_prompt.md` — MCP araç listesi + "join_room" talimatı (her agent'a gider).
- `prompts/manager_prompt.md` — manager routing politikası. Charter, bu ikisinin **arasına/yanına** değil, mevcut `teamPrompt` slotuna girer; sıralamayı bozmaz.

**Özet:** Borunun %50'si hazır. Eksik olan tek zincir: **UI girişi → Wails binding → `team.Store` persist**. `custom_prompt` bir kez yazıldığı an, agent enjeksiyonu kendiliğinden çalışır.

## Çözüm Tasarımı

### 1. Alan seçimi: mevcut `custom_prompt` mi, yeni `intro`/`charter` mı?

**Karar (DOĞRULANDI): Mevcut `custom_prompt` alanı yeniden kullanılsın; persist için ayrı `SetCustomPrompt(id, text)` endpoint'i (SetManager deseni) kullanılsın.**

Gerekçe:
- Enjeksiyon borusu (`app.go:608`, `startup.go:23`) zaten bu alana bağlı; yeni alan eklemek aynı boruyu ikinci kez kurmak demek olur (gereksiz iş + iki "team prompt" kavramı karışıklığı).
- Persist yolu pozisyonel `Store.Update` genişletmesi DEĞİL: tek `updateTeam` çağrı-yeri (`TerminalGrid.tsx:176`, layout değişiminde) `custom_prompt` geçmediği için imza genişletilirse charter her layout değişiminde sıfırlanır. Bu yüzden `SetManager` deseninde tek-alan `SetCustomPrompt(id, text)` endpoint'i tercih edilir (bkz. Denetim Revizyonu).
- `teams.json`'da alan zaten boş kayıtlı; migration gerekmez, eski odalar `""` ile uyumlu kalır.
- Frontend tipi (`types.ts:28`) zaten `custom_prompt` taşıyor.

**Alternatif (değerlendirildi, önerilmiyor):** Ayrı `Charter string \`json:"charter"\`` alanı eklemek. Avantajı semantik netlik ("charter = kullanıcı misyonu", "custom_prompt = serbest ek talimat" ayrımı). Dezavantajı: ikinci enjeksiyon noktası + iki alanın startup sırasındaki önceliği için yeni karar gerekir. Bugünkü tek-kullanıcı/tek-amaç ihtiyaç için fazladan karmaşıklık. **Eğer ileride "kullanıcı misyonu" ile "teknik ek talimat" gerçekten ayrışacaksa** o zaman ayrı alana geçilir; şimdilik `custom_prompt` tek alan olarak hem misyon hem ek talimatı taşır.

> UI'da etiket olarak teknik "custom_prompt" yerine kullanıcıya dönük **"Oda Açıklaması / Başlangıç Bağlamı"** ifadesi gösterilir; depolama alanı adı (`custom_prompt`) içeride kalır.

### 2. UI — nereye girilir?

Oda gerçekte `TabBar.tsx`'te tek bir input ile oluşturuluyor. Tasarım:

- **Oluşturma (create):** `TabBar`'daki "yeni oda" akışı, tek satırlık ad input'undan, ad + çok satırlı "Oda Açıklaması (opsiyonel)" textarea içeren küçük bir form/popover'a yükseltilir. Açıklama opsiyonel; boş bırakılırsa bugünkü davranış aynen korunur.
- **Düzenleme (edit):** Var olan bir odanın açıklaması sonradan güncellenebilmeli. Aktif takımın yanında/sekme bağlam menüsünde "Oda Ayarları / Açıklamayı Düzenle" girişi açan aynı formu yeniden kullanan bir düzenleme modali. `updateTeam` üzerinden persist.
- **(Opsiyonel) Görünürlük:** `SetupWizard` içinde, agent kurulurken o odanın açıklaması salt-okunur bir bilgi kutusunda gösterilebilir — kullanıcı agent'ı kurarken bağlamı hatırlar. Zorunlu değil; ikinci faza bırakılabilir.

### 3. Enjeksiyon noktası

**Yeni enjeksiyon kodu yazılmaz.** `custom_prompt` doldurulduğu an mevcut zincir devreye girer:

```
app.go composeAgentPrompt() → teamPrompt = t.CustomPrompt   (app.go:608)
   → cli.ComposeStartupPrompt(... teamPrompt ...)           (app.go:637)
      → parts[2] = teamPrompt                               (startup.go:23-25)
         → sendStartupPrompt() PTY'ye gönderir              (app.go:640+)
```

Sıralama: **base → global → ODA AÇIKLAMASI → library prompt → join talimatı**. Bu sıra mantıklı: genel kurallar önce, oda-özgül misyon ortada, agent-özgül rol/talimat sonra.

Bu otomatik olarak hem **manager** hem normal agent'a gider (her ikisi de `composeAgentPrompt`'tan geçer), istenen davranış budur.

### 4. Düzenlenebilirlik & persist

- `team.Store.Create` charter parametresi alacak ve `Team.CustomPrompt`'a yazacak.
- **Düzenleme `Store.Update` üzerinden YAPILMAZ.** Yeni `SetCustomPrompt(id, text)` endpoint'i (`SetManager` deseni) charter'ı bağımsız günceller. Gerekçe (doğrulandı): pozisyonel `Update` genişletilirse tek çağrı-yeri `TerminalGrid.tsx:176` (layout değişimi) `custom_prompt` geçmediğinden charter **her grid-layout değişiminde `""`'a sıfırlanır**. `Update` olduğu gibi bırakılır (in-place mutate, `CustomPrompt`'a dokunmaz → mevcut charter korunur).
- Açıklamanın çalışan agent'lara **anında** yansımayacağı not edilmeli: değişiklik yalnızca **bir sonraki** agent başlatıldığında/terminal restart edildiğinde startup prompt'a girer. Bu kabul edilebilir; UI'da küçük bir ipucu ("Değişiklik yeni başlatılan agent'lara uygulanır") gösterilebilir.

### 5. "Oda Özeti" özelliği ile ilişkisi (ÖNEMLİ — karıştırılmamalı)

Bu özellik, `feature-ideas-room-team.md`'deki **#3 "Oda Özeti + Arşiv"** özelliğiyle **aynı amaca hizmet eder ama kaynağı farklıdır**:

| | Bu özellik (Oda Açıklaması / Charter) | Oda Özeti (#3, ayrı plan) |
|---|---|---|
| Kaynak | **Kullanıcının elle yazdığı** kalıcı misyon metni | Geçmiş konuşmadan **otomatik üretilen** özet |
| Ne zaman | Oda kurulurken / düzenlenirken | Oda kapanırken / yeniden açılırken |
| Yaşam süresi | Kalıcı (odanın kimliği) | Türetilmiş (her oturumda yenilenebilir) |
| Hedef | Agent'lara "neden buradayız" | Agent'lara "şu ana kadar ne konuşuldu" (geçmiş bağlamı ham mesaj akışından önce verir) |

İkisi **birbirini tamamlar** ve enjeksiyon fazları **aynı `ComposeStartupPrompt` noktasında birleşir**: ileride startup prompt'a hem charter (kalıcı misyon) hem özet (geçmiş bağlam) birlikte enjekte edilebilir. Önerilen sıra: `[charter (değişmez core)] → [oda özeti (türetilmiş)] → [son N ham mesaj]`. Bu planda **yalnızca charter** ele alınır; özet ayrı planın (#13) işidir ve bu sıra #13-C ile **aynı refactor'da** sabitlenmeli. Tasarım, gelecekte aynı startup-compose noktasına ayrı bir `summary` parçası eklenmesini engellemeyecek şekilde tutulmalıdır.

## Etkilenen / Yeni Dosyalar

| Dosya | Tür | Değişiklik |
|---|---|---|
| `internal/team/store.go` | Değişiklik | `Create` imzasına charter (`customPrompt string`) parametresi ekle; `Team.CustomPrompt`'a yaz. **Düzenleme için ayrı `SetCustomPrompt(id, text)` endpoint'i ekle** (`SetManager` deseni, satır 155-176). `Update` **dokunulmaz** (pozisyonel genişletme charter'ı layout değişiminde sıfırlar). Alan (satır 33) zaten var. |
| `app.go` | Değişiklik | `CreateTeam` binding'ine charter parametresi ekle; ayrıca yeni `SetCustomPrompt` binding'i (charter düzenleme için). `UpdateTeam` **dokunulmaz**. Charter PTY'ye girmeden önce **sanitize edilir** (ESC / `ESC[201~` / kontrol baytları strip, yumuşak uzunluk sınırı). Enjeksiyon (`composeAgentPrompt`, satır 608) zaten hazır — **dokunulmaz**. |
| `internal/cli/startup.go` | Dokunulmaz | `teamPrompt` slotu (satır 23) zaten charter'ı taşıyor. Değişiklik gerekmez. |
| `frontend/wailsjs/go/main/App.d.ts` + `App.js` | Üretilir | `wails generate module` ile yeniden üretilir (yeni imzalar). Elle düzenlenmez. |
| `frontend/src/lib/types.ts` | Dokunulmaz (muhtemelen) | `Team.custom_prompt` (satır 28) zaten var. |
| `frontend/src/store/useTeams.ts` | Değişiklik | `createTeam` / `updateTeam` action imzalarına `customPrompt` ekle, binding'lere geçir. |
| `frontend/src/components/TabBar.tsx` | Değişiklik | Yeni-oda akışını ad + açıklama textarea içeren forma yükselt; `createTeam(name, layout, [], customPrompt)`. |
| `frontend/src/components/RoomSettings.tsx` (veya benzeri) | **Yeni (opsiyonel)** | Var olan odanın açıklamasını düzenleyen modal; `updateTeam` çağırır. (TabBar içine de gömülebilir.) |
| `frontend/src/components/SetupWizard.tsx` | Değişiklik (opsiyonel, faz 2) | Oda açıklamasını salt-okunur bilgi kutusunda göster. |
| `frontend/src/index.css` (veya ilgili stil) | Değişiklik | Yeni form/textarea/modal stilleri. |

## Adım Adım İmplementasyon

### Faz 1 — Backend persist (charter'ı yazılabilir yap)
1. `internal/team/store.go`: `Create(name, gridLayout string, agents []AgentConfig, customPrompt string)` imzasına çevir; oluşturulan `Team`'e `CustomPrompt: customPrompt` ekle (satır 111-118 bloğu).
2. `internal/team/store.go`: **`Update`'e DOKUNMA.** Charter'ı bağımsız güncellemek için `SetManager` (satır 155-176) desenini izleyen `SetCustomPrompt(id, text string) (Team, error)` endpoint'i ekle: eşleşen takımda `s.teams[i].CustomPrompt = sanitize(text)` yaz + `s.save()`. (Pozisyonel `Update` genişletmesi tek çağrı-yeri `TerminalGrid.tsx:176` layout değişiminde charter'ı sıfırlardı — bu yüzden ayrı endpoint.)
3. `sanitizeCharter(text)` yardımcısı: ESC (`0x1B`), bracketed-paste sonlandırıcısı (`ESC[201~`) ve yazdırılamaz kontrol baytlarını strip/escape et; satırsonu/tab korunur; yumuşak uzunluk sınırı (örn. >2000 karakter uyarısı/kırpma). `ValidateName` UYGULANMAZ (serbest metin).
4. `go build ./...` ile derlemenin bozulmadığını doğrula (`Create` imzası değişti, çağıranlar güncellenmeli; `Update` aynı kaldığından çağrı-yerleri etkilenmez).

### Faz 2 — Wails binding'leri
5. `app.go`: `CreateTeam` imzasına `customPrompt string` ekle, `teamStore.Create`'e ilet. Yeni `SetCustomPrompt(teamID, text string)` binding'i ekle → `teamStore.SetCustomPrompt`. `UpdateTeam` **değişmez**.
6. `wails generate module` (veya `make dev`/`make build` üzerinden) çalıştırıp `App.d.ts`/`App.js` binding'lerini yenile (yeni `CreateTeam` imzası + `SetCustomPrompt`).

### Faz 3 — Frontend store + tipler
7. `frontend/src/lib/types.ts`: `Team.custom_prompt` zaten var — doğrula, gerekirse hiçbir şey yapma.
8. `frontend/src/store/useTeams.ts`: `createTeam` imzasına `customPrompt?: string` ekle (varsayılan `""`); ayrı `setCustomPrompt(id, text)` action'ı ekle → yeni `SetCustomPrompt` binding'i. `updateTeam` imzası **değişmez** (charter taşımaz).

### Faz 4 — UI: oluşturma
9. `frontend/src/components/TabBar.tsx`: yeni-oda akışını ad input'undan ad + çok satırlı "Oda Açıklaması (opsiyonel)" textarea içeren bir form/popover'a genişlet; `handleCreate`'i `createTeam(name, "2x2", [], description)` çağıracak şekilde güncelle.
10. `App.tsx:65`'teki otomatik "Default" oda oluşturma çağrısını yeni imzaya uyumla (boş açıklama geçir).

### Faz 5 — UI: düzenleme
11. Aktif odanın açıklamasını düzenleyebilen bir modal/popover ekle (yeni `RoomSettings` bileşeni ya da `TabBar` içi); **`setCustomPrompt(id, description)`** ile persist et (`updateTeam` DEĞİL — layout/agents'a hiç dokunmaz, charter'ı izole eder).
12. UI ipucu (zorunlu): "Açıklama değişikliği yalnızca **yeni başlatılan** agent'lara uygulanır." Yoksa kullanıcı, çalışan agent'ların güncellenmemesini bug sanar.

### Faz 6 — (Opsiyonel) Görünürlük
13. `SetupWizard.tsx`: terminal kurulurken o odanın açıklamasını salt-okunur göster.

## Açık Sorular / Karar Gerektiren Noktalar

1. ~~**`custom_prompt` mi yeni `charter` alanı mı?**~~ **KARAR VERİLDİ:** `custom_prompt` yeniden kullanılır (boru hazır, migration yok). İleride "kullanıcı misyonu" ile "serbest ek talimat" gerçekten ayrışırsa ayrı alana geçilir.
2. ~~**`Update` davranışı:**~~ **KARAR VERİLDİ:** `Update`'e dokunulmaz; ayrı `SetCustomPrompt` endpoint'i kullanılır. (Pozisyonel `Update` genişletmesi `TerminalGrid.tsx:176` layout-değişimi çağrısında charter'ı sıfırlardı — doğrulandı.)
3. **Canlı yeniden enjeksiyon:** Açıklama değişince çalışan agent'lara otomatik enjekte edilsin mi, yoksa yalnızca yeni/restart edilen agent'lara mı? **KARAR (önerilen):** yalnızca yeni/restart (basit, mevcut akışla uyumlu, UI'da belirtilir). Otomatik canlı enjeksiyon orchestrator/PTY'ye dokunmayı gerektirir — kapsam dışı.
4. ~~**Değişken substitution:**~~ **ERTELENDİ:** MVP'de düz metin. `{{TEAM_NAME}}` gibi `internal/prompt` substitution ileride eklenebilir; ilk sürüm kapsamı dışı.
5. ~~**Uzunluk/sanitizasyon:**~~ **KARAR VERİLDİ (zorunlu):** Charter PTY'ye bracketed-paste ile aynen gittiği için sanitizasyon şart — ESC / `ESC[201~` / kontrol baytları strip/escape + yumuşak uzunluk sınırı (örn. >2000 karakter). `ValidateName` UYGULANMAZ (serbest metin), ama minimal kontrol-karakteri temizliği şart.
6. **"Oda Özeti" ile birleşik enjeksiyon:** İleride charter + otomatik özet birlikte enjekte edilecek; enjeksiyon fazı **aynı `ComposeStartupPrompt` noktasında** birleşir (sıra: charter → özet → son N ham mesaj). Bu plan kapsamı dışı ama #13-C ile aynı refactor'da sabitlenmeli.

## Doğrulama / Test

- **Birim (Go):** `internal/team/store_test.go` (yeni) — `Create` ile charter yazıldığını ve `Get`/`List`'te döndüğünü; `Update` ile değiştirilebildiğini; boş charter'ın geri uyumlu olduğunu doğrula.
- **Birim (Go):** `internal/cli/startup_test.go` (mevcut) — `ComposeStartupPrompt`'a dolu `teamPrompt` verildiğinde çıktının 3. parça olarak içerdiğini; boş verildiğinde atlandığını doğrula (boru zaten var; bu testle regression koruması eklenir).
- **Manuel uçtan uca:**
  1. Yeni oda kur, açıklama gir → `~/.agent-chat/teams.json`'da `custom_prompt` dolu mu?
  2. Odaya bir Claude/Gemini agent ekle → agent'a giden startup prompt'unda açıklama metni görünüyor mu (PTY'de gözle / `~/.agent-chat/mcp-server.log` ile çapraz)?
  3. Açıklamayı düzenle → yeni agent'ta güncel metin; eski (çalışan) agent değişmemiş.
  4. Açıklamayı boş bırakarak oda kur → bugünkü davranışla aynı (regression yok).
- **Geri uyumluluk:** Eski `teams.json` (charter `""`) sorunsuz yükleniyor ve davranış değişmiyor.

## Tahmini Efor (S/M/L)

**S (Küçük).** Enjeksiyon borusu zaten mevcut (`app.go:608`, `startup.go:23`); iş esasen birkaç imzaya alan eklemek (`team.Store.Create/Update`, iki Wails binding), binding'leri yeniden üretmek ve `TabBar`'a bir textarea + opsiyonel bir düzenleme modali koymaktan ibaret. Yeni mimari, yeni servis veya orchestrator/PTY değişikliği yok. Opsiyonel düzenleme modali + `SetupWizard` görünürlüğü dahil edilirse S/M sınırına yaklaşır.
