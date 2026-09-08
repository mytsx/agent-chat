# Yapısal agent konuşma/olay logu + analiz aracı

Issue: [#101](https://github.com/mytsx/agent-chat/issues/101) — şemsiye [#102](https://github.com/mytsx/agent-chat/issues/102)

## Problem

Agentların birbiriyle ne konuştuğunu ve neden birbirini kaçırdığını sonradan inceleyecek bir yer yok.

Bugün tek bir düz metin dosyası var: `~/.agent-chat/mcp-server.log`. Hub ve **tüm** MCP instance'ları aynı dosyaya append ediyor. Dört eksiği var:

- **Rotasyon yok.** 11 MB / 107.597 satır, 2026-02-17'den beri hiç dönmemiş.
- **Yapısal değil.** `log.Printf` düz metni; oda, agent ve korelasyon kimliği ancak regex ile kazınabiliyor.
- **Konuşma içeriği yok.** `send_message` yalnızca `contentLen=N` yazıyor. "Agent neden yanlış anladı" sorusu bu logdan cevaplanamıyor.
- **Bağıntı kurulamıyor.** Bir agent'ın düşmesi ile o sırada kaybolan mesaj arasındaki ilişki elle eşleştiriliyor.

Mevcut arşiv (`internal/hub/archive.go`, JSONL) yalnızca odadan **çıkan** mesajları (truncate / clear) saklıyor. Canlı akışın ve olayların — join, leave, stale-eviction, reconnect — kaydı değil.

Bu iş #98 (agentların odadan düşmesi) ve #99 (oda/isim karışıklığı) için ölçüm altyapısıdır. Önce ölçemezsek düzeldiğini de bilemeyiz.

## Kapsam dışı

- Orchestrator'ın PTY bildirimleri (`agent_chat.notification.sent`) — desktop tarafında yaşıyor, #98'de eklenir.
- Görsel analiz paneli (Wails UI) — ayrı follow-up issue.
- Dağıtık izleme (OpenTelemetry SDK, OTLP dışa aktarım, span'ler) — alan adları buna hazır seçiliyor ama SDK bu issue'da bağlanmıyor; #100'de mcp-go v1.0.0'ın `tracing` paketiyle birlikte.
- Mevcut 47 `logger.Printf` çağrısının kaldırılması. Bu bilinçli: düz metin log, yapısal logun **yanında** kalır. Yapısal log hattı bozulduğunda elde kalan tek şey odur.

## Araştırma ve gerekçeler

Tasarımdaki her karar mevcut endüstri pratiğine dayanıyor. Kaynaklar en altta.

### Yayım API'si: `log/slog` (standart kütüphane)

Go 1.21 yapısal loglamayı standart kütüphaneye taşıdı. Yeni bir Go servisi için makul varsayılan budur: sürüm çalkantısı yok, tedarik zinciri yüzeyi yok, Go uyumluluk garantisi altında. Projede Go 1.25.5 var, doğrudan kullanılabilir.

Kendi `Event` struct'ımızı `json.Marshal` ile yazmak yerine `slog.Handler` kullanmak ayrıca şunu kazandırıyor: seviye filtreleme, `Handler` arayüzü sayesinde çoklu hedef, ve ileride OTLP köprüsü takma imkânı.

### Alan adları: OpenTelemetry semantic conventions

Alan adlarını kendimiz uydurmuyoruz. OpenTelemetry'nin GenAI ve MCP semantic convention'ları tam da bu alanı — agent, konuşma, MCP tool çağrısı — kapsıyor ve Google Cloud, AWS, Azure, Datadog tarafından destekleniyor. Standart adlar kullanınca log dosyası ileride herhangi bir gözlemlenebilirlik aracına dönüştürülebilir; uydurma adlar kullanırsak dönüştürülemez.

Doğrudan işimize yarayan, hazır tanımlı alanlar:

| OTel alanı | Bizdeki karşılığı |
|---|---|
| `gen_ai.conversation.id` | oda adı ("a conversation (session, thread)") |
| `gen_ai.agent.name` | agent adı |
| `gen_ai.tool.name` | MCP tool adı |
| `gen_ai.input.messages` | mesaj içeriği (opt-in) |
| `mcp.method.name` | MCP metodu |
| `mcp.session.id` | MCP oturumu |
| `mcp.protocol.version` | MCP protokol sürümü |
| `jsonrpc.request.id` | korelasyon kimliği |
| `network.transport` | `websocket` (hub) / `pipe` (stdio) |
| `error.type` | düşük kardinaliteli hata sınıfı |

`event.name` alanı OTel log veri modelinde olayın **sınıfını** benzersiz tanımlar ve **dinamik değer içeremez** — değişken şeyler attribute olur. Adlarımız bu kurala uyar ve `agent_chat.` ile ad alanına alınır.

Projeye özgü, OTel'de karşılığı olmayan alanlar `agent_chat.` ön ekiyle yazılır. Standart alanı olan hiçbir şey için kendi adımızı uydurmuyoruz.

### Tek akış, oda başına dosya değil

İlk taslakta oda başına ayrı JSONL vardı. Yanlış:

- Hub kesintisi tüm odaları aynı anda etkiler; oda başına dosyada bu soru cevaplanamaz.
- Rotasyon N dosya için ayrı ayrı yönetilir.
- Oda adı dosya adına girdiği için yol kaçışı (path traversal) savunması gerekir.

Standart log hattı pratiği tek akış + attribute ile filtrelemedir. Oda bir **alan değeri** olunca üç sorun da ortadan kalkar. Tek dosya: `~/.agent-chat/events.jsonl`.

### Rotasyon: lumberjack

`gopkg.in/natefinch/lumberjack.v2` bu iş için fiili standart: boyut sınırı, yedek sayısı, sıkıştırma, yaş sınırı; `io.WriteCloser` olduğu için `slog`'a doğrudan takılır. **Geçişli bağımlılığı yok** (`go.mod`'u boş), 5.4k yıldız, v2.2.1 stabil.

Bilinen zayıflığı çok-process'li yazımdır; bizde yazan tek process var (hub), dolayısıyla geçerli değil.

Rotasyonu elle yazmak alternatifti; eşzamanlı rename, yedek sayımı ve yaş temizliği ince işler — bu alanın referans kütüphanesini kullanmak doğrusu.

### İçerik yakalama: açık opt-in

OTel, mesaj içeriği taşıyan alanları (`gen_ai.input.messages` vb.) **opt-in** işaretler ve şöyle der: "Instrumentations SHOULD NOT capture this attribute by default. Capture SHOULD be gated by an explicit user opt-in, for example `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT`."

Bizde içerik yakalama **açık bir ayardır** ve varsayılanı `true`'dur. Bu, spesifikasyonun varsayılanından bilinçli bir sapmadır; gerekçesi:

- Bu logun varlık sebebi zaten içerik. #101'in sorduğu "agent neden yanlış anladı" sorusu içerik olmadan cevaplanamaz.
- Veri kullanıcının kendi makinesinde kalır, hiçbir yere gönderilmez, dosya izni `0600`.

Kapatma anahtarı standart adı taşır: `AGENT_CHAT_CAPTURE_MESSAGE_CONTENT=false`. `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` de okunur ki OTel alışkanlığı olan biri beklediği düğmeyi bulsun.

İçerik 8 KB'da kırpılır ve `agent_chat.content.truncated = true` işaretlenir — OTel'in "instrumentations should provide filtering/truncation mechanisms" tavsiyesi.

### İzleme (tracing) için hazırlık

mcp-go v1.0.0, `tracing` paketiyle bir `Tracer` arayüzü, `WithTracer` / `WithPropagator` sunucu seçenekleri ve `github.com/mark3labs/mcp-go/otel` adresinde bir OpenTelemetry adaptörü getiriyor. Bağlam yayılımı `params._meta` içindeki `traceparent` ile yapılıyor.

Bu issue SDK bağlamıyor. Ama log kaydında `trace_id` ve `span_id` alanları **ayrılmıştır**: doldurulabildiklerinde doldurulur, doldurulamadıklarında yazılmaz. #100'de tracer takıldığında olay logu ile izler kendiliğinden birbirine bağlanır. Şimdilik korelasyon `jsonrpc.request.id` üzerinden kurulur — hub isteklerinde zaten var (`types.Request.ID`).

## Mimari

### Yeni paket: `internal/eventlog`

`internal/sessionlog` ve `internal/usage` gibi bağımsız leaf paket. `internal/hub`'a bağımlılığı yok; hub ona bağımlı.

```go
// Logger, olayları OTel semantic convention alan adlarıyla JSONL'e yazar.
// Sıfır değeri kullanılmaz; New ya da NopLogger ile kurulur.
type Logger struct { ... }

type Options struct {
    Dir            string // varsayılan: <dataDir>
    MaxSizeMB      int    // varsayılan 32
    MaxBackups     int    // varsayılan 5
    MaxAgeDays     int    // varsayılan 30
    Compress       bool   // varsayılan true
    CaptureContent bool   // varsayılan true; bkz. içerik yakalama
    BufferSize     int    // varsayılan 1024
    now            func() time.Time // testler için
}

func New(opts Options) (*Logger, error)
func NopLogger() *Logger                  // New hata verdiğinde hub bununla çalışır
func (l *Logger) Log(name string, attrs ...slog.Attr)  // async, asla bloke etmez
func (l *Logger) Dropped() uint64
func (l *Logger) Flush()
func (l *Logger) Close() error
```

`Log` çağıran açısından ateşle-unut. Kanal doluysa olay **düşürülür** ve `dropped` sayacı artar; log yazımı hub'ın routing yolunu asla yavaşlatmaz, kilitlemez, panik ettirmez.

Yazma zinciri:

```
Log() ──> eventCh (cap 1024) ──> yazar goroutine ──> slog.JSONHandler ──> lumberjack ──> events.jsonl
              │ dolu ise
              └──> düşür, dropped++
```

Kayıt biçimi (slog `JSONHandler` çıktısı, tek satır):

```json
{"time":"2026-09-08T11:04:23.918Z","level":"INFO","msg":"agent_chat.message.sent",
 "event.name":"agent_chat.message.sent",
 "gen_ai.conversation.id":"MapegCbs","gen_ai.agent.name":"Backend",
 "agent_chat.recipient.name":"Frontend","agent_chat.recipient.in_room":false,
 "agent_chat.message.id":4711,"jsonrpc.request.id":"7f3a…",
 "gen_ai.input.messages":"Servis katmanını ayırdım, endpoint …"}
```

`msg` ve `event.name` aynı değeri taşır: `msg` slog'un zorunlu alanı ve düz okumada işe yarar, `event.name` OTel'in olay sınıfı alanıdır.

### Tek yazar: hub

Yalnızca hub process'i yazar. MCP instance'ları yazmaz.

Gerekçe: hub zaten routing otoritesi — join, leave, send, read, stale eviction hepsi oradan geçiyor. Tek process yazınca hem bugünkü `mcp-server.log`'un temel derdi (çok sayıda process aynı dosyaya append ediyor) doğmuyor, hem de lumberjack'in çok-process zayıflığı devre dışı kalıyor.

MCP'nin görüp hub'ın göremediği tek sınıf olay var: "hub'a hiç bağlanamadım" (log kanıtı: 12.751 `hub.port not found`, 5.331 `connection refused`). Bunlar tanımı gereği hub'a ulaşamaz. Mevcut `mcp-server.log`'da kalmaya devam ederler ve analiz aracı o dosyayı ayrıca okur.

## Olay kataloğu

`event.name` değerleri sabit ve dinamik parça içermez. Hepsi mevcut kod yollarında; yeni akış icat edilmiyor.

| `event.name` | Yer | Ek alanlar |
|---|---|---|
| `agent_chat.hub.started` | `hub.Run` | `server.port`, `agent_chat.pid` |
| `agent_chat.hub.stopped` | `hub.Shutdown` | `agent_chat.events.dropped` |
| `agent_chat.client.connected` | `protocol.go` `handleIdentify` | `agent_chat.client.type`, `network.transport` |
| `agent_chat.client.disconnected` | `hub.go` unregister | `gen_ai.agent.name`, `error.type` |
| `agent_chat.agent.joined` | `protocol.go` `handleJoinRoom` | `gen_ai.agent.name`, `agent_chat.agent.role` |
| `agent_chat.agent.left` | unregister / explicit leave | `agent_chat.leave.reason` = `disconnect` \| `explicit` |
| `agent_chat.agent.evicted` | `room.go` `cleanupStaleLocked` | `agent_chat.agent.idle_seconds` |
| `agent_chat.message.sent` | `protocol.go` `handleSendMessage` | `agent_chat.recipient.name`, `agent_chat.recipient.in_room`, `agent_chat.delivery.target`, `agent_chat.message.id`, `gen_ai.input.messages` |
| `agent_chat.message.rerouted` | manager gateway | `agent_chat.recipient.name`, `agent_chat.reroute.target` |
| `agent_chat.messages.read` | `protocol.go` `handleGetMessages` **ve** `handleGetAllMessages` | `agent_chat.read.since_id`, `agent_chat.read.returned`, `agent_chat.read.message_ids`, `agent_chat.read.max_id` |
| `agent_chat.room.reset` | `handleClearRoom`, `handleDeleteRoom` | `agent_chat.room.lifecycle` = `cleared` \| `deleted` |
| `agent_chat.error` | ilgili hata dalları | `error.type`, `mcp.method.name` |

Her kayıtta ortak: `gen_ai.conversation.id` (oda, varsa), `jsonrpc.request.id` (varsa), `trace_id` / `span_id` (varsa).

Üç alan analizi doğrudan mümkün kılıyor:

- **`agent_chat.recipient.name` + `.in_room`** — göndericinin yazdığı alıcı ve o adın roster'da olup olmadığı. Hub bunu gönderim anında zaten biliyor; kaydetmek bedava ve analiz tarafında roster'ı olay akışından yeniden kurmaya gerek bırakmıyor. 2. rapor buna dayanır.
- **`agent_chat.delivery.target`** — mesajın *fiilen* kimin için kaydedildiği. Manager gateway araya girdiğinde bu, alıcıdan farklıdır (mesaj manager'a gider). Okuma ilerlemesi buna göre ölçülür; aksi halde manager'lı bir odadaki neredeyse tüm trafik sonsuza kadar "okunmamış" görünürdü.
- **`agent_chat.read.message_ids`** — okumanın döndürdüğü mesajların **tam listesi**, yalnızca en yükseği değil. `ReadMessages` limit dolduğunda yalnızca en yeni kuyruğu döndürür, dolayısıyla en yüksek kimlik alttakilerin görüldüğünü kanıtlamaz: eski bir doğrudan mesaj yeni broadcast'lerce dışarı itilebilir. İlerleme bir **küme**dir, watermark değil. `read.max_id` yalnızca bu alandan önce yazılmış akışlar ve liste `MaxReadIDs`'i aştığında (`read.ids_truncated`) kaba yedek olarak kullanılır.

## Analiz aracı

`mcp-server-bin` üçüncü bir mod kazanır. Bugün: `--hub` → hub sunucusu, bayraksız → stdio MCP sunucusu. Ekleniyor: `--analyze` → rapor bas, çık.

Yeni ikili dosya üretmiyoruz; `//go:embed build/mcp-server-bin` kısıtı zaten tek ikiliye bağlı, ikinci bir hedef Makefile'a ve dağıtıma yük olurdu.

```
mcp-server-bin --analyze [--room X] [--since 24h] [--legacy-log] [--json]
```

Dört soruyu cevaplar:

**1. Agent başına düşme sayısı ve sebebi.** `agent_chat.agent.left` olayları `agent_chat.leave.reason`'a, `agent_chat.agent.evicted` ayrı sayılır. `disconnect` baskınsa sorun bağlantı kararlılığında; `evicted` baskınsa sorun `staleTimeout`'ta. #98'in iki mekanizmasını birbirinden bu ayırır.

**2. Odada olmayan alıcıya gitmiş mesajlar.** `agent_chat.message.sent` olayları `agent_chat.recipient.in_room == false` ile filtrelenir. Gönderen, alıcı, zaman ve içerik listelenir. #99'un ölçüsü.

**3. Gönderilmiş ama hiç okunmamış mesajlar.** Her alıcı için `agent_chat.messages.read` olaylarındaki en yüksek `agent_chat.read.max_id` bulunur; o alıcıya gönderilmiş ve bu eşiğin üstünde kalan mesajlar listelenir.

**4. Hub kesinti pencereleri ve etkisi.** `agent_chat.hub.stopped` → `agent_chat.hub.started` aralıkları pencereleri verir. `--legacy-log` ile `mcp-server.log` da okunur; bu pencerelere düşen `hub.port not found` ve `connection refused` satırları sayılır — yani "kaç agent, ne kadar süre hub'sız kaldı".

`--json` çıktısı makine okunur; ileride UI paneli aynı veriyi tüketir.

`--legacy-log` best-effort: eski düz metin logdan yalnızca hub bağlantı hataları ve `MCP server initialized — defaultRoom=` satırları çıkarılır. Yapısal log birikene kadar geriye dönük görüş sağlar.

## Hata davranışı

Log yazımı hiçbir koşulda hub'ı bozmaz.

- `New` başarısızsa (dizin açılamıyor, disk yok) hub `NopLogger` ile çalışır; `Log` sessizce yutar. Hub başlamayı reddetmez.
- Yazar goroutine'de yazma hatası mevcut `h.logger`'a **bir kez** raporlanır, tekrarında susulur — log fırtınası olmasın.
- Kanal dolduğunda olay düşürülür. Düşen sayısı `agent_chat.hub.stopped` olayının `agent_chat.events.dropped` alanına yazılır — sessiz kayıp olmaz, analiz bunu görür.
- `agent_chat.hub.stopped` kanaldan değil, `Close` içinden **senkron** yazılır; aksi halde kendisi de düşebilirdi.

## Test

TDD. Her davranış önce başarısız test.

`internal/eventlog` saf leaf olduğu için doğrudan test edilebilir:

- Enjekte edilebilir saat — `internal/sessionlog`'daki `s.now()` deseni
- `t.Run` ile table-driven alt testler (proje konvansiyonu)
- Şema: her olay için beklenen OTel alan adları yazılıyor mu, `event.name` dinamik değer içermiyor mu
- Rotasyon: sınırı aşan yazımdan sonra yedek dosyalar oluşuyor mu, sayı sınırı tutuyor mu
- İçerik kapısı: `CaptureContent=false` iken `gen_ai.input.messages` hiç yazılmıyor
- İçerik kırpma: 8 KB üstü kırpılıp `agent_chat.content.truncated` işaretleniyor
- Eşzamanlılık: `-race` altında çok sayıda goroutine'den `Log`
- Kanal doluluğu: yazar bloklandığında `Log` bloke olmuyor, `Dropped()` artıyor
- `NopLogger`: `Log` panik etmiyor, `Close` hata vermiyor

Hub entegrasyonu `internal/hub` testlerinde: bir join/send/leave akışından sonra `Flush` + dosya okuması ile beklenen olay dizisi doğrulanır.

Analiz: sentetik olay akışları verilip dört sorunun çıktısı doğrulanır.

## Uygulama sırası

1. `internal/eventlog` paketi — Options, Logger, async yazar, lumberjack, NopLogger (hub'a dokunmadan, tam test kapsamı)
2. Hub entegrasyonu — olay noktalarının bağlanması, `hub.started` / `hub.stopped` yaşam döngüsü
3. `--analyze` modu — dört rapor, `--json`
4. `--legacy-log` geriye dönük okuyucu

Her adım kendi commit'i; hepsi tek PR.

## Kaynaklar

- [Logging in Go with Slog: A Practitioner's Guide — Dash0](https://www.dash0.com/guides/logging-in-go-with-slog)
- [The Complete Guide to slog (Go 1.21+) — BuanaCoding](https://buanacoding.com/2025/09/complete-guide-slog-go-structured-logging-2025)
- [6 Best Go Logging Libraries in 2026 — Dash0](https://www.dash0.com/guides/golang-logging-libraries)
- [OpenTelemetry GenAI semantic conventions — MCP](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/mcp.md)
- [OpenTelemetry GenAI semantic conventions — agent spans](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-agent-spans.md)
- [OpenTelemetry attribute registry — Gen AI](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/)
- [OpenTelemetry attribute registry — MCP](https://opentelemetry.io/docs/specs/semconv/registry/attributes/mcp/)
- [OpenTelemetry — Semantic conventions for events](https://opentelemetry.io/docs/specs/semconv/general/events/)
- [OpenTelemetry — Logs Data Model](https://opentelemetry.io/docs/specs/otel/logs/data-model/)
- [Inside the LLM Call: GenAI Observability with OpenTelemetry](https://opentelemetry.io/blog/2026/genai-observability/)
- [How OpenTelemetry Traces LLM Calls, Agent Reasoning, and MCP Tools — Greptime](https://greptime.com/blogs/2026-05-09-opentelemetry-genai-semantic-conventions)
