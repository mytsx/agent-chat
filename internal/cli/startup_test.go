package cli

import (
	"strings"
	"testing"
)

func TestComposeStartupPrompt_ManagerRole(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "", "manager-agent", "backend", "team-a", "manager", "")
	if !strings.Contains(got, `join_room("manager-agent", "manager")`) {
		t.Fatalf("expected manager join instruction, got:\n%s", got)
	}
	if !strings.Contains(got, "read_all_messages") {
		t.Fatalf("expected read_all_messages instruction for manager")
	}
}

func TestComposeStartupPrompt_ManagerReadInstructionPassesExplicitLimit(t *testing.T) {
	// read_all_messages default limit is 15 (mcpserver + hub); without an explicit
	// limit a fresh manager silently sees only the last 15 messages while believing
	// it read the full history. With NO summary present the startup instruction must
	// pass a high limit so "tüm mesajları oku" matches actual behavior.
	got := ComposeStartupPrompt("base", "", "", "", "", "manager-agent", "backend", "team-a", "manager", "")
	if !strings.Contains(got, "read_all_messages(since_id=0, limit=") {
		t.Fatalf("expected manager read_all_messages instruction to pass an explicit limit, got:\n%s", got)
	}
}

func TestComposeStartupPrompt_UsesConfiguredRole(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "", "backend", "Backend API Developer", "team-a", "", "")
	if !strings.Contains(got, `join_room("backend", "Backend API Developer")`) {
		t.Fatalf("expected normal join instruction, got:\n%s", got)
	}
	if !strings.Contains(got, `read_messages("backend")`) {
		t.Fatalf("expected read_messages instruction for non-manager")
	}
}

func TestComposeStartupPrompt_FallbackRoleUsesAgentName(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "", "backend", "", "team-a", "", "")
	if !strings.Contains(got, `join_room("backend", "backend")`) {
		t.Fatalf("expected fallback role=agent_name, got:\n%s", got)
	}
}

// #29: the room summary is injected as its own segment AFTER the team charter
// and BEFORE the library-selected prompt, with a clear header so agents treat it
// as prior-session context rather than an instruction.
func TestComposeStartupPrompt_RoomSummarySegmentOrder(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "CHARTER", "OZET-METNI", "SELECTED", "agent", "", "team", "", "")
	if !strings.Contains(got, "OZET-METNI") {
		t.Fatalf("summary text missing:\n%s", got)
	}
	if !strings.Contains(got, "Önceki Session Özeti") {
		t.Fatalf("summary header missing:\n%s", got)
	}
	iCharter := strings.Index(got, "CHARTER")
	iSummary := strings.Index(got, "OZET-METNI")
	iSelected := strings.Index(got, "SELECTED")
	if !(iCharter < iSummary && iSummary < iSelected) {
		t.Fatalf("segment order wrong: charter=%d summary=%d selected=%d\n%s", iCharter, iSummary, iSelected, got)
	}
}

func TestComposeStartupPrompt_NoSummaryNoHeader(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "CHARTER", "", "SELECTED", "agent", "", "team", "", "")
	if strings.Contains(got, "Önceki Session Özeti") {
		t.Fatalf("unexpected summary header when no summary:\n%s", got)
	}
}

// #29: when a summary is present, the manager join instruction must steer to
// read_summary instead of pulling the entire history (limit=1000), avoiding the
// token bloat the summary exists to prevent.
func TestComposeStartupPrompt_ManagerSummaryRedirect(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "OZET", "", "mgr", "", "team", "manager", "")
	if !strings.Contains(got, "read_summary") {
		t.Fatalf("expected read_summary redirect for manager with summary:\n%s", got)
	}
	if strings.Contains(got, "limit=1000") {
		t.Fatalf("manager-with-summary should avoid full-history limit=1000:\n%s", got)
	}
}

func TestComposeStartupPrompt_NonManagerSummaryMentionsSummary(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "OZET", "", "backend", "", "team", "", "")
	if !strings.Contains(got, "read_summary") {
		t.Fatalf("expected read_summary mention for non-manager with summary:\n%s", got)
	}
}

// #17: an observer joins with role "observer", watches via read_all_messages, and
// is explicitly told NOT to send messages — only talk to the user.
func TestComposeStartupPrompt_ObserverRole(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "", "watcher", "", "team-a", "observer", "")
	if !strings.Contains(got, `join_room("watcher", "observer")`) {
		t.Fatalf("expected observer join instruction, got:\n%s", got)
	}
	if !strings.Contains(got, "read_all_messages") {
		t.Fatalf("expected observer to watch via read_all_messages, got:\n%s", got)
	}
	// read_all_messages defaults to limit=15; without an explicit high limit the
	// observer would only see the last 15 messages, not "all traffic" (Codex P2).
	if !strings.Contains(got, "read_all_messages(since_id=0, limit=") {
		t.Fatalf("expected observer read_all to pass an explicit limit, got:\n%s", got)
	}
	if !strings.Contains(got, "GÖNDERME") {
		t.Fatalf("expected observer to be told NOT to send messages, got:\n%s", got)
	}
	// An observer must never be steered to send_message or be locked as manager.
	if strings.Contains(got, `join_room("watcher", "manager")`) {
		t.Fatalf("observer must not join as manager:\n%s", got)
	}
}

// #17: the observer's role overrides any descriptive agentRole — it must join as
// "observer" so the hub recognizes and gates it, not as its free-text role.
func TestComposeStartupPrompt_ObserverRoleOverridesAgentRole(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "", "watcher", "Senior Reviewer", "team-a", "observer", "")
	if !strings.Contains(got, `join_room("watcher", "observer")`) {
		t.Fatalf("observer mode must force role=observer regardless of agentRole, got:\n%s", got)
	}
}

// #17: even with a prior-session summary present, the observer is still steered to
// watch (read_all_messages) and not to send.
func TestComposeStartupPrompt_ObserverSummaryStillWatches(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "OZET", "", "watcher", "", "team-a", "observer", "")
	if !strings.Contains(got, "read_all_messages") {
		t.Fatalf("observer-with-summary should still watch via read_all_messages, got:\n%s", got)
	}
	if !strings.Contains(got, "GÖNDERME") {
		t.Fatalf("observer-with-summary must still be told not to send, got:\n%s", got)
	}
}

func TestComposeStartupPromptHandoff(t *testing.T) {
	out := ComposeStartupPrompt("BASE", "", "", "", "SEL", "pilot", "", "takim", "", "codex")
	if !strings.Contains(out, "Devralma Notu") || !strings.Contains(out, "codex") {
		t.Fatalf("handoff segmenti yok: %s", out)
	}
	// Segment sırası: BASE ... Devralma ... SEL ... join
	iBase := strings.Index(out, "BASE")
	iHand := strings.Index(out, "Devralma Notu")
	iSel := strings.Index(out, "SEL")
	iJoin := strings.Index(out, "join_room")
	if !(iBase < iHand && iHand < iSel && iSel < iJoin) {
		t.Fatalf("segment sırası yanlış: base=%d hand=%d sel=%d join=%d", iBase, iHand, iSel, iJoin)
	}

	none := ComposeStartupPrompt("BASE", "", "", "", "SEL", "pilot", "", "takim", "", "")
	if strings.Contains(none, "Devralma Notu") {
		t.Fatal("handoffFrom boşken segment olmamalı")
	}
}
