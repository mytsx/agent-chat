package prompt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Prompt represents a saved prompt template
type Prompt struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Content   string   `json:"content"`
	Category  string   `json:"category"` // "role", "task", "system"
	Tags      []string `json:"tags"`
	Variables []string `json:"variables"` // e.g., {{AGENT_NAME}}, {{PROJECT_DIR}}
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// Store manages prompt persistence
type Store struct {
	mu       sync.RWMutex
	filePath string
	prompts  []Prompt
}

// NewStore creates a new prompt store
func NewStore(dataDir string) (*Store, error) {
	os.MkdirAll(dataDir, 0700)
	fp := filepath.Join(dataDir, "prompts.json")

	s := &Store{
		filePath: fp,
	}

	if err := s.load(); err != nil {
		// File doesn't exist yet, start with empty
		s.prompts = []Prompt{}
	}

	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.prompts)
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(s.prompts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0644)
}

// List returns all prompts
func (s *Store) List() []Prompt {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Prompt, len(s.prompts))
	copy(result, s.prompts)
	return result
}

// Get returns a prompt by ID
func (s *Store) Get(id string) (Prompt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, p := range s.prompts {
		if p.ID == id {
			return p, nil
		}
	}
	return Prompt{}, fmt.Errorf("prompt not found: %s", id)
}

// Create creates a new prompt
func (s *Store) Create(name, content, category string, tags []string) (Prompt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Format(time.RFC3339)
	p := Prompt{
		ID:        uuid.New().String(),
		Name:      name,
		Content:   content,
		Category:  category,
		Tags:      tags,
		Variables: extractVariables(content),
		CreatedAt: now,
		UpdatedAt: now,
	}

	s.prompts = append(s.prompts, p)
	if err := s.save(); err != nil {
		return Prompt{}, err
	}
	return p, nil
}

// Update updates an existing prompt
func (s *Store) Update(id, name, content, category string, tags []string) (Prompt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, p := range s.prompts {
		if p.ID == id {
			s.prompts[i].Name = name
			s.prompts[i].Content = content
			s.prompts[i].Category = category
			s.prompts[i].Tags = tags
			s.prompts[i].Variables = extractVariables(content)
			s.prompts[i].UpdatedAt = time.Now().Format(time.RFC3339)

			if err := s.save(); err != nil {
				return Prompt{}, err
			}
			return s.prompts[i], nil
		}
	}
	return Prompt{}, fmt.Errorf("prompt not found: %s", id)
}

// Delete deletes a prompt
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, p := range s.prompts {
		if p.ID == id {
			s.prompts = append(s.prompts[:i], s.prompts[i+1:]...)
			return s.save()
		}
	}
	return fmt.Errorf("prompt not found: %s", id)
}

// RenderPrompt renders a prompt with variable substitution
func RenderPrompt(content string, vars map[string]string) string {
	result := content
	for key, val := range vars {
		result = strings.ReplaceAll(result, "{{"+key+"}}", val)
	}
	return result
}

// Seed adds default prompts if none exist
func (s *Store) Seed(basePrompt, managerPrompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.prompts) > 0 {
		return
	}

	now := time.Now().Format(time.RFC3339)

	s.prompts = []Prompt{
		{
			ID:        uuid.New().String(),
			Name:      "Agent Base Prompt",
			Content:   basePrompt,
			Category:  "system",
			Tags:      []string{"base", "agent"},
			Variables: extractVariables(basePrompt),
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        uuid.New().String(),
			Name:      "Manager Prompt",
			Content:   managerPrompt,
			Category:  "role",
			Tags:      []string{"manager", "coordinator"},
			Variables: extractVariables(managerPrompt),
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	s.save()
}

// extractVariables finds {{VAR_NAME}} patterns in content
func extractVariables(content string) []string {
	var vars []string
	seen := make(map[string]bool)

	for {
		start := strings.Index(content, "{{")
		if start == -1 {
			break
		}
		end := strings.Index(content[start:], "}}")
		if end == -1 {
			break
		}
		varName := content[start+2 : start+end]
		if !seen[varName] {
			vars = append(vars, varName)
			seen[varName] = true
		}
		content = content[start+end+2:]
	}

	return vars
}
