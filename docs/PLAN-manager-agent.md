# Plan: Orchestrator → Manager Agent

## Context

Mevcut orchestrator basit Go pattern-matching kodu — ack/soru tespiti yapıp PTY'ye bildirim yazıyor. Kullanıcı tüm mesajların gerçek bir AI agent'ından (manager) geçmesini istiyor. Manager bir CLI terminali (Claude/Gemini/Copilot) olacak, tüm mesajları alacak, analiz edecek ve uygun agent'a iletecek veya drop edecek.

**Sonuç:** Orchestrator'ın routing mantığı değişecek — non-manager mesajları sadece manager'a yönlendirilecek, manager'ın mesajları ise doğrudan hedef agent'a gidecek.

---

## Adım 1: Team Struct'a `ManagerAgent` Alanı Ekle

**Dosya:** `internal/team/store.go`

```go
type Team struct {
    // ...mevcut alanlar...
    ManagerAgent string `json:"manager_agent"` // Yönetici agent adı (boş = yok)
}
```

- `Create(name, gridLayout, agents, managerAgent)` — yeni parametre
- `Update(id, name, gridLayout, agents, managerAgent)` — yeni parametre

**Dosya:** `frontend/src/lib/types.ts`

```typescript
export interface Team {
    // ...mevcut alanlar...
    manager_agent: string;
}
```

---

## Adım 2: Orchestrator'a Manager Routing Ekle

**Dosya:** `internal/orchestrator/orchestrator.go`

### Yeni alan:
```go
type Orchestrator struct {
    // ...mevcut alanlar...
    managerAgents map[string]string // chatDir → manager agent adı
}
```

### Yeni metod:
```go
func (o *Orchestrator) SetManagerAgent(chatDir, agentName string)
```

### `ProcessMessage` değişikliği:

```
mesaj geldi
  │
  ├─ system mesajı? → skip (değişmez)
  │
  ├─ managerActive && sender == manager?
  │   └─ YES → AnalyzeMessage + mevcut routing (doğrudan hedef agent'a)
  │            Manager'ın ack'leri de skip edilir (mevcut mantık)
  │
  ├─ managerActive && sender != manager?
  │   └─ YES → Sadece manager'ın PTY'sine bildirim gönder
  │            (manager read_all_messages ile okur, karar verir)
  │
  └─ manager yoksa → mevcut fallback davranışı (pattern-matching)
```

**Bildirim metni (manager'a):**
- Broadcast: `[agent-chat] New broadcast from {from} (to all). read_all_messages() to review.`
- Direct: `[agent-chat] New message from {from} to {to}. read_all_messages() to review.`

**Cooldown/batching:** Manager bildirimleri de mevcut cooldown mekanizmasını kullanır.

---

## Adım 3: app.go Değişiklikleri

**Dosya:** `app.go`

### CreateTerminal (satır ~368):
```go
a.orchestrator.RegisterAgent(teamName, agentName, sessionID)
// Yeni: bu agent team'in manager'ı mı?
if t.ManagerAgent == agentName {
    a.orchestrator.SetManagerAgent(teamName, agentName)
}
```

### CloseTerminal (satır ~494):
```go
a.orchestrator.UnregisterAgent(teamName, session.AgentName)
// Yeni: manager kapandıysa temizle
if t.ManagerAgent == session.AgentName {
    a.orchestrator.SetManagerAgent(teamName, "")
}
```

### composeAgentPrompt (satır ~402):
Eğer agent team'in manager'ı ise ve özel prompt seçilmemişse, otomatik olarak `prompts/manager_prompt.md` enjekte et:
```go
if t.ManagerAgent == agentName {
    managerPrompt, _ := promptsFS.ReadFile("prompts/manager_prompt.md")
    if selectedPrompt == "" {
        selectedPrompt = string(managerPrompt)
    } else {
        selectedPrompt += "\n\n" + string(managerPrompt)
    }
}
```

### Team CRUD binding'leri:
- `CreateTeam(name, gridLayout, agents, managerAgent)`
- `UpdateTeam(id, name, gridLayout, agents, managerAgent)`

---

## Adım 4: Frontend Değişiklikleri

### `frontend/src/store/useTeams.ts`
`createTeam` ve `updateTeam` fonksiyonlarına `managerAgent` parametresi ekle.

### `frontend/src/components/SetupWizard.tsx`
Terminal oluşturma formuna "Manager Agent" checkbox/toggle ekle. İşaretlendiğinde `UpdateTeam` ile `manager_agent` güncellenir.

### `frontend/src/components/TabBar.tsx`
`createTeam` çağrısına boş `managerAgent` parametresi ekle.

### `frontend/src/App.tsx`
Default team oluştururken boş `managerAgent` geçir.

---

## Adım 5: Manager Prompt Güncelle

**Dosya:** `prompts/manager_prompt.md`

Yeni mimariye uygun prompt — manager'ın tek bildirim alıcısı olduğunu, `read_all_messages()` ile okuması ve `send_message("manager", "...", "target")` ile yönlendirmesi gerektiğini belirtir. Ack mesajlarını iletmemesi gerektiğini vurgular (sonsuz döngü önleme).

---

## Adım 6: Testler

**Dosya:** `internal/orchestrator/orchestrator_test.go`

Mevcut `newTestOrchestrator()` helper'ı kullanarak:

| Test | Beklenti |
|------|----------|
| `TestProcessMessage_RoutesToManager` | Non-manager mesajı sadece manager'a gider |
| `TestProcessMessage_ManagerBypassesToTarget` | Manager mesajı doğrudan hedef agent'a |
| `TestProcessMessage_ManagerBroadcast` | Manager broadcast → tüm agent'lara (manager hariç) |
| `TestProcessMessage_NoManagerFallback` | Manager yoksa mevcut davranış |
| `TestProcessMessage_ManagerUnregisteredFallback` | Manager terminal kapandıysa fallback |
| `TestProcessMessage_AckStillRoutedToManager` | Non-manager ack → manager'a gider (manager karar verir) |
| `TestProcessMessage_AckFromManagerSkipped` | Manager'ın ack'si skip |
| `TestSetManagerAgent_ThreadSafety` | Concurrent SetManagerAgent race yok |

---

## Değişen Dosyalar

| Dosya | Değişiklik |
|-------|-----------|
| `internal/team/store.go` | `ManagerAgent` alanı, Create/Update parametreleri |
| `internal/orchestrator/orchestrator.go` | `managerAgents` map, `SetManagerAgent`, `ProcessMessage` routing |
| `internal/orchestrator/orchestrator_test.go` | 8 yeni test case |
| `app.go` | CreateTerminal/CloseTerminal manager wire-up, composeAgentPrompt injection, CRUD binding'ler |
| `prompts/manager_prompt.md` | Yeni mimari için güncellenmiş prompt |
| `frontend/src/lib/types.ts` | Team interface'e `manager_agent` |
| `frontend/src/store/useTeams.ts` | createTeam/updateTeam signature |
| `frontend/src/components/SetupWizard.tsx` | Manager toggle UI |
| `frontend/src/components/TabBar.tsx` | createTeam çağrısı güncelle |
| `frontend/src/App.tsx` | Default team createTeam çağrısı güncelle |

## Değişmeyen

- **Hub** — Mesaj depolama/broadcast değişmez, hub "aptal" kalır
- **MCP tools** — send_message, read_messages, read_all_messages aynı
- **types.Message** — Yeni alan gerekmez
- **Cooldown/batching** — Manager bildirimleri de mevcut cooldown'ı kullanır

---

## Doğrulama

1. `go test ./internal/orchestrator/ -v` — tüm yeni testler geçmeli
2. `go test ./...` — mevcut testler bozulmamalı
3. `make dev` — uygulama açılmalı
4. Team oluştur, bir terminal "manager" olarak işaretle → manager prompt otomatik gitmeli
5. İkinci terminal oluştur → mesaj gönderince sadece manager bildirim almalı
6. Manager `send_message("manager", "@agent: ...", "agent")` ile iletince agent bildirim almalı
7. Manager olmayan team → mevcut davranış (direct/broadcast routing)

---

## Edge Cases

- **Manager kapanırsa:** Fallback'e düşer (mevcut pattern-matching)
- **Manager yoksa:** Tamamen eski davranış (backward compatible)
- **Manager kendine mesaj gönderirse:** sender == from kontrolü ile skip
- **Manager broadcast yaparsa:** Tüm agent'lara gider (manager hariç, mevcut broadcast mantığı)
- **Birden fazla manager:** Desteklenmiyor — team başına tek manager (string alan)
