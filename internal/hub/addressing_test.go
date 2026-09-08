package hub

import (
	"strings"
	"testing"

	"desktop/internal/types"
)

// #99 C: a read capped by its limit returned the NEWEST matching tail, while the
// agent advanced since_id to the highest id it saw. Everything between the
// cursor and that tail was skipped — silently, with no way for the agent to
// notice. Paging must move forward from the cursor instead.
func TestReadMessagesPagesForwardFromCursor(t *testing.T) {
	r := NewRoomState()
	joinMsg, _, err := r.Join("bob", "")
	if err != nil {
		t.Fatal(err)
	}
	// Page from after the join's own system message.
	cursor := joinMsg.ID
	for i := range 5 {
		if _, err := r.SendMessage("alice", "bob", string(rune('a'+i)), false, "", SendOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	got, total, _ := r.ReadMessages("bob", cursor, 2, true)
	if total < 5 {
		t.Fatalf("eşleşen toplam = %d, want >= 5", total)
	}
	if len(got) != 2 {
		t.Fatalf("dönen = %d, want 2", len(got))
	}
	// Oldest first: the two returned must be the two oldest, not the newest.
	if got[0].Content != "a" || got[1].Content != "b" {
		t.Errorf("dönen = %q,%q; want a,b — kuyruk döndürmek aradaki mesajları atlatıyor",
			got[0].Content, got[1].Content)
	}

	// Paging with the returned cursor must continue without a gap.
	next, _, _ := r.ReadMessages("bob", got[len(got)-1].ID, 2, true)
	if len(next) != 2 || next[0].Content != "c" {
		t.Errorf("ikinci sayfa = %+v; want c,d", next)
	}
}

// #99 B: a direct message to somebody who is not in the room was stored and
// silently went nowhere. The sender must be told, and told who IS there.
func TestSendToAbsentRecipientIsRejectedWithRoster(t *testing.T) {
	h, alice, _ := newEventHub(t)
	joinAgent(t, h, alice, "r1", "alice")

	h.handleSendMessage(alice, types.Request{
		ID: "send", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "hayalet", "content": "merhaba"}),
	})
	resp := readResponse(t, alice, "send_message")
	if resp.Success {
		t.Fatal("odada olmayan alıcıya gönderim başarılı sayıldı")
	}
	if !strings.Contains(resp.Error, "hayalet") {
		t.Errorf("hata alıcıyı söylemiyor: %q", resp.Error)
	}
	if !strings.Contains(resp.Error, "alice") {
		t.Errorf("hata odadaki mevcut isimleri listelemiyor: %q", resp.Error)
	}
}

// Broadcasts have no single recipient to check, and must keep working.
func TestBroadcastIsNotAffectedByRecipientCheck(t *testing.T) {
	h, alice, _ := newEventHub(t)
	joinAgent(t, h, alice, "r1", "alice")

	h.handleSendMessage(alice, types.Request{
		ID: "send", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "all", "content": "herkese"}),
	})
	if resp := readResponse(t, alice, "send_message"); !resp.Success {
		t.Errorf("broadcast reddedildi: %s", resp.Error)
	}
}

// With a manager gateway active the recipient is advisory — the message goes to
// the manager regardless — so rejecting it would break manager routing.
func TestManagerRoutingAllowsUnknownRecipient(t *testing.T) {
	h, alice, _ := newEventHub(t)
	mgr := &Client{hub: h, send: make(chan []byte, 64), rooms: make(map[string]bool)}
	h.setConfiguredManager("r1", "yonetici")
	h.handleJoinRoom(mgr, types.Request{
		ID: "join-mgr", Type: "join_room", Room: "r1",
		Data: mustRawJSON(t, map[string]string{"agent_name": "yonetici", "role": "manager"}),
	})
	if resp := readResponse(t, mgr, "join_room"); !resp.Success {
		t.Fatalf("manager join başarısız: %s", resp.Error)
	}
	joinAgent(t, h, alice, "r1", "alice")

	h.handleSendMessage(alice, types.Request{
		ID: "send", Type: "send_message", Room: "r1",
		Data: mustRawJSON(t, map[string]any{"from": "alice", "to": "henuz-yok", "content": "merhaba"}),
	})
	if resp := readResponse(t, alice, "send_message"); !resp.Success {
		t.Errorf("manager aktifken bilinmeyen alıcı reddedildi: %s", resp.Error)
	}
}

// #99 A: an empty room must resolve to the room this connection joined, not to a
// process-wide default. The default is what put 1.479 agents into "default" and
// left them unable to see their team.
func TestEmptyRoomResolvesToJoinedRoom(t *testing.T) {
	h, c, _ := newEventHub(t)
	joinAgent(t, h, c, "takim", "alice")

	if got := h.resolveRoomFor(c, ""); got != "takim" {
		t.Errorf("boş oda %q'ya çözüldü, want takim (bağlantının katıldığı oda)", got)
	}
	if got := h.resolveRoomFor(c, "baska"); got != "baska" {
		t.Errorf("açık oda ezildi: %q", got)
	}

	unjoined := &Client{hub: h, send: make(chan []byte, 8), rooms: make(map[string]bool)}
	if got := h.resolveRoomFor(unjoined, ""); got != h.defaultRoom {
		t.Errorf("katılmamış istemci için %q, want varsayılan %q", got, h.defaultRoom)
	}
}
