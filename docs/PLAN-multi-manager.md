# Multi-Manager Desteği

## Context

9 kişilik ekipte tek manager tüm agent'lara yetişemiyor. Odaya birden fazla manager eklenebilmeli. Non-manager mesajları TÜM aktif manager'lara broadcast edilecek — hangisi müsaitse o yanıtlayabilecek.

## Yaklaşım

Tek string manager alanlarını map/slice'a çevirmek. Mesaj routing'de `to = "managers"` sentinel değeri kullanmak (N kopya yerine tek mesaj). `ReadMessages` filtresi bu sentinel'i tanıyacak.

---

## Adım 1: Room State (`internal/hub/room.go`)

**Struct değişikliği:**
```go
// Eski:
managerAgent    string
managerLastSeen float64

// Yeni:
managerAgents map[string]float64 // name -> lastSeen
```

`NewRoomState()` → `managerAgents: make(map[string]float64)`

**Method değişiklikleri:**
- `Join()` (satır 70-76): Birden fazla manager reddi kaldır → `r.managerAgents[agentName] = types.Now()`
- `Leave()` (satır 224-227): `delete(r.managerAgents, agentName)`
- `Clear()` (satır 295-296): `r.managerAgents = make(map[string]float64)`
- `getActiveManagerLocked()` → `getActiveManagersLocked() []string` — stale olmayanları döndür
- `GetActiveManager()` → `GetActiveManagers() []string` (public)
- Eski `GetActiveManager()` compat için: ilk aktif manager veya "" döndür
- `GetActiveManagerAndTouch()` → `GetActiveManagersAndTouch(agentName) []string` — çağıran agent manager ise heartbeat güncelle, tüm aktif manager listesi döndür
- `TouchManagerHeartbeat()`: map'ten index
- `ResetManagerLockIfDifferent()` → `ResetManagerLocksIfNotIn(allowed []string)`: allowed'da olmayanları sil
- `clearManagerIfStale()` → `clearStaleManagersLocked()`: map iterate, stale olanları delete
- **Yeni:** `isManagerLocked(name string) bool` — map lookup
- **ReadMessages()** (satır 163): Filtre genişlet:
  ```go
  if msg.To == "all" || msg.To == agentName || msg.Type == "system" || (msg.To == "managers" && r.isManagerLocked(agentName)) {
  ```

## Adım 2: Hub Config (`internal/hub/hub.go`)

```go
// Eski:
roomManager map[string]string

// Yeni:
roomManagers map[string]map[string]bool
```

- `setConfiguredManager()` → `setConfiguredManagers(room string, managers []string)`
- `getConfiguredManager()` → `getConfiguredManagers(room string) []string`

## Adım 3: Protocol (`internal/hub/protocol.go`)

### `handleSetManager` (set_manager)
Payload'a `manager_agents []string` ekle, eski `manager_agent string` ile uyumlu:
```go
var data struct {
    ManagerAgent  string   `json:"manager_agent"`
    ManagerAgents []string `json:"manager_agents"`
}
// Merge: ManagerAgents boşsa ama ManagerAgent doluysa → tek elemanlı slice
```

### `handleJoinRoom` (satır 202-211)
`getConfiguredManagers()` kullan. Agent name configured set'te mi kontrol et.

### `handleSendMessage` (satır 306-316)
```go
activeManagers := roomState.GetActiveManagersAndTouch(data.From)
isFromManager := slices.Contains(activeManagers, data.From)
if len(activeManagers) > 0 && !isFromManager {
    intercepted = true
    opts.OriginalTo = data.To
    opts.RoutedByManager = true
    to = "managers"  // sentinel
}
```
Feedback mesajı: `fmt.Sprintf("Mesaj %d manager'a iletildi...", len(activeManagers), msg.ID)`

### `handleGetAllMessages` (satır 420-436)
Herhangi bir aktif manager izinli: `slices.Contains(activeManagers, c.agentName)`

### `handleClearRoom` (satır 570-577)
Aynı pattern.

## Adım 4: Hub Client (`internal/hubclient/client.go`)

`SetManager()` → `SetManagers(room string, managers []string)` — payload `{"manager_agents": [...]}`

## Adım 5: Team Store (`internal/team/store.go`)

```go
// Eski:
ManagerAgent string `json:"manager_agent"`

// Yeni:
ManagerAgents      []string `json:"manager_agents"`
ManagerAgentLegacy string   `json:"manager_agent,omitempty"` // migration
```

`load()` migration: `ManagerAgentLegacy != "" && len(ManagerAgents) == 0` → `ManagerAgents = []string{ManagerAgentLegacy}`

`SetManager()` → `AddManager(id, name)` ve `RemoveManager(id, name)`

## Adım 6: App Bindings (`app.go`)

- `syncHubManager()` → `syncHubManagers(room string, managers []string)`
- `SetTeamManager()` → `AddTeamManager(id, name)` + `RemoveTeamManager(id, name)`
- `resolveManagerIntent()`: `slices.Contains(t.ManagerAgents, agentName)` kontrol
- `CreateTerminal()`: managers slice'ı geç
- Tüm `subscribeExistingTeams` ve `UpdateTeam` call site'ları güncelle

## Adım 7: Orchestrator (`internal/orchestrator/orchestrator.go`)

Agent tracking'e `isManager` ekle:
```go
type agentInfo struct {
    sessionID string
    isManager bool
}
```

`ProcessMessage` (satır 276-298): `msg.To == "managers"` → tüm manager session'larına notify.

## Adım 8: Frontend

### `types.ts` (satır 27)
`manager_agent: string` → `manager_agents: string[]`

### `useTeams.ts`
- `setTeamManager` → `addTeamManager` + `removeTeamManager`
- Wails binding import'ları güncelle

### `SetupWizard.tsx`
"Set as Manager" checkbox → `addTeamManager(teamID, name)` çağır

### `AgentStatus.tsx`
Değişiklik yok — zaten `agent.role === "manager"` kontrol ediyor.

### Wails generate
JS/TS binding'leri yeniden oluştur.

## Adım 9: Testler

- `room_test.go`: `TestRoomJoin_SecondManagerRejected` → `TestRoomJoin_MultipleManagersAllowed`
- Yeni: `TestRoomReadMessages_ManagersSentinel` — `to="managers"` mesajları doğru filtreleniyor mu
- `protocol_test.go`: join, send, auth testleri güncelle
- `orchestrator_test.go`: `RegisterAgent` isManager parametresi, manager broadcast testi

## Verification

1. `go test ./...` — tüm testler geçmeli
2. `make dev` — uygulama açılmalı
3. Manuel test: Bir team'de 2 terminal'i manager yap, 3. terminal'den mesaj gönder → her iki manager'a da ulaşmalı
4. Tek manager senaryosu hala çalışmalı (backward compat)
5. Manager timeout: Bir manager'ı kapat → diğeri hala aktif kalmalı
6. `read_all_messages` ve `clear_room` her iki manager'dan da çalışmalı

## Kritik Dosyalar

1. `internal/hub/room.go` — core state değişikliği
2. `internal/hub/protocol.go` — routing ve auth
3. `internal/hub/hub.go` — configured managers map
4. `internal/team/store.go` — persistence + migration
5. `app.go` — Wails bindings
6. `internal/hubclient/client.go` — RPC method
7. `internal/orchestrator/orchestrator.go` — notification routing
8. `frontend/src/lib/types.ts` — TS type
9. `frontend/src/store/useTeams.ts` — store methods
10. `frontend/src/components/SetupWizard.tsx` — UI
