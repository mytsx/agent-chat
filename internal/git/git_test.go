package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo creates a temp dir with an initialized git repo and an initial commit.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run("init")
	run("checkout", "-b", "main")
	// Create an initial commit so HEAD exists
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0644)
	run("add", ".")
	run("commit", "-m", "initial")

	return dir
}

func TestIsGitRepo(t *testing.T) {
	t.Run("git repo", func(t *testing.T) {
		dir := initTestRepo(t)
		if !IsGitRepo(dir) {
			t.Error("expected true for git repo")
		}
	})

	t.Run("non-git directory", func(t *testing.T) {
		dir := t.TempDir()
		if IsGitRepo(dir) {
			t.Error("expected false for non-git dir")
		}
	})
}

func TestCreateWorktree(t *testing.T) {
	t.Run("new worktree", func(t *testing.T) {
		repo := initTestRepo(t)
		wtPath := filepath.Join(t.TempDir(), "wt1")

		if err := CreateWorktree(repo, wtPath, "test-branch"); err != nil {
			t.Fatalf("CreateWorktree: %v", err)
		}

		if !IsGitRepo(wtPath) {
			t.Error("worktree should be a git repo")
		}

		branch, err := CurrentBranch(wtPath)
		if err != nil {
			t.Fatalf("CurrentBranch: %v", err)
		}
		if branch != "test-branch" {
			t.Errorf("expected branch test-branch, got %s", branch)
		}
	})

	t.Run("idempotent reuse", func(t *testing.T) {
		repo := initTestRepo(t)
		wtPath := filepath.Join(t.TempDir(), "wt2")

		if err := CreateWorktree(repo, wtPath, "reuse-branch"); err != nil {
			t.Fatalf("first CreateWorktree: %v", err)
		}

		// Second call should not error (idempotent)
		if err := CreateWorktree(repo, wtPath, "reuse-branch"); err != nil {
			t.Fatalf("second CreateWorktree should be idempotent: %v", err)
		}
	})

	t.Run("reuse rejects branch mismatch", func(t *testing.T) {
		repo := initTestRepo(t)
		wtPath := filepath.Join(t.TempDir(), "wt-mismatch")

		if err := CreateWorktree(repo, wtPath, "branch-a"); err != nil {
			t.Fatalf("CreateWorktree: %v", err)
		}

		// Reuse with different branch should fail
		err := CreateWorktree(repo, wtPath, "branch-b")
		if err == nil {
			t.Fatal("expected error for branch mismatch reuse")
		}
		if !strings.Contains(err.Error(), "expected branch-b") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("reuse rejects different repo", func(t *testing.T) {
		repo1 := initTestRepo(t)
		repo2 := initTestRepo(t)
		wtPath := filepath.Join(t.TempDir(), "wt-repo-mismatch")

		if err := CreateWorktree(repo1, wtPath, "shared-branch"); err != nil {
			t.Fatalf("CreateWorktree: %v", err)
		}

		// Reuse from different repo should fail
		err := CreateWorktree(repo2, wtPath, "shared-branch")
		if err == nil {
			t.Fatal("expected error for repo mismatch reuse")
		}
		if !strings.Contains(err.Error(), "belongs to repo") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("existing branch", func(t *testing.T) {
		repo := initTestRepo(t)
		branchName := "existing-branch"

		// Create branch first
		cmd := exec.Command("git", "-C", repo, "branch", branchName)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git branch: %v\n%s", err, out)
		}

		wtPath := filepath.Join(t.TempDir(), "wt3")
		if err := CreateWorktree(repo, wtPath, branchName); err != nil {
			t.Fatalf("CreateWorktree with existing branch: %v", err)
		}

		branch, _ := CurrentBranch(wtPath)
		if branch != branchName {
			t.Errorf("expected %s, got %s", branchName, branch)
		}
	})
}

func TestRemoveWorktree(t *testing.T) {
	repo := initTestRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt-remove")

	if err := CreateWorktree(repo, wtPath, "remove-branch"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := RemoveWorktree(repo, wtPath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	if IsGitRepo(wtPath) {
		t.Error("worktree should no longer exist")
	}
}

func TestIsDirty(t *testing.T) {
	t.Run("clean repo", func(t *testing.T) {
		repo := initTestRepo(t)
		dirty, err := IsDirty(repo)
		if err != nil {
			t.Fatalf("IsDirty: %v", err)
		}
		if dirty {
			t.Error("expected clean repo")
		}
	})

	t.Run("dirty repo", func(t *testing.T) {
		repo := initTestRepo(t)
		os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("change"), 0644)

		dirty, err := IsDirty(repo)
		if err != nil {
			t.Fatalf("IsDirty: %v", err)
		}
		if !dirty {
			t.Error("expected dirty repo")
		}
	})

	t.Run("non-git dir returns error", func(t *testing.T) {
		dir := t.TempDir()
		_, err := IsDirty(dir)
		if err == nil {
			t.Error("expected error for non-git dir")
		}
	})
}

func TestSlug(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Takım Alfa", "takim-alfa"},
		{"Hello World", "hello-world"},
		{"Türkçe Karakter", "turkce-karakter"},
		{"agent/test", "agenttest"},
		{"My Agent!", "my-agent"},
		{"  spaces  ", "spaces"},
		{"UPPERCASE", "uppercase"},
		{"", "agent"},
		{"🚀 Rocket", "rocket"},
		{"çğıöşü", "cgiosu"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := Slug(tt.input)
			if got != tt.expected {
				t.Errorf("Slug(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
