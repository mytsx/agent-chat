package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"desktop/internal/eventlog"
	"desktop/internal/sanitize"
)

// runAnalyze is the binary's third mode (alongside --hub and stdio MCP): it
// reads the structured event stream and answers the four questions #101 was
// opened for. Report-and-exit; it starts nothing.
func runAnalyze(args []string) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		room      = fs.String("room", "", "yalnızca bu odayı raporla (varsayılan: hepsi)")
		since     = fs.Duration("since", 0, "yalnızca son bu süredeki olaylar, örn. 24h (varsayılan: tümü)")
		legacy    = fs.Bool("legacy-log", false, "eski düz metin mcp-server.log'u da tara")
		asJSON    = fs.Bool("json", false, "raporu JSON olarak bas")
		dirFlag   = fs.String("dir", "", "veri dizini (varsayılan: AGENT_CHAT_DATA_DIR ya da ~/.agent-chat)")
		maxDetail = fs.Int("limit", 20, "listelerde gösterilecek en fazla satır")
	)
	fs.Bool("analyze", false, "bu modu seçer") // mod bayrağının kendisi; yutulur
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir := *dirFlag
	if dir == "" {
		dir = dataDir()
	}

	opts := eventlog.AnalyzeOptions{Dir: dir, Room: *room}
	if *since > 0 {
		opts.Since = time.Now().Add(-*since)
	}
	if *legacy {
		opts.LegacyLog = filepath.Join(dir, "mcp-server.log")
	}

	rep, err := eventlog.Analyze(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "analiz başarısız: %v\n", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(os.Stderr, "JSON yazılamadı: %v\n", err)
			return 1
		}
		return 0
	}

	writeReport(os.Stdout, rep, *maxDetail)
	return 0
}

// dataDir mirrors the resolution the hub and MCP modes use.
func dataDir() string {
	if d := os.Getenv("AGENT_CHAT_DATA_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agent-chat")
}

func writeReport(w io.Writer, rep eventlog.Report, limit int) {
	if rep.Events == 0 {
		fmt.Fprintln(w, "Seçilen pencerede olay kaydı yok.")
		// An outage reconstructed from a pre-cutoff stop is exactly the finding
		// this report exists for — a hub that is down right now produces no
		// events at all, so returning early here would hide it.
		// Integrity warnings must survive this return too: telling the operator
		// to retry while the stream is known to be lossy or damaged would hide
		// exactly the global evidence the room filter was changed to preserve.
		if len(rep.Outages) == 0 && rep.LegacyUnreachable == 0 &&
			rep.Dropped == 0 && rep.Corrupted == 0 {
			fmt.Fprintln(w, "Hub bir kez çalıştıktan sonra tekrar deneyin.")
			return
		}
	} else {
		fmt.Fprintf(w, "Olay akışı: %d kayıt, %s – %s\n",
			rep.Events, rep.From.Format(time.RFC3339), rep.To.Format(time.RFC3339))
	}
	if rep.Dropped > 0 {
		// Never present a partial picture as complete.
		fmt.Fprintf(w, "UYARI: %d olay kaydedilemedi (tampon doldu ya da yazma başarısız); rapor eksik.\n", rep.Dropped)
	}
	if rep.Corrupted > 0 {
		fmt.Fprintf(w, "UYARI: %d kayıt okunamadı (bozuk satır); rapor eksik.\n", rep.Corrupted)
	}

	fmt.Fprintln(w, "\n1) Agent düşmeleri (sebebe göre)")
	if len(rep.Drops) == 0 {
		fmt.Fprintln(w, "   — yok")
	} else {
		fmt.Fprintf(w, "   %-20s %-16s %10s %10s %10s\n", "ODA", "AGENT", "KOPUŞ", "AYRILMA", "STALE")
		for i, d := range rep.Drops {
			if i >= limit {
				fmt.Fprintf(w, "   … %d agent daha\n", len(rep.Drops)-limit)
				break
			}
			fmt.Fprintf(w, "   %-20s %-16s %10d %10d %10d\n", d.Room, d.Agent, d.Disconnect, d.Explicit, d.Evicted)
		}
		fmt.Fprintln(w, "   KOPUŞ baskınsa sorun bağlantı kararlılığında; STALE baskınsa staleTimeout'ta (#98).")
	}

	fmt.Fprintln(w, "\n2) Odada olmayan alıcıya gönderilen mesajlar")
	if len(rep.Misaddressed) == 0 {
		fmt.Fprintln(w, "   — yok")
	} else {
		for i, m := range rep.Misaddressed {
			if i >= limit {
				fmt.Fprintf(w, "   … %d mesaj daha\n", len(rep.Misaddressed)-limit)
				break
			}
			to := m.To
			if m.DeliveredTo != "" {
				// Rerouted by the manager gateway: the addressing mistake is
				// real, but the message was not lost — say so.
				to = fmt.Sprintf("%s (manager'a yönlendirildi: %s)", m.To, m.DeliveredTo)
			}
			fmt.Fprintf(w, "   %s [%s] %s → %s (id %d) %s\n",
				m.Time.Format("01-02 15:04:05"), m.Room, m.From, to, m.MessageID, snippet(m.Content))
		}
	}

	fmt.Fprintln(w, "\n3) Gönderilmiş ama alıcısı tarafından hiç okunmamış mesajlar")
	if len(rep.Unread) == 0 {
		fmt.Fprintln(w, "   — yok")
	} else {
		for i, u := range rep.Unread {
			if i >= limit {
				fmt.Fprintf(w, "   … %d mesaj daha\n", len(rep.Unread)-limit)
				break
			}
			to := u.To
			if u.DeliveredTo != "" {
				// Rerouted by the manager gateway: show who actually owed the read.
				to = fmt.Sprintf("%s (teslim: %s)", u.To, u.DeliveredTo)
			}
			fmt.Fprintf(w, "   %s [%s] %s → %s (id %d) %s\n",
				u.Time.Format("01-02 15:04:05"), u.Room, u.From, to, u.MessageID, snippet(u.Content))
		}
	}

	fmt.Fprintln(w, "\n4) Hub kesintileri")
	if len(rep.Outages) == 0 {
		fmt.Fprintln(w, "   — yok")
	} else {
		for i, o := range rep.Outages {
			if i >= limit {
				fmt.Fprintf(w, "   … %d kesinti daha\n", len(rep.Outages)-limit)
				break
			}
			impact := ""
			if o.LegacyHits > 0 {
				// Attempts and clients are different numbers: one process that
				// exhausts its retries writes six lines. Report both rather than
				// letting a retry factor masquerade as impact.
				impact = fmt.Sprintf("  — %d bağlantı denemesi başarısız", o.LegacyHits)
				if o.LegacyClientsFailed > 0 {
					impact += fmt.Sprintf(", %d MCP süreci vazgeçti", o.LegacyClientsFailed)
				}
			}
			if o.Ongoing {
				fmt.Fprintf(w, "   %s → (sürüyor)%s\n", o.Start.Format(time.RFC3339), impact)
				continue
			}
			note := ""
			if o.Unclean {
				// Start is a lower bound here: the hub died without logging it.
				note = "  [kirli kapanış — hub stop kaydı yok, başlangıç en erken sınır]"
			}
			fmt.Fprintf(w, "   %s → %s (%s)%s%s\n",
				o.Start.Format(time.RFC3339), o.End.Format("15:04:05"), o.Duration.Round(time.Second), note, impact)
		}
	}

	if rep.LegacyUnreachable > 0 {
		writeLegacy(w, rep)
	}
}

func writeLegacy(w io.Writer, rep eventlog.Report) {
	fmt.Fprintf(w, "\nEski düz metin log: %d başarısız bağlantı denemesi satırı, %d MCP süreci hub'a hiç ulaşamadan vazgeçti (%s – %s).\n",
		rep.LegacyUnreachable, rep.LegacyClientsFailed,
		rep.LegacyFirst.Format(time.RFC3339), rep.LegacyLast.Format(time.RFC3339))
	fmt.Fprintln(w, "Bu satırlar yapısal akışta görünemez: o MCP instance'larının hub'a bağlantısı hiç kurulmadı.")
	if rep.LegacyOutsideOutages > 0 {
		// A hub that was UP and still unreachable is a different fault from one
		// that was down, so the two must not be presented as one number.
		fmt.Fprintf(w, "Bunların %d tanesi bilinen hiçbir kesinti penceresine düşmüyor (hub ayaktayken ulaşılamamış).\n",
			rep.LegacyOutsideOutages)
	}
}

// snippet keeps a report line to one terminal row.
//
// Captured content is agent-authored, so it is stripped of control and
// invisible-format runes before reaching the terminal: truncating to one line
// does not defuse an ANSI CSI or OSC sequence, and a malformed or compromised
// agent could otherwise repaint the operator's report or trigger terminal
// features such as OSC 52 clipboard writes. --json output is left untouched;
// its consumer is not a terminal.
func snippet(s string) string {
	const max = 60
	s = firstLine(sanitizeForTerminal(s))
	if len(s) <= max {
		if s == "" {
			return ""
		}
		return "— " + s
	}
	cut := max
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return "— " + s[:cut] + "…"
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			return s[:i]
		}
	}
	return s
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// sanitizeForTerminal drops the rune classes that can drive a terminal rather
// than print on it, reusing the project's shared classifiers so this cannot
// drift from the PTY-injection path.
func sanitizeForTerminal(s string) string {
	return strings.Map(func(r rune) rune {
		if sanitize.IsControl(r) || sanitize.IsInvisibleFormat(r) {
			return -1
		}
		return r
	}, s)
}
