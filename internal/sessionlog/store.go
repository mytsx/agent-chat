// Package sessionlog persists a per-(room,agent) history of CLI sessions a
// terminal has run, so the user can later resume a SPECIFIC past session and
// correlate sessions across agents by the time window they were open (#40 Faz-2).
package sessionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

func nowUnix() float64 { return float64(time.Now().Unix()) }

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
		r.LastSeen = t
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

// ListSessions returns a room+agent's sessions, newest LastSeen first.
func (s *Store) ListSessions(room, agent string) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, r := range s.records {
		if r.Room == room && r.AgentName == agent {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out
}

// ListAgents returns distinct agent names seen in a room, newest activity first.
func (s *Store) ListAgents(room string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := map[string]float64{}
	for _, r := range s.records {
		if r.Room != room {
			continue
		}
		if r.LastSeen > last[r.AgentName] {
			last[r.AgentName] = r.LastSeen
		}
	}
	names := make([]string, 0, len(last))
	for n := range last {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return last[names[i]] > last[names[j]] })
	return names
}

// save writes records atomically (temp+rename). Called under mu.
func (s *Store) save() {
	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, s.filePath); err != nil {
		os.Remove(tmp)
	}
}
