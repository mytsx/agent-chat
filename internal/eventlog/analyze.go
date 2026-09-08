package eventlog

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Record is one parsed event. Attrs holds every attribute verbatim, so a report
// can reach a field this package has no typed accessor for.
type Record struct {
	Time  time.Time
	Name  string
	Attrs map[string]any
}

func (r Record) Str(key string) string {
	s, _ := r.Attrs[key].(string)
	return s
}

// Int reads a numeric attribute. JSON decodes numbers as float64, so this is
// the only correct way to read one back.
func (r Record) Int(key string) int {
	f, _ := r.Attrs[key].(float64)
	return int(f)
}

func (r Record) Float(key string) float64 {
	f, _ := r.Attrs[key].(float64)
	return f
}

// Ints reads a numeric list attribute. JSON decodes it as []any of float64.
func (r Record) Ints(key string) []int {
	raw, ok := r.Attrs[key].([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, v := range raw {
		if f, ok := v.(float64); ok {
			out = append(out, int(f))
		}
	}
	return out
}

func (r Record) Bool(key string) bool {
	b, _ := r.Attrs[key].(bool)
	return b
}

// Room is the conversation this event belongs to.
func (r Record) Room() string { return r.Str(AttrConversationID) }

// Read returns every event in dir at or after since, oldest first. Rotated
// backups (including gzipped ones) are included, so a report is not limited to
// whatever happens to be in the live file.
//
// A missing stream is not an error: nothing logged yet is a normal state.
func Read(dir string, since time.Time) ([]Record, error) {
	paths, err := streamFiles(dir)
	if err != nil {
		return nil, err
	}

	var out []Record
	for _, p := range paths {
		recs, err := readFile(p, since)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		out = append(out, recs...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// streamFiles lists the live stream plus lumberjack's backups. Backup names are
// the base name with a timestamp before the extension, optionally .gz.
func streamFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	base := strings.TrimSuffix(fileName, filepath.Ext(fileName)) // "events"
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if n == fileName || (strings.HasPrefix(n, base+"-") && strings.Contains(n, ".jsonl")) {
			paths = append(paths, filepath.Join(dir, n))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func readFile(path string, since time.Time) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}

	var out []Record
	sc := bufio.NewScanner(r)
	// A captured message can be 8 KB, so the default 64 KB line cap is raised
	// rather than silently truncating events into unparseable halves.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var attrs map[string]any
		// A torn final line (killed mid-write) must not fail the whole report.
		if err := json.Unmarshal(line, &attrs); err != nil {
			continue
		}
		ts, _ := attrs["time"].(string)
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			continue
		}
		if !since.IsZero() && t.Before(since) {
			continue
		}
		name, _ := attrs[AttrEventName].(string)
		out = append(out, Record{Time: t, Name: name, Attrs: attrs})
	}
	return out, sc.Err()
}

// AgentDrops counts how one agent left a room, split by mechanism. The split is
// the point: a disconnect-heavy agent and an eviction-heavy agent have
// different causes (#98).
type AgentDrops struct {
	Room       string `json:"room"`
	Agent      string `json:"agent"`
	Disconnect int    `json:"disconnect"`
	Explicit   int    `json:"explicit"`
	Evicted    int    `json:"evicted"`
}

// Total is every way this agent left, used for ranking.
func (d AgentDrops) Total() int { return d.Disconnect + d.Explicit + d.Evicted }

// Misaddressed is a message whose addressee was not in the room (#99).
// DeliveredTo is set when the manager gateway rerouted it anyway: the addressing
// mistake is still real and worth reporting, but the message was not lost.
type Misaddressed struct {
	Time        time.Time `json:"time"`
	Room        string    `json:"room"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	DeliveredTo string    `json:"delivered_to,omitempty"`
	MessageID   int       `json:"message_id"`
	Content     string    `json:"content,omitempty"`
}

// Unread is a message whose delivery target never read that far. To is the
// addressee the sender named; DeliveredTo is who it was actually stored for and
// whose read progress was compared. They differ when the manager gateway
// intercepted the message.
type Unread struct {
	Time        time.Time `json:"time"`
	Room        string    `json:"room"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	DeliveredTo string    `json:"delivered_to,omitempty"`
	MessageID   int       `json:"message_id"`
	Content     string    `json:"content,omitempty"`
}

// Outage is a window during which the hub was not running.
type Outage struct {
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end"`
	Duration time.Duration `json:"duration"`
	// Ongoing marks a hub that stopped and never started again within the
	// analyzed window — the stream simply ends.
	Ongoing bool `json:"ongoing"`
	// Unclean marks an outage inferred from two starts with no stop between
	// them: the hub crashed or was killed and never got to log its stop. Start
	// is then only a lower bound (the last event the dead instance produced),
	// which is precisely the failure this report exists to surface.
	Unclean bool `json:"unclean"`
}

// Report answers the four questions #101 was opened to answer.
type Report struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Events int       `json:"events"`
	// Dropped is what the hub itself reported losing to a full buffer, so a
	// report never quietly presents an incomplete picture as complete.
	Dropped uint64 `json:"dropped"`

	Drops        []AgentDrops   `json:"drops"`
	Misaddressed []Misaddressed `json:"misaddressed"`
	Unread       []Unread       `json:"unread"`
	Outages      []Outage       `json:"outages"`

	// Legacy* summarise the old plain-text log: MCP instances that could not
	// reach the hub at all never had a connection to log an event through, so
	// this is the only place they appear.
	LegacyUnreachable int       `json:"legacy_unreachable"`
	LegacyFirst       time.Time `json:"legacy_first,omitempty"`
	LegacyLast        time.Time `json:"legacy_last,omitempty"`
}

// AnalyzeOptions selects what to report on.
type AnalyzeOptions struct {
	Dir  string
	Room string // empty = every room
	// Since drops events older than this. Zero means everything.
	Since time.Time
	// LegacyLog, when set, is the plain-text mcp-server.log to additionally scan.
	LegacyLog string
}

// Analyze reads the stream and builds the report.
func Analyze(opts AnalyzeOptions) (Report, error) {
	recs, err := Read(opts.Dir, opts.Since)
	if err != nil {
		return Report{}, err
	}
	if opts.Room != "" {
		kept := recs[:0]
		for _, r := range recs {
			// Hub lifecycle events belong to no room but bound every room's outages.
			if r.Room() == opts.Room || r.Name == EventHubStarted || r.Name == EventHubStopped {
				kept = append(kept, r)
			}
		}
		recs = kept
	}

	rep := Report{Events: len(recs)}
	if len(recs) > 0 {
		rep.From, rep.To = recs[0].Time, recs[len(recs)-1].Time
	}

	drops := map[string]*AgentDrops{}
	dropFor := func(r Record) *AgentDrops {
		key := r.Room() + "\x00" + r.Str(AttrAgentName)
		d, ok := drops[key]
		if !ok {
			d = &AgentDrops{Room: r.Room(), Agent: r.Str(AttrAgentName)}
			drops[key] = d
		}
		return d
	}

	// sent indexes messages by (room, recipient) so unread detection is a single
	// pass against each recipient's high-water mark.
	type sentMsg struct {
		rec    Record
		id     int
		target string
	}
	sent := map[string][]sentMsg{}
	// readState per (room, agent): the exact IDs a read returned, plus a coarse
	// watermark for records written before read.message_ids existed (or whose
	// list was too large to record).
	type readState struct {
		ids       map[int]bool
		watermark int
	}
	reads := map[string]*readState{}
	readFor := func(key string) *readState {
		rs, ok := reads[key]
		if !ok {
			rs = &readState{ids: map[int]bool{}}
			reads[key] = rs
		}
		return rs
	}

	var stoppedAt time.Time
	// hubRunning tracks whether a hub instance is believed to be up, so a start
	// with no intervening stop can be reported as an unclean restart.
	var hubRunning bool
	var lastEventAt time.Time

	for _, r := range recs {
		switch r.Name {
		case EventAgentLeft:
			d := dropFor(r)
			switch r.Str(AttrLeaveReason) {
			case LeaveReasonExplicit:
				d.Explicit++
			default:
				d.Disconnect++
			}
		case EventAgentEvicted:
			dropFor(r).Evicted++

		case EventMessageSent:
			to := r.Str(AttrRecipientName)
			// Read progress belongs to whoever the message was stored for. The
			// manager gateway rewrites that target, so without this an
			// intercepted message would sit unread forever against an addressee
			// who was never a delivery target — in a manager-gated room that is
			// nearly all traffic. Streams written before this attribute existed
			// fall back to the addressee.
			target := r.Str(AttrDeliveryTarget)
			if target == "" {
				target = to
			}
			if !r.Bool(AttrRecipientInRoom) {
				m := Misaddressed{
					Time: r.Time, Room: r.Room(), From: r.Str(AttrAgentName), To: to,
					MessageID: r.Int(AttrMessageID), Content: r.Str(AttrInputMessages),
				}
				if target != to {
					m.DeliveredTo = target
				}
				rep.Misaddressed = append(rep.Misaddressed, m)
			}
			// Unread counts messages that reached a real recipient who never
			// read them. A broadcast has no single delivery target whose
			// progress could be compared. A message to somebody who was not in
			// the room is a misaddressing (already reported above) with a
			// different cause — counting it here too would double-report one
			// problem — but only when it was not rerouted: an intercepted
			// message did reach the manager regardless of the addressee.
			undelivered := target == to && !r.Bool(AttrRecipientInRoom)
			if target != "" && target != "all" && !undelivered {
				key := r.Room() + "\x00" + target
				sent[key] = append(sent[key], sentMsg{rec: r, id: r.Int(AttrMessageID), target: target})
			}

		case EventMessagesRead:
			rs := readFor(r.Room() + "\x00" + r.Str(AttrAgentName))
			ids := r.Ints(AttrReadMessageIDs)
			for _, id := range ids {
				rs.ids[id] = true
			}
			// Only a record that could not list its IDs contributes a watermark.
			// A read returns just the newest matching tail, so treating its
			// highest ID as "everything below was seen" would silently mark
			// older direct messages read that the agent never saw.
			if len(ids) == 0 || r.Bool(AttrReadIDsTruncated) {
				if id := r.Int(AttrReadMaxID); id > rs.watermark {
					rs.watermark = id
				}
			}

		case EventRoomReset:
			// clear_room restarts message IDs at 1. Without dropping the read
			// state here, reused IDs in the fresh room would look already read.
			prefix := r.Room() + "\x00"
			for k := range reads {
				if strings.HasPrefix(k, prefix) {
					delete(reads, k)
				}
			}
			for k := range sent {
				if strings.HasPrefix(k, prefix) {
					delete(sent, k)
				}
			}

		case EventHubStopped:
			stoppedAt = r.Time
			hubRunning = false
			if d := r.Attrs[AttrEventsDropped]; d != nil {
				if f, ok := d.(float64); ok {
					rep.Dropped += uint64(f)
				}
			}
		case EventHubStarted:
			switch {
			case !stoppedAt.IsZero():
				rep.Outages = append(rep.Outages, Outage{
					Start: stoppedAt, End: r.Time, Duration: r.Time.Sub(stoppedAt),
				})
				stoppedAt = time.Time{}
			case hubRunning:
				// A start with no stop before it: the previous instance crashed
				// or was killed and never logged its exit. We only know it was
				// alive at its last event, so that bounds the outage from below.
				rep.Outages = append(rep.Outages, Outage{
					Start: lastEventAt, End: r.Time,
					Duration: r.Time.Sub(lastEventAt), Unclean: true,
				})
			}
			hubRunning = true
		}
		lastEventAt = r.Time
	}

	// A stop with no matching start means the hub is still down as far as this
	// stream knows — the most interesting outage of all, so never dropped.
	if !stoppedAt.IsZero() {
		rep.Outages = append(rep.Outages, Outage{Start: stoppedAt, Ongoing: true})
	}

	for key, msgs := range sent {
		rs := reads[key]
		for _, m := range msgs {
			read := rs != nil && (rs.ids[m.id] || m.id <= rs.watermark)
			if !read {
				u := Unread{
					Time: m.rec.Time, Room: m.rec.Room(), From: m.rec.Str(AttrAgentName),
					To: m.rec.Str(AttrRecipientName), MessageID: m.id,
					Content: m.rec.Str(AttrInputMessages),
				}
				if m.target != u.To {
					u.DeliveredTo = m.target
				}
				rep.Unread = append(rep.Unread, u)
			}
		}
	}
	sort.Slice(rep.Unread, func(i, j int) bool { return rep.Unread[i].MessageID < rep.Unread[j].MessageID })

	for _, d := range drops {
		rep.Drops = append(rep.Drops, *d)
	}
	// Worst offender first: the report should lead with the problem.
	sort.Slice(rep.Drops, func(i, j int) bool {
		if a, b := rep.Drops[i].Total(), rep.Drops[j].Total(); a != b {
			return a > b
		}
		return rep.Drops[i].Agent < rep.Drops[j].Agent
	})

	if opts.LegacyLog != "" {
		// The cutoff must apply here too: without it a --since 24h report would
		// mix one day of events with months of historical unreachable-hub lines
		// and present the total as belonging to the selected window.
		if err := scanLegacyLog(opts.LegacyLog, opts.Since, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// legacyTimeLayout matches the standard log package's LstdFlags output, which
// is what mcp-server.log has always used.
const legacyTimeLayout = "2006/01/02 15:04:05"

// legacyUnreachableMarkers identify a hub the MCP instance could not reach at
// all. Those instances had no connection, so these lines exist nowhere else.
var legacyUnreachableMarkers = []string{
	"hub.port not found",
	"connect: connection refused",
	"failed to connect to hub after",
}

// scanLegacyLog counts unreachable-hub lines in the old plain-text log. Best
// effort by design: it is a bridge until the structured stream has history.
func scanLegacyLog(path string, since time.Time, rep *Report) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		var hit bool
		for _, m := range legacyUnreachableMarkers {
			if strings.Contains(line, m) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}

		// Lines look like: "[MCP] 2026/09/08 09:02:00 main.go:108: ...".
		var stamp time.Time
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			t, err := time.ParseInLocation(legacyTimeLayout, fields[i]+" "+fields[i+1], time.Local)
			if err != nil {
				continue
			}
			stamp = t
			break
		}
		// An undated line cannot be placed in the window; with a cutoff in force
		// it is excluded rather than silently counted as recent.
		if !since.IsZero() && (stamp.IsZero() || stamp.Before(since)) {
			continue
		}

		rep.LegacyUnreachable++
		if stamp.IsZero() {
			continue
		}
		if rep.LegacyFirst.IsZero() || stamp.Before(rep.LegacyFirst) {
			rep.LegacyFirst = stamp
		}
		if stamp.After(rep.LegacyLast) {
			rep.LegacyLast = stamp
		}
	}
	return sc.Err()
}
