package gitvcs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateWorktreeRegistration verifies both directions of a linked worktree's
// registration without running Git against a potentially unrelated HEAD/index.
// A leftover .git file is not ownership proof: Git can reuse its registration
// name after an interrupted worktree add. Main checkouts have a .git directory.
func ValidateWorktreeRegistration(worktreePath string) error {
	_, err := worktreeGitDir(worktreePath)
	return err
}

func worktreeGitDir(worktreePath string) (string, error) {
	marker := filepath.Join(worktreePath, ".git")
	info, err := os.Stat(marker)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return marker, nil
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return "", err
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok || gitdir == "" {
		return "", fmt.Errorf("invalid .git registration in %s", worktreePath)
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(worktreePath, gitdir)
	}
	data, err = os.ReadFile(filepath.Join(gitdir, "gitdir"))
	if err != nil {
		return "", fmt.Errorf("reading worktree registration backlink for %s: %w", worktreePath, err)
	}
	backlink := strings.TrimSpace(string(data))
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(gitdir, backlink)
	}
	// Compare directory identities, not path spelling or marker inodes. Aliases
	// of the same slot are valid, but a copied or hard-linked .git file is not.
	worktreeInfo, err := os.Stat(worktreePath)
	if err != nil {
		return "", err
	}
	backlinkInfo, err := os.Stat(filepath.Dir(backlink))
	if err != nil {
		return "", fmt.Errorf("resolving worktree registration backlink for %s: %w", worktreePath, err)
	}
	if filepath.Base(backlink) != ".git" || !os.SameFile(worktreeInfo, backlinkInfo) {
		return "", fmt.Errorf("worktree registration for %s points back to %s, not this slot; inspect before reuse", worktreePath, backlink)
	}
	return gitdir, nil
}

// ExistingWorktreeLock checks validated per-worktree metadata without changing locks.
// HEAD.lock is left to the guarded reset, which recovers Treehouse-owned locks
// and reports anonymous ones.
func ExistingWorktreeLock(worktreePath string) (string, error) {
	gitdir, err := worktreeGitDir(worktreePath)
	if err != nil {
		return "", err
	}
	lockPath := filepath.Join(gitdir, "index.lock")
	if _, err := os.Lstat(lockPath); err == nil {
		return lockPath, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return "", nil
}
