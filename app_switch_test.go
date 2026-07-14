package main

import "testing"

func TestSwitchTargetValidation(t *testing.T) {
	// Boş / aynı / desteklenmeyen hedef reddedilir; geçerli AI CLI kabul edilir.
	cases := []struct {
		cur, target string
		ok          bool
	}{
		{"codex", "claude", true},
		{"codex", "codex", false}, // aynı CLI'a geçiş anlamsız
		{"codex", "", false},      // boş hedef
		{"codex", "shell", false}, // AI olmayan
		{"codex", "bogus", false}, // bilinmeyen
	}
	for _, c := range cases {
		if got := validSwitchTarget(c.cur, c.target); got != c.ok {
			t.Errorf("validSwitchTarget(%q,%q) = %v, want %v", c.cur, c.target, got, c.ok)
		}
	}
}
