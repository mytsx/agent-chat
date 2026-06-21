package cli

import (
	"strings"
	"testing"
)

func TestComposeStartupPrompt_ManagerRole(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "manager-agent", "backend", "team-a", true)
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
	// it read the full history. The startup instruction must pass a high limit so
	// "tüm mesajları oku" matches actual behavior.
	got := ComposeStartupPrompt("base", "", "", "", "manager-agent", "backend", "team-a", true)
	if !strings.Contains(got, "read_all_messages(since_id=0, limit=") {
		t.Fatalf("expected manager read_all_messages instruction to pass an explicit limit, got:\n%s", got)
	}
}

func TestComposeStartupPrompt_UsesConfiguredRole(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "backend", "Backend API Developer", "team-a", false)
	if !strings.Contains(got, `join_room("backend", "Backend API Developer")`) {
		t.Fatalf("expected normal join instruction, got:\n%s", got)
	}
	if !strings.Contains(got, `read_messages("backend")`) {
		t.Fatalf("expected read_messages instruction for non-manager")
	}
}

func TestComposeStartupPrompt_FallbackRoleUsesAgentName(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "backend", "", "team-a", false)
	if !strings.Contains(got, `join_room("backend", "backend")`) {
		t.Fatalf("expected fallback role=agent_name, got:\n%s", got)
	}
}
