package gitvcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// defaultGitCommandTimeout bounds metadata commands, whose runtime does
	// not scale with repository size or network conditions.
	defaultGitCommandTimeout = 2 * time.Minute
	// defaultGitLongCommandTimeout bounds commands whose legitimate runtime
	// does scale that way (fetching, and writing out a working tree through
	// smudge/LFS filters). They stay bounded so a stalled command still
	// surfaces, but generously enough that a merely slow one is not killed
	// part-way through creating a worktree.
	defaultGitLongCommandTimeout = 30 * time.Minute
	gitCommandWaitDelay          = 250 * time.Millisecond

	gitTimeoutEnv     = "TREEHOUSE_GIT_TIMEOUT"
	gitLongTimeoutEnv = "TREEHOUSE_GIT_LONG_TIMEOUT"

	// worktreeInitializingLock is the lock reason git writes while
	// "git worktree add" is creating a worktree.
	worktreeInitializingLock = "initializing"
)

var longRunningGitCommands = map[string]bool{
	"checkout":  true,
	"clean":     true,
	"fetch":     true,
	"ls-remote": true,
	"read-tree": true,
}

var longRunningGitSubcommands = map[string]map[string]bool{
	"worktree": {"add": true, "remove": true},
}

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
//
// Both are returned fully qualified. A bare branch name would let git's
// disambiguation pick refs/tags/<name>, which it ranks above refs/heads/<name>,
// and silently cut the worktree from a same-named tag.
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
			return local
		}
		return remote
	case hasLocal:
		return local
	default:
		return remote
	}
}

// BranchExists reports whether branch names refs/heads/<branch> or
// refs/remotes/origin/<branch>, the two refs branchRef chooses between.
//
// It looks the refs up EXACTLY rather than through rev-parse --verify, which
// resolves revision expressions: refs/heads/<b>^, ~3 and @{0} all verify under
// rev-parse, so the prefixes alone would accept a pinned commit as a base and
// persist the expression as the slot's recorded base. HEAD is rejected by name
// because git clone writes refs/remotes/origin/HEAD. An unreadable repository
// reports every branch as missing, which fails closed.
func BranchExists(repoRoot, branch string) bool {
	if branch == "" || branch == "HEAD" {
		return false
	}
	return exactRefExists(repoRoot, "refs/heads/"+branch) ||
		exactRefExists(repoRoot, remoteTrackingRef("origin", branch))
}

func exactRefExists(repoRoot, ref string) bool {
	_, err := runGit(repoRoot, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// BranchMergeRef returns the fully qualified ref merge-safety checks should
// compare against for branch, or "" when branch names no local or
// remote-tracking branch.
func BranchMergeRef(repoRoot, branch string) string {
	if !BranchExists(repoRoot, branch) {
		return ""
	}
	return branchRef(repoRoot, branch)
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

// PruneWorktreeAt prunes stale worktree bookkeeping like PruneWorktrees and,
// in addition, clears the registration for path when an interrupted
// "git worktree add" left git's own lock behind. Git locks a worktree while
// it creates it and unlocks it only on success, and "git worktree prune"
// silently skips every locked registration, so a timed-out or crashed add
// leaves the path registered forever and later adds fail with "is a missing
// but locked worktree". Only git's own initializing lock is cleared, and only
// while the worktree directory is gone, so a lock a user took (for example on
// a worktree stored on removable media) is never disturbed.
func PruneWorktreeAt(repoRoot, path string) error {
	if err := PruneWorktrees(repoRoot); err != nil {
		return err
	}
	locked, err := initializingLockedWorktree(repoRoot, path)
	if err != nil || locked == "" {
		return err
	}
	if _, err := runGit(repoRoot, "worktree", "unlock", locked); err != nil {
		return err
	}
	return PruneWorktrees(repoRoot)
}

// initializingLockedWorktree returns git's own spelling of path when it is
// still registered, gone from disk, and locked with the reason git writes
// while creating a worktree. It returns an empty path in every other case,
// including a creation still in flight (its directory already exists).
func initializingLockedWorktree(repoRoot, path string) (string, error) {
	out, err := runGit(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	want := resolveDeepestExisting(path)
	current := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if registered, ok := strings.CutPrefix(line, "worktree "); ok {
			current = registered
			continue
		}
		if line != "locked "+worktreeInitializingLock || current == "" {
			continue
		}
		if !samePath(resolveDeepestExisting(current), want) {
			continue
		}
		if _, err := os.Stat(current); err == nil {
			return "", nil
		}
		return current, nil
	}
	return "", nil
}

// resolveDeepestExisting canonicalizes p for comparison with the paths git
// prints, which are symlink-resolved. The worktree is already gone by the
// time this runs, so it resolves the deepest ancestor that still exists and
// re-appends the rest: a component that does not exist cannot be a symlink.
func resolveDeepestExisting(p string) string {
	abs, err := filepath.Abs(filepath.FromSlash(p))
	if err != nil {
		return filepath.Clean(filepath.FromSlash(p))
	}
	rest := ""
	for cur := abs; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// samePath compares two already canonicalized paths, honoring the
// case-insensitive file systems Windows uses.
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
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
	ref, err := resolveResetRef(worktreePath, branch)
	if err != nil {
		return err
	}
	head, err := worktreeHead(worktreePath)
	if err != nil {
		return err
	}
	return ResetWorktreeToRef(worktreePath, ref, head, false)
}

// ResetWorktreeToRef resets worktreePath to an already resolved commit.
// expectedHead is the worktree HEAD recorded at check time.
//
// The re-read and the destructive update run while holding git's own
// HEAD.lock (O_CREAT|O_EXCL). Concurrent git processes that would change
// HEAD (commit, checkout, merge, rebase) cannot create that lock, so they
// cannot sneak a new commit in after the comparison. When requireClean is
// set, dirtiness is re-checked under that lock before read-tree/clean, so
// uncommitted file or index changes that landed after the caller's dirty
// check are not overwritten. The worktree is updated with read-tree/clean,
// which do not need HEAD.lock; HEAD itself is committed by renaming the
// lock file onto HEAD, the same protocol git uses.
func ResetWorktreeToRef(worktreePath, ref, expectedHead string, requireClean bool) error {
	if !isCommitID(expectedHead) || !isCommitID(ref) {
		return fmt.Errorf("worktree reset requires resolved commit IDs")
	}
	headPath, err := gitPath(worktreePath, "HEAD")
	if err != nil {
		return err
	}
	lockPath := headPath + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		return fmt.Errorf("cannot lock worktree HEAD: %w", err)
	}
	held := true
	defer func() {
		_ = lf.Close()
		if held {
			_ = os.Remove(lockPath)
		}
	}()

	head, err := worktreeHead(worktreePath)
	if err != nil {
		return err
	}
	if head != expectedHead {
		return fmt.Errorf("worktree HEAD changed since safety check: was %s, now %s", expectedHead, head)
	}
	if requireClean {
		dirty, err := IsDirty(worktreePath)
		if err != nil {
			return err
		}
		if dirty {
			return fmt.Errorf("worktree became dirty after safety check")
		}
	}

	if _, err := runGit(worktreePath, "read-tree", "--reset", "-u", ref); err != nil {
		return err
	}
	if _, err := runGit(worktreePath, "clean", "-fd"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(lf, "%s\n", ref); err != nil {
		return err
	}
	if err := lf.Sync(); err != nil {
		return err
	}
	if err := lf.Close(); err != nil {
		return err
	}
	if err := replaceFile(lockPath, headPath); err != nil {
		return err
	}
	held = false
	return nil
}

func resolveResetRef(worktreePath, branch string) (string, error) {
	repoRoot, err := runGit(worktreePath, "rev-parse", "--show-toplevel")
	if err != nil {
		repoRoot = worktreePath
	}
	ref := branchRef(repoRoot, branch)
	return refCommit(worktreePath, ref)
}

func worktreeHead(worktreePath string) (string, error) {
	return runGit(worktreePath, "rev-parse", "--verify", "HEAD^{commit}")
}

func gitPath(worktreePath, name string) (string, error) {
	out, err := runGit(worktreePath, "rev-parse", "--path-format=absolute", "--git-path", name)
	if err == nil {
		return filepath.Clean(filepath.FromSlash(out)), nil
	}
	gitDir, dirErr := runGit(worktreePath, "rev-parse", "--absolute-git-dir")
	if dirErr != nil {
		return "", err
	}
	rel, relErr := runGit(worktreePath, "rev-parse", "--git-path", name)
	if relErr != nil {
		return "", err
	}
	p := filepath.FromSlash(rel)
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.FromSlash(gitDir), p)
	}
	return filepath.Clean(p), nil
}

func isCommitID(s string) bool {
	n := len(s)
	if n != 40 && n != 64 {
		return false
	}
	for i := 0; i < n; i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// IsWorktreeSafeToReset reports whether worktreePath can be reset to branch
// without discarding committed work and returns the immutable reset target and
// the worktree HEAD recorded at check time. Callers must pass both to
// ResetWorktreeToRef so verification and reset share one target and a later
// HEAD change is refused. The check fails closed when the target or HEAD
// cannot be resolved.
func IsWorktreeSafeToReset(worktreePath, branch string) (bool, string, string, error) {
	ref, err := resolveResetRef(worktreePath, branch)
	if err != nil {
		return false, "", "", err
	}
	head, err := worktreeHead(worktreePath)
	if err != nil {
		return false, "", "", err
	}
	safe, err := IsHeadMergedIntoRef(worktreePath, ref)
	return safe, ref, head, err
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
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeoutFor("merge-base"))
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
	return false, gitCombinedOutputError(worktreePath, args, out, err)
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
		return false, gitCombinedOutputError(worktreePath, args, out, err)
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

func runGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeoutFor(args...))
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

func runGitRawContext(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, err := gitCommandContext(ctx, dir, args...).Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, gitTimeoutError(dir, args)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		if errors.Is(err, exec.ErrWaitDelay) {
			return nil, fmt.Errorf(
				"git %s in %q left its output pipe open past %s, most likely held by a background helper (fsmonitor, credential cache, or a smudge/clean filter): %w",
				strings.Join(args, " "),
				gitWorkingDir(dir),
				gitCommandWaitDelay,
				err,
			)
		}
		return nil, fmt.Errorf("git %s in %q: %w", strings.Join(args, " "), gitWorkingDir(dir), err)
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
	return fmt.Errorf(
		"git %s timed out in \"%s\"; check for a stale index lock (locate it with 'git rev-parse --git-path index.lock'), blocked credential prompts, or network connectivity. Raise %s if this repository legitimately needs longer",
		strings.Join(args, " "),
		gitWorkingDir(dir),
		gitTimeoutEnvFor(args...),
	)
}

// gitCombinedOutputError reports a CombinedOutput failure. git's own message
// is the best diagnostic when it produced one; otherwise the subcommand, the
// working directory, and the underlying exec error keep the failure
// identifiable instead of collapsing to an empty suffix.
func gitCombinedOutputError(dir string, args []string, out []byte, err error) error {
	detail := strings.TrimSpace(string(out))
	if detail != "" {
		if _, isExit := err.(*exec.ExitError); isExit {
			return fmt.Errorf("git %s: %s", strings.Join(args, " "), detail)
		}
		return fmt.Errorf("git %s in %q: %w: %s", strings.Join(args, " "), gitWorkingDir(dir), err, detail)
	}
	return fmt.Errorf("git %s in %q: %w", strings.Join(args, " "), gitWorkingDir(dir), err)
}

func gitWorkingDir(dir string) string {
	if dir != "" {
		return dir
	}
	if currentDir, err := os.Getwd(); err == nil {
		return currentDir
	}
	return "."
}

// gitCommandTimeoutFor resolves the deadline for a git invocation. Commands
// whose runtime scales with repository size or network conditions get the
// longer budget, and either budget can be overridden for repositories that
// legitimately need more time.
func gitCommandTimeoutFor(args ...string) time.Duration {
	if isLongRunningGitCommand(args) {
		return gitTimeoutFromEnv(gitLongTimeoutEnv, defaultGitLongCommandTimeout)
	}
	return gitTimeoutFromEnv(gitTimeoutEnv, defaultGitCommandTimeout)
}

func gitTimeoutEnvFor(args ...string) string {
	if isLongRunningGitCommand(args) {
		return gitLongTimeoutEnv
	}
	return gitTimeoutEnv
}

func isLongRunningGitCommand(args []string) bool {
	command, subcommand := gitSubcommand(args)
	if longRunningGitCommands[command] {
		return true
	}
	return longRunningGitSubcommands[command][subcommand]
}

// gitSubcommand skips leading global options ("-c foo=bar", "--exec-path=...")
// so classification sees the actual command.
func gitSubcommand(args []string) (string, string) {
	command := ""
	subcommand := ""
	skipValue := false
	for _, arg := range args {
		if skipValue {
			skipValue = false
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if arg == "-c" || arg == "-C" {
				skipValue = true
			}
			continue
		}
		if command == "" {
			command = arg
			continue
		}
		subcommand = arg
		break
	}
	return command, subcommand
}

func gitTimeoutFromEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout <= 0 {
		warnOnce(name+"="+raw, fmt.Sprintf("🌳 Warning: ignoring invalid %s=%q; using %s\n", name, raw, fallback))
		return fallback
	}
	return timeout
}

var warnedTimeoutValues sync.Map

func warnOnce(key, message string) {
	if _, seen := warnedTimeoutValues.LoadOrStore(key, struct{}{}); seen {
		return
	}
	fmt.Fprint(os.Stderr, message)
}

// Backend adapts this package's functions to the vcs.Backend interface. All
// methods delegate to the package-level implementations so behavior is
// identical whether callers use the interface or the functions directly.
type Backend struct{}

// New returns the git backend.
func New() *Backend { return &Backend{} }

func (*Backend) Name() string { return "git" }

func (*Backend) FindRepoRootFrom(dir string) (string, error)     { return FindRepoRootFrom(dir) }
func (*Backend) FindMainRepoRootFrom(dir string) (string, error) { return FindMainRepoRootFrom(dir) }
func (*Backend) GetDefaultBranch(repoRoot string) (string, error) {
	return GetDefaultBranch(repoRoot)
}
func (*Backend) CommonGitDir(dir string) (string, error)      { return CommonGitDir(dir) }
func (*Backend) HasRemote(repoRoot, name string) bool         { return HasRemote(repoRoot, name) }
func (*Backend) GetRemoteURL(repoRoot string) (string, error) { return GetRemoteURL(repoRoot) }
func (*Backend) AddWorktree(repoRoot, path, branch string) error {
	return AddWorktree(repoRoot, path, branch)
}
func (*Backend) PruneWorktrees(repoRoot string) error { return PruneWorktrees(repoRoot) }
func (*Backend) PruneWorktreeAt(repoRoot, path string) error {
	return PruneWorktreeAt(repoRoot, path)
}
func (*Backend) RemoveWorktree(repoRoot, path string) error {
	return RemoveWorktree(repoRoot, path)
}
func (*Backend) RemoveCleanWorktree(repoRoot, path string) error {
	return RemoveCleanWorktree(repoRoot, path)
}
func (*Backend) Fetch(repoRoot string) error { return Fetch(repoRoot) }
func (*Backend) ResetWorktree(worktreePath, branch string) error {
	return ResetWorktree(worktreePath, branch)
}
func (*Backend) ResetWorktreeToRef(worktreePath, ref, expectedHead string, requireClean bool) error {
	return ResetWorktreeToRef(worktreePath, ref, expectedHead, requireClean)
}
func (*Backend) IsWorktreeSafeToReset(worktreePath, branch string) (bool, string, string, error) {
	return IsWorktreeSafeToReset(worktreePath, branch)
}
func (*Backend) DetachWorktree(worktreePath string) error { return DetachWorktree(worktreePath) }
func (*Backend) DefaultBranchMergeRef(repoRoot string) (string, error) {
	return DefaultBranchMergeRef(repoRoot)
}
func (*Backend) IsHeadMergedIntoRef(worktreePath, ref string) (bool, error) {
	return IsHeadMergedIntoRef(worktreePath, ref)
}
func (*Backend) IsDirty(worktreePath string) (bool, error) { return IsDirty(worktreePath) }

// IsOriginAccessError reports whether err reads like a failure to reach or
// use the origin remote, in git's own vocabulary. The patterns lived in the
// pool package before backends existed; they are owned here now so pool code
// never string-matches one backend's errors.
func IsOriginAccessError(err error) bool {
	if err == nil {
		return false
	}
	detail := err.Error()
	return strings.Contains(detail, "git ls-remote") ||
		strings.Contains(detail, "Could not read from remote repository") ||
		strings.Contains(detail, "does not appear to be a git repository") ||
		strings.Contains(detail, "repository") && strings.Contains(detail, "not found")
}
