# Manager-Zorunlu Mesaj Yonlendirme Plani

## Ozet

- Amac: Manager aktifken tum agent mesajlari once manager'a dusecek; manager iletip iletmeme kararini verecek.
- Manager pasifken mevcut davranis korunacak (mesajlar dogrudan hedefe gider).
- Manager secimi prompt bazli olacak; ayni odada ayni anda yalnizca 1 aktif manager olacak.
- Guvenlik: `send_message.from_agent` sadece bagli agent adi ile eslesirse kabul edilecek.

## Sabit Kararlar

- Manager prompt tespiti: prompt tag icinde `manager` (case-insensitive) varsa manager kabul edilir.
- Manager heartbeat timeout: `30s`.
- Manager yoksa davranis: mevcut direct/broadcast routing aynen devam.
- Manager forwarding araci: mevcut `send_message` (yeni tool yok).
- `read_all_messages`: herkese acik kalacak.
- Duplicate agent name: reddedilecek.
- Ayni odada ikinci manager: reddedilecek (manager pasif olana kadar).

## Implementasyon Adimlari

1. Mesaj modelini manager routing metadata'si tasiyacak sekilde genislet.
   - Dosya: `internal/types/message.go`
   - `types.Message` icine opsiyonel `original_to` ve `routed_by_manager` (veya esdeger iki alan) eklenecek.

2. RoomState icine manager yasam dongusu alanlarini ekle.
   - Dosya: `internal/hub/room.go`
   - Aktif manager adi ve son heartbeat zamani tutulacak.
   - Bu state persistence'a yazilmayacak (ephemeral).

3. Join kurallarini manager ve duplicate isim icin sikilastir.
   - Dosya: `internal/hub/room.go`
   - Ayni odada ayni agent adi tekrar join edilirse hata verilecek.
   - `role == "manager"` join'inde aktif manager yoksa set edilecek; varsa ve farkliysa hata donecek.
   - Manager agent leave/stale oldugunda manager lock kaldirilacak.

4. Client kimligini baglanti bazinda sabitle ve `from_agent` spoof'u engelle.
   - Dosya: `internal/hub/protocol.go`
   - `join_room` sonrasi baglanti bir agent adina bind edilecek (`c.agentName`).
   - `send_message` cagrisinda `from` alani bind edilen adla ayni degilse hata.
   - `send_message` join oncesi cagrilirsa hata.

5. Manager aktifken mesaj interception kuralini hub'da uygula.
   - Dosya: `internal/hub/protocol.go`
   - Manager aktif ve gonderen manager degilse mesaj manager'a route edilecek.
   - Orijinal hedef `original_to` alaninda korunacak; `to` manager adi olacak.
   - `to="all"` dahil tum non-manager gonderimleri interception'a girecek.
   - Manager gonderdiyse interception yapilmayacak; mesaj normal akista hedefe iletilecek.
   - Sender'a donen tool sonucu "manager onayi bekliyor" semantiginde olacak.

6. Manager heartbeat (30s) ve aktif/pasif gecisini request akisina bagla.
   - Dosya: `internal/hub/protocol.go`
   - Aktif manager ayni odada `send_message`, `get_messages`, `get_all_messages`, `list_agents`, `get_last_message_id` cagrilarinda heartbeat guncellenecek.
   - 30s asilinca manager pasif kabul edilip routing otomatik direct moda donecek.

7. Prompt bazli manager aktivasyonunu startup prompt uretimine bagla.
   - Dosya: `app.go`
   - `composeAgentPrompt` icinde secili prompt tag'lerinden `manager` tespiti yapilacak.
   - Dosya: `internal/cli/startup.go`
   - `ComposeStartupPrompt` imzasi manager rolu bilgisini alacak sekilde guncellenecek.
   - Manager prompt secildiyse join talimati `join_room("<agentName>", "manager")` olacak.

8. Manager prompt metnini yeni akisa gore revize et.
   - Dosya: `prompts/manager_prompt.md`
   - Route edilen mesajlarin okunmasi, duzenli polling/heartbeat, iletme-karar mantigi, self-forward yapmama.
   - Manager forwarding ayni `send_message` ile yapilacak.

9. MCP tool aciklamalarini yeni davranisla hizala.
   - Dosya: `internal/mcpserver/server.go`
   - `send_message` aciklamasina strict `from_agent` eslesmesi ve manager interception notu eklenecek.
   - `join_room` aciklamasina duplicate name ve tek manager kurali eklenecek.

10. Dokumantasyonu yeni mimariyi yansitacak sekilde guncelle.
   - Dosya: `README.md`
   - Dosya: `CLAUDE.md`
   - "manager-active gateway mode", "manager-inactive fallback mode", "single manager lock", "30s heartbeat" acikca dokumante edilecek.

## Public API / Interface / Type Degisiklikleri

- `types.Message` JSON semasi genisleyecek: `original_to`, `routed_by_manager` (opsiyonel).
- `cli.ComposeStartupPrompt(...)` fonksiyon imzasi manager rolu bilgisini alacak sekilde degisecek.
- MCP tool isimleri degismeyecek, ama davranis sozlesmeleri degisecek:
  - `join_room`: duplicate ad ve ikinci manager durumunda hata donebilir.
  - `send_message`: join sonrasi ve kimlik eslesmesi zorunlu; manager aktifse non-manager mesaji manager'a route edilir.

## Test Plani

1. Manager yokken `alice -> bob` direct mesaj bob'a normal gider.
2. Manager aktifken `alice -> bob` mesaji manager'a route edilir ve `original_to=bob` set edilir.
3. Manager aktifken `alice -> all` mesaji manager'a route edilir ve `original_to=all` set edilir.
4. Manager mesaji (`manager -> bob` veya `manager -> all`) interception'a girmez.
5. Manager 30s heartbeat vermezse pasiflesir, routing direct moda doner.
6. Aktif manager varken ikinci manager join denemesi hata verir.
7. Ayni agent adiyla ikinci join denemesi hata verir.
8. `send_message` join oncesi hata verir; `from_agent` mismatch hata verir.
9. `read_all_messages` manager disi agent icin acik kalir.
10. `go test ./internal/hub/...`, `go test ./internal/orchestrator/...`, `go test ./...` temiz gecer.

## Rollout ve Gozlem

- Feature flag eklenmeyecek; yeni davranis default olacak.
- Hub loglarina manager routing kararlarini gorunur kilan satirlar eklenecek (`manager_active`, `intercepted`, `original_to`, `from`).
- Ilk canli dogrulama: manager aktif/pasif gecisinde ayni senaryoda teslim deseninin degistigi gozlemlenecek.

## Varsayimlar ve Defaultlar

- Manager adi sabit degil; manager prompt ile acilan agent adi manager kimligi olur.
- Manager prompt tanimi tag bazlidir (`manager`).
- Manager pasifken kuyruklama yapilmaz; mevcut dogrudan iletim modeli calisir.
- Manager forwarding tamamen manager agent kararindadir; hub otomatik forwarding yapmaz.
- `read_all_messages` erisimi kisitlanmaz.
