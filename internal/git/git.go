package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	gitCommandTimeout   = 2 * time.Minute
	gitCommandWaitDelay = 250 * time.Millisecond
)

// FindMainRepoRoot returns the main repository root for the current working
// directory. Inside a linked worktree it resolves back to the owning
// repository, so pool resolution is stable no matter where a command runs.
func FindMainRepoRoot() (string, error) {
	return FindMainRepoRootFrom("")
}

func FindRepoRoot() (string, error) {
	return runGit("", "rev-parse", "--show-toplevel")
}

func FindRepoRootFrom(dir string) (string, error) {
	return runGit(dir, "rev-parse", "--show-toplevel")
}

// FindMainRepoRootFrom returns the main repository root for dir.
// For linked worktrees, it resolves the worktree root back to the owning
// repository.
func FindMainRepoRootFrom(dir string) (string, error) {
	repoRoot, err := FindRepoRootFrom(dir)
	if err != nil {
		return "", err
	}
	return mainRepoRoot(repoRoot), nil
}

func GetDefaultBranch(repoRoot string) (string, error) {
	mainRoot := mainRepoRoot(repoRoot)

	// Try remote HEAD first (most reliable when remote exists).
	if HasRemote(mainRoot, "origin") {
		if out, err := runGit(mainRoot, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
			if branch, ok := strings.CutPrefix(out, "refs/remotes/origin/"); ok && branch != "" {
				return branch, nil
			}
		}
	}

	return getLocalDefaultBranch(mainRoot)
}

func mainRepoRoot(repoRoot string) string {
	mainRoot := repoRoot
	if dir, err := runGit(repoRoot, "rev-parse", "--git-common-dir"); err == nil {
		if d, err2 := runGit(repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir"); err2 == nil {
			dir = d
		}
		if root, ok := repoRootFromCommonGitDir(dir); ok {
			mainRoot = root
		}
	}
	return mainRoot
}

// CommonGitDir returns the absolute path to the repository's common git
// directory for the repo containing dir. For a linked worktree this is the
// shared .git of the main repository, so files such as info/exclude resolve to
// the single shared location git actually reads.
func CommonGitDir(dir string) (string, error) {
	out, err := runGit(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		// Older git without --path-format falls back to a possibly-relative path.
		out, err = runGit(dir, "rev-parse", "--git-common-dir")
		if err != nil {
			return "", err
		}
	}
	p := filepath.Clean(filepath.FromSlash(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p, nil
}

func repoRootFromCommonGitDir(dir string) (string, bool) {
	cleaned := filepath.Clean(filepath.FromSlash(dir))
	if filepath.Base(cleaned) != ".git" {
		return "", false
	}
	return filepath.Dir(cleaned), true
}

func getLocalDefaultBranch(mainRoot string) (string, error) {
	if out, err := runGit(mainRoot, "symbolic-ref", "HEAD"); err == nil {
		if branch, ok := strings.CutPrefix(out, "refs/heads/"); ok && branch != "" {
			return branch, nil
		}
	}

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

// branchRef returns whichever of the local branch or remote-tracking branch is
// further ahead. If they have diverged (neither is an ancestor of the other),
// it prefers origin. Falls back to whichever ref exists.
func branchRef(repoRoot, branch string) string {
	local := "refs/heads/" + branch
	remote := remoteTrackingRef("origin", branch)
	hasLocal := refExists(repoRoot, local)
	hasRemote := refExists(repoRoot, remote)

	switch {
	case hasLocal && hasRemote:
		// If local is ancestor of remote, remote is ahead (or equal).
		if isAncestor(repoRoot, local, remote) {
			return remote
		}
		// Otherwise local is ahead or they diverged; prefer local when
		// it's strictly ahead, prefer remote on divergence.
		if isAncestor(repoRoot, remote, local) {
			return branch
		}
		return remote
	case hasLocal:
		return branch
	default:
		return remote
	}
}

func remoteTrackingRef(remote, branch string) string {
	return "refs/remotes/" + remote + "/" + branch
}

// isAncestor returns true if ref a is an ancestor of (or equal to) ref b.
func isAncestor(repoRoot, a, b string) bool {
	_, err := runGit(repoRoot, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

func AddWorktree(repoRoot, path, branch string) error {
	_, err := runGit(repoRoot, "worktree", "add", "--detach", path, branchRef(repoRoot, branch))
	return err
}

// PruneWorktrees removes git worktree bookkeeping for worktrees whose
// directories no longer exist. It is safe by design: git only deletes
// registrations for already-missing directories and never touches live
// worktrees or their data.
func PruneWorktrees(repoRoot string) error {
	_, err := runGit(repoRoot, "worktree", "prune")
	return err
}

func RemoveWorktree(repoRoot, path string) error {
	_, err := runGit(repoRoot, "worktree", "remove", "--force", path)
	return err
}

// RemoveCleanWorktree removes a clean git worktree without forcing deletion.
func RemoveCleanWorktree(repoRoot, path string) error {
	_, err := runGit(repoRoot, "worktree", "remove", path)
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
	if _, err := runGit(worktreePath, "checkout", "--detach", "--force", ref); err != nil {
		return err
	}
	if _, err := runGit(worktreePath, "reset", "--hard", ref); err != nil {
		return err
	}
	_, err = runGit(worktreePath, "clean", "-fd")
	return err
}

func DetachWorktree(worktreePath string) error {
	_, err := runGit(worktreePath, "checkout", "--detach")
	return err
}

// DefaultBranchMergeRef returns the fully qualified ref used for merge safety checks.
// Repositories with origin use the current remote default tracking ref and fail
// closed if that local tracking ref does not match remote HEAD; local-only
// repositories use the local default branch ref.
func DefaultBranchMergeRef(repoRoot string) (string, error) {
	if HasRemote(repoRoot, "origin") {
		branch, sha, err := remoteDefaultBranch(repoRoot, "origin")
		if err != nil {
			return "", err
		}
		ref := remoteTrackingRef("origin", branch)
		localSHA, err := refCommit(repoRoot, ref)
		if err != nil {
			return "", fmt.Errorf("%s is unavailable", ref)
		}
		if localSHA != sha {
			return "", fmt.Errorf("%s is stale: expected %s, got %s", ref, sha, localSHA)
		}
		return ref, nil
	}

	branch, err := GetDefaultBranch(repoRoot)
	if err != nil {
		return "", err
	}
	ref := "refs/heads/" + branch
	if _, err := refCommit(repoRoot, ref); err != nil {
		return "", fmt.Errorf("%s is unavailable", ref)
	}
	return ref, nil
}

func refCommit(repoRoot, ref string) (string, error) {
	return runGit(repoRoot, "rev-parse", "--verify", ref+"^{commit}")
}

func remoteDefaultBranch(repoRoot, remote string) (string, string, error) {
	out, err := runGit(repoRoot, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", "", err
	}
	var branch string
	var sha string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			if value, ok := strings.CutPrefix(fields[1], "refs/heads/"); ok {
				branch = value
			}
			continue
		}
		if len(fields) == 2 && fields[1] == "HEAD" {
			sha = fields[0]
		}
	}
	if branch == "" {
		return "", "", fmt.Errorf("cannot determine %s default branch", remote)
	}
	if sha == "" {
		return "", "", fmt.Errorf("cannot determine %s default branch commit", remote)
	}
	return branch, sha, nil
}

// IsHeadMergedIntoDefault reports whether HEAD is merged into DefaultBranchMergeRef.
func IsHeadMergedIntoDefault(repoRoot, worktreePath string) (bool, string, error) {
	ref, err := DefaultBranchMergeRef(repoRoot)
	if err != nil {
		return false, "", err
	}

	merged, err := IsHeadMergedIntoRef(worktreePath, ref)
	return merged, ref, err
}

// IsHeadMergedIntoRef reports whether worktreePath's HEAD is merged into ref.
// Ancestry is the fast proof; when it is absent, a path-scoped tree comparison
// detects a squash merge without treating unrelated target-branch changes as a
// mismatch.
func IsHeadMergedIntoRef(worktreePath, ref string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()

	return isHeadMergedIntoRefContext(ctx, worktreePath, ref)
}

func isHeadMergedIntoRefContext(ctx context.Context, worktreePath, ref string) (bool, error) {
	args := []string{"merge-base", "--is-ancestor", "HEAD", ref}
	out, err := gitCommandContext(ctx, worktreePath, args...).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false, gitTimeoutError(worktreePath, args)
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return isHeadContentMergedIntoRefContext(ctx, worktreePath, ref)
	}
	return false, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
}

func isHeadContentMergedIntoRefContext(ctx context.Context, worktreePath, ref string) (bool, error) {
	args := []string{"merge-base", "HEAD", ref}
	out, err := gitCommandContext(ctx, worktreePath, args...).CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, gitTimeoutError(worktreePath, args)
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, fmt.Errorf("git merge-base HEAD %s returned no common ancestor", ref)
		}
		return false, fmt.Errorf("git merge-base HEAD %s: %s", ref, strings.TrimSpace(string(out)))
	}
	base := strings.TrimSpace(string(out))
	if base == "" {
		return false, fmt.Errorf("git merge-base HEAD %s returned no common ancestor", ref)
	}

	baseTree, err := readTreeContext(ctx, worktreePath, base)
	if err != nil {
		return false, err
	}
	headTree, err := readTreeContext(ctx, worktreePath, "HEAD")
	if err != nil {
		return false, err
	}
	targetTree, err := readTreeContext(ctx, worktreePath, ref)
	if err != nil {
		return false, err
	}

	hasDelta := false
	for path, baseEntry := range baseTree {
		if baseEntry == headTree[path] {
			continue
		}
		hasDelta = true
		if headTree[path] != targetTree[path] {
			return false, nil
		}
	}
	for path, headEntry := range headTree {
		if _, ok := baseTree[path]; ok {
			continue
		}
		hasDelta = true
		if headEntry != targetTree[path] {
			return false, nil
		}
	}
	if !hasDelta {
		return false, nil
	}
	return true, nil
}

func readTreeContext(ctx context.Context, repoRoot, ref string) (map[string]string, error) {
	out, err := runGitRawContext(ctx, repoRoot, "ls-tree", "-r", "-z", "--full-tree", ref)
	if err != nil {
		return nil, err
	}

	tree := make(map[string]string)
	for len(out) > 0 {
		end := bytes.IndexByte(out, 0)
		if end == -1 {
			return nil, fmt.Errorf("git ls-tree %s returned malformed NUL-delimited output", ref)
		}
		record := out[:end]
		out = out[end+1:]
		separator := bytes.IndexByte(record, '\t')
		if separator == -1 || separator == len(record)-1 {
			return nil, fmt.Errorf("git ls-tree %s returned malformed tree entry", ref)
		}
		path := string(record[separator+1:])
		if _, exists := tree[path]; exists {
			return nil, fmt.Errorf("git ls-tree %s returned duplicate path %q", ref, path)
		}
		tree[path] = string(record[:separator])
	}
	return tree, nil
}

// IsDirty reports tracked or untracked changes, ignoring status.showUntrackedFiles.
func IsDirty(worktreePath string) (bool, error) {
	out, err := runGit(worktreePath, "status", "--porcelain", "--untracked-files=all")
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
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()

	return runGitContext(ctx, dir, args...)
}

func runGitContext(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := runGitRawContext(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runGitRaw keeps upstream's byte-returning entry point but routes it through
// the same timeout budget as runGit, so the ls-tree caller cannot hang either.
func runGitRaw(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()

	return runGitRawContext(ctx, dir, args...)
}

func runGitRawContext(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, err := gitCommandContext(ctx, dir, args...).Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, gitTimeoutError(dir, args)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

func gitCommandContext(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = gitCommandWaitDelay
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd
}

func gitTimeoutError(dir string, args []string) error {
	workingDir := dir
	if workingDir == "" {
		if currentDir, err := os.Getwd(); err == nil {
			workingDir = currentDir
		} else {
			workingDir = "."
		}
	}
	return fmt.Errorf(
		"git %s timed out in \"%s\"; check for a stale index lock (locate it with 'git rev-parse --git-path index.lock'), blocked credential prompts, or network connectivity",
		strings.Join(args, " "),
		workingDir,
	)
}
