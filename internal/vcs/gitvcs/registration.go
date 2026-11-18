package gitvcs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveWorktreeRegistration resolves the gitdir named by a linked worktree's
// .git file and the backlink recorded in that gitdir. A relative gitdir is
// read against the worktree and a relative backlink against the gitdir, as Git
// writes them, never against the process's working directory.
func resolveWorktreeRegistration(worktreePath, gitdir string) (string, string, error) {
	if gitdir == "" {
		return "", "", fmt.Errorf("empty gitdir registration")
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(worktreePath, gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	data, err := os.ReadFile(filepath.Join(gitdir, "gitdir"))
	if err != nil {
		return "", "", err
	}
	backlink := strings.TrimSpace(string(data))
	if backlink == "" {
		return "", "", fmt.Errorf("empty worktree registration backlink")
	}
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(gitdir, backlink)
	}
	return gitdir, filepath.Clean(backlink), nil
}

// ValidateWorktreeRegistration verifies both directions of a linked worktree's
// registration without running Git against a potentially unrelated HEAD/index.
// A leftover .git file is not ownership proof: Git can reuse its registration
// name after an interrupted worktree add. Main checkouts have a .git directory.
func ValidateWorktreeRegistration(worktreePath string) error {
	marker := filepath.Join(worktreePath, ".git")
	info, err := os.Stat(marker)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return err
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok || gitdir == "" {
		return fmt.Errorf("invalid .git registration in %s", worktreePath)
	}
	_, backlink, err := resolveWorktreeRegistration(worktreePath, gitdir)
	if err != nil {
		return fmt.Errorf("reading worktree registration backlink for %s: %w", worktreePath, err)
	}
	// Compare directory identities, not path spelling or marker inodes. Aliases
	// of the same slot are valid, but a copied or hard-linked .git file is not.
	worktreeInfo, err := os.Stat(worktreePath)
	if err != nil {
		return err
	}
	backlinkInfo, err := os.Stat(filepath.Dir(backlink))
	if err != nil {
		return fmt.Errorf("resolving worktree registration backlink for %s: %w", worktreePath, err)
	}
	if filepath.Base(backlink) != ".git" || !os.SameFile(worktreeInfo, backlinkInfo) {
		return fmt.Errorf("worktree registration for %s points back to %s, not this slot; inspect before reuse", worktreePath, backlink)
	}
	return nil
}
