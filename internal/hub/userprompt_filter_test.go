package hub

import (
	"testing"

	"desktop/internal/types"
)

// A logged user_prompt must feed summaries (raw read / transcript) but must NOT
// be returned by the agent-facing read RPCs — the target agent already received
// it via its PTY, so re-reading it would make the agent re-handle the same
// instruction (#29 Codex review).
func TestUserPromptFilteredFromAgentReads(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("alice", "developer"); err != nil {
		t.Fatal(err)
	}
	r.SendMessage("bob", "alice", "merhaba", true, "normal", SendOptions{})
	r.LogUserPrompt(types.UserPromptFrom, "alice", "şu görevi yap")

	// read_messages("alice") — no user_prompt, but the normal message stays.
	msgs, _ := r.ReadMessages("alice", 0, 0, false)
	for _, m := range msgs {
		if m.Type == types.MsgTypeUserPrompt {
			t.Fatalf("ReadMessages leaked a user_prompt to the agent: %+v", m)
		}
	}
	foundNormal := false
	for _, m := range msgs {
		if m.Content == "merhaba" {
			foundNormal = true
		}
	}
	if !foundNormal {
		t.Fatal("ReadMessages dropped the normal direct message")
	}

	// read_all_messages (manager) — also excludes user_prompt.
	all, _ := r.ReadAllMessages(0, 0)
	for _, m := range all {
		if m.Type == types.MsgTypeUserPrompt {
			t.Fatalf("ReadAllMessages leaked a user_prompt: %+v", m)
		}
	}

	// Raw read (desktop human view + summary source) MUST keep it.
	rawHasPrompt := false
	for _, m := range r.GetMessages() {
		if m.Type == types.MsgTypeUserPrompt {
			rawHasPrompt = true
		}
	}
	if !rawHasPrompt {
		t.Fatal("GetMessages (raw) must keep user_prompt for the human view + summary")
	}
}

// get_last_message_id must report the last AGENT-VISIBLE message id, consistent
// with read_messages/read_all_messages (which hide user_prompt). Otherwise an
// agent seeding its polling cursor from it sets since_id past the hidden prompt
// and permanently skips older unread visible messages (#29 Codex review).
func TestGetLastMessageIDSkipsUserPrompt(t *testing.T) {
	r := NewRoomState()
	if _, _, err := r.Join("alice", "developer"); err != nil {
		t.Fatal(err)
	}
	visible, _ := r.SendMessage("bob", "alice", "m1", true, "normal", SendOptions{})
	r.LogUserPrompt(types.UserPromptFrom, "alice", "hidden") // last raw message, filtered from reads

	got := r.GetLastMessageID("alice")
	if got != visible.ID {
		t.Fatalf("GetLastMessageID = %d, want %d (last visible, skipping the trailing user_prompt)", got, visible.ID)
	}
}
