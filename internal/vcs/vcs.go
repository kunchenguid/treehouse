// Package vcs is the version-control seam for treehouse. Every VCS operation
// the rest of the codebase needs goes through this package, so the pool
// lifecycle, commands, and configuration stay backend-agnostic.
//
// Git is the default backend everywhere. The jj backend is strictly an
// explicit opt-in via the TREEHOUSE_VCS environment variable, the "vcs" key
// in the repository's treehouse.toml, or the "vcs" key in the user-level
// ~/.config/treehouse/config.toml, in that precedence order. The jj opt-in
// only takes effect where a .jj directory actually exists: in a repository
// without one, an ambient "jj" opt-in silently keeps the git backend, so a
// shell-wide TREEHOUSE_VCS=jj never breaks plain git repositories. Colocated
// repositories (both .jj and .git) stay on git worktrees without the opt-in,
// and a .jj-only repository without the opt-in simply keeps git's error
// behavior. Pooled jj workspaces are .jj-only trees that cannot carry
// an untracked config file, so they inherit the opt-in from their main
// repository root, located by reading the .jj/repo pointer — file
// inspection only; the decision still comes from explicit configuration.
//
// Operations on an existing worktree dispatch on the slot's own marker (a
// .git entry or a .jj directory; see backendForWorktree), so the configured
// backend never answers for a slot of the other flavor. Worktree creation
// and paths without a marker still follow the configured backend.
package vcs

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/kunchenguid/treehouse/internal/vcs/gitvcs"
	"github.com/kunchenguid/treehouse/internal/vcs/jjvcs"
)

// Backend is the set of version-control operations treehouse's lifecycle
// needs. Path-taking methods accept either a repository root or a worktree
// path exactly as the underlying VCS command would.
type Backend interface {
	// Name identifies the backend (e.g. "git").
	Name() string
	// FindRepoRootFrom returns the root of the repository or worktree
	// containing dir. An empty dir means the current working directory.
	FindRepoRootFrom(dir string) (string, error)
	// FindMainRepoRootFrom resolves dir to the main repository root, mapping
	// linked worktrees back to the repository that owns them.
	FindMainRepoRootFrom(dir string) (string, error)
	// GetDefaultBranch returns the repository's default branch name.
	GetDefaultBranch(repoRoot string) (string, error)
	// CommonGitDir returns the shared git metadata directory used for
	// repo-local, untracked exclusions (info/exclude). Backends without a
	// usable git dir return an error and callers degrade gracefully.
	CommonGitDir(dir string) (string, error)
	// HasRemote reports whether the named remote exists.
	HasRemote(repoRoot, name string) bool
	// GetRemoteURL returns the origin remote URL.
	GetRemoteURL(repoRoot string) (string, error)
	// AddWorktree creates a new worktree at path based on branch.
	AddWorktree(repoRoot, path, branch string) error
	// SeedWorktree copies backend-specific ignored files into a new worktree
	// and returns their paths so they can be removed without trusting mutable
	// worktree metadata.
	SeedWorktree(repoRoot, worktreePath string) ([]string, error)
	// PruneWorktrees clears bookkeeping for worktrees whose directories no
	// longer exist. It never touches live worktrees or their data.
	PruneWorktrees(repoRoot string) error
	// RemoveWorktree removes a worktree even if it has local changes.
	RemoveWorktree(repoRoot, path string) error
	// RemoveCleanWorktree removes a worktree, refusing if it is not clean.
	RemoveCleanWorktree(repoRoot, path string) error
	// Fetch updates refs from origin when an origin remote exists.
	Fetch(repoRoot string) error
	// ResetWorktree returns a worktree to a pristine checkout of branch,
	// discarding local modifications.
	ResetWorktree(worktreePath, branch string) error
	// ResetWorktreeWithSeededPaths resets a worktree after removing its trusted
	// seed inventory. A nil inventory does not authorize ignored-file deletion.
	ResetWorktreeWithSeededPaths(worktreePath, branch string, seededPaths []string) error
	// ResetWorktreeToRef resets worktreePath to an already resolved commit.
	// Callers that verified safety must pass the reset target and worktree
	// HEAD returned by IsWorktreeSafeToReset. The reset re-reads HEAD and,
	// when requireClean is set, re-checks dirtiness under the exclusive
	// lock before any destructive tree update, so concurrent uncommitted
	// work is not discarded. Refuse if HEAD changed, the lock cannot be
	// taken, or (when requireClean) the tree is dirty.
	ResetWorktreeToRef(worktreePath, ref, expectedHead string, requireClean bool) error
	// ResetWorktreeToRefWithSeededPaths applies the same guarded reset after
	// removing the trusted seed inventory. A nil inventory does not authorize
	// ignored-file deletion.
	ResetWorktreeToRefWithSeededPaths(worktreePath, ref, expectedHead string, requireClean bool, seededPaths []string) error
	// IsWorktreeSafeToReset reports whether worktreePath can be reset to
	// branch without discarding committed work. It returns the immutable
	// reset target and the worktree HEAD recorded at check time. Callers
	// must pass both to ResetWorktreeToRef. The check fails closed when
	// the target or HEAD cannot be resolved.
	IsWorktreeSafeToReset(worktreePath, branch string) (bool, string, string, error)
	// DetachWorktree releases any branch the worktree has checked out so
	// pooled worktrees never hold branch names.
	DetachWorktree(worktreePath string) error
	// DefaultBranchMergeRef returns the fully qualified ref merge-safety
	// checks compare against, failing closed when it cannot be verified.
	DefaultBranchMergeRef(repoRoot string) (string, error)
	// IsHeadMergedIntoRef reports whether the worktree's current head is
	// merged into ref.
	IsHeadMergedIntoRef(worktreePath, ref string) (bool, error)
	// IsDirty reports tracked or untracked local changes.
	IsDirty(worktreePath string) (bool, error)
}

var (
	gitBackend Backend = gitvcs.New()
	jjBackend  Backend = jjvcs.New()
)

// backendFor selects the backend responsible for path (a repository root,
// worktree path, or any directory inside one). Git is the default
// everywhere; TREEHOUSE_VCS, a repo-root treehouse.toml "vcs" key, or a
// user-level config "vcs" key opts in to jj explicitly (see vcsOverride for
// the precedence). A jj opt-in applies only when the marker root actually
// has a .jj directory; otherwise it silently falls back to git. The opt-in
// is read at the path's marker root and, for a .jj-only tree (such as a
// pooled jj workspace, whose checkout cannot carry an untracked
// treehouse.toml), also at the main repository root that its .jj/repo
// pointer names. Backend choice always comes from that explicit
// configuration, never from the marker itself; paths outside any repository
// fall back to git so errors surface exactly as they always did.
func backendFor(path string) Backend {
	dir := path
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return gitBackend
		}
		dir = cwd
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	root, hasJJ, hasGit := findMarkerRoot(dir)
	if root == "" {
		return gitBackend
	}
	switch vcsOverride(root) {
	case "git":
		return gitBackend
	case "jj":
		if hasJJ {
			return jjBackend
		}
		return gitBackend
	}
	if hasJJ && !hasGit {
		// A workspace's own tree holds no untracked config; the opt-in, if
		// any, lives at the main repository root the .jj/repo pointer names.
		if mainRoot, err := jjvcs.MainRootFromWorkspaceRoot(root); err == nil && mainRoot != root {
			if vcsOverride(mainRoot) == "jj" {
				return jjBackend
			}
		}
	}
	return gitBackend
}

// findMarkerRoot walks up from dir and stops at the first level holding a VCS
// marker, reporting which markers exist there so the caller can tell a
// colocated repository (.jj and .git together) from a jj-only one.
func findMarkerRoot(dir string) (root string, hasJJ, hasGit bool) {
	for {
		if info, err := os.Stat(filepath.Join(dir, ".jj")); err == nil && info.IsDir() {
			hasJJ = true
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			hasGit = true
		}
		if hasJJ || hasGit {
			return dir, hasJJ, hasGit
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, false
		}
		dir = parent
	}
}

// vcsOverride returns a forced backend name ("git" or "jj") for repoRoot, or
// "" when selection should stay automatic. Precedence, highest first: the
// TREEHOUSE_VCS environment variable, the "vcs" key of the repository's
// treehouse.toml, the "vcs" key of the user-level
// ~/.config/treehouse/config.toml. The files are read directly here (rather
// than through internal/config) because config depends on this package.
func vcsOverride(repoRoot string) string {
	if v := normalizeVCSNameFrom("TREEHOUSE_VCS", os.Getenv("TREEHOUSE_VCS")); v != "" {
		return v
	}
	if v := vcsFromFile(filepath.Join(repoRoot, "treehouse.toml")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return vcsFromFile(filepath.Join(home, ".config", "treehouse", "config.toml"))
	}
	return ""
}

func vcsFromFile(path string) string {
	var cfg struct {
		VCS string `toml:"vcs"`
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return ""
	}
	return normalizeVCSNameFrom(path, cfg.VCS)
}

func normalizeVCSName(v string) string {
	switch v {
	case "git", "jj":
		return v
	}
	return ""
}

// warnedVCSValues dedupes unrecognized-value warnings: backend selection
// runs many times per command and the warning is for a human, once.
var warnedVCSValues sync.Map

// normalizeVCSNameFrom is normalizeVCSName plus a one-time stderr warning
// naming the source, so a misconfigured opt-in ("Jujutsu", "JJ", a stray
// space) surfaces instead of silently keeping the default. The value is
// still ignored: a typo must not break every command in the repository, and
// stdout stays clean for machine callers.
func normalizeVCSNameFrom(source, v string) string {
	if n := normalizeVCSName(v); n != "" || v == "" {
		return n
	}
	if _, seen := warnedVCSValues.LoadOrStore(source+"\x00"+v, true); !seen {
		fmt.Fprintf(os.Stderr, "treehouse: unrecognized vcs value %q in %s (expected \"git\" or \"jj\"); ignoring it\n", v, source)
	}
	return ""
}

// FindRepoRoot returns the repository or worktree root for the current
// working directory.
func FindRepoRoot() (string, error) { return backendFor("").FindRepoRootFrom("") }

// FindRepoRootFrom returns the repository or worktree root containing dir.
func FindRepoRootFrom(dir string) (string, error) { return backendFor(dir).FindRepoRootFrom(dir) }

// FindMainRepoRoot returns the main repository root for the current working
// directory, resolving linked worktrees back to their owning repository.
func FindMainRepoRoot() (string, error) { return backendFor("").FindMainRepoRootFrom("") }

// FindMainRepoRootFrom returns the main repository root for dir. When dir is
// itself a worktree (it holds a VCS marker), its own flavor answers; for any
// other directory the configured backend does, exactly as before.
func FindMainRepoRootFrom(dir string) (string, error) {
	return backendForWorktree(dir).FindMainRepoRootFrom(dir)
}

// GetDefaultBranch returns the repository's default branch name.
func GetDefaultBranch(repoRoot string) (string, error) {
	return backendFor(repoRoot).GetDefaultBranch(repoRoot)
}

// CommonGitDir returns the shared git metadata directory for the repo
// containing dir.
func CommonGitDir(dir string) (string, error) { return backendFor(dir).CommonGitDir(dir) }

// HasRemote reports whether the named remote exists.
func HasRemote(repoRoot, name string) bool { return backendFor(repoRoot).HasRemote(repoRoot, name) }

// GetRemoteURL returns the origin remote URL.
func GetRemoteURL(repoRoot string) (string, error) {
	return backendFor(repoRoot).GetRemoteURL(repoRoot)
}

// AddWorktree creates a new worktree at path based on branch.
func AddWorktree(repoRoot, path, branch string) error {
	return backendFor(repoRoot).AddWorktree(repoRoot, path, branch)
}

// PruneWorktrees clears bookkeeping for worktrees whose directories no longer
// exist.
func PruneWorktrees(repoRoot string) error { return backendFor(repoRoot).PruneWorktrees(repoRoot) }

// RemoveWorktree removes a worktree even if it has local changes.
func RemoveWorktree(repoRoot, path string) error {
	return backendForRemoval(repoRoot, path).RemoveWorktree(repoRoot, path)
}

// RemoveCleanWorktree removes a worktree, refusing if it is not clean.
func RemoveCleanWorktree(repoRoot, path string) error {
	return backendForRemoval(repoRoot, path).RemoveCleanWorktree(repoRoot, path)
}

// slotMarkerBackend reports the backend a worktree's own marker names: a
// .git entry means a git worktree, a .jj directory means a jj workspace.
// Pool slots hold exactly one of the two (jj workspaces are never
// colocated), so the marker identifies what the slot actually is regardless
// of the repository's configured backend.
func slotMarkerBackend(path string) Backend {
	name, _ := WorktreeBackendNameChecked(path)
	switch name {
	case "git":
		return gitBackend
	case "jj":
		return jjBackend
	}
	return nil
}

// WorktreeBackendNameChecked names the backend identified by a worktree's
// marker while preserving filesystem errors for callers that must fail closed.
func WorktreeBackendNameChecked(path string) (string, error) {
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return "git", nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if info, err := os.Stat(filepath.Join(path, ".jj")); err == nil {
		if info.IsDir() {
			return "jj", nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return "", nil
}

// backendForWorktree dispatches per-worktree operations - the facts that
// gate destructive decisions (dirty, merged, main-root) and the actions on a
// slot's own state (reset, detach) - on what the worktree actually is. The
// configured backend must not answer for a slot of the other flavor: a
// .jj-only slot inspected through git resolves the repository ENCLOSING the
// pool, and with an in-project pool root a clean enclosing repo makes dirty
// jj work classify as disposable. Paths without a marker (ordinary
// directories inside a repository) keep the configured-backend resolution.
func backendForWorktree(path string) Backend {
	if b := slotMarkerBackend(path); b != nil {
		return b
	}
	return backendFor(path)
}

// backendForRemoval dispatches removal the same way, but falls back to the
// repository's backend when the path is already gone, so error surfacing and
// stale-registration cleanup stay exactly as they were.
func backendForRemoval(repoRoot, path string) Backend {
	if b := slotMarkerBackend(path); b != nil {
		return b
	}
	return backendFor(repoRoot)
}

// destructiveBackendForWorktree dispatches the operations that rewrite a
// worktree's checkout (reset, detach). Unlike backendForWorktree it refuses a
// path holding no .git or .jj marker instead of falling back to the
// configured backend, which in an in-project pool would resolve — and rewrite
// — the repository ENCLOSING the pool. Callers guard markerless slots first;
// this refusal is defense in depth. Pool slots are the only callers of these
// operations, so ordinary directories are unaffected.
func destructiveBackendForWorktree(path string) (Backend, error) {
	if b := slotMarkerBackend(path); b != nil {
		return b, nil
	}
	return nil, fmt.Errorf("refusing to modify %s: it holds no .git or .jj marker", path)
}

// Fetch updates refs from origin when an origin remote exists.
func Fetch(repoRoot string) error { return backendFor(repoRoot).Fetch(repoRoot) }

// ResetWorktree returns a worktree to a pristine checkout of branch.
func ResetWorktree(worktreePath, branch string) error {
	b, err := destructiveBackendForWorktree(worktreePath)
	if err != nil {
		return err
	}
	return b.ResetWorktree(worktreePath, branch)
}

// ResetWorktreeWithSeededPaths resets after removing trusted seeded files.
func ResetWorktreeWithSeededPaths(worktreePath, branch string, seededPaths []string) error {
	b, err := destructiveBackendForWorktree(worktreePath)
	if err != nil {
		return err
	}
	return b.ResetWorktreeWithSeededPaths(worktreePath, branch, seededPaths)
}

// SeedWorktree copies backend-specific ignored files into a new worktree.
func SeedWorktree(repoRoot, worktreePath string) ([]string, error) {
	return backendFor(repoRoot).SeedWorktree(repoRoot, worktreePath)
}

func JJSeedAuthenticationIdentity(worktreePath string) (string, error) {
	return gitvcs.JJSeedAuthenticationIdentity(worktreePath)
}

func RemoveStaleJJSeedAuthentication(worktreePath, expectedIdentity string) error {
	return gitvcs.RemoveStaleJJSeedAuthentication(worktreePath, expectedIdentity)
}

// ResetWorktreeToRef resets worktreePath to an already resolved commit.
func ResetWorktreeToRef(worktreePath, ref, expectedHead string, requireClean bool) error {
	b, err := destructiveBackendForWorktree(worktreePath)
	if err != nil {
		return err
	}
	return b.ResetWorktreeToRef(worktreePath, ref, expectedHead, requireClean)
}

// ResetWorktreeToRefWithSeededPaths resets after removing trusted seeded files.
func ResetWorktreeToRefWithSeededPaths(worktreePath, ref, expectedHead string, requireClean bool, seededPaths []string) error {
	b, err := destructiveBackendForWorktree(worktreePath)
	if err != nil {
		return err
	}
	return b.ResetWorktreeToRefWithSeededPaths(worktreePath, ref, expectedHead, requireClean, seededPaths)
}

// IsWorktreeSafeToReset reports whether worktreePath can be reset to branch
// without discarding committed work and returns the immutable reset target and
// the worktree HEAD recorded at check time.
func IsWorktreeSafeToReset(worktreePath, branch string) (bool, string, string, error) {
	return backendForWorktree(worktreePath).IsWorktreeSafeToReset(worktreePath, branch)
}

// DetachWorktree releases any branch the worktree has checked out.
func DetachWorktree(worktreePath string) error {
	b, err := destructiveBackendForWorktree(worktreePath)
	if err != nil {
		return err
	}
	return b.DetachWorktree(worktreePath)
}

// DefaultBranchMergeRef returns the fully qualified ref merge-safety checks
// compare against.
func DefaultBranchMergeRef(repoRoot string) (string, error) {
	return backendFor(repoRoot).DefaultBranchMergeRef(repoRoot)
}

// DefaultBranchMergeRefForWorktree returns the merge ref in the vocabulary of
// the worktree's own backend, resolved against repoRoot. A slot of the other
// flavor came from an era when its backend worked against this repository, so
// that backend can still name the ref merge-safety checks should compare
// against; the configured backend's ref never parses for it.
func DefaultBranchMergeRefForWorktree(worktreePath, repoRoot string) (string, error) {
	return backendForWorktree(worktreePath).DefaultBranchMergeRef(repoRoot)
}

// DefaultBranchForWorktree returns the default branch of the repository that
// owns worktreePath, with both the root discovery and the branch lookup
// resolved by the worktree's own backend (the same dispatch as ResetWorktree
// and friends), so a pooled slot can always be released even when the opt-in
// that created it is no longer visible.
func DefaultBranchForWorktree(worktreePath string) (string, error) {
	b := backendForWorktree(worktreePath)
	repoRoot, err := b.FindRepoRootFrom(worktreePath)
	if err != nil {
		return "", err
	}
	return b.GetDefaultBranch(repoRoot)
}

// IsHeadMergedIntoRef reports whether the worktree's current head is merged
// into ref.
func IsHeadMergedIntoRef(worktreePath, ref string) (bool, error) {
	return backendForWorktree(worktreePath).IsHeadMergedIntoRef(worktreePath, ref)
}

// IsDirty reports tracked or untracked local changes in the worktree.
func IsDirty(worktreePath string) (bool, error) {
	return backendForWorktree(worktreePath).IsDirty(worktreePath)
}

// ShortHash returns a short stable hash of s, used for pool directory naming.
// It is VCS-independent.
func ShortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:3])
}

// IsOriginAccessError reports whether err reads like a failure to reach or
// use the origin remote. Classification is by the error's content, not the
// configured backend: the error already happened in whichever backend
// produced it, and each backend owns its own vocabulary.
func IsOriginAccessError(err error) bool {
	return gitvcs.IsOriginAccessError(err) || jjvcs.IsOriginAccessError(err)
}

// BackendNameFor names the backend the repository's configuration selects
// for path ("git" or "jj"), without performing any operation.
func BackendNameFor(path string) string { return backendFor(path).Name() }

// WorktreeBackendName names the backend a worktree's own marker identifies
// ("git" or "jj"), or "" when path holds no marker. This is the flavor a
// pool slot actually is, as opposed to what the repository currently
// selects.
func WorktreeBackendName(path string) string {
	if b := slotMarkerBackend(path); b != nil {
		return b.Name()
	}
	return ""
}
