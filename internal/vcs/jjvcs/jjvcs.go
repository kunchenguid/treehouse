// Package jjvcs implements the vcs backend for Jujutsu (jj) repositories
// using jj workspaces as the worktree primitive.
//
// Mapping from the git backend's semantics:
//
//   - worktree                -> jj workspace (jj workspace add / forget)
//   - detached HEAD           -> nothing: jj working copies are anonymous
//   - dirty                   -> the working-copy commit @ is non-empty or
//     has a description
//   - reset to default branch -> jj abandon -r @ && jj new <ref>
//     (recoverable via jj op restore, unlike git reset --hard)
//   - merged into ref         -> revset "@- & ~::<ref>" is empty
//
// Known, documented constraints:
//
//   - Secondary jj workspaces are not colocated: pooled worktrees contain
//     .jj but no .git, so only jj commands work inside them.
//   - jj repositories do not record workspace directory paths, so
//     PruneWorktrees cannot enumerate stale registrations; AddWorktree
//     self-heals a stale same-path registration by forgetting its name
//     first. Workspace names are a 128-bit digest of the absolute path, so
//     distinct paths never share a name and the forget can never deregister
//     a live workspace at another path.
//   - Merge detection uses ancestry only. A squash-merged head is reported
//     as unmerged, which fails safe: lifecycle commands refuse to delete it.
package jjvcs

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/treehouse/internal/vcs/gitvcs"
)

// Backend implements the vcs.Backend interface for jj repositories.
type Backend struct{}

var prepareJJSeededCleanup = gitvcs.PrepareJJSeededCleanup

// New returns the jj backend.
func New() *Backend { return &Backend{} }

func (*Backend) Name() string { return "jj" }

// defaultBranchCandidates mirrors jj's own trunk() preference order.
var defaultBranchCandidates = []string{"main", "master", "trunk"}

// FindRepoRootFrom returns the root of the jj workspace containing dir. An
// empty dir means the current working directory.
func (*Backend) FindRepoRootFrom(dir string) (string, error) {
	root, err := runJJ(dir, "workspace", "root")
	if err != nil {
		return "", err
	}
	// jj prints a physical path today, but its output form is not
	// contractual; canonicalize so every root-resolution route agrees.
	return canonicalize(root), nil
}

// FindMainRepoRootFrom resolves dir to the main repository root. For a
// secondary workspace the .jj/repo file holds the path (possibly relative to
// the workspace's .jj directory) of the main repository's store.
func (b *Backend) FindMainRepoRootFrom(dir string) (string, error) {
	wsRoot, err := b.FindRepoRootFrom(dir)
	if err != nil {
		return "", err
	}
	return MainRootFromWorkspaceRoot(wsRoot)
}

// MainRootFromWorkspaceRoot resolves a workspace root to its main repository
// root by reading the .jj/repo store pointer. It is pure file inspection (no
// jj invocation), so callers can use it to locate where a repository's
// configuration lives without selecting a backend first.
func MainRootFromWorkspaceRoot(wsRoot string) (string, error) {
	repoPath := filepath.Join(wsRoot, ".jj", "repo")
	info, err := os.Stat(repoPath)
	if err != nil {
		return "", fmt.Errorf("cannot inspect %s: %w", repoPath, err)
	}
	if info.IsDir() {
		// The store lives here: this is the main workspace.
		return canonicalize(wsRoot), nil
	}
	contents, err := os.ReadFile(repoPath)
	if err != nil {
		return "", err
	}
	storePath := strings.TrimSpace(string(contents))
	if !filepath.IsAbs(storePath) {
		storePath = filepath.Join(wsRoot, ".jj", storePath)
	}
	// storePath is <main>/.jj/repo; the main root is two levels up.
	// Canonicalized on read as well as on write, so pointers written before
	// canonicalization existed still resolve to the same pool identity as
	// `jj workspace root`.
	return canonicalize(filepath.Clean(filepath.Join(storePath, "..", ".."))), nil
}

// GetDefaultBranch returns the default bookmark name, preferring remote
// bookmarks on origin and falling back to local bookmarks. Bare trunk() is
// deliberately not used: it silently resolves to the root commit when no
// remote bookmarks exist.
func (b *Backend) GetDefaultBranch(repoRoot string) (string, error) {
	if b.HasRemote(repoRoot, "origin") {
		for _, name := range defaultBranchCandidates {
			if revsetNonEmpty(repoRoot, fmt.Sprintf("present(%s@origin)", name)) {
				return name, nil
			}
		}
	}
	for _, name := range defaultBranchCandidates {
		if revsetNonEmpty(repoRoot, fmt.Sprintf("present(%s)", name)) {
			return name, nil
		}
	}
	return "", fmt.Errorf("cannot determine default bookmark: expected one of %s locally or on origin; try 'jj git fetch' or 'jj bookmark create main'", strings.Join(defaultBranchCandidates, ", "))
}

// CommonGitDir returns the colocated .git directory of the main repository,
// which is where repo-local exclusions (info/exclude) live. Non-colocated jj
// repositories have no usable git dir, so callers degrade gracefully on error.
func (b *Backend) CommonGitDir(dir string) (string, error) {
	mainRoot, err := b.FindMainRepoRootFrom(dir)
	if err != nil {
		return "", err
	}
	gitDir := filepath.Join(mainRoot, ".git")
	if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
		return gitDir, nil
	}
	return "", fmt.Errorf("jj repository at %s is not colocated: no .git directory", mainRoot)
}

// HasRemote reports whether the named git remote exists.
func (*Backend) HasRemote(repoRoot, name string) bool {
	out, err := runJJ(repoRoot, "git", "remote", "list")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) >= 1 && fields[0] == name {
			return true
		}
	}
	return false
}

// GetRemoteURL returns the origin remote URL.
func (*Backend) GetRemoteURL(repoRoot string) (string, error) {
	out, err := runJJ(repoRoot, "git", "remote", "list")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "origin" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("no origin remote")
}

// AddWorktree creates a jj workspace at path based on branch. The workspace
// name is a collision-free digest of the absolute path, so a stale
// registration under that name can only have been left by a previous
// worktree at this same path and is forgotten before adding (jj repositories
// do not record workspace directories, so this is the prune equivalent for
// re-used paths). Like git worktrees, registrations are effectively
// path-keyed: a live workspace at a different path is never deregistered.
func (b *Backend) AddWorktree(repoRoot, path, branch string) error {
	name := workspaceNameFor(path)
	// Best-effort: forgetting a name that does not exist is a no-op in jj.
	_, _ = runJJ(repoRoot, "workspace", "forget", name)

	ref, err := branchRef(repoRoot, branch)
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := runJJ(repoRoot, "workspace", "add", "--name", name, "--revision", ref, absPath); err != nil {
		return err
	}
	return makeRepoPointerAbsolute(absPath)
}

func (*Backend) SeedWorktree(repoRoot, worktreePath string) ([]string, error) {
	head, err := worktreeHead(worktreePath)
	if err != nil {
		return nil, err
	}
	gitDir := filepath.Join(repoRoot, ".jj", "repo", "store", "git")
	seeded, err := gitvcs.SeedWorktreeWithInventoryFromGitStore(repoRoot, worktreePath, gitDir, head)
	if err != nil || len(seeded) == 0 {
		return seeded, err
	}
	if err := prepareJJSeededCleanup(worktreePath); err != nil {
		return seeded, err
	}
	return seeded, nil
}

// makeRepoPointerAbsolute rewrites the workspace's .jj/repo store pointer to
// an absolute, symlink-canonicalized path. jj writes a relative pointer,
// which breaks when the pool directory and the repository do not move
// together (the pool usually lives under ~/.treehouse, far from the repo).
// Canonicalizing on write mirrors git, which stores physical paths in a
// worktree's .git gitdir pointer: every later read of the pointer then
// agrees with `jj workspace root` (physical), so a repository reached
// through a symlinked path (such as macOS /tmp) resolves to one pool
// identity instead of forking into a real pool and a phantom one.
func makeRepoPointerAbsolute(wsRoot string) error {
	repoPath := filepath.Join(wsRoot, ".jj", "repo")
	info, err := os.Stat(repoPath)
	if err != nil || info.IsDir() {
		return err
	}
	contents, err := os.ReadFile(repoPath)
	if err != nil {
		return err
	}
	storePath := strings.TrimSpace(string(contents))
	if filepath.IsAbs(storePath) {
		return nil
	}
	abs := canonicalize(filepath.Clean(filepath.Join(wsRoot, ".jj", storePath)))
	return os.WriteFile(repoPath, []byte(abs), 0o644)
}

// canonicalize resolves symlinks in path, falling back to the input when
// resolution fails (for example when the path does not exist yet). Root
// resolution must return one canonical form no matter which route produced
// the path, because the pool identity is derived from the path string.
func canonicalize(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// workspaceNameFor derives a stable jj workspace name from a worktree path.
// The name embeds a 128-bit digest of the absolute path, making it unique
// per path for all practical purposes: the self-healing forget in
// AddWorktree can then only ever target this path's own stale registration.
func workspaceNameFor(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	h := sha256.Sum256([]byte(abs))
	return fmt.Sprintf("th-%x", h[:16])
}

// PruneWorktrees is a no-op for jj: the repository does not record workspace
// directory paths, so stale registrations cannot be enumerated here.
// AddWorktree forgets a same-path stale registration before re-adding.
func (*Backend) PruneWorktrees(repoRoot string) error { return nil }

// RemoveWorktree forgets the workspace and deletes its directory even if it
// has local changes.
func (*Backend) RemoveWorktree(repoRoot, path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Refuse to delete a directory that exists but is not a jj workspace:
	// removing a git worktree here would delete its files while leaving the
	// .git/worktrees registration stale. A path that is already gone still
	// gets its stale registration forgotten below. A main workspace (whose
	// .jj/repo is the repository store itself, not a pointer file) is refused
	// too: forgetting the digest-named workspace would no-op and RemoveAll
	// would delete the entire repository.
	if _, statErr := os.Stat(absPath); statErr == nil {
		if info, jjErr := os.Stat(filepath.Join(absPath, ".jj")); jjErr != nil || !info.IsDir() {
			return fmt.Errorf("refusing to remove %s: not a jj workspace", absPath)
		}
		if info, repoErr := os.Stat(filepath.Join(absPath, ".jj", "repo")); repoErr == nil && info.IsDir() {
			return fmt.Errorf("refusing to remove %s: main jj workspace, not a pooled secondary workspace", absPath)
		}
	}
	if err := gitvcs.RemoveJJSeedAuthentication(absPath); err != nil {
		return err
	}
	_, _ = runJJ(repoRoot, "workspace", "forget", workspaceNameFor(absPath))
	if err := os.RemoveAll(absPath); err != nil {
		return err
	}
	return nil
}

// RemoveCleanWorktree removes a workspace, refusing if it has local changes.
func (b *Backend) RemoveCleanWorktree(repoRoot, path string) error {
	dirty, err := b.IsDirty(path)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("workspace at %s has local changes", path)
	}
	return b.RemoveWorktree(repoRoot, path)
}

// Fetch updates refs from origin when an origin remote exists.
func (b *Backend) Fetch(repoRoot string) error {
	if !b.HasRemote(repoRoot, "origin") {
		return nil
	}
	_, err := runJJ(repoRoot, "git", "fetch", "--remote", "origin")
	return err
}

// ResetWorktree returns a workspace to a pristine, empty working copy on top
// of branch. The previous working-copy commit is abandoned, which discards
// its changes from view while remaining recoverable via jj op restore.
func (b *Backend) ResetWorktree(worktreePath, branch string) error {
	ref, err := resolveResetRef(worktreePath, branch)
	if err != nil {
		return err
	}
	head, err := worktreeHead(worktreePath)
	if err != nil {
		return err
	}
	return b.ResetWorktreeToRef(worktreePath, ref, head, false)
}

func (b *Backend) ResetWorktreeWithSeededPaths(worktreePath, branch string, seededPaths []string) error {
	if len(seededPaths) > 0 {
		if err := gitvcs.AuthenticateJJSeededCleanup(worktreePath); err != nil {
			return err
		}
	}
	ref, err := resolveResetRef(worktreePath, branch)
	if err != nil {
		return err
	}
	head, err := worktreeHead(worktreePath)
	if err != nil {
		return err
	}
	if err := b.ResetWorktreeToRef(worktreePath, ref, head, false); err != nil {
		return err
	}
	if seededPaths == nil {
		return nil
	}
	return gitvcs.RemoveSeededPathsFromJJWorkspace(worktreePath, seededPaths)
}

// ResetWorktreeToRef resets worktreePath to an already resolved commit.
// expectedHead is the working-copy commit recorded at check time.
//
// jj is lock-free: concurrent commands snapshot the operation log and always
// commit, so no flock or private lock can exclude a parallel `jj commit`.
// The destructive rewrite is therefore a single `jj rebase` whose revset is
// `@ & commit_id(expectedHead)`. That command loads one snapshot, so a
// concurrent change of @ makes the revset empty and the rebase a no-op. If
// the workspace is then not parked on the reset target, the reset is refused
// and the slot is skipped. When requireClean is set, dirtiness is re-checked
// before rebase/abandon so uncommitted working-copy changes that landed after
// the caller's dirty check are not discarded.
func (b *Backend) ResetWorktreeToRef(worktreePath, ref, expectedHead string, requireClean bool) error {

	// A sibling workspace may have moved the repo since this workspace was
	// last used; recover first so the commands below see current state.
	_, _ = runJJ(worktreePath, "workspace", "update-stale")

	if !isCommitID(expectedHead) {
		return fmt.Errorf("worktree reset requires a resolved working-copy commit")
	}

	revset := "@ & commit_id(\"" + expectedHead + "\")"
	dirty, err := b.IsDirty(worktreePath)
	if err != nil {
		return err
	}
	if dirty {
		if requireClean {
			return fmt.Errorf("worktree became dirty after safety check")
		}
	} else {
		if _, err := runJJ(worktreePath, "rebase", "-d", ref, "-r", revset); err != nil {
			if parked, perr := b.parkedOnRef(worktreePath, ref); perr == nil && parked {
				if head, herr := worktreeHead(worktreePath); herr == nil && head == expectedHead {
					return nil
				}
			}
			return err
		}
		parked, err := b.parkedOnRef(worktreePath, ref)
		if err != nil {
			return err
		}
		if !parked {
			return fmt.Errorf("worktree HEAD changed since safety check: was %s", expectedHead)
		}
		return nil
	}

	if _, err := runJJ(worktreePath, "abandon", "-r", revset); err != nil {
		return err
	}
	if revsetNonEmpty(worktreePath, revset) {
		return fmt.Errorf("worktree HEAD changed since safety check: was %s", expectedHead)
	}
	dirty, err = b.IsDirty(worktreePath)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("worktree HEAD changed since safety check: was %s", expectedHead)
	}
	merged, err := b.IsHeadMergedIntoRef(worktreePath, ref)
	if err != nil || !merged {
		return fmt.Errorf("worktree HEAD changed since safety check: was %s", expectedHead)
	}
	_, err = runJJ(worktreePath, "new", ref)
	return err
}

func (b *Backend) ResetWorktreeToRefWithSeededPaths(worktreePath, ref, expectedHead string, requireClean bool, seededPaths []string) error {
	if len(seededPaths) > 0 {
		if err := gitvcs.AuthenticateJJSeededCleanup(worktreePath); err != nil {
			return err
		}
	}
	if err := b.ResetWorktreeToRef(worktreePath, ref, expectedHead, requireClean); err != nil {
		return err
	}
	if seededPaths == nil {
		return nil
	}
	return gitvcs.RemoveSeededPathsFromJJWorkspace(worktreePath, seededPaths)
}

func (b *Backend) parkedOnRef(worktreePath, ref string) (bool, error) {
	dirty, err := b.IsDirty(worktreePath)
	if err != nil {
		return false, err
	}
	if dirty {
		return false, nil
	}
	parent, err := runJJ(worktreePath, "log", "-r", "@-", "--no-graph", "-T", `commit_id ++ "\n"`)
	if err != nil {
		return false, err
	}
	if i := strings.IndexByte(parent, '\n'); i >= 0 {
		parent = parent[:i]
	}
	return parent != "" && parent == ref, nil
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

func resolveResetRef(worktreePath, branch string) (string, error) {
	_, _ = runJJ(worktreePath, "workspace", "update-stale")
	ref, err := branchRef(worktreePath, branch)
	if err != nil {
		return "", err
	}
	target, err := runJJ(worktreePath, "log", "-r", ref, "--no-graph", "-T", `commit_id ++ "\n"`)
	if err != nil {
		return "", err
	}
	if i := strings.IndexByte(target, '\n'); i >= 0 {
		target = target[:i]
	}
	if target == "" {
		return "", fmt.Errorf("cannot resolve %s to a commit", ref)
	}
	return target, nil
}

func worktreeHead(worktreePath string) (string, error) {
	target, err := runJJ(worktreePath, "log", "-r", "@", "--no-graph", "-T", `commit_id ++ "\n"`)
	if err != nil {
		return "", err
	}
	if i := strings.IndexByte(target, '\n'); i >= 0 {
		target = target[:i]
	}
	if target == "" {
		return "", fmt.Errorf("cannot resolve working-copy commit")
	}
	return target, nil
}

// IsWorktreeSafeToReset reports whether worktreePath can be reset to branch
// without discarding committed work and returns the immutable reset target and
// the working-copy commit recorded at check time. Callers must pass both to
// ResetWorktreeToRef so verification and reset share one target and a later
// HEAD change is refused. The check fails closed when the target or HEAD
// cannot be resolved.
func (b *Backend) IsWorktreeSafeToReset(worktreePath, branch string) (bool, string, string, error) {
	ref, err := resolveResetRef(worktreePath, branch)
	if err != nil {
		return false, "", "", err
	}
	head, err := worktreeHead(worktreePath)
	if err != nil {
		return false, "", "", err
	}
	safe, err := b.IsHeadMergedIntoRef(worktreePath, ref)
	return safe, ref, head, err
}

// DetachWorktree is a no-op: jj working copies are anonymous commits and
// never hold a bookmark the way a git worktree holds a branch.
func (*Backend) DetachWorktree(worktreePath string) error { return nil }

// DefaultBranchMergeRef returns the revset merge-safety checks compare
// against: the origin-tracking default bookmark when origin exists (callers
// fetch first in lifecycle flows), otherwise the local default bookmark.
func (b *Backend) DefaultBranchMergeRef(repoRoot string) (string, error) {
	branch, err := b.GetDefaultBranch(repoRoot)
	if err != nil {
		return "", err
	}
	if b.HasRemote(repoRoot, "origin") {
		ref := branch + "@origin"
		if !revsetNonEmpty(repoRoot, fmt.Sprintf("present(%s)", ref)) {
			return "", fmt.Errorf("%s is unavailable", ref)
		}
		return ref, nil
	}
	if !revsetNonEmpty(repoRoot, fmt.Sprintf("present(%s)", branch)) {
		return "", fmt.Errorf("bookmark %s is unavailable", branch)
	}
	return branch, nil
}

// IsHeadMergedIntoRef reports whether every parent of the working-copy commit
// is an ancestor of ref. The working-copy commit itself is excluded: a clean
// @ is empty and undescribed, and a non-empty @ is already reported by
// IsDirty. Ancestry is the only proof used; squash-merged work is reported as
// unmerged, which fails safe for destructive callers.
func (*Backend) IsHeadMergedIntoRef(worktreePath, ref string) (bool, error) {
	out, err := runJJ(worktreePath, "log", "-r", fmt.Sprintf("@- & ~::(%s)", ref), "--no-graph", "-T", `commit_id ++ "\n"`)
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// IsDirty reports whether the working-copy commit has changes or a
// description. Running any jj command snapshots the working copy first, so
// filesystem changes are always reflected.
func (*Backend) IsDirty(worktreePath string) (bool, error) {
	out, err := runJJ(worktreePath, "log", "-r", "@", "--no-graph", "-T", `if(empty && description == "", "clean", "dirty")`)
	if err != nil {
		return false, err
	}
	return out != "clean", nil
}

// branchRef returns the revset to base new or reset worktrees on: whichever
// of the local bookmark or its origin counterpart is further ahead, matching
// the git backend's freshest-wins behavior. On divergence origin wins.
func branchRef(dir, branch string) (string, error) {
	local := branch
	remote := branch + "@origin"
	hasLocal := revsetNonEmpty(dir, fmt.Sprintf("present(%s)", local))
	hasRemote := revsetNonEmpty(dir, fmt.Sprintf("present(%s)", remote))

	switch {
	case hasLocal && hasRemote:
		if isAncestor(dir, local, remote) {
			return remote, nil
		}
		if isAncestor(dir, remote, local) {
			return local, nil
		}
		return remote, nil
	case hasLocal:
		return local, nil
	case hasRemote:
		return remote, nil
	default:
		return "", fmt.Errorf("bookmark %s not found locally or on origin", branch)
	}
}

// isAncestor reports whether revset a is an ancestor of (or equal to) b.
func isAncestor(dir, a, b string) bool {
	return revsetNonEmpty(dir, fmt.Sprintf("(%s) & ::(%s)", a, b))
}

// revsetNonEmpty reports whether the revset resolves to at least one commit.
func revsetNonEmpty(dir, revset string) bool {
	out, err := runJJ(dir, "log", "-r", revset, "--no-graph", "-T", `commit_id ++ "\n"`)
	return err == nil && out != ""
}

func runJJ(dir string, args ...string) (string, error) {
	fullArgs := append([]string{"--color", "never"}, args...)
	cmd := exec.Command("jj", fullArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("jj %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// IsOriginAccessError reports whether err reads like a failure to reach or
// use the origin remote, in jj's vocabulary. jj shells out to git for
// network transport, so its errors wrap git's ("External git program
// failed" around "unable to access"/"Could not resolve host"); local-path
// remotes fail with jj's own "Could not find repository at". Patterns
// captured from real jj git fetch runs against unreachable remotes.
func IsOriginAccessError(err error) bool {
	if err == nil {
		return false
	}
	detail := err.Error()
	return strings.Contains(detail, "External git program failed") ||
		strings.Contains(detail, "unable to access") ||
		strings.Contains(detail, "Could not resolve host") ||
		strings.Contains(detail, "Could not find repository at")
}
