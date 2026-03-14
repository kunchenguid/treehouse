package git

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func FindRepoRoot() (string, error) {
	return runGit("", "rev-parse", "--show-toplevel")
}

func GetDefaultBranch(repoRoot string) (string, error) {
	// Resolve to the main repo if we're inside a worktree.
	mainRoot := repoRoot
	if dir, err := runGit(repoRoot, "rev-parse", "--git-common-dir"); err == nil {
		// --git-common-dir returns the .git dir of the main repo.
		// Derive the working tree root from it.
		if d, err2 := runGit(repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir"); err2 == nil {
			dir = d
		}
		gitSuffix := string(filepath.Separator) + ".git"
		if strings.HasSuffix(dir, gitSuffix) {
			mainRoot = strings.TrimSuffix(dir, gitSuffix)
		}
	}

	// Try remote HEAD first (most reliable when remote exists).
	if out, err := runGit(mainRoot, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		parts := strings.SplitN(out, "/", 4)
		if len(parts) >= 4 {
			return parts[3], nil
		}
	}

	// Fall back to the local HEAD of the main repo.
	if out, err := runGit(mainRoot, "symbolic-ref", "HEAD"); err == nil {
		// output is like "refs/heads/main"
		parts := strings.SplitN(out, "/", 3)
		if len(parts) >= 3 {
			return parts[2], nil
		}
	}

	// Fall back to git config init.defaultBranch.
	if out, err := runGit(mainRoot, "config", "init.defaultBranch"); err == nil && out != "" {
		return out, nil
	}

	return "", fmt.Errorf("cannot determine default branch: try running 'git fetch' or ensure you are on a branch")
}

func HasRemote(repoRoot, name string) bool {
	out, err := runGit(repoRoot, "remote")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

func GetRemoteURL(repoRoot string) (string, error) {
	return runGit(repoRoot, "remote", "get-url", "origin")
}

func refExists(repoRoot, ref string) bool {
	_, err := runGit(repoRoot, "rev-parse", "--verify", ref)
	return err == nil
}

// branchRef returns "origin/<branch>" if that ref exists,
// otherwise just "<branch>" for local-only or not-yet-pushed repos.
func branchRef(repoRoot, branch string) string {
	remote := "origin/" + branch
	if refExists(repoRoot, remote) {
		return remote
	}
	return branch
}

func AddWorktree(repoRoot, path, branch string) error {
	_, err := runGit(repoRoot, "worktree", "add", "--detach", path, branchRef(repoRoot, branch))
	return err
}

func RemoveWorktree(repoRoot, path string) error {
	_, err := runGit(repoRoot, "worktree", "remove", "--force", path)
	return err
}

func Fetch(repoRoot string) error {
	if !HasRemote(repoRoot, "origin") {
		return nil
	}
	_, err := runGit(repoRoot, "fetch", "origin")
	return err
}

func ResetWorktree(worktreePath, branch string) error {
	repoRoot, err := runGit(worktreePath, "rev-parse", "--show-toplevel")
	if err != nil {
		repoRoot = worktreePath
	}
	ref := branchRef(repoRoot, branch)
	if _, err := runGit(worktreePath, "checkout", "--detach", ref); err != nil {
		return err
	}
	_, err = runGit(worktreePath, "clean", "-fd")
	return err
}

func IsDirty(worktreePath string) (bool, error) {
	out, err := runGit(worktreePath, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

func ShortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:3])
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
