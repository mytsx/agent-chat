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
	mu       sync.Mutex
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
		_ = json.Unmarshal(data, &s.records) // corrupt/empty → start empty
		if s.records == nil {
			s.records = make(map[string]Record)
		}
	}
	return s, nil
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
	if r, ok := s.records[sessionID]; ok {
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
		s.records[sessionID] = r
	} else {
		s.records[sessionID] = Record{
			SessionID: sessionID, Room: room, AgentName: agent,
			CLIType: cliType, Cwd: cwd, FirstSeen: t, LastSeen: t,
		}
	}
	s.save()
}

// Touch advances LastSeen for a known session (FirstSeen preserved). Unknown → no-op.
func (s *Store) Touch(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[sessionID]; ok {
		r.LastSeen = s.now()
		s.records[sessionID] = r
		s.save()
	}
}

// ListSessions returns a room+agent's sessions, newest LastSeen first. Nil-safe:
// a failed New leaves the app's store nil, and the enumeration binding calls this
// directly — returning nil (→ empty history) instead of panicking (Codex/Copilot).
func (s *Store) ListSessions(room, agent string) []Record {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	// Group case-insensitively (see ListSessions); the newest-seen casing is the
	// display name so "Alice"/"alice" collapse to one entry (Codex P2).
	last := map[string]float64{}
	display := map[string]string{}
	for _, r := range s.records {
		if !strings.EqualFold(r.Room, room) {
			continue
		}
		key := strings.ToLower(r.AgentName)
		if r.LastSeen > last[key] {
			last[key] = r.LastSeen
			display[key] = r.AgentName
		}
	}
	names := make([]string, 0, len(last))
	for key := range last {
		names = append(names, display[key])
	}
	sort.Slice(names, func(i, j int) bool {
		return last[strings.ToLower(names[i])] > last[strings.ToLower(names[j])]
	})
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
	changed := false
	for id, r := range s.records {
		if strings.EqualFold(r.Room, oldRoom) {
			r.Room = newRoom
			s.records[id] = r
			changed = true
		}
	}
	if changed {
		s.save()
	}
}

// Get returns the record for a session id (the resume target's recorded metadata,
// e.g. its cwd), or false if unknown. Nil-safe (#40 Faz-2, Codex P2).
func (s *Store) Get(sessionID string) (Record, bool) {
	if s == nil {
		return Record{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[sessionID]
	return r, ok
}

// save writes records atomically (temp+rename). Called under mu. Record/Touch are
// void, so a persist failure can't be returned — but it must not be SILENT (the user
// would lose resume history with no trace), so each failure is logged, mirroring how
// team.Store surfaces its persistence errors (Copilot).
func (s *Store) save() {
	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		log.Printf("[SESSIONLOG] marshal failed: %v", err)
		return
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("[SESSIONLOG] write %s failed: %v", tmp, err)
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, s.filePath); err != nil {
		log.Printf("[SESSIONLOG] rename %s -> %s failed: %v", tmp, s.filePath, err)
		os.Remove(tmp)
	}
}
