package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"desktop/internal/sanitize"
	"desktop/internal/validation"

	"github.com/google/uuid"
)

// RoleObserver is the AgentConfig.Role value (#17) marking a read-only observer.
// It is the single source the hub's IsObserver gate and broadcastRoleLookup read,
// and is mutually exclusive with the team's ManagerAgent (an agent is one or the
// other, never both).
const RoleObserver = "observer"

// AgentConfig represents an agent's configuration within a team
type AgentConfig struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	PromptID string `json:"prompt_id"`
	WorkDir  string `json:"work_dir"`
	CLIType  string `json:"cli_type"`
	// SlotIndex is the grid position the agent reopens into. No omitempty:
	// 0 is a valid slot and must survive serialization round-trips.
	SlotIndex int `json:"slot_index"`
	// UseWorktree records whether the agent runs in a git worktree.
	UseWorktree bool `json:"use_worktree"`
}

// Team represents a tab/team configuration
type Team struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Agents       []AgentConfig `json:"agents"`
	GridLayout   string        `json:"grid_layout"` // "1x1", "2x2", "2x3", etc.
	ChatDir      string        `json:"chat_dir"`
	ManagerAgent string        `json:"manager_agent"`
	CustomPrompt string        `json:"custom_prompt"`
	CreatedAt    string        `json:"created_at"`
}

// IsManagerAgent reports whether name is this team's manager, matching
// case-insensitively (and trimming whitespace). Manager identity must not depend
// on casing: an agent saved as "Pilot" and one named "pilot" are the same.
func (t Team) IsManagerAgent(name string) bool {
	mgr := strings.TrimSpace(t.ManagerAgent)
	if mgr == "" {
		return false
	}
	return sameAgentName(mgr, name)
}

func sameAgentName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func hasTeamNameCollision(teams []Team, name, excludeID string) bool {
	for _, team := range teams {
		if team.ID != excludeID && strings.EqualFold(team.Name, name) {
			return true
		}
	}
	return false
}

func normalizeRequiredName(kind, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%s name is required", kind)
	}
	if err := validation.ValidateName(name); err != nil {
		return "", err
	}
	return name, nil
}

func validateRequiredName(kind, name string) error {
	_, err := normalizeRequiredName(kind, name)
	return err
}

func normalizeAgentConfigs(agents []AgentConfig) ([]AgentConfig, error) {
	if agents == nil {
		return nil, nil
	}
	normalized := cloneAgents(agents)
	seen := make(map[string]struct{}, len(normalized))
	for i := range normalized {
		name, err := normalizeRequiredName("agent", normalized[i].Name)
		if err != nil {
			return nil, err
		}
		normalized[i].Name = name
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate agent name: %s", name)
		}
		seen[key] = struct{}{}
	}
	return normalized, nil
}

// Store manages team/tab persistence
type Store struct {
	mu       sync.RWMutex
	filePath string
	teams    []Team
}

func cloneAgents(agents []AgentConfig) []AgentConfig {
	if agents == nil {
		return nil
	}
	cloned := make([]AgentConfig, len(agents))
	copy(cloned, agents)
	return cloned
}

func cloneTeam(t Team) Team {
	t.Agents = cloneAgents(t.Agents)
	return t
}

// NewStore creates a new team store
func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create team data dir: %w", err)
	}
	fp := filepath.Join(dataDir, "teams.json")

	s := &Store{
		filePath: fp,
	}

	if err := s.load(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.teams = []Team{}
		} else {
			return nil, fmt.Errorf("load teams: %w", err)
		}
	}

	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return fmt.Errorf("read teams file %s: %w", s.filePath, err)
	}
	if err := json.Unmarshal(data, &s.teams); err != nil {
		return fmt.Errorf("parse teams file %s: %w", s.filePath, err)
	}
	return nil
}

// save serializes the teams to disk atomically (temp file + rename) so a crash
// mid-write — e.g. during the batch UpsertAgent calls of OpenTeamFromConfig —
// can never leave a partial/corrupt teams.json behind. Mirrors the hub-state
// persistence pattern (internal/hub/persistence.go).
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.teams, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal teams file %s: %w", s.filePath, err)
	}

	tmpPath := s.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		os.Remove(tmpPath) // clean up a partially-written temp file
		return fmt.Errorf("write teams file temp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, s.filePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace teams file %s: %w", s.filePath, err)
	}
	return nil
}

// List returns all teams
func (s *Store) List() []Team {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Team, len(s.teams))
	for i, team := range s.teams {
		result[i] = cloneTeam(team)
	}
	return result
}

// Get returns a team by ID
func (s *Store) Get(id string) (Team, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.teams {
		if t.ID == id {
			return cloneTeam(t), nil
		}
	}
	return Team{}, fmt.Errorf("team not found: %s", id)
}

// Create creates a new team
func (s *Store) Create(name, gridLayout string, agents []AgentConfig) (Team, error) {
	name, err := normalizeRequiredName("team", name)
	if err != nil {
		return Team{}, fmt.Errorf("invalid team name: %w", err)
	}
	agents, err = normalizeAgentConfigs(agents)
	if err != nil {
		return Team{}, fmt.Errorf("invalid agent config: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// EqualFold: team names become room filenames; on case-insensitive filesystems
	// (macOS/Windows) "Alpha" and "alpha" collide on the same file, so reject names that
	// differ only by case to prevent data-losing collisions.
	if hasTeamNameCollision(s.teams, name, "") {
		return Team{}, fmt.Errorf("aynı adda takım zaten var: %s", name)
	}

	id := uuid.New().String()
	// All teams share the same rooms base dir; team name is used as room name
	chatDir := filepath.Join(filepath.Dir(s.filePath), "rooms")

	t := Team{
		ID:         id,
		Name:       name,
		Agents:     agents,
		GridLayout: gridLayout,
		ChatDir:    chatDir,
		CreatedAt:  time.Now().Format(time.RFC3339),
	}

	prev := s.teams
	s.teams = append(s.teams, t)
	if err := s.save(); err != nil {
		s.teams = prev // roll back so memory matches disk
		return Team{}, err
	}
	if err := os.MkdirAll(chatDir, 0700); err != nil {
		s.teams = prev // roll back the in-memory create
		if rollbackErr := s.save(); rollbackErr != nil {
			return Team{}, fmt.Errorf("create team chat dir %s: %w (rollback save failed: %v)", chatDir, err, rollbackErr)
		}
		return Team{}, fmt.Errorf("create team chat dir %s: %w", chatDir, err)
	}

	return cloneTeam(t), nil
}

// Update updates a team
func (s *Store) Update(id, name, gridLayout string, agents []AgentConfig) (Team, error) {
	name, err := normalizeRequiredName("team", name)
	if err != nil {
		return Team{}, fmt.Errorf("invalid team name: %w", err)
	}
	agents, err = normalizeAgentConfigs(agents)
	if err != nil {
		return Team{}, fmt.Errorf("invalid agent config: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Same filesystem-collision guard as Create, but excluding the team being renamed:
	// a rename that case-insensitively matches a DIFFERENT team would map both to the
	// same room file on macOS/Windows.
	if hasTeamNameCollision(s.teams, name, id) {
		return Team{}, fmt.Errorf("aynı adda takım zaten var: %s", name)
	}

	for i, t := range s.teams {
		if t.ID == id {
			prev := s.teams[i]
			s.teams[i].Name = name
			s.teams[i].GridLayout = gridLayout
			s.teams[i].Agents = agents

			if err := s.save(); err != nil {
				s.teams[i] = prev // roll back so memory matches disk
				return Team{}, err
			}
			return cloneTeam(s.teams[i]), nil
		}
	}
	return Team{}, fmt.Errorf("team not found: %s", id)
}

// UpsertAgent inserts or updates a single agent config in a team, keyed by Name.
// If an agent with the same name exists, its fields are updated in place;
// otherwise the agent is appended. Follows the single-field SetManager pattern
// rather than a positional full-team Update so concurrent callers don't clobber
// each other.
//
// INVARIANT: an empty cfg.Role does NOT overwrite an existing Role. Callers like
// CreateTerminal never supply Role, so a naive upsert would erase a Role the user
// set earlier (consumed by composeAgentPrompt). A non-empty cfg.Role overwrites.
func (s *Store) UpsertAgent(teamID string, cfg AgentConfig) (Team, error) {
	name, err := normalizeRequiredName("agent", cfg.Name)
	if err != nil {
		return Team{}, fmt.Errorf("invalid agent name: %w", err)
	}
	cfg.Name = name

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, t := range s.teams {
		if t.ID != teamID {
			continue
		}
		prev := t.Agents // keep the old slice for rollback
		for j, existing := range t.Agents {
			if sameAgentName(existing.Name, cfg.Name) {
				// Match is case-insensitive, but keep the stored Name's original
				// casing: Team.ManagerAgent holds that spelling and resolveManagerIntent
				// compares it case-sensitively, so a case-only re-create must not
				// rewrite the name and break manager recognition.
				cfg.Name = existing.Name
				// Preserve Role when the upsert payload omits it.
				if cfg.Role == "" {
					cfg.Role = existing.Role
				}
				// No-op: skip the disk write when nothing changed. OpenTeamFromConfig
				// reopens N agents (each → CreateTerminal → UpsertAgent); without this
				// an unchanged batch would rewrite teams.json N times.
				if existing == cfg {
					return cloneTeam(s.teams[i]), nil
				}
				// Copy-on-write: mutate a fresh slice instead of the shared one so any
				// stale in-process Team values cannot observe a partial update, and so
				// concurrent readers never race with an in-place Agents mutation.
				updated := make([]AgentConfig, len(prev))
				copy(updated, prev)
				updated[j] = cfg
				s.teams[i].Agents = updated
				if err := s.save(); err != nil {
					s.teams[i].Agents = prev // roll back so memory matches disk
					return Team{}, err
				}
				return cloneTeam(s.teams[i]), nil
			}
		}
		// Not found: append into a fresh slice (don't append in place — append may
		// write into the shared backing array when spare capacity exists).
		appended := make([]AgentConfig, len(prev), len(prev)+1)
		copy(appended, prev)
		appended = append(appended, cfg)
		s.teams[i].Agents = appended
		if err := s.save(); err != nil {
			s.teams[i].Agents = prev // roll back append
			return Team{}, err
		}
		return cloneTeam(s.teams[i]), nil
	}
	return Team{}, fmt.Errorf("team not found: %s", teamID)
}

func agentsWithoutObserverRole(agents []AgentConfig, agentName string) ([]AgentConfig, bool) {
	if agentName == "" {
		return agents, false
	}
	for i, agent := range agents {
		if !sameAgentName(agent.Name, agentName) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(agent.Role), RoleObserver) {
			return agents, false
		}
		updated := make([]AgentConfig, len(agents))
		copy(updated, agents)
		updated[i].Role = ""
		return updated, true
	}
	return agents, false
}

// SetManager sets or clears manager agent for a team. Empty string clears manager.
func (s *Store) SetManager(id, managerAgent string) (Team, error) {
	if managerAgent != "" {
		name, err := normalizeRequiredName("manager agent", managerAgent)
		if err != nil {
			return Team{}, fmt.Errorf("invalid manager agent name: %w", err)
		}
		managerAgent = name
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, t := range s.teams {
		if t.ID == id {
			prevMgr := s.teams[i].ManagerAgent
			prevAgents := t.Agents
			s.teams[i].ManagerAgent = managerAgent
			// Mutual exclusion (#17): a manager is not an observer. Clear a stale
			// observer Role on the new manager, else broadcastRoleLookup would wrongly
			// skip the manager from broadcasts. Copy-on-write so concurrent readers of
			// the shared Agents slice don't race.
			updatedAgents, agentsChanged := agentsWithoutObserverRole(t.Agents, managerAgent)
			s.teams[i].Agents = updatedAgents
			if prevMgr == managerAgent && !agentsChanged {
				return cloneTeam(s.teams[i]), nil
			}
			if err := s.save(); err != nil {
				s.teams[i].ManagerAgent = prevMgr
				s.teams[i].Agents = prevAgents
				return Team{}, err
			}
			return cloneTeam(s.teams[i]), nil
		}
	}
	return Team{}, fmt.Errorf("team not found: %s", id)
}

// SetObserver marks an agent as the room's read-only observer (#17) by setting its
// Role to RoleObserver, appending the agent if it doesn't exist yet. Because an
// agent can't be both manager and observer, the manager assignment is cleared if
// this agent held it. The observer Role is what the hub's IsObserver gate and
// broadcastRoleLookup read. Copy-on-write so concurrent readers don't race.
func (s *Store) SetObserver(teamID, name string) (Team, error) {
	name, err := normalizeRequiredName("agent", name)
	if err != nil {
		return Team{}, fmt.Errorf("invalid agent name: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, t := range s.teams {
		if t.ID != teamID {
			continue
		}
		prevAgents := t.Agents
		prevMgr := s.teams[i].ManagerAgent

		updated := make([]AgentConfig, len(prevAgents))
		copy(updated, prevAgents)
		found := false
		agentsChanged := false
		for j, a := range updated {
			if sameAgentName(a.Name, name) {
				if updated[j].Role != RoleObserver {
					updated[j].Role = RoleObserver
					agentsChanged = true
				}
				found = true
				break
			}
		}
		if !found {
			updated = append(updated, AgentConfig{Name: name, Role: RoleObserver})
			agentsChanged = true
		}
		s.teams[i].Agents = updated
		// Mutual exclusion: an observer is not a manager.
		managerChanged := false
		if s.teams[i].IsManagerAgent(name) {
			s.teams[i].ManagerAgent = ""
			managerChanged = true
		}
		if !agentsChanged && !managerChanged {
			return cloneTeam(s.teams[i]), nil
		}

		if err := s.save(); err != nil {
			s.teams[i].Agents = prevAgents
			s.teams[i].ManagerAgent = prevMgr
			return Team{}, err
		}
		return cloneTeam(s.teams[i]), nil
	}
	return Team{}, fmt.Errorf("team not found: %s", teamID)
}

// maxCharterLen is the soft upper bound on a room charter (custom_prompt),
// counted in runes so a multibyte (Turkish) character is never split mid-encoding.
const maxCharterLen = 2000

// sanitizeCharter cleans free-text room charter input before it is persisted and
// later pasted verbatim into an agent's PTY at startup (sendStartupPrompt uses
// bracketed paste). It strips:
//   - the bracketed-paste markers (\x1b[200~ / \x1b[201~) plus every C0 control
//     byte and DEL — a raw ESC or an embedded paste terminator could otherwise
//     end paste mode early and let the rest of the charter run as live keystrokes;
//   - C1 control bytes and the invisible Unicode format set (bidi controls,
//     zero-width chars, BOM, Tags) plus line/paragraph separators — Trojan-Source
//     class. Classification is shared with the broadcast injection path via the
//     sanitize package so the two cannot drift (see sanitize.IsControl /
//     IsInvisibleFormat).
//
// Newline and tab are preserved so multi-line charters survive. ValidateName is
// deliberately NOT applied: a charter is free prose, not an identifier. Finally
// the text is capped at maxCharterLen runes (rune-based, UTF-8-safe).
func sanitizeCharter(text string) string {
	// Strip bracketed-paste markers + control/invisible runes (shared with the room
	// summary and transcript paths so the cleaning can't drift), then cap.
	text = sanitize.StripForTerminalPaste(text)
	if runes := []rune(text); len(runes) > maxCharterLen {
		text = string(runes[:maxCharterLen])
	}
	return text
}

// SetCustomPrompt sets a team's room charter (custom_prompt) via a targeted
// single-field update, mirroring SetManager. The positional Update is NOT extended
// for this: its sole caller (TerminalGrid.handleLayoutChange) omits custom_prompt,
// so widening Update's signature would reset the charter to "" on every grid-layout
// change. The text is sanitized (sanitizeCharter) because it is pasted verbatim
// into each agent's PTY at startup. The charter is injected into new agents only;
// already-running agents are unaffected (composeAgentPrompt runs at startup).
func (s *Store) SetCustomPrompt(id, text string) (Team, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, t := range s.teams {
		if t.ID == id {
			sanitized := sanitizeCharter(text)
			if s.teams[i].CustomPrompt == sanitized {
				return cloneTeam(s.teams[i]), nil // no-op: skip the disk write (matches UpsertAgent)
			}
			prev := s.teams[i].CustomPrompt
			s.teams[i].CustomPrompt = sanitized
			if err := s.save(); err != nil {
				s.teams[i].CustomPrompt = prev // roll back so memory matches disk
				return Team{}, err
			}
			return cloneTeam(s.teams[i]), nil
		}
	}
	return Team{}, fmt.Errorf("team not found: %s", id)
}

// Delete deletes a team
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, t := range s.teams {
		if t.ID == id {
			prev := s.teams
			updated := make([]Team, 0, len(prev)-1)
			updated = append(updated, prev[:i]...)
			updated = append(updated, prev[i+1:]...)
			s.teams = updated
			if err := s.save(); err != nil {
				s.teams = prev // roll back so memory matches disk
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("team not found: %s", id)
}
