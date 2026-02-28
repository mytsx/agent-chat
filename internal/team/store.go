package team

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"desktop/internal/validation"

	"github.com/google/uuid"
)

// AgentConfig represents an agent's configuration within a team
type AgentConfig struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	PromptID string `json:"prompt_id"`
	WorkDir  string `json:"work_dir"`
	CLIType  string `json:"cli_type"`
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

// Store manages team/tab persistence
type Store struct {
	mu       sync.RWMutex
	filePath string
	teams    []Team
}

// NewStore creates a new team store
func NewStore(dataDir string) (*Store, error) {
	os.MkdirAll(dataDir, 0700)
	fp := filepath.Join(dataDir, "teams.json")

	s := &Store{
		filePath: fp,
	}

	if err := s.load(); err != nil {
		s.teams = []Team{}
	}

	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.teams)
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(s.teams, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0644)
}

// List returns all teams
func (s *Store) List() []Team {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Team, len(s.teams))
	copy(result, s.teams)
	return result
}

// Get returns a team by ID
func (s *Store) Get(id string) (Team, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.teams {
		if t.ID == id {
			return t, nil
		}
	}
	return Team{}, fmt.Errorf("team not found: %s", id)
}

// Create creates a new team
func (s *Store) Create(name, gridLayout string, agents []AgentConfig) (Team, error) {
	if err := validation.ValidateName(name); err != nil {
		return Team{}, fmt.Errorf("invalid team name: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

	s.teams = append(s.teams, t)
	if err := s.save(); err != nil {
		return Team{}, err
	}

	// Create chat directory
	os.MkdirAll(chatDir, 0700)

	return t, nil
}

// Update updates a team
func (s *Store) Update(id, name, gridLayout string, agents []AgentConfig) (Team, error) {
	if err := validation.ValidateName(name); err != nil {
		return Team{}, fmt.Errorf("invalid team name: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, t := range s.teams {
		if t.ID == id {
			s.teams[i].Name = name
			s.teams[i].GridLayout = gridLayout
			s.teams[i].Agents = agents

			if err := s.save(); err != nil {
				return Team{}, err
			}
			return s.teams[i], nil
		}
	}
	return Team{}, fmt.Errorf("team not found: %s", id)
}

// SetManager sets or clears manager agent for a team. Empty string clears manager.
func (s *Store) SetManager(id, managerAgent string) (Team, error) {
	if managerAgent != "" {
		if err := validation.ValidateName(managerAgent); err != nil {
			return Team{}, fmt.Errorf("invalid manager agent name: %w", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, t := range s.teams {
		if t.ID == id {
			s.teams[i].ManagerAgent = managerAgent
			if err := s.save(); err != nil {
				return Team{}, err
			}
			return s.teams[i], nil
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
			s.teams = append(s.teams[:i], s.teams[i+1:]...)
			return s.save()
		}
	}
	return fmt.Errorf("team not found: %s", id)
}
