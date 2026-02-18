# Agent Chat — Geliştirme Planı

Bu doküman planlanmış tüm özellikleri, bilinen hataları ve araştırma notlarını içerir.

---

## Bilinen Hatalar

### BUG-001: Terminal Yanlış Panelde Açılıyor

- **Durum:** Beklemede
- **Sorun:** Herhangi bir panelde "Create Terminal" tıklandığında, terminal o panelde değil her zaman en sol üstteki boş slotta açılıyor.
- **Kök Neden:** `TerminalGrid.tsx` terminalleri sıralı dizi olarak render ediyor — yeni session her zaman dizinin sonuna ekleniyor (`push`). Grid slotları sırayla doldurulur: önce mevcut terminaller, sonra boş slotlar `SetupWizard` olur. Bu yüzden slot 2'deki wizard'dan terminal oluşturulunca, yeni terminal slot 1'e (bir sonraki boş slot) yerleşiyor.
- **Çözüm Yönü:** Session'lara `slotIndex` bilgisi eklenmeli, veya sessions dizisi sparse/pozisyonel olmalı ki terminal doğru slota yerleşsin.
- **İlgili Dosyalar:**
  - `frontend/src/components/TerminalGrid.tsx` — grid slot ataması
  - `frontend/src/store/useTerminals.ts` — `addTerminal` fonksiyonu (diziye push)
  - `frontend/src/components/SetupWizard.tsx` — `slotIndex` prop'u var ama kullanılmıyor

---

## Özellik 1: Resizable/Collapsible Sidebar + Unlimited Panel Mode

### Context

Sidebar şu an sabit 280px genişlikte, kapatılamıyor. Grid layout sistemi 8 sabit preset'e sınırlı (1x1 - 4x3). Kullanıcı sidebar'ı sürükleyerek boyutlandırabilmeli, kapatıp açabilmeli ve sınırsız panel modu eklenebilmeli.

### Değişecek Dosyalar

| Dosya | Değişiklik |
|-------|-----------|
| `frontend/src/App.tsx` | app-body'yi PanelGroup'a çevir, sidebar collapse toggle ekle |
| `frontend/src/components/TerminalGrid.tsx` | Custom mode render branch'i ekle |
| `frontend/src/components/TerminalPane.tsx` | Opsiyonel `onRemove` prop + close butonu |
| `frontend/src/components/GridSelector.tsx` | "Custom" butonu ekle |
| `frontend/src/lib/types.ts` | `CUSTOM_LAYOUT` sabiti, `isCustomLayout()` guard |
| `frontend/src/styles/globals.css` | Sidebar sabit genişlik kaldır, toggle/close buton stilleri |

### Adım 1: `types.ts` — Custom layout desteği

- `CUSTOM_LAYOUT = "custom"` sabiti export et
- `isCustomLayout(layout)` guard fonksiyonu export et
- `parseGrid()` ve `gridCapacity()` fonksiyonlarını "custom" için guard'la (NaN koruması)

### Adım 2: `GridSelector.tsx` — Custom buton ekle

- `CUSTOM_LAYOUT` import et
- `LAYOUTS.map()` sonrasına "Custom" butonu ekle (`+` ikonu ile, grid preview yerine)

### Adım 3: `TerminalPane.tsx` — Close butonu

- Props'a `onRemove?: () => void` ekle
- `.terminal-header-actions` içine koşullu close (`×`) butonu render et

### Adım 4: `TerminalGrid.tsx` — Custom mode render

- `isCustomLayout` import et, `removeTerminal` store'dan çek
- `renderCustomMode()` fonksiyonu ekle:
  - Dikey PanelGroup, her session bir Panel
  - Son panel = SetupWizard (yeni terminal ekleme)
  - Her terminal'de `onRemove` prop'u ile close butonu
- Ana render'da `isCustomLayout` branch'i ekle
- Toolbar'daki sayacı custom modda "X terminals" olarak göster

### Adım 5: `App.tsx` — Sidebar'ı resizable + collapsible yap

- `react-resizable-panels`'dan `Panel, Group as PanelGroup, Separator as PanelResizeHandle` import et
- `ImperativePanelHandle` tipini import et, `useRef` ekle
- `sidebarRef = useRef<ImperativePanelHandle>(null)` + `sidebarCollapsed` state
- `toggleSidebar()` fonksiyonu: `ref.current.collapse()` / `expand()`
- `.app-body` içeriğini horizontal PanelGroup ile sar:
  - Sol Panel: `<TerminalGrid />` (minSize={30})
  - PanelResizeHandle (sidebar toggle butonu içinde)
  - Sağ Panel: `<Sidebar />` (collapsible, defaultSize={20}, minSize={15}, maxSize={35})
- Sidebar Panel'e `onCollapse`/`onExpand` callback'leri ile `sidebarCollapsed` sync

### Adım 6: `globals.css` — Stil güncellemeleri

- `.sidebar`: `width: 280px; min-width: 280px;` kaldır → `width: 100%; height: 100%;`
- `.app-panel-group` ekle
- `.sidebar-toggle-btn` ekle (PanelResizeHandle içinde, ortada)
- `.resize-handle-sidebar` ekle (12px genişlik, buton barındıran handle)
- `.terminal-btn-remove` ekle (`×` butonu stili)

### Doğrulama

1. `cd frontend && npm run build` — derleme hatası olmadığını kontrol et
2. `make dev` — uygulamayı başlat
3. Test: Sidebar sürükleyerek boyutlandırılabilmeli
4. Test: Toggle butonuyla sidebar kapanıp açılabilmeli
5. Test: GridSelector'da "Custom" seçince tek panel ile başlamalı
6. Test: Custom modda "Create Terminal" ile yeni panel eklenebilmeli
7. Test: Custom modda `×` butonu ile terminal kapatılabilmeli

---

## Özellik 2: Session Persistence & Terminal Restart

### Context

Kullanıcı bir CLI terminal'inde çalışırken `exit` yaptığında Claude Code gibi CLI'lar session ID veriyor. Bu ID'ler saklanırsa daha sonra `--resume` ile kaldığı yerden devam edilebilir. Ayrıca terminal'i yeniden başlatma (restart) özelliği de gerekiyor.

### 2.1 Terminal Restart

- Terminal header'ına restart butonu ekle (mevcut focus butonunun yanına)
- Restart: mevcut PTY'yi kapat → aynı parametrelerle (teamID, agentName, workDir, cliType, promptID) yeni PTY oluştur
- Session ID değişir ama kullanıcı açısından aynı slot'ta yeni terminal açılır
- Backend'de `RestartTerminal(sessionID)` fonksiyonu gerekli

### 2.2 CLI Session ID Yakalama

- PTY output'u parse ederek CLI session ID'lerini yakala:
  - Claude Code: çıkışta `Resume this session with: claude --resume <uuid>` yazıyor
  - Gemini/Copilot: `--list-sessions` komutu ile listelenebilir
- Yakalanan session ID'yi `TerminalSession` struct'ına kaydet
- Go tarafında: `internal/pty/manager.go`'da output'u dinleyip regex ile session ID parse et
- Frontend'de: session ID'yi göster (terminal header'da tooltip veya sidebar'da)

### 2.3 Session Persistence (Uygulama Restart)

- Uygulama kapanırken aktif session bilgilerini kaydet:
  - `~/.agent-chat/sessions.json` → `{teamID, agentName, workDir, cliType, cliSessionID, slotIndex}`
- Uygulama açılınca:
  - Kaydedilmiş session'ları yükle
  - Kullanıcıya "Önceki oturumu devam ettirmek ister misiniz?" sor
  - Evet ise: CLI'ları `--resume <cliSessionID>` (Claude) veya `--continue` (Gemini/Copilot) ile başlat
  - Hayır ise: temiz başla

### 2.4 Resume Stratejisi (CLI Bazlı)

| CLI | Resume Komutu | Not |
|-----|--------------|-----|
| Claude Code | `claude --resume <uuid>` | UUID PTY output'tan parse edilecek |
| Gemini CLI | `gemini --resume` | En son session'ı otomatik yükler |
| Copilot CLI | `copilot --continue` | En son kapanan session'ı yükler |
| Shell | Yeni başlat | Shell session persist edilmez |

### Değişecek Dosyalar

| Dosya | Değişiklik |
|-------|-----------|
| `internal/pty/manager.go` | CLI session ID parse (output regex), `RestartSession()` |
| `app.go` | `RestartTerminal()` binding, session persistence (save/load) |
| `frontend/src/lib/types.ts` | `TerminalSession`'a `cliSessionID?: string` ekle |
| `frontend/src/store/useTerminals.ts` | `restartTerminal()` action, `setCLISessionID()` |
| `frontend/src/components/TerminalPane.tsx` | Restart butonu ekle |
| `frontend/src/components/Sidebar.tsx` veya `AgentStatus.tsx` | Session ID gösterimi |

### İmplementasyon Sırası

1. Terminal restart (en basit, bağımsız)
2. CLI session ID yakalama (PTY output parsing)
3. Session persistence (restart + session ID'ye bağımlı)

### Doğrulama

1. Terminal restart: Restart butonuna tıkla → aynı slotta yeni terminal açılmalı
2. Claude Code'da `exit` yap → session ID yakalanmalı
3. Uygulamayı kapat/aç → "Devam et" seçeneği çıkmalı → CLI'lar resume ile açılmalı

---

## Araştırma: CLI Resume/Session Mekanizmaları

### Claude Code

- **Resume:** `claude --resume <uuid>` veya `claude -r <uuid>`
- **Son session:** `claude --continue` veya `claude -c` (mevcut dizindeki en son)
- **Picker:** `claude --resume` (argümansız) → interaktif seçici
- **Fork:** `claude --resume <id> --fork-session`
- **Depolama:** `~/.claude/projects/<hash>/` dizininde `.jsonl` dosyaları
- **Çıkışta session ID:** Evet, her zaman UUID gösteriyor
- **Bilinen bug:** Resume'da session_id yeniden oluşturuluyor (#8069, #8024)

### Gemini CLI

- **Resume:** `gemini --resume` (en son session), `gemini --resume <index>`, `gemini --resume <uuid>`
- **Listeleme:** `gemini --list-sessions`
- **Silme:** `gemini --delete-session <index|uuid>`
- **In-session:** `/resume` slash komutu (interaktif picker)
- **Depolama:** `~/.gemini/tmp/<hash>/chats/`
- **Auto-save:** v0.20.0+ varsayılan olarak açık
- **Çıkışta session ID:** Hayır, picker'da gösteriyor

### GitHub Copilot CLI

- **Resume:** `copilot --resume` (picker), `copilot --resume <id>` (spesifik)
- **Son session:** `copilot --continue` (en son kapanan)
- **Depolama:** `~/.copilot/session-state/`
- **Cross-machine:** v0.0.376+ remote session desteği
- **Çıkışta session ID:** Hayır, picker'da gösteriyor

### Karşılaştırma

| Özellik | Claude Code | Gemini CLI | Copilot CLI |
|---------|------------|------------|-------------|
| Auto-save | Evet | Evet | Evet (incremental) |
| Çıkışta ID | Evet (UUID) | Hayır | Hayır |
| UUID ile resume | `--resume <uuid>` | `--resume <uuid>` | `--resume <id>` |
| Son session | `--continue` | `--resume` | `--continue` |
| Proje bazlı | Evet (dizin hash) | Evet (dizin hash) | Evet (cwd metadata) |
| Session fork | Evet | Hayır | Hayır |

### Agent-Chat İçin Çıkarımlar

1. Tüm CLI'lar session persistence destekliyor → yakalayıp saklayabiliriz
2. Claude Code çıkışta UUID veriyor → PTY output'tan parse edilebilir
3. Gemini ve Copilot'ta `--continue` veya `--resume` ile son session devam ettirilebilir
4. Session ID'leri `TerminalSession` struct'ına `cliSessionID` alanı olarak eklenebilir
5. Uygulama restart'ında `--resume <id>` ile CLI'lar yeniden başlatılabilir
