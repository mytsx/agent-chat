package orchestrator

import (
	"testing"

	"desktop/internal/types"
)

// A user_prompt is a transcript record of a human→agent prompt that was ALREADY
// delivered to the target agent's PTY. The orchestrator must never inject it,
// even when its content (a question, expects_reply) would normally notify —
// otherwise the prompt echoes back into terminals (#29 echo bug).
func TestProcessMessage_UserPromptSkipped(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "agent-1", "sess-11111111")

	msg := types.Message{
		From:         "user",
		To:           "agent-1",
		Content:      "Bunu nasıl yaparsın?",
		Type:         "user_prompt",
		ExpectsReply: true,
	}
	o.ProcessMessage("/rooms/t", msg)

	if len(*sent) != 0 {
		t.Fatalf("user_prompt must not be injected, got %d notification(s): %+v", len(*sent), *sent)
	}
}

func TestProcessMessage_UserPromptBroadcastSkipped(t *testing.T) {
	o, sent := newTestOrchestrator()
	o.RegisterAgent("/rooms/t", "agent-1", "sess-11111111")
	o.RegisterAgent("/rooms/t", "agent-2", "sess-22222222")

	msg := types.Message{
		From:         "user",
		To:           "all",
		Content:      "Herkes durumunu raporlasın?",
		Type:         "user_prompt",
		ExpectsReply: true,
	}
	o.ProcessMessage("/rooms/t", msg)

	if len(*sent) != 0 {
		t.Fatalf("broadcast user_prompt must not be injected, got %d notification(s): %+v", len(*sent), *sent)
	}
}
