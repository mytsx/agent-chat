# Birlesik Final Plan: Hub-Enforced Manager Gateway + Team/UX Entegrasyonu

## Kisa Ozet

Bu plan iki yaklasimi birlestiriyor:
1. Cekirdek garanti: Mesaj gecisi hub seviyesinde zorunlu manager gateway olacak (bypass edilemez).
2. Operasyonel kullanim: Team tarafinda manager secimi/yonetimi gorunur olacak ve startup prompt akisi manager rolunu otomatik destekleyecek.

Hedef sonuc:
- Manager aktifken tum non-manager mesajlari once manager'a duser.
- Manager pasifken sistem mevcut direct/broadcast akisina geri doner.
- Tek aktif manager kurali odada zorunlu olur.
- Kimlik spoof (from_agent sahteleme) engellenir.

## Basari Kriterleri

1. Manager aktif odada `send_message(from=agentX, to=Y)` cagrisinin kaydi `to=manager` olarak olusur.
2. Manager aktif odada manager disi hicbir mesaj dogrudan hedefe dusmez.
3. Manager mesajlari normal hedefe gider (direct/broadcast).
4. Manager dusunce (300s heartbeat timeout) routing otomatik fallback olur.
5. Ayni odada duplicate agent name join reddedilir.
6. `from_agent` ile bagli client kimligi eslesmiyorsa `send_message` reddedilir.

## Mimari Kararlar (Kilitleme)

1. Routing authority yalnizca hub katmani olacak.
2. Orchestrator routing authority olmayacak; sadece PTY notification katmani olarak kalacak.
3. Manager aktivasyonu `join_room(..., role="manager")` ile runtime'da olur.
4. Team `manager_agent` alani UX ve otomasyon amacli olacak; runtime lock yine hub'da.
5. `read_all_messages` herkese acik kalacak.
6. Kuyruklama yok; manager pasifse mevcut davranisa donulur.
7. Heartbeat timeout sabit: `300s` (5 dakika).

## Mesaj Akisi (Decision Complete)

1. Agent `join_room` cagirir.
2. Hub duplicate isim kontrolu yapar.
3. Hub connection-agent binding yapar (`c.agentName`).
4. Role `manager` ise:
5. Odada aktif manager yoksa manager lock atanir.
6. Aktif manager varsa ve farkli agent ise join hata verir.
7. Agent `send_message` cagirir.
8. Hub `c.agentName` var mi ve `from_agent == c.agentName` mi kontrol eder; degilse hata.
9. Hub manager aktifligini timeout ile hesaplar.
10. Manager aktif ve sender manager degilse mesaj manager'a reroute edilir:
11. `to = activeManager`
12. `original_to = kullanicinin istedigi hedef`
13. `routed_by_manager = true`
14. `message_new` event bu reroute edilmis mesajla yayinlanir.
15. Manager aktif degilse veya sender manager ise mesaj normal kaydedilir/yayinlanir.
16. Manager'dan gelen normal mesajlar hedefe dogrudan gider.

## Dosya Bazli Uygulama Plani

1. `/Users/yerli/Developer/agent-chat/internal/types/message.go`
- `types.Message` alanlari eklenecek:
- `OriginalTo string \`json:"original_to,omitempty"\``
- `RoutedByManager bool \`json:"routed_by_manager,omitempty"\``

2. `/Users/yerli/Developer/agent-chat/internal/team/store.go`
- `Team` alanina eklenecek:
- `ManagerAgent string \`json:"manager_agent,omitempty"\``
- Yeni store metodu eklenecek:
- `SetManager(teamID, managerAgent string) (Team, error)`
- `managerAgent` icin `ValidateName` uygulanacak, bos string clear anlami tasiyacak.

3. `/Users/yerli/Developer/agent-chat/app.go`
- Yeni binding eklenecek:
- `SetTeamManager(id, managerAgent string) (team.Team, error)`
- `CreateTerminal` akisinda manager intent cozulmesi eklenecek.
- Cozulum kurali:
- `isManager = (team.manager_agent == agentName) OR (selectedPrompt.tags icinde "manager")`
- `team.manager_agent == ""` ve prompt manager-tag ise team manager otomatik bu agent'a set edilir.
- `team.manager_agent != ""` ve farkli agent manager-tag ile acilmaya calisiyorsa hata donulur.
- `composeAgentPrompt` manager bilgisini alacak sekilde guncellenecek.
- Manager ise `join_room` role degeri `manager` olacak.

4. `/Users/yerli/Developer/agent-chat/internal/cli/startup.go`
- `ComposeStartupPrompt` imzasi guncellenecek:
- `ComposeStartupPrompt(..., agentName, teamName string, isManager bool) string`
- Join instruction role secimi:
- `isManager == true` ise `join_room(agentName, "manager")`
- degilse mevcut davranis.

5. `/Users/yerli/Developer/agent-chat/internal/hub/room.go`
- `RoomState` alanlari eklenecek:
- `managerAgent string`
- `managerLastSeen float64`
- Sabit: `managerTimeoutSec = 300`
- `Join` imzasi hata donecek sekilde guncellenecek:
- `Join(agentName, role string) (types.Message, map[string]types.Agent, error)`
- Join kontrolu:
- duplicate agent name => error
- role manager claim kurallari => tek aktif manager
- `Leave` icinde manager ayrildiysa manager lock temizlenecek.
- stale cleanup'da manager agent silinirse manager lock temizlenecek.
- `SendMessage` icin opsiyon yapisi eklenecek:
- `SendOptions{OriginalTo string, RoutedByManager bool}`
- Mesaj olusumunda yeni alanlar set edilecek.

6. `/Users/yerli/Developer/agent-chat/internal/hub/protocol.go`
- `handleJoinRoom`:
- Yeni `Join` imzasina gore hata handling
- `c.agentName` binding kurali
- `c.agentName` dolu ve farkli isimle ikinci join denemesi => error
- `handleSendMessage`:
- join zorunlulugu
- strict `from_agent == c.agentName`
- manager aktiflik kontrolu
- interception karari
- intercepted response metni: manager onayi bekledigini acik yazar
- `handleGetMessages`, `handleGetAllMessages`, `handleListAgents`, `handleGetLastMessageID`:
- cagrinin sahibi aktif manager ise heartbeat tazelenir.

7. `/Users/yerli/Developer/agent-chat/internal/orchestrator/orchestrator.go`
- `ProcessMessage` basina manager-routed override eklenecek:
- `msg.RoutedByManager == true` ise `AnalyzeMessage` skip etmeden manager'a notification gonder.
- Bu sayede ack/tamam gibi mesajlar manager'a daima bildirilir.
- Diger tum mesajlarda mevcut cooldown/batching davranisi korunur.

8. `/Users/yerli/Developer/agent-chat/internal/mcpserver/server.go`
- `join_room` ve `send_message` tool descriptions yeni sozlesmeleri aciklayacak:
- duplicate name rejection
- strict sender identity
- manager-active interception semantigi

9. `/Users/yerli/Developer/agent-chat/prompts/manager_prompt.md`
- Prompt su net davranislari icerecek:
- tum route edilen mesajlari `read_all_messages` ile izle
- sadece gerekli durumlarda hedefe `send_message` ile ilet
- tesekkur/ack/goodbye tipini drop et
- kendine tekrar forward etmeme kurali

10. Frontend entegrasyonu:
- `/Users/yerli/Developer/agent-chat/frontend/src/lib/types.ts`
- `Team` icine `manager_agent: string`
- `Message` icine `original_to?: string`, `routed_by_manager?: boolean`
- `/Users/yerli/Developer/agent-chat/frontend/src/store/useTeams.ts`
- `setTeamManager(teamID, managerAgent)` action eklenecek
- `/Users/yerli/Developer/agent-chat/frontend/src/components/SetupWizard.tsx`
- `Set as manager` checkbox eklenecek
- seciliyse create oncesi `setTeamManager(teamID, agentName)` cagirilacak
- `/Users/yerli/Developer/agent-chat/frontend/src/components/AgentStatus.tsx`
- manager agent UI'da etiketlenecek (`role === manager` veya team.manager_agent)
- `/Users/yerli/Developer/agent-chat/frontend/src/components/MessageFeed.tsx`
- intercepted mesajlarda `original_to` bilgisi gorunur olacak (ornek: `agent -> manager (intended: backend)`)

11. Dokumantasyon:
- `/Users/yerli/Developer/agent-chat/README.md`
- manager-active gateway mode, fallback mode, strict identity, timeout
- `/Users/yerli/Developer/agent-chat/CLAUDE.md`
- mimari akis ve yeni routing kurallari guncellenecek

## Public API / Interface / Type Degisiklikleri

1. `types.Message` genisletme:
- `original_to`
- `routed_by_manager`
2. `team.Team` genisletme:
- `manager_agent`
3. Yeni backend binding:
- `SetTeamManager(id, managerAgent) -> Team`
4. `ComposeStartupPrompt` fonksiyon imzasi:
- `isManager bool` parametresi

## Test Plani

1. Unit: `/Users/yerli/Developer/agent-chat/internal/hub/room_test.go`
- duplicate join reject
- manager claim/reclaim
- timeout ile manager deactivate
- leave/stale manager cleanup
- SendOptions alanlarinin mesaja yansimasi

2. Unit: `/Users/yerli/Developer/agent-chat/internal/hub/protocol_test.go`
- strict sender identity
- join before send zorunlulugu
- manager aktifken interception (direct/all)
- manager sender bypass
- manager lock conflict

3. Unit: `/Users/yerli/Developer/agent-chat/internal/orchestrator/orchestrator_test.go`
- `routed_by_manager=true` mesajlarinda forced manager notify
- mevcut cooldown/batching regressions

4. Unit: `/Users/yerli/Developer/agent-chat/internal/cli/startup_test.go` (yeni)
- manager/non-manager join instruction dogrulamasi

5. Frontend type/build dogrulamasi
- `cd /Users/yerli/Developer/agent-chat/frontend && npm run build`

6. Entegre dogrulama
- `go test /Users/yerli/Developer/agent-chat/internal/hub/...`
- `go test /Users/yerli/Developer/agent-chat/internal/orchestrator/...`
- `go test /Users/yerli/Developer/agent-chat/...`

7. Manuel senaryo testleri
1. Manager yok: mevcut direct/broadcast calisir.
2. Manager var: agent-to-agent mesaj once manager'a gider.
3. Manager forward: hedef agent notification alir.
4. Manager kapanir: 300s sonra fallback devreye girer.
5. Ayni isimle ikinci agent join: reject.
6. Sahte `from_agent`: reject.

## Rollout ve Risk Azaltma

1. Feature flag yok, default aktif.
2. Log satirlari eklenecek:
- `manager_active`, `intercepted`, `original_to`, `sender`, `room`.
3. Geriye uyumluluk:
- eski `teams.json` kayitlari `manager_agent=""` ile calisir.
- eski `hub-state` mesajlari yeni alanlar olmadan sorunsuz parse edilir.

## Varsayimlar ve Defaultlar

1. Runtime authority hub'dadir; orchestrator sadece bildirim katmanidir.
2. Tek aktif manager kurali odada zorunludur.
3. Manager routing kararinda kuyruklama yoktur; manager pasifse fallback vardir.
4. `read_all_messages` acik kalir; yetki daraltmasi bu fazda yoktur.
5. Manager prompt ve team manager secimi birbiriyle uyumlu calisir; cakisiyorsa backend hatasi kullaniciya acik mesajla doner.
