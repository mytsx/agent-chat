package cli

import (
	"strings"
	"testing"
)

func TestComposeStartupPrompt_ManagerRole(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "manager-agent", "team-a", true)
	if !strings.Contains(got, `join_room("manager-agent", "manager")`) {
		t.Fatalf("expected manager join instruction, got:\n%s", got)
	}
	if !strings.Contains(got, "read_all_messages") {
		t.Fatalf("expected read_all_messages instruction for manager")
	}
}

func TestComposeStartupPrompt_NormalRole(t *testing.T) {
	got := ComposeStartupPrompt("base", "", "", "", "backend", "team-a", false)
	if !strings.Contains(got, `join_room("backend", "backend")`) {
		t.Fatalf("expected normal join instruction, got:\n%s", got)
	}
	if !strings.Contains(got, `read_messages("backend")`) {
		t.Fatalf("expected read_messages instruction for non-manager")
	}
}
