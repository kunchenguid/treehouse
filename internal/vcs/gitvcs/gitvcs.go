package gitvcs

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
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

// SeedWorktree copies ignored files selected by .worktreeinclude.
func SeedWorktree(repoRoot, worktreePath string) error {
	_, err := SeedWorktreeWithInventory(repoRoot, worktreePath)
	return err
}

// SeedWorktreeWithInventory returns the paths it copied so the pool can remove
// them later without trusting mutable content in the acquired worktree.
func SeedWorktreeWithInventory(repoRoot, worktreePath string) ([]string, error) {
	return seedWorktreeWithInventory(repoRoot, worktreePath, nil, nil)
}

func SeedWorktreeWithInventoryFromGitStore(repoRoot, worktreePath, gitDir, ref string) ([]string, error) {
	env := append(os.Environ(), "GIT_DIR="+gitDir, "GIT_WORK_TREE="+repoRoot)
	trackedOutput, err := gitOutputEnv(repoRoot, env, nil, "ls-tree", "-rz", "--name-only", ref)
	if err != nil {
		return nil, err
	}
	if trackedOutput == nil {
		trackedOutput = []byte{}
	}
	return seedWorktreeWithInventory(repoRoot, worktreePath, env, trackedOutput)
}

func seedWorktreeWithInventory(repoRoot, worktreePath string, gitEnv []string, trackedOutput []byte) ([]string, error) {
	selected, err := selectedSeedPathsEnv(repoRoot, gitEnv)
	if err != nil || len(selected) == 0 {
		return nil, err
	}

	// checkout-index uses --force to refresh old seeds, so remove paths tracked
	// by the destination before building the temporary index.
	if trackedOutput == nil {
		trackedOutput, err = gitOutput(worktreePath, nil, "ls-files", "-z")
		if err != nil {
			return nil, err
		}
	}
	ignoreCase, err := worktreeCaseInsensitive(worktreePath)
	if err != nil {
		return nil, err
	}
	canonical := func(name string) string {
		if ignoreCase {
			return strings.ToLower(name)
		}
		return name
	}
	tracked := make(map[string]bool)
	for _, name := range bytes.Split(bytes.TrimSuffix(trackedOutput, []byte{0}), []byte{0}) {
		tracked[canonical(string(name))] = true
	}
	isTracked := func(name string) bool {
		name = canonical(name)
		for trackedName := range tracked {
			if strings.HasPrefix(trackedName, name+"/") {
				return true
			}
		}
		for {
			if tracked[name] {
				return true
			}
			separator := strings.LastIndexByte(name, '/')
			if separator < 0 {
				return false
			}
			name = name[:separator]
		}
	}
	var eligible bytes.Buffer
	for _, name := range bytes.Split(bytes.TrimSuffix(selected, []byte{0}), []byte{0}) {
		if !isTracked(string(name)) {
			eligible.Write(name)
			eligible.WriteByte(0)
		}
	}
	selected = eligible.Bytes()
	if len(selected) == 0 {
		return nil, nil
	}

	// Keep a no-follow handle open so a replaced root cannot inherit the expected identity.
	destination, err := openDirectoryNoFollow(worktreePath)
	if err != nil {
		return nil, err
	}
	defer destination.Close()
	expected, err := destination.Stat()
	if err != nil {
		return nil, err
	}
	if !expected.IsDir() || expected.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to seed symlinked worktree %s", worktreePath)
	}

	// Rooted operations reject paths that escape either checkout.
	sourceRoot, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil, err
	}
	defer sourceRoot.Close()

	destinationRoot, err := openRootUnchanged(worktreePath, expected)
	if err != nil {
		return nil, err
	}
	defer destinationRoot.Close()
	var copied []string
	failed := func(err error) ([]string, error) { return copied, err }
	for _, name := range bytes.Split(bytes.TrimSuffix(selected, []byte{0}), []byte{0}) {
		rel := filepath.FromSlash(string(name))
		info, err := sourceRoot.Lstat(rel)
		if err != nil {
			return failed(err)
		}

		var data []byte
		mode := info.Mode().Perm()
		switch {
		case info.Mode().IsRegular():
			data, err = sourceRoot.ReadFile(rel)
		case info.Mode()&os.ModeSymlink != 0:
			var target string
			target, err = sourceRoot.Readlink(rel)
			data = []byte(target)
			mode = 0o666
		default:
			continue
		}
		if err != nil {
			return failed(err)
		}

		// Copy through rooted filesystem operations rather than Git's index
		// machinery, which can execute repository-configured content filters.
		if err := ensureRootedDir(destinationRoot, filepath.Dir(rel)); err != nil {
			return failed(err)
		}
		dst, err := destinationRoot.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return failed(err)
		}
		// Once created, the path belongs in cleanup even if writing or closing it fails.
		copied = append(copied, filepath.ToSlash(rel))
		if _, err := dst.Write(data); err != nil {
			dst.Close()
			return failed(err)
		}
		if err := dst.Chmod(mode); err != nil {
			dst.Close()
			return failed(err)
		}
		if err := dst.Close(); err != nil {
			return failed(err)
		}
	}
	return copied, nil
}

func worktreeCaseInsensitive(worktreePath string) (bool, error) {
	markerName := ".git"
	marker, err := os.Stat(filepath.Join(worktreePath, markerName))
	if os.IsNotExist(err) {
		markerName = ".jj"
		marker, err = os.Stat(filepath.Join(worktreePath, markerName))
	}
	if err != nil {
		return false, err
	}
	// The filesystem, rather than a mutable Git setting, decides whether two
	// differently cased destination paths can refer to the same tracked file.
	alias, err := os.Stat(filepath.Join(worktreePath, strings.ToUpper(markerName)))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(marker, alias), nil
}

func selectedSeedPaths(repoRoot string) ([]byte, error) {
	return selectedSeedPathsEnv(repoRoot, nil)
}

func selectedSeedPathsEnv(repoRoot string, gitEnv []string) ([]byte, error) {
	manifest, err := os.ReadFile(filepath.Join(repoRoot, ".worktreeinclude"))
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return selectedSeedPathsWithManifestEnv(repoRoot, manifest, gitEnv)
}

func selectedSeedPathsWithManifest(repoRoot string, manifest []byte) ([]byte, error) {
	return selectedSeedPathsWithManifestEnv(repoRoot, manifest, nil)
}

func selectedSeedPathsWithManifestEnv(repoRoot string, manifest []byte, gitEnv []string) ([]byte, error) {
	selected, err := manifestSelectedPathsEnv(repoRoot, manifest, gitEnv)
	if err != nil || len(selected) == 0 {
		return selected, err
	}
	ignored, err := gitOutputEnv(repoRoot, gitEnv, nil,
		"ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	ignoredPaths := make(map[string]bool)
	for _, name := range bytes.Split(bytes.TrimSuffix(ignored, []byte{0}), []byte{0}) {
		ignoredPaths[string(name)] = true
	}
	var eligible bytes.Buffer
	for _, name := range bytes.Split(bytes.TrimSuffix(selected, []byte{0}), []byte{0}) {
		if ignoredPaths[string(name)] {
			eligible.Write(name)
			eligible.WriteByte(0)
		}
	}
	return eligible.Bytes(), nil
}

func manifestSelectedPaths(repoRoot string, manifest []byte) ([]byte, error) {
	return manifestSelectedPathsEnv(repoRoot, manifest, nil)
}

func manifestSelectedPathsEnv(repoRoot string, manifest []byte, gitEnv []string) ([]byte, error) {
	excludeFile, err := os.CreateTemp("", "treehouse-worktreeinclude-")
	if err != nil {
		return nil, err
	}
	excludePath := excludeFile.Name()
	defer os.Remove(excludePath)
	if _, err := excludeFile.Write(manifest); err != nil {
		excludeFile.Close()
		return nil, err
	}
	if err := excludeFile.Close(); err != nil {
		return nil, err
	}

	// Git owns the pattern language so .worktreeinclude behaves exactly like
	// an exclude file, including negation, escaping, and ** patterns.
	return gitOutputEnv(repoRoot, gitEnv, nil,
		"ls-files", "-z", "--others", "--ignored", "--exclude-from="+excludePath)
}

func openRootUnchanged(path string, expected os.FileInfo) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	reject := func() (*os.Root, error) {
		root.Close()
		return nil, fmt.Errorf("refusing to seed replaced worktree %s", path)
	}
	actual, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	if !os.SameFile(expected, actual) {
		return reject()
	}

	// Re-open without following the final component so an alias to the same
	// directory cannot pass the identity check. The rooted handle remains safe
	// if the pathname changes after this validation.
	current, err := openDirectoryNoFollow(path)
	if err != nil {
		return reject()
	}
	defer current.Close()
	currentInfo, err := current.Stat()
	if err != nil || !currentInfo.IsDir() || !os.SameFile(actual, currentInfo) {
		return reject()
	}
	return root, nil
}

func ensureRootedDir(root *os.Root, name string) error {
	if name == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(name, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			if err := root.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			continue
		}
		return fmt.Errorf("refusing to replace existing seed ancestor %s", name)
	}
	return nil
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
	return ResetWorktreeWithSeededPaths(worktreePath, branch, nil)
}

// ResetWorktreeWithSeededPaths resolves the branch once, records the current
// HEAD, then performs the same guarded reset used by pool reuse.
func ResetWorktreeWithSeededPaths(worktreePath, branch string, seededPaths []string) error {
	ref, err := resolveResetRef(worktreePath, branch)
	if err != nil {
		return err
	}
	head, err := worktreeHead(worktreePath)
	if err != nil {
		return err
	}
	return ResetWorktreeToRefWithSeededPaths(worktreePath, ref, head, false, seededPaths)
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
	return resetWorktreeToRef(worktreePath, ref, expectedHead, requireClean, nil, false)
}

// ResetWorktreeToRefWithSeededPaths removes the pool's trusted seed inventory
// before restoring tracked content. A nil inventory does not authorize any
// ignored-file deletion.
func ResetWorktreeToRefWithSeededPaths(worktreePath, ref, expectedHead string, requireClean bool, seededPaths []string) error {
	return resetWorktreeToRef(worktreePath, ref, expectedHead, requireClean, seededPaths, true)
}

func resetWorktreeToRef(worktreePath, ref, expectedHead string, requireClean bool, seededPaths []string, cleanSeeds bool) error {
	if !isCommitID(expectedHead) || !isCommitID(ref) {
		return fmt.Errorf("worktree reset requires resolved commit IDs")
	}
	var cleanupIdentity os.FileInfo
	if cleanSeeds && seededPaths != nil {
		worktree, err := openDirectoryNoFollow(worktreePath)
		if err != nil {
			return err
		}
		defer worktree.Close()
		cleanupIdentity, err = worktree.Stat()
		if err != nil || !cleanupIdentity.IsDir() {
			return fmt.Errorf("refusing to clean replaced worktree %s", worktreePath)
		}
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
	if cleanSeeds {
		if seededPaths != nil {
			err = removeSeededPaths(worktreePath, seededPaths, cleanupIdentity)
		}
		if err != nil {
			return err
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

func removeSeededPaths(worktreePath string, paths []string, expected os.FileInfo) error {
	return removeSeededPathsAuthenticated(worktreePath, paths, expected, authenticateLinkedWorktree)
}

func removeSeededPathsAuthenticated(worktreePath string, paths []string, expected os.FileInfo, authenticate func(*os.Root, string) error) error {
	for _, name := range paths {
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") {
			return fmt.Errorf("invalid seeded path %q", name)
		}
		first := strings.SplitN(name, "/", 2)[0]
		if strings.EqualFold(first, ".git") || strings.EqualFold(first, ".jj") {
			return fmt.Errorf("invalid seeded path %q", name)
		}
	}
	root, worktree, err := openCleanupRoot(worktreePath, expected, authenticate)
	if err != nil {
		return err
	}
	defer root.Close()
	defer worktree.Close()
	for _, name := range paths {
		if err := root.Remove(filepath.FromSlash(name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func RemoveSeededPaths(worktreePath string, paths []string) error {
	return removeSeededPathsFromWorktree(worktreePath, paths, authenticateLinkedWorktree)
}

func RemoveSeededPathsFromJJWorkspace(worktreePath string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if err := removeSeededPathsFromWorktree(worktreePath, paths, authenticateJJWorkspace); err != nil {
		return err
	}
	return RemoveJJSeedAuthentication(worktreePath)
}

func PrepareJJSeededCleanup(worktreePath string) error {
	authPath := jjSeedAuthenticationPath(worktreePath)
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		return fmt.Errorf("creating jj seed authentication directory: %w", err)
	}
	markerPath := filepath.Join(worktreePath, ".jj", "repo")
	if err := os.Link(markerPath, authPath); err != nil {
		marker, markerErr := os.Stat(markerPath)
		auth, authErr := os.Stat(authPath)
		if markerErr == nil && authErr == nil && os.SameFile(marker, auth) {
			return nil
		}
		return fmt.Errorf("authenticating seeded jj workspace: %w", err)
	}
	return nil
}

func AuthenticateJJSeededCleanup(worktreePath string) error {
	worktree, err := openDirectoryNoFollow(worktreePath)
	if err != nil {
		return err
	}
	defer worktree.Close()
	expected, err := worktree.Stat()
	if err != nil {
		return err
	}
	root, pinned, err := openCleanupRoot(worktreePath, expected, authenticateJJWorkspace)
	if err != nil {
		return err
	}
	root.Close()
	return pinned.Close()
}

func RemoveJJSeedAuthentication(worktreePath string) error {
	authPath := jjSeedAuthenticationPath(worktreePath)
	return quarantineAndRemoveJJAuthentication(authPath, func(candidate string) bool {
		auth, authErr := os.Stat(candidate)
		marker, markerErr := os.Stat(filepath.Join(worktreePath, ".jj", "repo"))
		return authErr == nil && markerErr == nil && os.SameFile(marker, auth)
	})
}

func JJSeedAuthenticationIdentity(worktreePath string) (string, error) {
	return fileIdentity(jjSeedAuthenticationPath(worktreePath))
}

func RemoveStaleJJSeedAuthentication(worktreePath, expectedIdentity string) error {
	authPath := jjSeedAuthenticationPath(worktreePath)
	if _, err := os.Stat(worktreePath); err == nil || !os.IsNotExist(err) {
		return fmt.Errorf("refusing to remove jj seed authentication while workspace exists at %s", worktreePath)
	}
	return quarantineAndRemoveJJAuthentication(authPath, func(candidate string) bool {
		info, statErr := os.Lstat(candidate)
		identity, identityErr := fileIdentity(candidate)
		return statErr == nil && info.Mode().IsRegular() && identityErr == nil && identity == expectedIdentity
	})
}

var jjAuthenticationQuarantined = func(string) {}

func quarantineAndRemoveJJAuthentication(authPath string, verify func(string) bool) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	quarantinePath := filepath.Join(filepath.Dir(authPath), ".quarantine-"+hex.EncodeToString(nonce[:]))
	if err := os.Rename(authPath, quarantinePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	jjAuthenticationQuarantined(authPath)
	if !verify(quarantinePath) {
		if err := os.Link(quarantinePath, authPath); err == nil {
			_ = os.Remove(quarantinePath)
		}
		return fmt.Errorf("refusing to remove unowned jj seed authentication %s", authPath)
	}
	return os.Remove(quarantinePath)
}

func jjSeedAuthenticationPath(worktreePath string) string {
	abs, err := filepath.Abs(worktreePath)
	if err != nil {
		abs = filepath.Clean(worktreePath)
	}
	digest := sha256.Sum256([]byte(abs))
	return filepath.Join(filepath.Dir(abs), ".treehouse-jj-seed-auth", fmt.Sprintf("%x", digest[:16]))
}

func removeSeededPathsFromWorktree(worktreePath string, paths []string, authenticate func(*os.Root, string) error) error {
	worktree, err := openDirectoryNoFollow(worktreePath)
	if err != nil {
		return err
	}
	defer worktree.Close()
	expected, err := worktree.Stat()
	if err != nil {
		return err
	}
	return removeSeededPathsAuthenticated(worktreePath, paths, expected, authenticate)
}

func openCleanupRoot(worktreePath string, expected os.FileInfo, authenticate func(*os.Root, string) error) (*os.Root, *os.File, error) {
	// Keep the no-follow handle open so a pathname swap cannot redirect cleanup.
	worktree, err := openDirectoryNoFollow(worktreePath)
	if err != nil {
		return nil, nil, err
	}
	current, err := worktree.Stat()
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		worktree.Close()
		return nil, nil, fmt.Errorf("refusing to clean replaced worktree %s", worktreePath)
	}
	root, err := openRootUnchanged(worktreePath, expected)
	if err != nil {
		worktree.Close()
		return nil, nil, err
	}
	if err := authenticate(root, worktreePath); err != nil {
		root.Close()
		worktree.Close()
		return nil, nil, err
	}
	return root, worktree, nil
}

func authenticateJJWorkspace(root *os.Root, worktreePath string) error {
	marker, err := root.Lstat(".jj")
	if err != nil || !marker.IsDir() || marker.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to clean unregistered worktree %s", worktreePath)
	}
	repo, err := root.Lstat(filepath.Join(".jj", "repo"))
	if err != nil || !repo.Mode().IsRegular() {
		return fmt.Errorf("refusing to clean unregistered worktree %s", worktreePath)
	}
	markerFile, err := root.Open(filepath.Join(".jj", "repo"))
	if err != nil {
		return err
	}
	defer markerFile.Close()
	markerInfo, err := markerFile.Stat()
	if err != nil {
		return err
	}
	authInfo, err := os.Stat(jjSeedAuthenticationPath(worktreePath))
	if err != nil || !os.SameFile(markerInfo, authInfo) {
		return fmt.Errorf("refusing to clean unregistered worktree %s", worktreePath)
	}
	return nil
}

func authenticateLinkedWorktree(root *os.Root, worktreePath string) error {
	marker, err := root.Lstat(".git")
	if err != nil || !marker.Mode().IsRegular() {
		return fmt.Errorf("refusing to clean unregistered worktree %s", worktreePath)
	}
	contents, err := root.ReadFile(".git")
	if err != nil {
		return err
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(contents)), "gitdir: ")
	if !ok || gitDir == "" {
		return fmt.Errorf("refusing to clean unregistered worktree %s", worktreePath)
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}
	backlink, err := os.ReadFile(filepath.Join(filepath.Clean(gitDir), "gitdir"))
	if err != nil {
		return fmt.Errorf("refusing to clean unregistered worktree %s", worktreePath)
	}
	backlinkInfo, err := os.Stat(filepath.Clean(string(bytes.TrimSpace(backlink))))
	if err != nil || !os.SameFile(marker, backlinkInfo) {
		return fmt.Errorf("refusing to clean unregistered worktree %s", worktreePath)
	}
	return nil
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
	cmd := exec.Command("git", "merge-base", "--is-ancestor", "HEAD", ref)
	cmd.Dir = worktreePath
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return isHeadContentMergedIntoRef(worktreePath, ref)
	}
	return false, fmt.Errorf("git merge-base --is-ancestor HEAD %s: %s", ref, strings.TrimSpace(string(out)))
}

func isHeadContentMergedIntoRef(worktreePath, ref string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "HEAD", ref)
	cmd.Dir = worktreePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, fmt.Errorf("git merge-base HEAD %s returned no common ancestor", ref)
		}
		return false, fmt.Errorf("git merge-base HEAD %s: %s", ref, strings.TrimSpace(string(out)))
	}
	base := strings.TrimSpace(string(out))
	if base == "" {
		return false, fmt.Errorf("git merge-base HEAD %s returned no common ancestor", ref)
	}

	baseTree, err := readTree(worktreePath, base)
	if err != nil {
		return false, err
	}
	headTree, err := readTree(worktreePath, "HEAD")
	if err != nil {
		return false, err
	}
	targetTree, err := readTree(worktreePath, ref)
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

func readTree(repoRoot, ref string) (map[string]string, error) {
	out, err := runGitRaw(repoRoot, "ls-tree", "-r", "-z", "--full-tree", ref)
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
	out, err := runGitRaw(dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func runGitRaw(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
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
func (*Backend) SeedWorktree(repoRoot, worktreePath string) ([]string, error) {
	return SeedWorktreeWithInventory(repoRoot, worktreePath)
}
func (*Backend) PruneWorktrees(repoRoot string) error { return PruneWorktrees(repoRoot) }
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
func (*Backend) ResetWorktreeWithSeededPaths(worktreePath, branch string, seededPaths []string) error {
	return ResetWorktreeWithSeededPaths(worktreePath, branch, seededPaths)
}
func (*Backend) ResetWorktreeToRef(worktreePath, ref, expectedHead string, requireClean bool) error {
	return ResetWorktreeToRef(worktreePath, ref, expectedHead, requireClean)
}
func (*Backend) ResetWorktreeToRefWithSeededPaths(worktreePath, ref, expectedHead string, requireClean bool, seededPaths []string) error {
	return ResetWorktreeToRefWithSeededPaths(worktreePath, ref, expectedHead, requireClean, seededPaths)
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

func gitOutput(dir string, stdin []byte, args ...string) ([]byte, error) {
	return gitOutputEnv(dir, nil, stdin, args...)
}

func gitOutputEnv(dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.Output()
	return out, err
}
