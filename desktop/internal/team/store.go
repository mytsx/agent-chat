package team

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AgentConfig represents an agent's configuration within a team
type AgentConfig struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	PromptID string `json:"prompt_id"`
	WorkDir  string `json:"work_dir"`
}

// Team represents a tab/team configuration
type Team struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Agents     []AgentConfig `json:"agents"`
	GridLayout string        `json:"grid_layout"` // "1x1", "2x2", "2x3", etc.
	ChatDir    string        `json:"chat_dir"`
	CreatedAt  string        `json:"created_at"`
}

// Store manages team/tab persistence
type Store struct {
	mu       sync.RWMutex
	filePath string
	teams    []Team
}

// NewStore creates a new team store
func NewStore(dataDir string) (*Store, error) {
	os.MkdirAll(dataDir, 0755)
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
	s.mu.Lock()
	defer s.mu.Unlock()

	id := uuid.New().String()
	chatDir := filepath.Join(os.TempDir(), fmt.Sprintf("agent-chat-room-%s", id[:8]))

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
	os.MkdirAll(chatDir, 0755)

	return t, nil
}

// Update updates a team
func (s *Store) Update(id, name, gridLayout string, agents []AgentConfig) (Team, error) {
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
