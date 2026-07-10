package prompt

import "testing"

func TestSeedIfMissingByName_CreatesWhenAbsent(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	p, created, err := s.SeedIfMissingByName("Session Özeti", "özetle {{TRANSCRIPT}}", "task", []string{"summary"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected created=true on first seed")
	}
	if p.Name != "Session Özeti" {
		t.Fatalf("name = %q, want Session Özeti", p.Name)
	}
	if len(p.Variables) != 1 || p.Variables[0] != "TRANSCRIPT" {
		t.Fatalf("variables = %v, want [TRANSCRIPT]", p.Variables)
	}

	// Idempotent: a second call must NOT duplicate and must NOT overwrite content
	// (user edits to the seeded prompt are preserved).
	_, created2, err := s.SeedIfMissingByName("Session Özeti", "değişmiş içerik", "task", nil)
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("expected created=false when a prompt with that name already exists")
	}
	count := 0
	for _, pr := range s.List() {
		if pr.Name == "Session Özeti" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("want exactly 1 prompt named 'Session Özeti', got %d", count)
	}
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "özetle {{TRANSCRIPT}}" {
		t.Fatalf("content overwritten: %q", got.Content)
	}
}

func TestSeedIfMissingByName_RunsEvenWhenStoreNonEmpty(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Pre-populate so the store is non-empty — Seed() (empty-only) would no-op here.
	if _, err := s.Create("Existing", "x", "system", nil); err != nil {
		t.Fatal(err)
	}

	_, created, err := s.SeedIfMissingByName("Yeni Prompt", "y", "task", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected SeedIfMissingByName to seed even when the store is non-empty")
	}
}

func TestSaveFailuresRollbackInMemoryState(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	existing, err := s.Create("Existing", "original", "system", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Point filePath at a directory so the temp write can succeed but the atomic
	// rename fails. Mutators must roll their in-memory changes back when disk
	// persistence fails, otherwise future saves would silently include failed edits.
	s.filePath = t.TempDir()

	if _, err := s.Create("New", "content", "task", nil); err == nil {
		t.Fatal("expected Create to fail")
	}
	if prompts := s.List(); len(prompts) != 1 || prompts[0].ID != existing.ID {
		t.Fatalf("Create rollback left prompts = %#v", prompts)
	}

	if _, err := s.Update(existing.ID, "Changed", "changed", "task", []string{"x"}); err == nil {
		t.Fatal("expected Update to fail")
	}
	got, err := s.Get(existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Existing" || got.Content != "original" || got.Category != "system" || len(got.Tags) != 0 {
		t.Fatalf("Update rollback left prompt = %#v", got)
	}

	if err := s.Delete(existing.ID); err == nil {
		t.Fatal("expected Delete to fail")
	}
	if prompts := s.List(); len(prompts) != 1 || prompts[0].ID != existing.ID {
		t.Fatalf("Delete rollback left prompts = %#v", prompts)
	}
}
