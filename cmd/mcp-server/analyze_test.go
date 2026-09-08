package main

import (
	"bytes"
	"strings"
	"testing"

	"desktop/internal/eventlog"
)

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

// Codex review round 6, PR #103: integrity warnings must not be swallowed by
// the empty-window early return — telling the operator to retry while the
// stream is known to be lossy hides the evidence the room filter preserves.
func TestReportShowsIntegrityWarningsWithNoEvents(t *testing.T) {
	var buf bytes.Buffer
	writeReport(&buf, eventlog.Report{Dropped: 7, Corrupted: 2}, 20)

	out := buf.String()
	if !strings.Contains(out, "7") || !strings.Contains(out, "2") {
		t.Errorf("bütünlük uyarıları basılmadı:\n%s", out)
	}
	if strings.Contains(out, "tekrar deneyin") {
		t.Errorf("kayıp varken 'tekrar deneyin' denmiş:\n%s", out)
	}
}

func TestReportStillSuggestsRetryWhenTrulyEmpty(t *testing.T) {
	var buf bytes.Buffer
	writeReport(&buf, eventlog.Report{}, 20)
	if !strings.Contains(buf.String(), "tekrar deneyin") {
		t.Errorf("gerçekten boş raporda yönlendirme yok:\n%s", buf.String())
	}
}
