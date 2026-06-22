package cli

import (
	"strings"
	"testing"
)

func TestComposeStartupPrompt_ManagerRole(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "", "manager-agent", "backend", "team-a", true)
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
	got := ComposeStartupPrompt("base", "", "", "", "", "manager-agent", "backend", "team-a", true)
	if !strings.Contains(got, "read_all_messages(since_id=0, limit=") {
		t.Fatalf("expected manager read_all_messages instruction to pass an explicit limit, got:\n%s", got)
	}
}

func TestComposeStartupPrompt_UsesConfiguredRole(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "", "backend", "Backend API Developer", "team-a", false)
	if !strings.Contains(got, `join_room("backend", "Backend API Developer")`) {
		t.Fatalf("expected normal join instruction, got:\n%s", got)
	}
	if !strings.Contains(got, `read_messages("backend")`) {
		t.Fatalf("expected read_messages instruction for non-manager")
	}
}

func TestComposeStartupPrompt_FallbackRoleUsesAgentName(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "", "backend", "", "team-a", false)
	if !strings.Contains(got, `join_room("backend", "backend")`) {
		t.Fatalf("expected fallback role=agent_name, got:\n%s", got)
	}
}

// #29: the room summary is injected as its own segment AFTER the team charter
// and BEFORE the library-selected prompt, with a clear header so agents treat it
// as prior-session context rather than an instruction.
func TestComposeStartupPrompt_RoomSummarySegmentOrder(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "CHARTER", "OZET-METNI", "SELECTED", "agent", "", "team", false)
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
	got := ComposeStartupPrompt("base", "", "CHARTER", "", "SELECTED", "agent", "", "team", false)
	if strings.Contains(got, "Önceki Session Özeti") {
		t.Fatalf("unexpected summary header when no summary:\n%s", got)
	}
}

// #29: when a summary is present, the manager join instruction must steer to
// read_summary instead of pulling the entire history (limit=1000), avoiding the
// token bloat the summary exists to prevent.
func TestComposeStartupPrompt_ManagerSummaryRedirect(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "OZET", "", "mgr", "", "team", true)
	if !strings.Contains(got, "read_summary") {
		t.Fatalf("expected read_summary redirect for manager with summary:\n%s", got)
	}
	if strings.Contains(got, "limit=1000") {
		t.Fatalf("manager-with-summary should avoid full-history limit=1000:\n%s", got)
	}
}

func TestComposeStartupPrompt_NonManagerSummaryMentionsSummary(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "OZET", "", "backend", "", "team", false)
	if !strings.Contains(got, "read_summary") {
		t.Fatalf("expected read_summary mention for non-manager with summary:\n%s", got)
	}
}
