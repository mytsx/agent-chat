package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// IsGitRepo checks if the given directory is inside a git repository.
func IsGitRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// toplevelDir returns the top-level directory of a git repository.
func toplevelDir(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CreateWorktree creates a git worktree at worktreePath branching from repoDir.
// If the worktree already exists with matching repo and branch, it is reused (idempotent).
// If it exists but belongs to a different repo or branch, an error is returned.
func CreateWorktree(repoDir, worktreePath, branchName string) error {
	// Check if worktree already exists at this path
	if IsGitRepo(worktreePath) {
		// Verify it belongs to the same repo
		repoTop, err := toplevelDir(repoDir)
		if err != nil {
			return fmt.Errorf("cannot determine repo toplevel: %w", err)
		}
		// For worktrees, commondir points back to the main repo's .git
		wtCommon := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-common-dir")
		wtOut, err := wtCommon.Output()
		if err != nil {
			return fmt.Errorf("cannot verify worktree origin: %w", err)
		}
		// Resolve the common dir to the repo toplevel
		commonDir := strings.TrimSpace(string(wtOut))
		// commonDir is typically /path/to/repo/.git — its parent is the repo toplevel
		commonParent := filepath.Dir(commonDir)
		if commonParent != repoTop {
			return fmt.Errorf("worktree at %s belongs to repo %s, expected %s", worktreePath, commonParent, repoTop)
		}

		// Verify branch matches
		currentBranch, err := CurrentBranch(worktreePath)
		if err != nil {
			return fmt.Errorf("cannot verify worktree branch: %w", err)
		}
		if currentBranch != branchName {
			return fmt.Errorf("worktree at %s is on branch %s, expected %s", worktreePath, currentBranch, branchName)
		}

		return nil // idempotent: verified match
	}

	// Check if branch already exists
	checkBranch := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", branchName)
	branchExists := checkBranch.Run() == nil

	var cmd *exec.Cmd
	if branchExists {
		cmd = exec.Command("git", "-C", repoDir, "worktree", "add", worktreePath, branchName)
	} else {
		cmd = exec.Command("git", "-C", repoDir, "worktree", "add", worktreePath, "-b", branchName)
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %w\n%s", err, string(out))
	}
	return nil
}

// RemoveWorktree removes a git worktree (non-force).
func RemoveWorktree(repoDir, worktreePath string) error {
	cmd := exec.Command("git", "-C", repoDir, "worktree", "remove", worktreePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, string(out))
	}
	return nil
}

// ForceRemoveWorktree removes a git worktree with --force (for explicit recovery).
func ForceRemoveWorktree(repoDir, worktreePath string) error {
	cmd := exec.Command("git", "-C", repoDir, "worktree", "remove", worktreePath, "--force")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove --force: %w\n%s", err, string(out))
	}
	return nil
}

// IsDirty checks if the given git directory has uncommitted changes.
// Returns (true, nil) if dirty, (false, nil) if clean, (false, err) on git errors.
func IsDirty(dir string) (bool, error) {
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// CurrentBranch returns the current branch name of the given git directory.
func CurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// slugRe matches characters that are not alphanumeric or hyphen.
var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

// turkishReplacer handles Turkish chars that NFD decomposition doesn't normalize.
var turkishReplacer = strings.NewReplacer(
	"ı", "i", "İ", "i",
	"ğ", "g", "Ğ", "g",
	"ş", "s", "Ş", "s",
)

// Slug converts a name to a git-safe branch segment.
// Spaces become hyphens, Turkish/accented chars become ASCII, lowercase.
func Slug(name string) string {
	// Handle Turkish chars that NFD can't decompose
	result := turkishReplacer.Replace(name)

	// NFD decompose → remove combining marks → NFC recompose
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	result, _, _ = transform.String(t, result)

	result = strings.ToLower(result)
	result = strings.ReplaceAll(result, " ", "-")
	result = slugRe.ReplaceAllString(result, "")

	// Trim leading/trailing hyphens
	result = strings.Trim(result, "-")
	if result == "" {
		result = "agent"
	}
	return result
}
