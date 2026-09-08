package main

import "testing"

// Codex review round 4, PR #103: captured content is agent-authored; ANSI CSI
// and OSC sequences must not reach the operator's terminal through the report.
func TestSnippetStripsTerminalControlSequences(t *testing.T) {
	cases := map[string]string{
		"OSC 52 pano yazımı": "zararsız\x1b]52;c;aGVsbG8=\x07metin",
		"CSI ekran temizle":  "zararsız\x1b[2J\x1b[Hmetin",
		"görünmez format":    "zarar​sız؜metin",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := snippet(in)
			for _, r := range got {
				if r == 0x1b || r == 0x07 || r == '​' || r == '؜' {
					t.Fatalf("kontrol/görünmez rune sızdı: %q", got)
				}
			}
			if got == "" {
				t.Fatal("temizleme metnin tamamını yuttu")
			}
		})
	}
}
