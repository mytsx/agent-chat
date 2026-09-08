package eventlog

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
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

// Read returns every event in dir at or after since, oldest first, plus a count
// of unparseable records. Rotated backups (including gzipped ones) are included,
// so a report is not limited to whatever happens to be in the live file.
//
// A missing stream is not an error: nothing logged yet is a normal state.
func Read(dir string, since time.Time) ([]Record, int, error) {
	paths, err := streamFiles(dir)
	if err != nil {
		return nil, 0, err
	}
	var corrupted int

	var out []Record
	for i, p := range paths {
		recs, corrupt, tornTail, err := readFile(p, since)
		if err != nil {
			// A backup listed a moment ago can be compressed or deleted by
			// lumberjack before we open it while the hub is live. Losing that
			// race must not fail the whole report — the live stream and the
			// remaining backups are still readable.
			if os.IsNotExist(err) {
				continue
			}
			// A gzip still being written reads as a truncated archive. Skip it,
			// but count it: the report must say it is incomplete rather than
			// quietly omit a backup's worth of events.
			if strings.HasSuffix(p, ".gz") && isTruncatedArchive(err) {
				corrupted++
				continue
			}
			return nil, 0, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		corrupted += corrupt
		// A torn trailing line is the expected mark of a killed process, so it
		// is forgiven only at the end of the whole stream. In a rotated backup
		// it is real damage: that file was closed before the next one opened.
		if tornTail > 0 && i != len(paths)-1 {
			corrupted += tornTail
		}
		out = append(out, recs...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, corrupted, nil
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

	// A backup being compressed exists twice for a moment: lumberjack writes
	// "x.jsonl.gz" and only then removes "x.jsonl". Key by the logical name and
	// prefer the uncompressed side — it is complete, whereas the .gz may still be
	// mid-write. Reading both would double-count every record in it.
	logical := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if n != fileName && !(strings.HasPrefix(n, base+"-") && strings.Contains(n, ".jsonl")) {
			continue
		}
		name := strings.TrimSuffix(n, ".gz")
		if prev, ok := logical[name]; ok && !strings.HasSuffix(prev, ".gz") {
			continue // already holding the uncompressed side
		}
		logical[name] = n
	}

	paths := make([]string, 0, len(logical))
	for _, n := range logical {
		paths = append(paths, filepath.Join(dir, n))
	}
	// Oldest first: backups carry a timestamp that sorts lexically, and
	// "events-" precedes "events." so the live file lands last.
	sort.Slice(paths, func(i, j int) bool {
		return strings.TrimSuffix(paths[i], ".gz") < strings.TrimSuffix(paths[j], ".gz")
	})
	return paths, nil
}

// readFile returns the file's records, the number of definitely-corrupt lines,
// and how many unparseable lines trail the file (which only the caller can judge:
// a torn tail is normal for the newest file and damage anywhere else).
func readFile(path string, since time.Time) (recs []Record, corrupted, tornTail int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, 0, 0, err
		}
		defer gz.Close()
		r = gz
	}

	// pendingBad defers judgement on an unparseable line: a bad line with valid
	// records after it is real corruption, while one that trails the file may be
	// a torn tail — which only the caller can decide.
	var pendingBad int
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
		if err := json.Unmarshal(line, &attrs); err != nil {
			pendingBad++
			continue
		}
		ts, _ := attrs["time"].(string)
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			pendingBad++
			continue
		}
		// A readable record after a bad one proves the bad one was not just a
		// torn tail.
		corrupted += pendingBad
		pendingBad = 0
		if !since.IsZero() && t.Before(since) {
			continue
		}
		name, _ := attrs[AttrEventName].(string)
		recs = append(recs, Record{Time: t, Name: name, Attrs: attrs})
	}
	// At most ONE trailing bad line can be a torn tail; anything beyond that is
	// damage regardless of which file it is in.
	if pendingBad > 1 {
		corrupted += pendingBad - 1
		pendingBad = 1
	}
	return recs, corrupted, pendingBad, sc.Err()
}

// attributeToOutage credits one unreachable-hub timestamp to the outage window
// containing it, reporting whether any did. An ongoing outage has no end, so it
// claims everything at or after its start.
func attributeToOutage(outages []Outage, t time.Time, gaveUp bool) bool {
	for i := range outages {
		o := &outages[i]
		if t.Before(o.Start) {
			continue
		}
		if o.Ongoing || !t.After(o.End) {
			o.LegacyHits++
			if gaveUp {
				o.LegacyClientsFailed++
			}
			return true
		}
	}
	return false
}

// isTruncatedArchive reports whether a gzip read failed the way a file still
// being written does, as opposed to genuine corruption of a settled file.
func isTruncatedArchive(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, gzip.ErrHeader) ||
		errors.Is(err, gzip.ErrChecksum)
}

// readState is one (room, generation, agent)'s read progress: the exact IDs a
// read returned, plus a coarse watermark for records written before
// read.message_ids existed (or whose list was too large to record).
type readState struct {
	ids       map[int]bool
	watermark int
}

// readFor returns (creating if needed) the read state at key.
func readFor(reads map[string]*readState, key string) *readState {
	rs, ok := reads[key]
	if !ok {
		rs = &readState{ids: map[int]bool{}}
		reads[key] = rs
	}
	return rs
}

// sentMsg is one message awaiting proof that its delivery target read it.
type sentMsg struct {
	rec    Record
	id     int
	target string
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
	// LegacyHits counts plain-text "could not reach the hub" LINES inside this
	// window; LegacyClientsFailed counts the MCP processes that gave up in it.
	// The two differ by the retry factor, so the impact figure is the latter.
	// Only populated when the legacy log is scanned.
	LegacyHits          int `json:"legacy_hits,omitempty"`
	LegacyClientsFailed int `json:"legacy_clients_failed,omitempty"`
}

// Report answers the four questions #101 was opened to answer.
type Report struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Events int       `json:"events"`
	// Dropped is what the hub itself reported losing to a full buffer or a
	// failing sink, so a report never quietly presents an incomplete picture as
	// complete. Read from the durable marker as well as the shutdown record, so
	// a crash cannot hide it.
	Dropped uint64 `json:"dropped"`
	// Corrupted counts unreadable records that were NOT a torn final line. Those
	// events are lost from the report just as surely as dropped ones.
	Corrupted int `json:"corrupted"`

	Drops        []AgentDrops   `json:"drops"`
	Misaddressed []Misaddressed `json:"misaddressed"`
	Unread       []Unread       `json:"unread"`
	Outages      []Outage       `json:"outages"`

	// Legacy* summarise the old plain-text log: MCP instances that could not
	// reach the hub at all never had a connection to log an event through, so
	// this is the only place they appear. LegacyOutsideOutages counts the ones
	// that fall in no reconstructed outage — a hub that was up and still
	// unreachable is a different problem from one that was down.
	// LegacyUnreachable counts LOG LINES, not clients: one MCP process that
	// exhausts ConnectWithRetry(5) writes five attempt lines plus a final
	// give-up line. LegacyClientsFailed counts those give-up lines, which is one
	// per process that actually lost the hub — the client-impact number.
	LegacyUnreachable    int `json:"legacy_unreachable"`
	LegacyClientsFailed  int `json:"legacy_clients_failed"`
	LegacyOutsideOutages int `json:"legacy_outside_outages"`

	LegacyFirst time.Time `json:"legacy_first,omitempty"`
	LegacyLast  time.Time `json:"legacy_last,omitempty"`
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
	// Read everything, then apply the cutoff per record below. Filtering at read
	// time would discard the hub lifecycle events an outage spanning the cutoff
	// is reconstructed from: a stop 25 hours ago followed by a restart one hour
	// ago must still show up under --since 24h.
	recs, corrupted, err := Read(opts.Dir, time.Time{})
	if err != nil {
		return Report{}, err
	}
	// The room filter selects FINDINGS, not records. Global evidence — loss
	// markers (which carry no room), hub lifecycle, and activity in other rooms
	// that bounds an unclean restart — must still be processed, or a
	// room-filtered report would claim zero losses after a crash and date an
	// outage from the selected room's much older last event.
	inRoom := func(r Record) bool {
		return opts.Room == "" || r.Room() == opts.Room
	}

	// inWindow reports whether a record counts toward the report's contents.
	// Lifecycle events outside it still drive state, but never contribute
	// findings or move the reported time range.
	inWindow := func(r Record) bool {
		return opts.Since.IsZero() || !r.Time.Before(opts.Since)
	}

	rep := Report{Corrupted: corrupted}
	for _, r := range recs {
		if !inWindow(r) || !inRoom(r) {
			continue
		}
		rep.Events++
		if rep.From.IsZero() {
			rep.From = r.Time
		}
		rep.To = r.Time
	}

	drops := map[string]*AgentDrops{}
	dropFor := func(r Record) *AgentDrops {
		// Drops are counted per room across generations: clearing a room does not
		// undo the fact that an agent fell out of it.
		k := r.Room() + "\x00" + r.Str(AttrAgentName)
		d, ok := drops[k]
		if !ok {
			d = &AgentDrops{Room: r.Room(), Agent: r.Str(AttrAgentName)}
			drops[k] = d
		}
		return d
	}

	// generation numbers a room's lifetime. clear_room restarts message IDs at 1,
	// so read progress must not cross that boundary — but deleting the room's
	// pending sends would erase genuinely unread messages from before the clear,
	// which are among the most interesting findings the report has. Keying by
	// generation keeps that history while making old IDs unmatchable by new reads.
	// clearCount counts clear boundaries, used only for streams written before
	// the hub stamped the generation itself.
	clearCount := map[string]int{}
	// lifetimeBase offsets every generation in a room's CURRENT lifetime. A
	// recreated room is a fresh RoomState whose stamped generation restarts at
	// zero, so without an offset its keys would collide with the deleted room's.
	lifetimeBase := map[string]int{}
	// maxGenSeen is the highest generation observed in the current lifetime, so
	// a delete can advance the base past all of it.
	maxGenSeen := map[string]int{}
	// resetAbove[room][gen] is the watermark a clear left behind for that
	// generation: messages above it survived into the next one. Needed because a
	// send can stall and be logged AFTER the boundary while still carrying the
	// older stamped generation.
	resetAbove := map[string]map[int]int{}
	// epoch separates hub runs that rolled back. Room state is persisted only
	// every five seconds, so an unclean restart can reload a snapshot that is
	// behind the log and REUSE message IDs. Without this boundary, a read of a
	// reused ID in the new run would clear the pre-crash message that was
	// actually lost — hiding exactly the data loss the report exists to find.
	epoch := 0
	// genOf prefers the generation the hub stamped under the room lock; only a
	// stream written before that attribute falls back to counting boundaries,
	// which is ordering-dependent and therefore racy.
	genOf := func(r Record) int {
		room := r.Room()
		g, stamped := r.Attrs[AttrRoomGeneration].(float64)
		gen := clearCount[room]
		if stamped {
			gen = int(g)
		}
		gen += lifetimeBase[room]
		if gen > maxGenSeen[room] {
			maxGenSeen[room] = gen
		}
		return gen
	}

	// survivorGen walks a stamped send forward through every clear it outlived.
	// A message stored above a clear's watermark physically survives into the
	// next generation, so a read there must be able to match it — even when the
	// send was logged after the boundary and still carries the older stamp.
	survivorGen := func(room string, gen, id int) int {
		marks := resetAbove[room]
		for {
			above, ok := marks[gen]
			if !ok || id <= above {
				return gen
			}
			gen++
		}
	}
	key := func(room string, gen int, agent string) string {
		return fmt.Sprintf("%s\x00%d\x00%d\x00%s", room, epoch, gen, agent)
	}

	sent := map[string][]sentMsg{}
	reads := map[string]*readState{}

	// runDropped is the highest loss count seen within the current hub run. Both
	// the durable marker and hub.stopped report a cumulative figure, so the run's
	// contribution is their maximum, not their sum; runs are summed at each
	// restart and at end of stream.
	var runDropped uint64
	// runBaseline is the loss count a run had already accumulated when the
	// --since window opened. Only growth past it belongs to the selected
	// interval: otherwise a healthy window inherits months-old drops and warns
	// that its own report is incomplete.
	var runBaseline uint64
	baselineSet := false
	var stoppedAt time.Time
	// hubRunning tracks whether a hub instance is believed to be up, so a start
	// with no intervening stop can be reported as an unclean restart.
	var hubRunning bool
	// lastStopPersisted records whether the most recent clean stop confirmed it
	// persisted room state.
	var lastStopPersisted bool
	var lastEventAt time.Time

	for _, r := range recs {
		// Outside the window a record contributes no finding, but it still has
		// to advance the reconstruction clock: an unclean outage is bounded by
		// the last evidence the dead hub was alive, and that evidence is often
		// an ordinary event just before the crash. Dropping it here would stretch
		// a short outage back to the previous lifecycle record.
		if !inWindow(r) {
			// Loss is loss regardless of the window: a run that dropped events
			// before the cutoff still produced an incomplete stream.
			if isHubLifecycle(r.Name) || r.Name == EventEventsDropped {
				applyHubLifecycle(r, &stoppedAt, &hubRunning, &runDropped, &rep, &epoch)
			}
			lastEventAt = r.Time
			continue
		}
		if !baselineSet {
			// First in-window record: everything the run lost before this point
			// happened outside the selected interval.
			runBaseline, baselineSet = runDropped, true
		}

		// Findings below are room-scoped; global state above is not.
		roomOK := inRoom(r)

		switch r.Name {
		case EventAgentLeft:
			if !roomOK {
				break
			}
			d := dropFor(r)
			switch r.Str(AttrLeaveReason) {
			case LeaveReasonExplicit:
				d.Explicit++
			default:
				d.Disconnect++
			}
		case EventAgentEvicted:
			if !roomOK {
				break
			}
			dropFor(r).Evicted++

		case EventMessageSent:
			if !roomOK {
				break
			}
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
				k := key(r.Room(), survivorGen(r.Room(), genOf(r), r.Int(AttrMessageID)), target)
				sent[k] = append(sent[k], sentMsg{rec: r, id: r.Int(AttrMessageID), target: target})
			}

		case EventMessagesRead:
			if !roomOK {
				break
			}
			readGen := genOf(r)
			agent := r.Str(AttrAgentName)
			ids := DecodeIDRanges(r.Ints(AttrReadIDRanges))
			if len(ids) == 0 {
				ids = r.Ints(AttrReadMessageIDs)
			}
			// Each ID walks the same survivor boundaries its send does. A read
			// can observe a message added after the clear took its snapshot but
			// before ClearArchived ran, and be logged after the boundary with the
			// older generation — the mirror of the late-send case. Without this
			// the send advances and the read does not, and a delivered, read
			// message is reported unread.
			for _, id := range ids {
				readFor(reads, key(r.Room(), survivorGen(r.Room(), readGen, id), agent)).ids[id] = true
			}
			rs := readFor(reads, key(r.Room(), readGen, agent))
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
			if !roomOK {
				break
			}
			// Start a new generation rather than discarding state: messages the
			// clear wiped unread stay reportable, while their IDs can no longer
			// be matched by reads in the fresh room.
			//
			// clear_room only wipes up to the ID it archived, deliberately
			// keeping anything that arrived while the archive I/O ran. Those
			// messages survive into the new room and stay readable, so they must
			// move with it — otherwise they would be reported unread forever.
			// ClearArchived(maxID) keeps everything ABOVE maxID, and maxID==0
			// (an empty snapshot) means every racing message survives — so a
			// zero watermark still migrates. A delete leaves no survivors, and
			// a stream written before this attribute existed cannot say what
			// survived, so it migrates nothing rather than guessing.
			room := r.Room()
			survivedAbove, haveWatermark := r.Attrs[AttrRoomResetMaxID]
			deleted := r.Str(AttrRoomLifecycle) == RoomLifecycleDeleted
			// Prefer the generation the hub stamped on the boundary itself; the
			// counted fallback drifts once rotation discards an older clear or a
			// rollback reloads an earlier generation.
			oldGen := clearCount[room] + lifetimeBase[room]
			if g, ok := r.Attrs[AttrRoomGeneration].(float64); ok {
				oldGen = int(g) + lifetimeBase[room]
				clearCount[room] = int(g)
			}
			if oldGen > maxGenSeen[room] {
				maxGenSeen[room] = oldGen
			}
			oldPrefix := key(room, oldGen, "")

			if deleted {
				// A new lifetime must not reuse ANY key of the old one, whose
				// generations ran up to maxGenSeen.
				lifetimeBase[room] = maxGenSeen[room] + 1
				clearCount[room] = 0
				maxGenSeen[room] = lifetimeBase[room]
				delete(resetAbove, room)
				break
			}

			clearCount[room]++
			if haveWatermark {
				above, _ := survivedAbove.(float64)
				if resetAbove[room] == nil {
					resetAbove[room] = map[int]int{}
				}
				resetAbove[room][oldGen] = int(above)
				newKey := func(room, agent string) string {
					return key(room, oldGen+1, agent)
				}
				migrateSurvivors(sent, reads, room, int(above), oldPrefix, newKey)
			}

		case EventEventsDropped:
			// The durable marker exists so a crash cannot hide the loss; the
			// analyzer has to actually read it.
			if f, ok := r.Attrs[AttrEventsDropped].(float64); ok && uint64(f) > runDropped {
				runDropped = uint64(f)
			}

		case EventHubUnavailable:
			// The listener is closed: refusals start now, not when the stopped
			// record is finally written.
			if stoppedAt.IsZero() {
				stoppedAt = r.Time
			}
			hubRunning = false

		case EventHubStopped:
			// Keep the earlier unavailable boundary if we have one; this record
			// remains authoritative for the drop and persistence counts.
			if stoppedAt.IsZero() {
				stoppedAt = r.Time
			}
			hubRunning = false
			lastStopPersisted, _ = r.Attrs[AttrPersistOK].(bool)
			if f, ok := r.Attrs[AttrEventsDropped].(float64); ok && uint64(f) > runDropped {
				runDropped = uint64(f)
			}
		case EventHubStarted:
			// A new hub process starts its counter over, so bank the old run's
			// in-window growth.
			rep.Dropped += runDropped - min(runBaseline, runDropped)
			runDropped, runBaseline = 0, 0
			switch {
			case !stoppedAt.IsZero():
				rep.Outages = append(rep.Outages, Outage{
					Start: stoppedAt, End: r.Time, Duration: r.Time.Sub(stoppedAt),
				})
				stoppedAt = time.Time{}
				// A clean stop only guarantees continuity if it actually
				// persisted. A failed persist rolls the next process back to an
				// older snapshot and lets it reuse message IDs, exactly like a
				// crash — so treat anything but a confirmed persist as a
				// generation boundary.
				if !lastStopPersisted {
					epoch++
				}
			case hubRunning:
				// A start with no stop before it: the previous instance crashed
				// or was killed and never logged its exit. We only know it was
				// alive at its last event, so that bounds the outage from below.
				rep.Outages = append(rep.Outages, Outage{
					Start: lastEventAt, End: r.Time,
					Duration: r.Time.Sub(lastEventAt), Unclean: true,
				})
				// The restarted hub may have rolled back to an older snapshot
				// and can reuse message IDs, so nothing it records may clear a
				// send from before the crash.
				epoch++
			}
			hubRunning = true
		}
		lastEventAt = r.Time
	}

	// Bank the final (or only) run's in-window losses.
	rep.Dropped += runDropped - min(runBaseline, runDropped)

	// A stop with no matching start means the hub is still down as far as this
	// stream knows — the most interesting outage of all, so never dropped.
	if !stoppedAt.IsZero() {
		rep.Outages = append(rep.Outages, Outage{Start: stoppedAt, Ongoing: true})
	}

	// An outage reconstructed from a pre-cutoff stop belongs in the report only
	// if it reached into the window.
	if !opts.Since.IsZero() {
		kept := rep.Outages[:0]
		for _, o := range rep.Outages {
			if o.Ongoing || !o.End.Before(opts.Since) {
				kept = append(kept, o)
			}
		}
		rep.Outages = kept
	}

	for k, msgs := range sent {
		rs := reads[k]
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

// migrateSurvivors moves everything above the cleared watermark into the room's
// new generation: both the sends that survived the clear and the reads that
// already covered them.
//
// The reset event is logged after the room lock is released, so a reader can
// legitimately read a surviving message in that gap and have its read recorded
// in the old generation. Moving only the sends would strand that read and report
// the message unread forever. Messages at or below the watermark were wiped and
// stay behind as permanently unread findings.
func migrateSurvivors(
	sent map[string][]sentMsg,
	reads map[string]*readState,
	room string, above int, oldPrefix string,
	newKey func(room, agent string) string,
) {
	for k, msgs := range sent {
		if !strings.HasPrefix(k, oldPrefix) {
			continue
		}
		var stay, move []sentMsg
		for _, m := range msgs {
			if m.id > above {
				move = append(move, m)
			} else {
				stay = append(stay, m)
			}
		}
		if len(move) == 0 {
			continue
		}
		sent[k] = stay
		nk := newKey(room, move[0].target)
		sent[nk] = append(sent[nk], move...)
	}

	for k, rs := range reads {
		if !strings.HasPrefix(k, oldPrefix) {
			continue
		}
		agent := strings.TrimPrefix(k, oldPrefix)
		var moved *readState
		for id := range rs.ids {
			if id <= above {
				continue
			}
			if moved == nil {
				moved = readFor(reads, newKey(room, agent))
			}
			moved.ids[id] = true
			delete(rs.ids, id)
		}
		if rs.watermark > above {
			if moved == nil {
				moved = readFor(reads, newKey(room, agent))
			}
			if rs.watermark > moved.watermark {
				moved.watermark = rs.watermark
			}
			rs.watermark = above
		}
	}
}

// isHubLifecycle reports whether a record drives hub up/down state.
func isHubLifecycle(name string) bool {
	return name == EventHubStarted || name == EventHubStopped || name == EventHubUnavailable
}

// applyHubLifecycle advances outage-reconstruction state for a record outside
// the --since window. It records no finding, but loss counts are still banked:
// events dropped by a hub that ran before the cutoff were still lost.
func applyHubLifecycle(r Record, stoppedAt *time.Time, hubRunning *bool, runDropped *uint64, rep *Report, epoch *int) {
	switch r.Name {
	case EventEventsDropped:
		if f, ok := r.Attrs[AttrEventsDropped].(float64); ok && uint64(f) > *runDropped {
			*runDropped = uint64(f)
		}
	case EventHubUnavailable:
		if stoppedAt.IsZero() {
			*stoppedAt = r.Time
		}
		*hubRunning = false
	case EventHubStopped:
		if stoppedAt.IsZero() {
			*stoppedAt = r.Time
		}
		*hubRunning = false
		if f, ok := r.Attrs[AttrEventsDropped].(float64); ok && uint64(f) > *runDropped {
			*runDropped = uint64(f)
		}
	case EventHubStarted:
		// Deliberately NOT banking: a run that both started and ended before the
		// --since cutoff lost its events outside the selected interval, and
		// adding them would make a healthy window declare itself incomplete.
		*runDropped = 0
		if *hubRunning {
			*epoch++ // unclean restart outside the window still rolls IDs back
		}
		*stoppedAt = time.Time{}
		*hubRunning = true
	}
}

// legacyTimeLayout matches the standard log package's LstdFlags output, which
// is what mcp-server.log has always used.
const legacyTimeLayout = "2006/01/02 15:04:05"

// legacyUnreachableMarkers identify a hub the MCP instance could not reach at
// all. Those instances had no connection, so these lines exist nowhere else.
var legacyUnreachableMarkers = []string{
	"hub.port not found",
	"connect: connection refused",
	legacyGaveUpMarker,
}

// legacyGaveUpMarker is written once per MCP process that exhausted its retries,
// so counting it gives affected CLIENTS rather than attempts.
const legacyGaveUpMarker = "failed to connect to hub after"

// scanLegacyLog counts unreachable-hub lines in the old plain-text log and
// attributes each to the outage window it falls in, so the report can say how
// many MCP clients each outage actually affected rather than only a total. Best
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
		gaveUp := strings.Contains(line, legacyGaveUpMarker)
		if gaveUp {
			rep.LegacyClientsFailed++
		}
		if stamp.IsZero() {
			continue
		}
		if rep.LegacyFirst.IsZero() || stamp.Before(rep.LegacyFirst) {
			rep.LegacyFirst = stamp
		}
		if stamp.After(rep.LegacyLast) {
			rep.LegacyLast = stamp
		}
		if !attributeToOutage(rep.Outages, stamp, gaveUp) {
			rep.LegacyOutsideOutages++
		}
	}
	return sc.Err()
}
