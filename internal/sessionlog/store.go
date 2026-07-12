// Package sessionlog persists a per-(room,agent) history of CLI sessions a
// terminal has run, so the user can later resume a SPECIFIC past session and
// correlate sessions across agents by the time window they were open (#40 Faz-2).
package sessionlog

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Record is one logged session. SessionID is the map key, flattened in for List
// results. FirstSeen/LastSeen are unix seconds (float64, project convention): the
// window the terminal was open, which is what "same period" correlation compares.
type Record struct {
	SessionID string  `json:"session_id"`
	Room      string  `json:"room"`
	AgentName string  `json:"agent_name"`
	CLIType   string  `json:"cli_type"`
	Cwd       string  `json:"cwd"`
	FirstSeen float64 `json:"first_seen"`
	LastSeen  float64 `json:"last_seen"`
}

// Store is the atomic JSON-backed session-history index. Safe for concurrent use.
type Store struct {
	mu       sync.RWMutex
	filePath string
	records  map[string]Record // keyed by SessionID
	now      func() float64
}

// newWindowGapSec: a re-Record of an existing session id with a gap larger than
// this (seconds) since LastSeen starts a fresh open-window rather than extending
// the old one — so a resumed Copilot session (same id, days later) doesn't merge
// into one interval that spans the idle gap (Codex P2).
const newWindowGapSec = 120.0

func nowUnix() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// New loads (or creates) the store under dataDir/session-history.json.
func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	s := &Store{
		filePath: filepath.Join(dataDir, "session-history.json"),
		records:  make(map[string]Record),
		now:      nowUnix,
	}
	if data, err := os.ReadFile(s.filePath); err == nil {
		// A corrupt/invalid history file must not leave a partially-filled map — reset to
		// empty on any unmarshal error so the store starts clean rather than half-loaded
		// (Gemini).
		if err := json.Unmarshal(data, &s.records); err != nil || s.records == nil {
			s.records = make(map[string]Record)
		} else {
			s.records = normalizeLoadedRecords(s.records)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func normalizeLoadedRecords(records map[string]Record) map[string]Record {
	normalized := make(map[string]Record, len(records))
	for id, record := range records {
		if id == "" {
			continue
		}
		// The JSON map key is the store's authoritative session id. Older/corrupt
		// files can have a missing or stale embedded session_id; if left as-is the UI
		// may list an empty/wrong resume target even though the map entry is usable.
		record.SessionID = id
		normalized[id] = record
	}
	return normalized
}

// Record adds a session (FirstSeen=LastSeen=now) or, if already present, only
// advances LastSeen (FirstSeen preserved). Empty sessionID is a no-op.
func (s *Store) Record(sessionID, room, agent, cliType, cwd string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.now()
	previous, hadPrevious := s.records[sessionID]
	s.records[sessionID] = refreshedRecord(previous, hadPrevious, t, sessionID, room, agent, cliType, cwd)
	if !s.save() {
		if hadPrevious {
			s.records[sessionID] = previous
		} else {
			delete(s.records, sessionID)
		}
	}
}

func refreshedRecord(previous Record, hadPrevious bool, t float64, sessionID, room, agent, cliType, cwd string) Record {
	if !hadPrevious {
		return Record{
			SessionID: sessionID, Room: room, AgentName: agent,
			CLIType: cliType, Cwd: cwd, FirstSeen: t, LastSeen: t,
		}
	}

	r := previous
	// A re-Record of an existing id is a NEW run — Copilot keeps the same
	// events.jsonl across resumes, so onSessionID records the same id again. Start
	// a fresh open-window so "same period" correlation compares the latest run, not
	// one interval spanning idle days between runs (Codex P2). A re-record within
	// newWindowGapSec is treated as the same run (defensive against a double-fire)
	// and only advances LastSeen.
	if t-r.LastSeen > newWindowGapSec {
		r.FirstSeen = t
	}
	r.LastSeen = t
	// Refresh metadata: a reused id (Copilot keeps the same events.jsonl) may be
	// resumed after a team rename or config change. Re-indexing under the CURRENT
	// room/agent/cli/cwd keeps the picker for the current team showing it (Codex P2).
	r.Room, r.AgentName, r.CLIType, r.Cwd = room, agent, cliType, cwd
	return r
}

// Touch advances LastSeen for a known session (FirstSeen preserved). Unknown → no-op.
func (s *Store) Touch(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateExistingRecord(sessionID, func(r Record) (Record, bool) {
		r.LastSeen = s.now()
		return r, true
	})
}

// TouchAt sets LastSeen to an EXPLICIT time (unix seconds) for a known session, but
// only if t is newer — it never regresses the window. Unknown id, nil store, or a
// non-newer t → no-op. Used to pin a session's window to its real PTY-exit time when a
// dead pane is UI-closed later, instead of stretching it to the close time (Codex P2).
func (s *Store) TouchAt(sessionID string, t float64) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateExistingRecord(sessionID, func(r Record) (Record, bool) {
		if t <= r.LastSeen {
			return r, false
		}
		r.LastSeen = t
		return r, true
	})
}

func (s *Store) updateExistingRecord(sessionID string, update func(Record) (Record, bool)) {
	previous, ok := s.records[sessionID]
	if !ok {
		return
	}
	updated, changed := update(previous)
	if !changed {
		return
	}
	s.records[sessionID] = updated
	if !s.save() {
		s.records[sessionID] = previous
	}
}

// ListSessions returns a room+agent's sessions, newest LastSeen first. Nil-safe:
// a failed New leaves the app's store nil, and the enumeration binding calls this
// directly — returning nil (→ empty history) instead of panicking (Codex/Copilot).
func (s *Store) ListSessions(room, agent string) []Record {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Record
	for _, r := range s.records {
		// Case-insensitive: UpsertAgent preserves the config casing while Record stores
		// the raw launch name, so "Alice" (config) and "alice" (launched) must match —
		// the rest of the team code treats names case-insensitively (Codex P2).
		if strings.EqualFold(r.Room, room) && strings.EqualFold(r.AgentName, agent) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out
}

// ListAgents returns distinct agent names seen in a room, newest activity first.
// Nil-safe (see ListSessions).
func (s *Store) ListAgents(room string) []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Group case-insensitively (see ListSessions); the newest-seen casing is the
	// display name so "Alice"/"alice" collapse to one entry (Codex P2).
	last := map[string]float64{}
	display := map[string]string{}
	for _, r := range s.records {
		if !strings.EqualFold(r.Room, room) {
			continue
		}
		key := strings.ToLower(r.AgentName)
		if _, seen := last[key]; !seen || r.LastSeen > last[key] {
			last[key] = r.LastSeen
			display[key] = r.AgentName
		}
	}
	// Sort the already-lowercased keys by last-seen, THEN map to display names — sorting
	// display names directly would re-ToLower each one O(N log N) times in the comparator
	// (Gemini).
	keys := make([]string, 0, len(last))
	for key := range last {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return last[keys[i]] > last[keys[j]] })
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, display[key])
	}
	return names
}

// RenameRoom re-indexes every history record from oldRoom to newRoom, so a team
// rename doesn't hide its past sessions from the resume picker (which queries by the
// team's CURRENT room name). Matched case-insensitively; no-op if unchanged or the
// store is nil (#40 Faz-2, Codex P2).
func (s *Store) RenameRoom(oldRoom, newRoom string) {
	if s == nil || oldRoom == "" || strings.EqualFold(oldRoom, newRoom) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := cloneRecords(s.records)
	changed := false
	for id, r := range s.records {
		if strings.EqualFold(r.Room, oldRoom) {
			r.Room = newRoom
			s.records[id] = r
			changed = true
		}
	}
	if changed {
		if !s.save() {
			s.records = previous
		}
	}
}

// Get returns the record for a session id (the resume target's recorded metadata,
// e.g. its cwd), or false if unknown. Nil-safe (#40 Faz-2, Codex P2).
func (s *Store) Get(sessionID string) (Record, bool) {
	if s == nil {
		return Record{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[sessionID]
	return r, ok
}

func cloneRecords(records map[string]Record) map[string]Record {
	cloned := make(map[string]Record, len(records))
	for id, record := range records {
		cloned[id] = record
	}
	return cloned
}

// save writes records atomically (temp+rename). Called under mu. Record/Touch are
// void, so a persist failure can't be returned — but it must not be SILENT (the user
// would lose resume history with no trace), so each failure is logged, mirroring how
// team.Store surfaces its persistence errors (Copilot). The boolean lets callers
// roll back in-memory mutations when persistence fails, keeping memory and disk in
// sync for future resume-history reads.
func (s *Store) save() bool {
	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		log.Printf("[SESSIONLOG] marshal failed: %v", err)
		return false
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("[SESSIONLOG] write %s failed: %v", tmp, err)
		os.Remove(tmp)
		return false
	}
	if err := os.Rename(tmp, s.filePath); err != nil {
		log.Printf("[SESSIONLOG] rename %s -> %s failed: %v", tmp, s.filePath, err)
		os.Remove(tmp)
		return false
	}
	return true
}
