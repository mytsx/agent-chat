package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"desktop/internal/eventlog"
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
		fmt.Fprintln(w, "Olay akışında kayıt yok. Hub bir kez çalıştıktan sonra tekrar deneyin.")
		if rep.LegacyUnreachable > 0 {
			writeLegacy(w, rep)
		}
		return
	}

	fmt.Fprintf(w, "Olay akışı: %d kayıt, %s – %s\n",
		rep.Events, rep.From.Format(time.RFC3339), rep.To.Format(time.RFC3339))
	if rep.Dropped > 0 {
		// Never present a partial picture as complete.
		fmt.Fprintf(w, "UYARI: tampon dolduğu için %d olay kaydedilemedi; rapor eksik olabilir.\n", rep.Dropped)
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
			fmt.Fprintf(w, "   %s [%s] %s → %s (id %d) %s\n",
				m.Time.Format("01-02 15:04:05"), m.Room, m.From, m.To, m.MessageID, snippet(m.Content))
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
			fmt.Fprintf(w, "   %s [%s] %s → %s (id %d) %s\n",
				u.Time.Format("01-02 15:04:05"), u.Room, u.From, u.To, u.MessageID, snippet(u.Content))
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
			if o.Ongoing {
				fmt.Fprintf(w, "   %s → (sürüyor)\n", o.Start.Format(time.RFC3339))
				continue
			}
			fmt.Fprintf(w, "   %s → %s (%s)\n",
				o.Start.Format(time.RFC3339), o.End.Format("15:04:05"), o.Duration.Round(time.Second))
		}
	}

	if rep.LegacyUnreachable > 0 {
		writeLegacy(w, rep)
	}
}

func writeLegacy(w io.Writer, rep eventlog.Report) {
	fmt.Fprintf(w, "\nEski düz metin log: hub'a hiç ulaşamayan %d satır (%s – %s).\n",
		rep.LegacyUnreachable,
		rep.LegacyFirst.Format(time.RFC3339), rep.LegacyLast.Format(time.RFC3339))
	fmt.Fprintln(w, "Bu satırlar yapısal akışta görünemez: o MCP instance'larının hub'a bağlantısı hiç kurulmadı.")
}

// snippet keeps a report line to one terminal row.
func snippet(s string) string {
	const max = 60
	s = firstLine(s)
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
