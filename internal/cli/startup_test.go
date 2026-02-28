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
