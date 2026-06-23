package ingest

import "testing"

func TestCodexParse_NewFormatUsesEventMsgOnly(t *testing.T) {
	dir := t.TempDir()
	// New (2026) format: the user turn appears as BOTH an event_msg/user_message
	// AND a response_item/message(role:user). Only the event_msg counts (dedup).
	jsonl := `{"timestamp":"2026-06-23T17:00:00.000Z","type":"session_meta","payload":{"cwd":"/x","cli_version":"0.142.0"}}
{"timestamp":"2026-06-23T17:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"build the thing"}}
{"timestamp":"2026-06-23T17:00:05.001Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"build the thing"}]}}
{"timestamp":"2026-06-23T17:00:09.000Z","type":"event_msg","payload":{"type":"agent_message","message":"done"}}
`
	p := writeFile(t, dir, "rollout-x.jsonl", jsonl)
	msgs, _, err := codexAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "build the thing" {
		t.Fatalf("got %+v, want exactly one user_message (no response_item dup)", msgs)
	}
}

func TestCodexParse_OldFormatMessageRoleUser(t *testing.T) {
	dir := t.TempDir()
	jsonl := `{"type":"message","role":"user","content":"eski format mesaj"}
{"type":"message","role":"assistant","content":"cevap"}
`
	p := writeFile(t, dir, "rollout-y.jsonl", jsonl)
	msgs, _, err := codexAdapter{}.ParseNewUserMessages(p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "eski format mesaj" {
		t.Fatalf("got %+v, want the old-format user message", msgs)
	}
}
