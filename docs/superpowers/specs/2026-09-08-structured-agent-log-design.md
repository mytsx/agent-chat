# Yapısal agent konuşma/olay logu + analiz aracı

Issue: [#101](https://github.com/mytsx/agent-chat/issues/101) — şemsiye [#102](https://github.com/mytsx/agent-chat/issues/102)

## Problem

Agentların birbiriyle ne konuştuğunu ve neden birbirini kaçırdığını sonradan inceleyecek bir yer yok.

Bugün tek bir düz metin dosyası var: `~/.agent-chat/mcp-server.log`. Hub ve **tüm** MCP instance'ları aynı dosyaya append ediyor. Dört ayrı eksiği var:

- **Rotasyon yok.** 11 MB / 107.597 satır, 2026-02-17'den beri hiç dönmemiş.
- **Yapısal değil.** `log.Printf` düz metni; oda, agent ve korelasyon kimliği ancak regex ile kazınabiliyor.
- **Konuşma içeriği yok.** `send_message` yalnızca `contentLen=N` yazıyor. "Agent neden yanlış anladı" sorusu bu logdan cevaplanamıyor.
- **Bağıntı kurulamıyor.** Bir agent'ın düşmesi ile o sırada kaybolan mesaj arasındaki ilişki elle eşleştiriliyor.

Mevcut arşiv (`internal/hub/archive.go`, JSONL) yalnızca odadan **çıkan** mesajları (truncate / clear) saklıyor. Canlı akışın ve olayların — join, leave, stale-eviction, reconnect — kaydı değil.

Bu iş #98 (agentların odadan düşmesi) ve #99 (oda/isim karışıklığı) için ölçüm altyapısıdır. Önce ölçemezsek düzeldiğini de bilemeyiz.

## Kapsam dışı

- Orchestrator'ın PTY bildirimleri (`notify` olayı) — desktop tarafında yaşıyor, #98'de eklenir.
- Görsel analiz paneli (Wails UI) — ayrı follow-up issue.
- Mevcut 47 `logger.Printf` çağrısının kaldırılması. Yapısal log onların yerine değil, yanına gelir; insan okuması için düz metin log kalır.

## Mimari

### Yeni paket: `internal/eventlog`

`internal/sessionlog` ve `internal/usage` gibi bağımsız leaf paket. `internal/hub`'a bağımlılığı yok; hub ona bağımlı.

```go
// Event, oda hayatındaki tek bir olaydır. JSONL'de bir satır.
type Event struct {
    TS      float64        `json:"ts"`                // unix saniye (float64 — proje konvansiyonu)
    Kind    string         `json:"kind"`
    Room    string         `json:"room"`
    Agent   string         `json:"agent,omitempty"`
    To      string         `json:"to,omitempty"`
    MsgID   int            `json:"msg_id,omitempty"`
    Reason  string         `json:"reason,omitempty"`
    Content string         `json:"content,omitempty"`
    Detail  map[string]any `json:"detail,omitempty"`
}
```

Paketin dış yüzeyi dar:

```go
func New(dataDir string, opts ...Option) (*Logger, error)
func (l *Logger) Append(e Event)      // async, asla bloke etmez, asla panik etmez
func (l *Logger) Flush()              // barrier; testler ve shutdown için
func (l *Logger) Close() error
func Read(dataDir, name string) ([]Event, error)  // analiz için; name = oda adı veya "hub", rotasyon kuşakları dahil, eskiden yeniye
```

`Append` çağıran açısından ateşle-unut. Kanal doluysa olay **düşürülür** ve bir sayaç artar (`dropped`); log yazımı hub'ın routing yolunu asla yavaşlatmaz veya kilitlemez.

### Dosya düzeni

```
~/.agent-chat/event-log/
    {oda}.jsonl        # oda başına olay akışı
    {oda}.jsonl.1      # rotasyon kuşakları
    {oda}.jsonl.2
    hub.jsonl          # odaya ait olmayan olaylar: hub_start, hub_stop
```

Oda adı kullanıcı etkisindeki bir yol parçası. `archive.go`'daki savunmayı birebir devralıyoruz: `validation.ValidateName` **ve** `strings.HasPrefix(path, dir+string(os.PathSeparator))` containment kontrolü. `hub.jsonl` sabit ad olduğu için oda yolundan geçmez.

Dosya izni `0600` (arşiv ile aynı) — konuşma içeriği barındırıyor.

### Yazma yolu

`archive.go`'daki desenin aynısı: buffered channel + tek yazar goroutine + shutdown'da drain.

```
Append() ──> eventCh (cap 512) ──> writer goroutine ──> {oda}.jsonl
                  │ dolu ise
                  └──> düşür, dropped++
```

Neden async: hub'ın sıcak yolunda (`send_message`, `get_messages`) senkron disk I/O olmasın. Neden tek yazar goroutine: aynı dosyaya eşzamanlı append yok, dolayısıyla dosya kilidi de yok.

### Tek yazar: hub

Yalnızca hub process'i yazar. MCP instance'ları yazmaz.

Gerekçe: hub zaten routing otoritesi — join, leave, send, read, stale eviction hepsi oradan geçiyor. Tek process yazınca bugünkü `mcp-server.log`'un temel derdi (çok sayıda process aynı dosyaya append ediyor) hiç doğmuyor.

MCP'nin görüp hub'ın göremediği tek sınıf olay var: "hub'a hiç bağlanamadım" (log kanıtı: 12.751 `hub.port not found`, 5.331 `connection refused`). Bunlar tanımı gereği hub'a ulaşamaz. Mevcut `mcp-server.log`'da kalmaya devam ederler ve analiz aracı o dosyayı ayrıca okur.

## Olay noktaları

Hepsi mevcut kod yollarında; yeni akış icat edilmiyor.

| Kind | Yer | Taşıdığı alanlar |
|---|---|---|
| `hub_start` | `hub.Run` | `detail.pid`, `detail.port` |
| `hub_stop` | `hub.Shutdown` | — |
| `connect` | `hub.go` register | `detail.client_type` |
| `disconnect` | `hub.go` unregister | `agent` (varsa) |
| `join` | `protocol.go` `handleJoinRoom` | `agent`, `detail.role` |
| `leave` | unregister / explicit leave / stale | `agent`, `reason` = `disconnect` \| `explicit` \| `stale` |
| `stale_evict` | `room.go` `cleanupStaleLocked` | `agent`, `detail.idle_sec` |
| `send` | `protocol.go` `handleSendMessage` | `agent`, `to`, `msg_id`, `content`, `detail.to_in_room` |
| `route` | manager gateway yeniden yönlendirmesi | `agent`, `to`, `detail.rerouted_to` |
| `read` | `protocol.go` `handleGetMessages` | `agent`, `detail.since_id`, `detail.returned`, `detail.max_id` |
| `error` | ilgili hata dalları | `reason`, `detail.err` |

İki alan analizi doğrudan mümkün kılıyor:

- **`send.detail.to_in_room`** — gönderim anında alıcının roster'da olup olmadığı. Hub bunu zaten biliyor; kaydetmek bedava ve analiz tarafında roster'ı olay akışından yeniden kurmaya gerek bırakmıyor.
- **`read.detail.max_id`** — o okumanın gördüğü en yüksek mesaj kimliği. Agent başına ilerleme (high-water mark) bundan çıkar; "gönderildi ama okunmadı" sorusu buna dayanır.

### İçerik

`send.content` mesajın tam metnidir. Bu logun varlık sebebi: "agent neden yanlış anladı" sorusu içerik olmadan cevaplanamaz. Veri makinede kalır, hiçbir yere gönderilmez, izni `0600`.

Tek sınır: 8 KB üstü içerik kırpılır ve `detail.content_truncated = true` işaretlenir. Bir agent'ın yapıştırdığı devasa dosya içeriği log dosyasını şişirmesin.

## Rotasyon

`Append` sırasında yazar goroutine boyutu kontrol eder. Sınır aşılınca `{oda}.jsonl` → `{oda}.jsonl.1`, eski `.1` → `.2` diye kayar; `.3` silinir.

- Boyut sınırı: **16 MB** kuşak başına
- Kuşak sayısı: **3** (yani oda başına en fazla ~64 MB)
- Yaş sınırı: **30 gün** — açılışta bundan eski kuşak dosyaları silinir

Bugünkü 11 MB'lık, yedi aylık, dönmemiş tek dosya bir daha oluşmasın.

## Analiz aracı

`mcp-server-bin` üçüncü bir mod kazanır. Bugün: `--hub` → hub sunucusu, bayraksız → stdio MCP sunucusu. Ekleniyor: `--analyze` → rapor bas, çık.

Yeni ikili dosya üretmiyoruz; `//go:embed build/mcp-server-bin` kısıtı zaten tek ikiliye bağlı, ikinci bir hedef Makefile'a ve dağıtıma yük olurdu.

```
mcp-server-bin --analyze [--room X] [--since 24h] [--legacy-log]
```

Dört soruyu cevaplar:

**1. Agent başına düşme sayısı ve sebebi.** `leave` olayları `reason`'a göre gruplanır. `disconnect` çok, `explicit` az ise sorun bağlantı kararlılığında; `stale` baskınsa sorun `staleTimeout`'ta. #98'in iki mekanizmasını birbirinden bu ayırır.

**2. Odada olmayan alıcıya gitmiş mesajlar.** `send` olayları `detail.to_in_room == false` ile filtrelenir. Gönderen, alıcı, zaman ve içerik listelenir. #99'un ölçüsü.

**3. Gönderilmiş ama hiç okunmamış mesajlar.** Her alıcı için `read` olaylarındaki en yüksek `detail.max_id` bulunur; o alıcıya gönderilmiş ve bu eşiğin üstünde kalan `send` olayları listelenir.

**4. Hub kesinti pencereleri ve o pencerede kaybolan mesajlar.** `hub.jsonl`'deki `hub_stop` → `hub_start` aralıkları pencereleri verir. `--legacy-log` ile `mcp-server.log` da okunur; bu pencerelere düşen `hub.port not found` ve `connection refused` satırları sayılır — yani "kaç agent, ne kadar süre hub'sız kaldı".

`--legacy-log` best-effort: eski düz metin logdan yalnızca hub bağlantı hataları ve `MCP server initialized — defaultRoom=` satırları çıkarılır. Yapısal log birikene kadar geriye dönük görüş sağlar.

## Hata davranışı

Log yazımı hiçbir koşulda hub'ı bozmaz.

- `New` başarısızsa (dizin açılamıyor, disk yok) hub **no-op logger** ile çalışır; `Append` sessizce yutar. Hub başlamayı reddetmez.
- Yazar goroutine'de yazma hatası mevcut `h.logger`'a bir kez raporlanır, tekrarında susulur (log fırtınası olmasın).
- Kanal dolduğunda olay düşürülür. Düşen sayısı `hub_stop` olayının `detail.dropped` alanına yazılır — sessiz kayıp olmaz, analiz bunu görür. `hub_stop` kanaldan değil, `Close` içinden **senkron** yazılır; aksi halde kendisi de düşebilirdi.

## Test

TDD. Her davranış önce başarısız test.

`internal/eventlog` saf leaf olduğu için doğrudan test edilebilir:

- Enjekte edilebilir saat — `internal/sessionlog`'daki `s.now()` deseni
- `t.Run` ile table-driven alt testler (proje konvansiyonu)
- Rotasyon: sınırı aşan yazımdan sonra kuşak dosyaları ve içerikleri doğrulanır
- Yaş sınırı: eski kuşak açılışta siliniyor mu
- Yol güvenliği: `../` içeren oda adı reddediliyor mu
- İçerik kırpma: 8 KB üstü kırpılıp `content_truncated` işaretleniyor mu
- Eşzamanlılık: `-race` altında çok sayıda goroutine'den `Append`
- Kanal doluluğu: yazar bloklandığında `Append` bloke olmuyor, `dropped` artıyor
- No-op logger: `New` hatasında `Append` panik etmiyor

Hub entegrasyonu `internal/hub` testlerinde: bir join/send/leave akışından sonra `Flush` + `Read` ile beklenen olay dizisi doğrulanır.

Analiz: sentetik olay akışları verilip dört sorunun çıktısı doğrulanır.

## Sıra

1. `internal/eventlog` paketi — tip, yazar, rotasyon, no-op yol (hub'a dokunmadan, tam test kapsamı)
2. Hub entegrasyonu — olay noktalarının bağlanması, `hub_start`/`hub_stop` yaşam döngüsü
3. `--analyze` modu — dört rapor
4. `--legacy-log` geriye dönük okuyucu

Her adım kendi commit'i; hepsi tek PR.
