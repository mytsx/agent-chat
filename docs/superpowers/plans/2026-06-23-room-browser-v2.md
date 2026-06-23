# Room Browser v2 (#18) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Room Browser'a oda silme (orphan-only, güvenli), orphan→takım içe aktarma ve geçmiş-oda agent isim türetimi ekle.

**Architecture:** Backend C→B→A sırasıyla: (C) `RoomSummary`'ye join-mesajından türetilen `HistoricalAgents` alanı; (B) `team.Store.Create` duplicate-ad guard'ı + frontend import diyaloğu (mevcut `CreateTeam`'i yeniden kullanır); (A) okuma-path sertleştirme (getRoom) + `deletedRooms` tombstone + `delete_room` RPC + app.go orphan-check binding. Silme yalnız orphan odalara; arşiv ve session snapshot'ları korunur.

**Tech Stack:** Go (Wails v2 backend, modül adı `desktop`), React 18 + TypeScript + Zustand (frontend), WebSocket hub.

## Global Constraints

- Go modül adı `desktop` (URL-tabanlı değil). Import yolu: `desktop/internal/...`.
- **`make mcp-server` her `go build`/Wails build'inden ÖNCE çalışmalı** (`//go:embed build/mcp-server-bin` constraint'i).
- Agent-facing / kullanıcıya dönük metinler **Türkçe + emoji**.
- `last_seen` alanları `float64` Unix timestamp.
- Go testleri **table-driven**, `t.Run()` alt-testleriyle.
- Frontend'de test runner YOK; frontend doğrulaması = `cd frontend && npm run build` (tsc typecheck) + `make dev` görsel.
- Silmede yalnız `hub-state/{room}.json` kaldırılır; `hub-state/archive/{room}.jsonl` ve `hub-state/sessions/{room}/` **korunur**.
- Silme **yalnız orphan** odalara (hiçbir takıma karşılık gelmeyen); default oda ve canlı/subscribe oda reddedilir.

---

## Dosya Yapısı

| Dosya | Sorumluluk | Dilim |
|---|---|---|
| `internal/types/message.go` | `RoomSummary`'ye `HistoricalAgents []string` | C |
| `internal/hub/room.go` | `deriveHistoricalAgents` + `Summary()` wiring | C |
| `internal/hub/historical_test.go` (yeni) | C türetim testleri | C |
| `internal/team/store.go` | `Create` duplicate-ad guard | B |
| `internal/team/store_test.go` | duplicate guard testi | B |
| `internal/hub/protocol.go` | read-path `getRoom`; `delete_room` dispatch+handler | A |
| `internal/hub/hub.go` | `deletedRooms` field + init + `getOrCreateRoom` clear | A |
| `internal/hub/persistence.go` | `persistDirtyRooms` tombstone-skip | A |
| `internal/hub/delete_room_test.go` (yeni) | tombstone + delete_room + non-reviving read testleri | A |
| `internal/hubclient/client.go` | `DeleteRoom(room) error` | A |
| `app.go` | `DeleteRoom` binding (orphan/default check) | A |
| `frontend/wailsjs/...` | Wails regen | — |
| `frontend/src/lib/types.ts` | `RoomSummary.historical_agents` | C |
| `frontend/src/store/useRooms.ts` | `deleteRoom` aksiyonu | A |
| `frontend/src/components/RoomBrowser.tsx` | historical rozetleri; sil butonu; import butonu | A/B/C |
| `frontend/src/components/ImportRoomModal.tsx` (yeni) | orphan→takım import diyaloğu | B |
| `frontend/src/styles/globals.css` | sil/import/historical stilleri | A/B/C |

---

## Task 1: C-backend — `HistoricalAgents` türetimi

**Files:**
- Modify: `internal/types/message.go:12-19`
- Modify: `internal/hub/room.go:557-573` (Summary)
- Create: `internal/hub/historical_test.go`

**Interfaces:**
- Produces: `types.RoomSummary.HistoricalAgents []string` (json `historical_agents`); `deriveHistoricalAgents(messages []types.Message) []string` (paket-içi).

- [ ] **Step 1: Failing test yaz**

Create `internal/hub/historical_test.go`:

```go
package hub

import (
	"reflect"
	"testing"

	"desktop/internal/types"
)

func TestDeriveHistoricalAgents(t *testing.T) {
	sys := func(content string) types.Message {
		return types.Message{Content: content, Type: types.MsgTypeSystem}
	}
	tests := []struct {
		name string
		msgs []types.Message
		want []string
	}{
		{
			name: "distinct join names sorted",
			msgs: []types.Message{
				sys("\U0001f7e2 Coder2 odaya katıldı (Rol: worker)"),
				sys("\U0001f7e2 Coder1 odaya katıldı"),
				sys("\U0001f534 Coder1 odadan ayrıldı"),
				sys("\U0001f7e2 Coder1 odaya katıldı"), // tekrar join → tek kez
			},
			want: []string{"Coder1", "Coder2"},
		},
		{
			name: "leave-only noise ignored",
			msgs: []types.Message{sys("\U0001f534 Ghost odadan ayrıldı")},
			want: nil,
		},
		{
			name: "non-system messages ignored",
			msgs: []types.Message{
				{Content: "\U0001f7e2 Fake odaya katıldı", Type: types.MsgTypeDirect},
			},
			want: nil,
		},
		{
			name: "name with space preserved",
			msgs: []types.Message{sys("\U0001f7e2 Coder 1 odaya katıldı")},
			want: []string{"Coder 1"},
		},
		{
			name: "empty input",
			msgs: nil,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveHistoricalAgents(tt.msgs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("deriveHistoricalAgents = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSummary_HistoricalOnlyWhenRosterEmpty(t *testing.T) {
	// Roster boş oda → historical dolu.
	empty := NewRoomState()
	empty.mu.Lock()
	empty.messages = []types.Message{
		{Content: "\U0001f7e2 Solo odaya katıldı", Type: types.MsgTypeSystem, Timestamp: "2026-01-01T00:00:00Z"},
	}
	empty.mu.Unlock()
	s := empty.Summary("orphan", false)
	if !reflect.DeepEqual(s.HistoricalAgents, []string{"Solo"}) {
		t.Fatalf("HistoricalAgents = %#v, want [Solo]", s.HistoricalAgents)
	}

	// Roster dolu oda → historical nil.
	live := NewRoomState()
	live.mu.Lock()
	live.agents["Live1"] = types.Agent{Role: "worker"}
	live.messages = []types.Message{
		{Content: "\U0001f7e2 Live1 odaya katıldı", Type: types.MsgTypeSystem, Timestamp: "2026-01-01T00:00:00Z"},
	}
	live.mu.Unlock()
	if s2 := live.Summary("live", false); s2.HistoricalAgents != nil {
		t.Fatalf("roster doluyken HistoricalAgents nil olmalı, got %#v", s2.HistoricalAgents)
	}
}
```

- [ ] **Step 2: Testin fail ettiğini doğrula**

Run: `go test ./internal/hub/ -run 'TestDeriveHistoricalAgents|TestSummary_HistoricalOnlyWhenRosterEmpty' -v`
Expected: FAIL — `undefined: deriveHistoricalAgents` ve `s.HistoricalAgents` alanı yok.

- [ ] **Step 3: `RoomSummary`'ye alan ekle**

`internal/types/message.go`, `RoomSummary` struct'ını (satır 12-19) güncelle:

```go
type RoomSummary struct {
	Name         string           `json:"name"`
	MessageCount int              `json:"message_count"`
	Agents       map[string]Agent `json:"agents"`
	// HistoricalAgents lists distinct agent names derived from join system-messages,
	// populated only when the persisted roster (Agents) is empty (archived rooms whose
	// agents were stale-cleaned). These are PAST participants, not current members.
	HistoricalAgents []string `json:"historical_agents"`
	LastActivity     string   `json:"last_activity"`
	IsDefault        bool     `json:"is_default"`
}
```

- [ ] **Step 4: `deriveHistoricalAgents` + Summary wiring ekle**

`internal/hub/room.go`, `Summary` method'unun (satır 554-573) hemen üstüne yardımcıyı ekle:

```go
const (
	joinMsgPrefix = "\U0001f7e2 "   // "🟢 " — join system message prefix
	joinMsgInfix  = " odaya katıldı" // text between the agent name and optional role suffix
)

// deriveHistoricalAgents extracts the distinct, sorted set of agent names that have
// joined the room at some point, parsed from join system-messages. Leave messages are
// ignored: the result answers "which agent names were ever created here", which is what
// archived (roster-empty) rooms need. The caller must hold r.mu (it reads r.messages).
func deriveHistoricalAgents(messages []types.Message) []string {
	seen := make(map[string]bool)
	var names []string
	for _, m := range messages {
		if m.Type != types.MsgTypeSystem {
			continue
		}
		if !strings.HasPrefix(m.Content, joinMsgPrefix) {
			continue
		}
		rest := m.Content[len(joinMsgPrefix):]
		idx := strings.Index(rest, joinMsgInfix)
		if idx <= 0 {
			continue
		}
		name := rest[:idx]
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
```

Sonra `Summary` method'unu güncelle (satır 557-573):

```go
func (r *RoomState) Summary(name string, isDefault bool) types.RoomSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	lastActivity := ""
	if len(r.messages) > 0 {
		lastActivity = r.messages[len(r.messages)-1].Timestamp
	}

	agents := r.copyAgentsLocked()
	var historical []string
	if len(agents) == 0 {
		historical = deriveHistoricalAgents(r.messages)
	}

	return types.RoomSummary{
		Name:             name,
		MessageCount:     len(r.messages),
		Agents:           agents,
		HistoricalAgents: historical,
		LastActivity:     lastActivity,
		IsDefault:        isDefault,
	}
}
```

`internal/hub/room.go` zaten `strings` ve `sort` import ediyor mu doğrula (ListRoomSummaries `sort` kullanıyor). `strings` yoksa import bloğuna ekle.

- [ ] **Step 5: Testin geçtiğini doğrula**

Run: `go test ./internal/hub/ -run 'TestDeriveHistoricalAgents|TestSummary_HistoricalOnlyWhenRosterEmpty' -v`
Expected: PASS (tüm alt-testler).

- [ ] **Step 6: Tüm hub testleri + commit**

Run: `go test ./internal/hub/ ./internal/types/`
Expected: PASS.

```bash
git add internal/types/message.go internal/hub/room.go internal/hub/historical_test.go
git commit -m "feat: RoomSummary.HistoricalAgents — geçmiş-oda agent isimlerini join mesajlarından türet (#18)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: B-backend — `team.Store.Create` duplicate-ad guard

**Files:**
- Modify: `internal/team/store.go:137-167`
- Create/Modify: `internal/team/store_test.go`

**Interfaces:**
- Produces: `Store.Create` aynı adda takım varsa hata döndürür (import güvenliği).

- [ ] **Step 1: Failing test yaz**

`internal/team/store_test.go` içine ekle (dosya yoksa oluştur; paket `team`):

```go
package team

import (
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir()) // NewStore(dataDir string) — teams.json'u bu dizinde tutar
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestCreate_RejectsDuplicateName(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("Alpha", "2x2", nil); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := s.Create("Alpha", "2x2", nil); err == nil {
		t.Fatalf("ikinci 'Alpha' Create hata döndürmeliydi")
	}
}
```


- [ ] **Step 2: Testin fail ettiğini doğrula**

Run: `go test ./internal/team/ -run TestCreate_RejectsDuplicateName -v`
Expected: FAIL — ikinci Create hata döndürmüyor.

- [ ] **Step 3: Guard ekle**

`internal/team/store.go`, `Create` method'unda `s.mu.Lock()` ve `defer s.mu.Unlock()`'tan SONRA, `id := uuid.New()...` satırından ÖNCE ekle:

```go
	for _, existing := range s.teams {
		if existing.Name == name {
			return Team{}, fmt.Errorf("aynı adda takım zaten var: %s", name)
		}
	}
```

`fmt` import edili olmalı (zaten ValidateName hatası için kullanılıyor).

- [ ] **Step 4: Testin geçtiğini doğrula**

Run: `go test ./internal/team/ -v`
Expected: PASS (mevcut testler + yeni test).

- [ ] **Step 5: Commit**

```bash
git add internal/team/store.go internal/team/store_test.go
git commit -m "feat: team.Store.Create duplicate-ad guard'ı — orphan import güvenliği (#18)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: A-backend — Okuma-path sertleştirme (getRoom)

**Files:**
- Modify: `internal/hub/protocol.go:934-961` (handleGetAgents, handleGetMessagesRaw)
- Create: `internal/hub/delete_room_test.go` (bu task'ta non-reviving read testi başlar)

**Interfaces:**
- Consumes: `Hub.getRoom(room) *RoomState` (hub.go:310, mevcut).
- Produces: var-olmayan oda okuması artık oda materialize ETMEZ; boş roster/mesaj döner.

- [ ] **Step 1: Failing test yaz**

Create `internal/hub/delete_room_test.go` (import'lar minimal — Task 4/5 gerektikçe ekler):

```go
package hub

import (
	"io"
	"log"
	"testing"

	"desktop/internal/types"
)

// authedDesktop builds a hub (with a real temp dataDir) + an authorized desktop client.
func authedDesktop(t *testing.T) (*Hub, *Client) {
	t.Helper()
	h := New(t.TempDir(), "default", log.New(io.Discard, "", 0))
	h.desktopAuthToken = "secret"
	c := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.handleRequest(c, types.Request{
		ID: "id", Type: "identify",
		Data: mustRawJSON(t, map[string]any{"client_type": "desktop", "auth_token": "secret"}),
	})
	if r := readResponse(t, c, "identify"); !r.Success {
		t.Fatalf("desktop identify failed: %s", r.Error)
	}
	return h, c
}

func TestGetMessagesRaw_DoesNotReviveRoom(t *testing.T) {
	h, c := authedDesktop(t)

	h.handleRequest(c, types.Request{ID: "1", Type: "get_messages_raw", Room: "ghost"})
	if resp := readResponse(t, c, "get_messages_raw"); !resp.Success {
		t.Fatalf("get_messages_raw should succeed with empty result: %s", resp.Error)
	}
	if h.getRoom("ghost") != nil {
		t.Fatalf("ham mesaj okuması var-olmayan odayı materialize ETMEMELİ")
	}

	h.handleRequest(c, types.Request{ID: "2", Type: "get_agents", Room: "ghost2"})
	if resp := readResponse(t, c, "get_agents"); !resp.Success {
		t.Fatalf("get_agents should succeed with empty result: %s", resp.Error)
	}
	if h.getRoom("ghost2") != nil {
		t.Fatalf("ham agent okuması var-olmayan odayı materialize ETMEMELİ")
	}
}
```

- [ ] **Step 2: Testin fail ettiğini doğrula**

Run: `go test ./internal/hub/ -run TestGetMessagesRaw_DoesNotReviveRoom -v`
Expected: FAIL — handler'lar `getOrCreateRoom` kullandığından `getRoom("ghost")` non-nil döner.

- [ ] **Step 3: Read handler'larını getRoom'a geçir**

`internal/hub/protocol.go`, `handleGetAgents` (satır 940-942) içinde:

```go
	room := h.resolveRoom(req.Room)
	roomState := h.getRoom(room)
	agents := map[string]types.Agent{}
	if roomState != nil {
		agents = roomState.GetAgents()
	}
```

`handleGetMessagesRaw` (satır 955-957) içinde:

```go
	room := h.resolveRoom(req.Room)
	roomState := h.getRoom(room)
	messages := []types.Message{}
	if roomState != nil {
		messages = roomState.GetMessages()
	}
```

> `types` paketi protocol.go'da zaten import edili. `agents`/`messages` boş (nil değil) literal'le başlat ki JSON `{}`/`[]` olarak serialize olsun, `null` değil.

- [ ] **Step 4: Testin geçtiğini + regresyon doğrula**

Run: `go test ./internal/hub/ -run TestGetMessagesRaw_DoesNotReviveRoom -v`
Expected: PASS.

Run: `go test ./internal/hub/`
Expected: PASS — mevcut get_agents/get_messages_raw testleri kırılmamalı (boş oda artık `{}`/`[]` döner; var olan odalar aynı).

- [ ] **Step 5: Commit**

```bash
git add internal/hub/protocol.go internal/hub/delete_room_test.go
git commit -m "fix: get_agents/get_messages_raw artık odayı materialize etmiyor (getRoom) — phantom-room + revival önlemi (#18)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: A-backend — `deletedRooms` tombstone

**Files:**
- Modify: `internal/hub/hub.go:24-84` (struct), `:87-106` (New), `:294-305` (getOrCreateRoom)
- Modify: `internal/hub/persistence.go:74-93` (persistDirtyRooms)
- Modify: `internal/hub/delete_room_test.go`

**Interfaces:**
- Produces: `Hub.deletedRooms map[string]bool` (h.mu altında); `getOrCreateRoom` create-branch'inde tombstone temizler; `persistDirtyRooms` tombstoned odayı yazmaz.

- [ ] **Step 1: Failing test yaz**

Önce `internal/hub/delete_room_test.go` import bloğuna `os` ve `path/filepath` ekle (bu task'tan itibaren kullanılıyor):

```go
import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"desktop/internal/types"
)
```

Sonra dosyaya testi ekle:

```go
func TestTombstone_PersistSkipsAndRecreateClears(t *testing.T) {
	h := New(t.TempDir(), "default", log.New(io.Discard, "", 0))

	// Dirty bir oda + tombstone → persistDirtyRooms onu YAZMAMALI.
	rs := h.getOrCreateRoom("doomed")
	if _, err := rs.SendMessage("a", "all", "hi", false, "", SendOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h.mu.Lock()
	h.deletedRooms["doomed"] = true
	h.mu.Unlock()

	h.persistDirtyRooms()

	if _, err := os.Stat(filepath.Join(h.dataDir, "hub-state", "doomed.json")); !os.IsNotExist(err) {
		t.Fatalf("tombstoned oda persist edilmemeliydi (dosya var)")
	}

	// Meşru recreation tombstone'u temizler.
	h.getOrCreateRoom("doomed")
	h.mu.RLock()
	stillTomb := h.deletedRooms["doomed"]
	h.mu.RUnlock()
	if stillTomb {
		t.Fatalf("getOrCreateRoom recreation tombstone'u temizlemeliydi")
	}
}
```

- [ ] **Step 2: Testin fail ettiğini doğrula**

Run: `go test ./internal/hub/ -run TestTombstone_PersistSkipsAndRecreateClears -v`
Expected: FAIL — `h.deletedRooms` alanı yok (compile hatası).

- [ ] **Step 3: Struct alanı + init ekle**

`internal/hub/hub.go`, `Hub` struct'ında `roomObservers` alanından sonra (satır 33 civarı) ekle:

```go
	// deletedRooms tombstones rooms removed via delete_room. The periodic persist loop
	// skips tombstoned names so an in-flight write cannot resurrect a just-deleted state
	// file; getOrCreateRoom clears the tombstone when a same-named room is legitimately
	// (re)created. Guarded by h.mu.
	deletedRooms map[string]bool
```

`New` (satır 89-105) initializer'ına ekle:

```go
		deletedRooms:     make(map[string]bool),
```

- [ ] **Step 4: getOrCreateRoom clear + persistDirtyRooms skip**

`internal/hub/hub.go`, `getOrCreateRoom` (satır 294-305), create-branch'i güncelle:

```go
func (h *Hub) getOrCreateRoom(room string) *RoomState {
	h.mu.Lock()
	defer h.mu.Unlock()

	if r, ok := h.rooms[room]; ok {
		return r
	}
	delete(h.deletedRooms, room) // legitimate (re)creation lifts any tombstone
	r := NewRoomState()
	r.SetArchiveFn(h.archiveFnFor(room))
	h.rooms[room] = r
	return r
}
```

`internal/hub/persistence.go`, `persistDirtyRooms` (satır 83-92) loop'unu güncelle:

```go
	for _, name := range roomNames {
		h.mu.RLock()
		room, ok := h.rooms[name]
		deleted := h.deletedRooms[name]
		h.mu.RUnlock()
		if !ok || deleted || !room.IsDirty() {
			continue
		}

		h.persistRoom(name, room)
	}
```

- [ ] **Step 5: Testin geçtiğini doğrula**

Run: `go test ./internal/hub/ -run TestTombstone_PersistSkipsAndRecreateClears -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/hub/hub.go internal/hub/persistence.go internal/hub/delete_room_test.go
git commit -m "feat: deletedRooms tombstone — persist resurrection guard + recreation clear (#18)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: A-backend — `delete_room` RPC + hubclient.DeleteRoom

**Files:**
- Modify: `internal/hub/protocol.go:14-60` (dispatch), yeni `handleDeleteRoom`
- Modify: `internal/hubclient/client.go:277-289` civarı (yeni `DeleteRoom`)
- Modify: `internal/hub/delete_room_test.go`

**Interfaces:**
- Consumes: `c.isDesktopAuthorized()`, `h.resolveRoom`, `h.deletedRooms`, `h.dataDir`.
- Produces: `delete_room` RPC; başarıda odayı `rooms`/`subs`/`roomManager`/`roomObservers`'tan kaldırır, `hub-state/{room}.json` siler, arşiv/sessions korunur. `HubClient.DeleteRoom(room string) error`.

- [ ] **Step 1: Failing test yaz**

`internal/hub/delete_room_test.go`'a ekle:

```go
func TestHandleDeleteRoom(t *testing.T) {
	t.Run("requires desktop auth", func(t *testing.T) {
		h := New(t.TempDir(), "default", log.New(io.Discard, "", 0))
		guest := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
		h.handleRequest(guest, types.Request{ID: "1", Type: "delete_room", Room: "x"})
		if readResponse(t, guest, "delete_room").Success {
			t.Fatalf("yetkisiz client silememeli")
		}
	})

	t.Run("rejects default room", func(t *testing.T) {
		h, c := authedDesktop(t)
		h.handleRequest(c, types.Request{ID: "1", Type: "delete_room", Room: "default"})
		if readResponse(t, c, "delete_room").Success {
			t.Fatalf("default oda silinememeli")
		}
	})

	t.Run("rejects subscribed room", func(t *testing.T) {
		h, c := authedDesktop(t)
		h.getOrCreateRoom("live")
		sub := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
		h.mu.Lock()
		h.subs["live"] = map[*Client]bool{sub: true}
		h.mu.Unlock()
		h.handleRequest(c, types.Request{ID: "1", Type: "delete_room", Room: "live"})
		if readResponse(t, c, "delete_room").Success {
			t.Fatalf("abonesi olan oda silinememeli")
		}
	})

	t.Run("deletes orphan room, preserves archive", func(t *testing.T) {
		h, c := authedDesktop(t)
		rs := h.getOrCreateRoom("orphan")
		if _, err := rs.SendMessage("a", "all", "hi", false, "", SendOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		h.persistDirtyRooms() // hub-state/orphan.json yaz

		statePath := filepath.Join(h.dataDir, "hub-state", "orphan.json")
		if _, err := os.Stat(statePath); err != nil {
			t.Fatalf("state dosyası önce var olmalı: %v", err)
		}
		// Arşiv dosyasını simüle et (silmede korunmalı).
		archiveDir := filepath.Join(h.dataDir, "hub-state", "archive")
		os.MkdirAll(archiveDir, 0700)
		archivePath := filepath.Join(archiveDir, "orphan.jsonl")
		os.WriteFile(archivePath, []byte("{}\n"), 0644)

		h.handleRequest(c, types.Request{ID: "1", Type: "delete_room", Room: "orphan"})
		if resp := readResponse(t, c, "delete_room"); !resp.Success {
			t.Fatalf("orphan silme başarılı olmalı: %s", resp.Error)
		}

		if h.getRoom("orphan") != nil {
			t.Fatalf("oda in-memory'den kaldırılmalı")
		}
		h.mu.RLock()
		tomb := h.deletedRooms["orphan"]
		h.mu.RUnlock()
		if !tomb {
			t.Fatalf("oda tombstone'lanmalı")
		}
		if _, err := os.Stat(statePath); !os.IsNotExist(err) {
			t.Fatalf("state dosyası silinmeliydi")
		}
		if _, err := os.Stat(archivePath); err != nil {
			t.Fatalf("arşiv dosyası KORUNMALIYDI: %v", err)
		}
	})
}
```

- [ ] **Step 2: Testin fail ettiğini doğrula**

Run: `go test ./internal/hub/ -run TestHandleDeleteRoom -v`
Expected: FAIL — `unknown request type: delete_room`.

- [ ] **Step 3: Dispatch + handler ekle**

`internal/hub/protocol.go`, `handleRequest` switch'ine (satır 56'dan sonra, `default`'tan önce) ekle:

```go
	case "delete_room":
		h.handleDeleteRoom(c, req)
```

`handleListRoomsDetailed`'in (satır 1004) altına yeni handler ekle:

```go
// handleDeleteRoom removes an orphan room's live state + persisted state file. Only the
// authorized desktop may call it; the default room and any subscribed/live room are
// refused (the authoritative orphan check — "no team owns this name" — lives in app.go,
// where teams are known). The append-only archive (hub-state/archive/{room}.jsonl) and
// session snapshots (hub-state/sessions/{room}/) are PRESERVED — only the live snapshot
// goes. A tombstone keeps the persist loop from resurrecting the file.
func (h *Hub) handleDeleteRoom(c *Client, req types.Request) {
	if !c.isDesktopAuthorized() {
		c.sendError(req.ID, req.Type, "yalnızca yetkili desktop istemcisi oda silebilir")
		return
	}
	room := h.resolveRoom(req.Room)
	if room == h.defaultRoom {
		c.sendError(req.ID, req.Type, "varsayılan oda silinemez")
		return
	}

	h.mu.Lock()
	if len(h.subs[room]) > 0 {
		h.mu.Unlock()
		c.sendError(req.ID, req.Type, fmt.Sprintf("'%s' odası aktif (aboneleri var); silinemez", room))
		return
	}
	h.deletedRooms[room] = true
	delete(h.rooms, room)
	delete(h.subs, room)
	delete(h.roomManager, room)
	delete(h.roomObservers, room)
	h.mu.Unlock()

	// Remove ONLY the live state file (+ stray temp). Archive + session snapshots stay.
	stateFile := filepath.Join(h.dataDir, "hub-state", room+".json")
	if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
		h.logger.Printf("delete_room: state dosyası kaldırılamadı (%s): %v", room, err)
	}
	os.Remove(stateFile + ".tmp")

	text := fmt.Sprintf("\U0001f5d1️ '%s' odası silindi.", room)
	respData, _ := json.Marshal(map[string]string{"text": text})
	c.sendJSON(types.Response{ID: req.ID, RequestType: req.Type, Success: true, Data: respData})
}
```

> `os`, `filepath`, `fmt`, `json`, `types` protocol.go'da import edili mi doğrula. `os`/`path/filepath` yoksa import bloğuna ekle (`grep -n '"os"\|path/filepath' internal/hub/protocol.go`).

- [ ] **Step 4: Testin geçtiğini doğrula**

Run: `go test ./internal/hub/ -run TestHandleDeleteRoom -v`
Expected: PASS (4 alt-test).

- [ ] **Step 5: hubclient.DeleteRoom ekle**

`internal/hubclient/client.go`, `SetObservers` (satır 289) altına ekle:

```go
// DeleteRoom removes an orphan room's state from the hub (desktop-authorized).
func (c *HubClient) DeleteRoom(room string) error {
	resp, err := c.Send(types.Request{Type: "delete_room", Room: room})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("delete_room failed: %s", resp.Error)
	}
	return nil
}
```

- [ ] **Step 6: Build + commit**

Run: `go build ./... && go test ./internal/hub/ ./internal/hubclient/`
Expected: PASS.

```bash
git add internal/hub/protocol.go internal/hub/delete_room_test.go internal/hubclient/client.go
git commit -m "feat: delete_room RPC — orphan oda güvenli silme + hubclient.DeleteRoom (#18)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 6: A-backend — app.go `DeleteRoom` binding (orphan/default check)

**Files:**
- Modify: `app.go` (Team Bindings bölgesi, ~satır 1500-1530 civarı)

**Interfaces:**
- Consumes: `a.teamStore.List()`, `a.hubClient.Load()`, `roomNameOrDefault`, `hubClient.DeleteRoom`.
- Produces: `(a *App) DeleteRoom(room string) error` — Wails binding.

- [ ] **Step 1: Binding'i ekle**

`app.go`, `CreateTeam` (satır 1529) sonrasına ekle:

```go
// DeleteRoom removes an orphan room (one that no team owns) from the hub. Team-backed
// rooms must go through DeleteTeam instead; the default room cannot be deleted. The
// hub re-checks subscribers as defense-in-depth, but the authoritative "is this orphan"
// decision lives here because the hub does not know about teams.
func (a *App) DeleteRoom(room string) error {
	room = roomNameOrDefault(room)
	if room == "default" {
		return fmt.Errorf("varsayılan oda silinemez")
	}
	for _, t := range a.teamStore.List() {
		if roomNameOrDefault(t.Name) == room {
			return fmt.Errorf("'%s' bir takıma bağlı; önce takımı silin", room)
		}
	}
	client := a.hubClient.Load()
	if client == nil {
		return fmt.Errorf("hub bağlı değil")
	}
	return client.DeleteRoom(room)
}
```

> `fmt` app.go'da import edili (zaten kullanılıyor).

- [ ] **Step 2: Build + vet doğrula**

Run: `make mcp-server && go build ./... && go vet ./...`
Expected: hatasız derlenir.

- [ ] **Step 3: Commit**

```bash
git add app.go
git commit -m "feat: App.DeleteRoom binding — orphan-only + default koruması (#18)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 7: Wails binding regen

**Files:**
- Modify (generated): `frontend/wailsjs/go/main/App.d.ts`, `App.js`, `frontend/wailsjs/go/models.ts`

**Interfaces:**
- Produces: `DeleteRoom(room: string): Promise<void>` binding; `types.RoomSummary` modelinde `historical_agents`.

- [ ] **Step 1: Binding'leri üret**

Run: `make mcp-server && wails generate module`
(Alternatif: `make dev`'i bir kez başlatıp binding üretimini tetikle, sonra durdur.)

- [ ] **Step 2: Üretimi doğrula**

Run: `grep -n "DeleteRoom" frontend/wailsjs/go/main/App.d.ts && grep -n "historical_agents\|HistoricalAgents" frontend/wailsjs/go/models.ts`
Expected: `DeleteRoom` deklarasyonu + `RoomSummary` modelinde historical alanı görünür.

- [ ] **Step 3: Commit**

```bash
git add frontend/wailsjs/
git commit -m "chore: Wails binding regen — DeleteRoom + RoomSummary.historical_agents (#18)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 8: C-frontend — `historical_agents` tip + RoomRow rozetleri

**Files:**
- Modify: `frontend/src/lib/types.ts:65-71`
- Modify: `frontend/src/components/RoomBrowser.tsx:67-83` (RoomRow agents bölgesi)
- Modify: `frontend/src/styles/globals.css`

**Interfaces:**
- Consumes: `RoomSummary.historical_agents`.
- Produces: roster boş ama historical dolu odalarda "geçmişte bulunmuş" rozetleri.

- [ ] **Step 1: TS tipini güncelle**

`frontend/src/lib/types.ts`, `RoomSummary` interface'ine ekle:

```ts
export interface RoomSummary {
  name: string;
  message_count: number;
  agents: Record<string, Agent>;
  historical_agents: string[];
  last_activity: string;
  is_default: boolean;
}
```

- [ ] **Step 2: RoomRow'da historical rozetleri göster**

`frontend/src/components/RoomBrowser.tsx`, `RoomRow` içindeki agents bloğunu (satır 67-83) değiştir:

```tsx
      <div className="room-row-agents">
        {isEmpty ? (
          <span className="room-badge room-badge-empty">empty room</span>
        ) : agentNames.length > 0 ? (
          agentNames.map((n) => (
            <span
              key={n}
              className="room-agent-badge"
              title={room.agents[n]?.role || ""}
            >
              {n}
            </span>
          ))
        ) : room.historical_agents?.length > 0 ? (
          <span className="room-historical">
            <span className="room-historical-label">geçmişte bulunmuş:</span>
            {room.historical_agents.map((n) => (
              <span key={n} className="room-agent-badge room-agent-badge-historical">
                {n}
              </span>
            ))}
          </span>
        ) : (
          <span className="room-agent-empty">no agents (archived room)</span>
        )}
      </div>
```

- [ ] **Step 3: Stil ekle**

`frontend/src/styles/globals.css` sonuna ekle:

```css
.room-historical { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; }
.room-historical-label { font-size: 11px; opacity: 0.6; margin-right: 2px; }
.room-agent-badge-historical { opacity: 0.7; border-style: dashed; }
```

- [ ] **Step 4: Typecheck + görsel doğrula**

Run: `cd frontend && npm run build`
Expected: tsc hatasız derler.

Manuel (`make dev`): roster boş bir geçmiş odasında (örn. `ExportGeo`) "geçmişte bulunmuş: …" rozetleri görünür.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/types.ts frontend/src/components/RoomBrowser.tsx frontend/src/styles/globals.css
git commit -m "feat: RoomBrowser geçmiş-oda agent rozetleri (historical_agents) (#18)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 9: A-frontend — Oda silme UI (useRooms.deleteRoom + sil butonu)

**Files:**
- Modify: `frontend/src/store/useRooms.ts`
- Modify: `frontend/src/components/RoomBrowser.tsx` (RoomRow yeniden yapılandır + sil)
- Modify: `frontend/src/styles/globals.css`

**Interfaces:**
- Consumes: `DeleteRoom` (wailsjs).
- Produces: `useRooms.deleteRoom(name)`; orphan satırlarda 🗑 + onay.

- [ ] **Step 1: useRooms'a deleteRoom ekle**

`frontend/src/store/useRooms.ts`'i güncelle — import ve interface + aksiyon:

```ts
import { create } from "zustand";
import { RoomSummary } from "../lib/types";
import { ListRooms, DeleteRoom } from "../../wailsjs/go/main/App";

interface RoomsState {
  rooms: RoomSummary[];
  loading: boolean;
  error: string | null;
  selectedRoom: string | null;

  loadRooms: () => Promise<void>;
  selectRoom: (name: string | null) => void;
  deleteRoom: (name: string) => Promise<void>;
}
```

`selectRoom` satırından sonra aksiyon ekle:

```ts
  deleteRoom: async (name) => {
    await DeleteRoom(name);
    set((s) => ({
      rooms: s.rooms.filter((r) => r.name !== name),
      selectedRoom: s.selectedRoom === name ? null : s.selectedRoom,
    }));
  },
```

- [ ] **Step 2: RoomRow'u sil butonuyla yeniden yapılandır**

`<button class="room-row">` içine başka bir buton gömülemez. `RoomRow`'u (satır 32-86) tümüyle değiştir — dış öğe `div`, içinde tıklanır alan + orphan'da sil butonu (Task 8'in historical rozetlerini KORUR):

```tsx
function RoomRow({
  room,
  isActiveTeam,
  onClick,
  onDelete,
}: {
  room: RoomSummary;
  isActiveTeam: boolean;
  onClick: () => void;
  onDelete: () => void;
}) {
  const agentNames = Object.keys(room.agents || {});
  const isEmpty = room.message_count === 0 && agentNames.length === 0;
  const countLabel =
    room.message_count >= MESSAGE_CAP
      ? `last ${room.message_count} messages`
      : `${room.message_count} messages`;

  return (
    <div className="room-row" role="button" tabIndex={0} onClick={onClick}>
      <div className="room-row-top">
        <span className="room-row-name">
          {room.name}
          {room.is_default && <span className="room-tag">default</span>}
        </span>
        <span className="room-row-time">{relativeTime(room.last_activity)}</span>
        {!isActiveTeam && !room.is_default && (
          <button
            className="room-delete"
            title="Bu orphan odayı sil"
            onClick={(e) => {
              e.stopPropagation();
              onDelete();
            }}
          >
            🗑
          </button>
        )}
      </div>
      <div className="room-row-meta">
        <span className="room-row-count">{countLabel}</span>
        <span
          className={`room-row-origin ${
            isActiveTeam ? "origin-team" : "origin-orphan"
          }`}
        >
          {isActiveTeam ? "team" : "no team"}
        </span>
      </div>
      <div className="room-row-agents">
        {isEmpty ? (
          <span className="room-badge room-badge-empty">empty room</span>
        ) : agentNames.length > 0 ? (
          agentNames.map((n) => (
            <span
              key={n}
              className="room-agent-badge"
              title={room.agents[n]?.role || ""}
            >
              {n}
            </span>
          ))
        ) : room.historical_agents?.length > 0 ? (
          <span className="room-historical">
            <span className="room-historical-label">geçmişte bulunmuş:</span>
            {room.historical_agents.map((n) => (
              <span key={n} className="room-agent-badge room-agent-badge-historical">
                {n}
              </span>
            ))}
          </span>
        ) : (
          <span className="room-agent-empty">no agents (archived room)</span>
        )}
      </div>
    </div>
  );
}
```

- [ ] **Step 3: RoomBrowser ana liste — onDelete + onay bağla**

`RoomBrowser` bileşeninde `deleteRoom` al ve RoomRow'a geçir. `selectRoom` satırından sonra:

```tsx
  const deleteRoom = useRooms((s) => s.deleteRoom);
```

Liste render'ında (satır 209-215) RoomRow çağrısını güncelle:

```tsx
            <RoomRow
              key={r.name}
              room={r}
              isActiveTeam={teamNames.has(r.name)}
              onClick={() => selectRoom(r.name)}
              onDelete={() => {
                if (
                  window.confirm(
                    `'${r.name}' odası silinsin mi? Mesaj geçmişi (state) kaldırılır; arşiv korunur.`,
                  )
                ) {
                  deleteRoom(r.name).catch((e) =>
                    window.alert(`Silme başarısız: ${e}`),
                  );
                }
              }}
            />
```

- [ ] **Step 4: Stil ekle**

`frontend/src/styles/globals.css` sonuna ekle:

```css
.room-row { cursor: pointer; }
.room-delete {
  background: none; border: none; cursor: pointer; opacity: 0.5;
  font-size: 13px; padding: 0 2px; margin-left: 4px;
}
.room-delete:hover { opacity: 1; }
```

- [ ] **Step 5: Typecheck + görsel doğrula**

Run: `cd frontend && npm run build`
Expected: tsc hatasız.

Manuel (`make dev`): orphan odada 🗑 görünür; takım/default odada görünmez; tıkla → onay → silinince listeden düşer; uygulama yeniden başlatınca oda geri gelmez (dirilmez).

- [ ] **Step 6: Commit**

```bash
git add frontend/src/store/useRooms.ts frontend/src/components/RoomBrowser.tsx frontend/src/styles/globals.css
git commit -m "feat: RoomBrowser orphan oda silme (sil butonu + onay + useRooms.deleteRoom) (#18)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 10: B-frontend — Orphan→takım import diyaloğu

**Files:**
- Create: `frontend/src/components/ImportRoomModal.tsx`
- Modify: `frontend/src/components/RoomBrowser.tsx` (RoomDetail'e "Takıma Aktar" + summary geçişi)
- Modify: `frontend/src/styles/globals.css`

**Interfaces:**
- Consumes: `useTeams.createTeam(name, gridLayout, agents)`; `RoomSummary` (agents/historical_agents).
- Produces: orphan odayı takım olarak içe aktaran modal.

- [ ] **Step 1: ValidateName mirror + modal bileşeni**

Create `frontend/src/components/ImportRoomModal.tsx`:

```tsx
import { useState } from "react";
import { RoomSummary, AgentConfig } from "../lib/types";
import { useTeams } from "../store/useTeams";

// Mirror of internal/validation.ValidateName: ASCII letters/digits/._- and space,
// 1-50 chars, no leading dot, no "..". Turkish chars are rejected — a room whose name
// fails this cannot be imported, because team name must equal room name for the new
// team to subscribe to the existing room and inherit its history.
function isValidName(name: string): boolean {
  if (!/^[a-zA-Z0-9._\- ]{1,50}$/.test(name)) return false;
  if (name.includes("..")) return false;
  if (name.startsWith(".")) return false;
  return true;
}

type Row = { name: string; role: string; cli_type: string };

function seedRows(room: RoomSummary): Row[] {
  const agentNames = Object.keys(room.agents || {});
  if (agentNames.length > 0) {
    return agentNames.map((n) => ({
      name: n,
      role: room.agents[n]?.role || "",
      cli_type: "claude",
    }));
  }
  if (room.historical_agents?.length > 0) {
    return room.historical_agents.map((n) => ({ name: n, role: "", cli_type: "claude" }));
  }
  return [];
}

export default function ImportRoomModal({
  room,
  onClose,
  onImported,
}: {
  room: RoomSummary;
  onClose: () => void;
  onImported: () => void;
}) {
  const createTeam = useTeams((s) => s.createTeam);
  const [rows, setRows] = useState<Row[]>(() => seedRows(room));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const nameOk = isValidName(room.name);

  const update = (i: number, patch: Partial<Row>) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const remove = (i: number) => setRows((rs) => rs.filter((_, j) => j !== i));
  const add = () => setRows((rs) => [...rs, { name: "", role: "", cli_type: "claude" }]);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const agents: AgentConfig[] = rows
        .filter((r) => r.name.trim() !== "")
        .map((r, i) => ({
          name: r.name.trim(),
          role: r.role.trim(),
          prompt_id: "",
          work_dir: "",
          cli_type: r.cli_type,
          slot_index: i,
          use_worktree: false,
        }));
      await createTeam(room.name, "2x2", agents);
      onImported();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal import-modal" onClick={(e) => e.stopPropagation()}>
        <h3>Takıma Aktar: {room.name}</h3>
        {!nameOk && (
          <p className="import-error">
            Oda adı geçersiz karakter içeriyor (yalnız ASCII harf/rakam/._- ve boşluk).
            İçe aktarılamaz.
          </p>
        )}
        <div className="import-rows">
          {rows.map((r, i) => (
            <div key={i} className="import-row">
              <input
                placeholder="ad"
                value={r.name}
                onChange={(e) => update(i, { name: e.target.value })}
              />
              <input
                placeholder="rol"
                value={r.role}
                onChange={(e) => update(i, { role: e.target.value })}
              />
              <select
                value={r.cli_type}
                onChange={(e) => update(i, { cli_type: e.target.value })}
              >
                <option value="claude">claude</option>
                <option value="gemini">gemini</option>
                <option value="copilot">copilot</option>
              </select>
              <button onClick={() => remove(i)} title="Kaldır">
                ✕
              </button>
            </div>
          ))}
          <button className="import-add" onClick={add}>
            + Agent ekle
          </button>
        </div>
        {err && <p className="import-error">İçe aktarma başarısız: {err}</p>}
        <div className="modal-actions">
          <button onClick={onClose} disabled={busy}>
            İptal
          </button>
          <button onClick={submit} disabled={busy || !nameOk}>
            {busy ? "Aktarılıyor…" : "Takım oluştur"}
          </button>
        </div>
      </div>
    </div>
  );
}
```

> Doğrulanmış: `useTeams.createTeam(name, gridLayout, agents): Promise<Team>` (CreateTeam binding'ini sarar). `modal-overlay`/`modal`/`modal-actions` sınıfları `RoomSummaryModal.tsx`'te mevcut → yeniden kullan; yalnız import'a özgü stiller Step 3'te eklenir.

- [ ] **Step 2: RoomDetail'e "Takıma Aktar" butonu + summary geçişi**

`frontend/src/components/RoomBrowser.tsx`:

İmport satırına ekle:

```tsx
import ImportRoomModal from "./ImportRoomModal";
```

`RoomDetail` imzasını ve gövdesini güncelle — `summary` prop'u al, orphan'da import butonu göster:

```tsx
function RoomDetail({
  room,
  summary,
  isActiveTeam,
  onBack,
  onImported,
}: {
  room: string;
  summary: RoomSummary | undefined;
  isActiveTeam: boolean;
  onBack: () => void;
  onImported: () => void;
}) {
  const loadMessages = useMessages((s) => s.loadMessages);
  const loadAgents = useMessages((s) => s.loadAgents);
  const agents = useAgentsFor(room);
  const [showSummary, setShowSummary] = useState(false);
  const [showImport, setShowImport] = useState(false);

  useEffect(() => {
    loadMessages(room);
    loadAgents(room);
  }, [room, loadMessages, loadAgents]);

  const agentNames = Object.keys(agents || {});
```

Header'a (📝 Özet butonunun yanına) ekle:

```tsx
        {!isActiveTeam && summary && (
          <button
            className="room-back"
            title="Bu orphan odayı yeni takım olarak içe aktar"
            onClick={() => setShowImport(true)}
          >
            ⬇️ Takıma Aktar
          </button>
        )}
```

Render sonuna (RoomSummaryModal'dan sonra) ekle:

```tsx
      {showImport && summary && (
        <ImportRoomModal
          room={summary}
          onClose={() => setShowImport(false)}
          onImported={() => {
            setShowImport(false);
            onImported();
          }}
        />
      )}
```

`RoomBrowser` ana bileşeninde, `selectedRoom` branch'ini (satır 178-186) güncelle:

```tsx
  if (selectedRoom) {
    return (
      <RoomDetail
        room={selectedRoom}
        summary={rooms.find((r) => r.name === selectedRoom)}
        isActiveTeam={teamNames.has(selectedRoom)}
        onBack={() => selectRoom(null)}
        onImported={() => {
          selectRoom(null);
          loadRooms();
        }}
      />
    );
  }
```

- [ ] **Step 3: Import'a özgü stilleri ekle**

`modal-overlay`/`modal`/`modal-actions` zaten var; yalnız import'a özgü sınıfları `frontend/src/styles/globals.css` sonuna ekle:

```css
.import-modal { min-width: 360px; max-width: 90vw; }
.import-rows { display: flex; flex-direction: column; gap: 6px; margin: 10px 0; }
.import-row { display: flex; gap: 6px; align-items: center; }
.import-row input { flex: 1; min-width: 0; }
.import-add { align-self: flex-start; }
.import-error { color: #c0392b; font-size: 12px; }
```

- [ ] **Step 4: Typecheck + görsel doğrula**

Run: `cd frontend && npm run build`
Expected: tsc hatasız.

Manuel (`make dev`):
- Orphan odayı aç → "⬇️ Takıma Aktar" görünür (takım odasında görünmez).
- Agent satırları persist roster'dan / historical'dan ön-dolu; CLI seç; "Takım oluştur" → yeni takım açılır, geçmiş mesajlar görünür (oda adı=takım adı subscribe ettiği için).
- Türkçe karakterli oda adında import butonu uyarı verir, "Takım oluştur" disabled.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/ImportRoomModal.tsx frontend/src/components/RoomBrowser.tsx frontend/src/styles/globals.css
git commit -m "feat: orphan→takım içe aktarma diyaloğu (ImportRoomModal) (#18)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Final Doğrulama

- [ ] **Tüm Go testleri:** `go test ./...` → PASS.
- [ ] **Build:** `make mcp-server && go build ./... && go vet ./...` → temiz.
- [ ] **Frontend:** `cd frontend && npm run build` → temiz.
- [ ] **Uçtan uca (`make dev`, localhost:34115):**
  - Geçmiş oda (roster boş) "geçmişte bulunmuş" rozetleriyle görünüyor.
  - Orphan oda silinebiliyor; takım/default oda silinemiyor; silinen oda yeniden başlatınca dirilmiyor; arşiv dosyası diskte duruyor.
  - Orphan→import sonrası yeni takımda geçmiş mesajlar akıyor.
  - Türkçe-adlı oda import'u net hata ile bloklanıyor.
- [ ] **PR aç:** `gh pr create` — başlık "feat: Room Browser v2 — oda silme + orphan import + geçmiş-oda agent isimleri (#18)".

## Riskler / Notlar

- **Tombstone'un asıl güvenliği:** Silmeye uygun orphan odalar zaten *dirty değil* ve abonesiz; persist döngüsü onları `!IsDirty()` ile zaten atlıyor, in-memory'den kalkınca `!ok` ile atlıyor. Tombstone, persist-IO yarışına karşı defansif ve `getOrCreateRoom`'un "yeniden yaratma"yı temiz ayırt etmesini sağlar (recreation tombstone'u temizler). Okuma-path sertleştirmesi (getRoom) stray okumanın diriltmesini engeller.
- **Türkçe oda adı kısıtı (B):** Süreklilik takım adı=oda adı gerektirdiğinden Türkçe-adlı orphan oda import edilemez (bilinen kısıt, net hata).
- **MCP `list_rooms` (agent-facing metin) değişmiyor** → agent davranışı aynı.
- **Frontend test runner yok:** doğrulama tsc + görsel. Mümkünse her frontend task'ından sonra `make dev` ile bak.
