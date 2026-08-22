package jjvcs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/internal/vcs/gitvcs"
)

// requireJJ skips the test when jj is not installed, so the suite stays green
// in environments without Jujutsu.
func requireJJ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
}

func mustJJ(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runJJ(dir, args...)
	if err != nil {
		t.Fatalf("jj %s failed: %v", strings.Join(args, " "), err)
	}
	return out
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// isolateJJConfig points JJ_CONFIG at a minimal config so tests are hermetic
// against the developer's own jj configuration (e.g. git.colocate = true
// would silently colocate every repo these tests create).
func isolateJJConfig(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "jjconfig.toml")
	contents := "[user]\nname = \"Treehouse Tests\"\nemail = \"treehouse-tests@example.com\"\n\n[git]\n# jj colocates new repos by default; tests opt out so \"jj-only\" fixtures\n# really are jj-only, and colocated fixtures say --colocate explicitly.\ncolocate = false\n"
	if err := os.WriteFile(cfg, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JJ_CONFIG", cfg)
}

// newLocalRepo creates a jj repository with one commit on a local main
// bookmark and no remotes.
func newLocalRepo(t *testing.T) string {
	t.Helper()
	isolateJJConfig(t)
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustJJ(t, "", "git", "init", repoDir)
	writeFile(t, filepath.Join(repoDir, "README.md"), "hello\n")
	mustJJ(t, repoDir, "commit", "-m", "initial")
	mustJJ(t, repoDir, "bookmark", "create", "main", "-r", "@-")
	return repoDir
}

// newRemoteRepo creates a bare git origin plus a jj clone whose main bookmark
// is tracked on origin.
func newRemoteRepo(t *testing.T) (repoDir, originDir string) {
	t.Helper()
	isolateJJConfig(t)
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	originDir = filepath.Join(base, "origin")
	seedDir := filepath.Join(base, "seed")
	repoDir = filepath.Join(base, "repo")

	mustGit(t, "", "init", "--bare", originDir)
	mustGit(t, "", "clone", originDir, seedDir)
	mustGit(t, seedDir, "config", "user.email", "test@test.com")
	mustGit(t, seedDir, "config", "user.name", "Test")
	writeFile(t, filepath.Join(seedDir, "README.md"), "hello\n")
	mustGit(t, seedDir, "add", ".")
	mustGit(t, seedDir, "commit", "-m", "initial")
	mustGit(t, seedDir, "push", "origin", "HEAD:main")

	mustJJ(t, "", "git", "clone", originDir, repoDir)
	return repoDir, originDir
}

func addWorkspace(t *testing.T, repoDir string) string {
	t.Helper()
	wtPath := filepath.Join(filepath.Dir(repoDir), "worktree")
	b := New()
	branch, err := b.GetDefaultBranch(repoDir)
	if err != nil {
		t.Fatalf("GetDefaultBranch failed: %v", err)
	}
	if err := b.AddWorktree(repoDir, wtPath, branch); err != nil {
		t.Fatalf("AddWorktree failed: %v", err)
	}
	return wtPath
}

func TestWorkspaceNamesAreDistinctAndStablePerPath(t *testing.T) {
	base := t.TempDir()
	a := workspaceNameFor(filepath.Join(base, "wt1"))
	b := workspaceNameFor(filepath.Join(base, "wt2"))
	if a == b {
		t.Fatalf("expected distinct workspace names for distinct paths, both %q", a)
	}
	if again := workspaceNameFor(filepath.Join(base, "wt1")); again != a {
		t.Fatalf("expected a stable workspace name per path, got %q then %q", a, again)
	}
}

func TestGetDefaultBranchLocalOnly(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)

	branch, err := New().GetDefaultBranch(repoDir)
	if err != nil {
		t.Fatalf("GetDefaultBranch failed: %v", err)
	}
	if branch != "main" {
		t.Fatalf("expected main, got %q", branch)
	}
}

func TestGetDefaultBranchPrefersOrigin(t *testing.T) {
	requireJJ(t)
	repoDir, _ := newRemoteRepo(t)

	branch, err := New().GetDefaultBranch(repoDir)
	if err != nil {
		t.Fatalf("GetDefaultBranch failed: %v", err)
	}
	if branch != "main" {
		t.Fatalf("expected main, got %q", branch)
	}
}

func TestAddWorktreeCreatesUsableWorkspace(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	wtPath := addWorkspace(t, repoDir)

	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Fatalf("expected checked-out file in workspace: %v", err)
	}

	// The store pointer must be absolute so pool directories far from the
	// repository keep working if either side moves.
	contents, err := os.ReadFile(filepath.Join(wtPath, ".jj", "repo"))
	if err != nil {
		t.Fatalf("expected workspace repo pointer: %v", err)
	}
	if !filepath.IsAbs(strings.TrimSpace(string(contents))) {
		t.Fatalf("expected absolute store pointer, got %q", contents)
	}
}

func TestSeedWorktreeCopiesIgnoredManifestSelection(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	writeFile(t, filepath.Join(repoDir, ".gitignore"), "*.env\n")
	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), "selected.env\n")
	writeFile(t, filepath.Join(repoDir, "selected.env"), "secret\n")
	mustJJ(t, repoDir, "commit", "-m", "add seed manifest")
	wtPath := addWorkspace(t, repoDir)

	seeded, err := New().SeedWorktree(repoDir, wtPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 1 || seeded[0] != "selected.env" {
		t.Fatalf("seeded paths = %v, want [selected.env]", seeded)
	}
	data, err := os.ReadFile(filepath.Join(wtPath, "selected.env"))
	if err != nil || string(data) != "secret\n" {
		t.Fatalf("seeded file = %q, %v", data, err)
	}
}

func TestSeedWorktreePreservesInventoryWhenAuthenticationFails(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	writeFile(t, filepath.Join(repoDir, ".gitignore"), "*.env\n")
	writeFile(t, filepath.Join(repoDir, ".worktreeinclude"), "selected.env\n")
	writeFile(t, filepath.Join(repoDir, "selected.env"), "secret\n")
	mustJJ(t, repoDir, "commit", "-m", "add seed manifest")
	worktree := addWorkspace(t, repoDir)
	oldPrepare := prepareJJSeededCleanup
	prepareJJSeededCleanup = func(string) error { return errors.New("authentication failed") }
	t.Cleanup(func() { prepareJJSeededCleanup = oldPrepare })

	seeded, err := New().SeedWorktree(repoDir, worktree)
	if err == nil {
		t.Fatal("expected authentication failure")
	}
	if len(seeded) != 1 || seeded[0] != "selected.env" {
		t.Fatalf("seeded paths = %v, want [selected.env]", seeded)
	}
}

func TestResetWorktreeToRefFailurePreservesSeededPaths(t *testing.T) {
	worktree := t.TempDir()
	seed := filepath.Join(worktree, "selected.env")
	writeFile(t, seed, "keep\n")

	err := New().ResetWorktreeToRefWithSeededPaths(worktree, "target", "invalid", true, []string{"selected.env"})
	if err == nil {
		t.Fatal("expected guarded reset to fail")
	}
	data, readErr := os.ReadFile(seed)
	if readErr != nil || string(data) != "keep\n" {
		t.Fatalf("seeded file = %q, %v", data, readErr)
	}
}

func TestResetWorktreeResolutionFailurePreservesSeededPaths(t *testing.T) {
	worktree := t.TempDir()
	seed := filepath.Join(worktree, "selected.env")
	writeFile(t, seed, "keep\n")

	err := New().ResetWorktreeWithSeededPaths(worktree, "main", []string{"selected.env"})
	if err == nil {
		t.Fatal("expected reset resolution to fail")
	}
	data, readErr := os.ReadFile(seed)
	if readErr != nil || string(data) != "keep\n" {
		t.Fatalf("seeded file = %q, %v", data, readErr)
	}
}

func TestResetWorktreeWithSeededPathsRemovesSeedFromWorkspace(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	writeFile(t, filepath.Join(repoDir, ".gitignore"), "*.env\n")
	mustJJ(t, repoDir, "commit", "-m", "ignore environment files")
	worktree := addWorkspace(t, repoDir)
	seed := filepath.Join(worktree, "selected.env")
	writeFile(t, seed, "secret\n")
	if err := gitvcs.PrepareJJSeededCleanup(worktree); err != nil {
		t.Fatal(err)
	}

	if err := New().ResetWorktreeWithSeededPaths(worktree, "main", []string{"selected.env"}); err != nil {
		t.Fatalf("ResetWorktreeWithSeededPaths failed: %v", err)
	}
	if _, err := os.Stat(seed); !os.IsNotExist(err) {
		t.Fatalf("seeded file still exists: %v", err)
	}
}

func TestFindMainRepoRootFromWorkspace(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	wtPath := addWorkspace(t, repoDir)

	b := New()
	mainRoot, err := b.FindMainRepoRootFrom(wtPath)
	if err != nil {
		t.Fatalf("FindMainRepoRootFrom failed: %v", err)
	}
	if mainRoot != repoDir {
		t.Fatalf("expected main root %s, got %s", repoDir, mainRoot)
	}

	wsRoot, err := b.FindRepoRootFrom(wtPath)
	if err != nil {
		t.Fatalf("FindRepoRootFrom failed: %v", err)
	}
	if wsRoot != wtPath {
		t.Fatalf("expected workspace root %s, got %s", wtPath, wsRoot)
	}
}

func TestAddWorktreeSelfHealsStaleRegistration(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	wtPath := addWorkspace(t, repoDir)

	// Simulate a crash that removed the directory but left the registration.
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatal(err)
	}

	b := New()
	if err := b.AddWorktree(repoDir, wtPath, "main"); err != nil {
		t.Fatalf("expected re-add over stale registration to succeed: %v", err)
	}
}

func TestIsDirtyReflectsWorkingCopy(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	wtPath := addWorkspace(t, repoDir)

	b := New()
	dirty, err := b.IsDirty(wtPath)
	if err != nil {
		t.Fatalf("IsDirty failed: %v", err)
	}
	if dirty {
		t.Fatal("expected fresh workspace to be clean")
	}

	writeFile(t, filepath.Join(wtPath, "scratch.txt"), "wip\n")
	dirty, err = b.IsDirty(wtPath)
	if err != nil {
		t.Fatalf("IsDirty failed: %v", err)
	}
	if !dirty {
		t.Fatal("expected workspace with new file to be dirty")
	}
}

func TestResetWorktreeDiscardsChanges(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	wtPath := addWorkspace(t, repoDir)

	writeFile(t, filepath.Join(wtPath, "README.md"), "modified\n")
	writeFile(t, filepath.Join(wtPath, "scratch.txt"), "wip\n")

	b := New()
	if err := b.ResetWorktree(wtPath, "main"); err != nil {
		t.Fatalf("ResetWorktree failed: %v", err)
	}

	dirty, err := b.IsDirty(wtPath)
	if err != nil {
		t.Fatalf("IsDirty failed: %v", err)
	}
	if dirty {
		t.Fatal("expected reset workspace to be clean")
	}
	contents, err := os.ReadFile(filepath.Join(wtPath, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "hello\n" {
		t.Fatalf("expected README restored to bookmark contents, got %q", contents)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatal("expected scratch file to be gone after reset")
	}
}

func TestResetWorktreeToRefRefusesWhenHeadChanged(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	wtPath := addWorkspace(t, repoDir)

	b := New()
	safe, resetRef, head, err := b.IsWorktreeSafeToReset(wtPath, "main")
	if err != nil {
		t.Fatalf("IsWorktreeSafeToReset: %v", err)
	}
	if !safe {
		t.Fatal("expected fresh workspace to be safe to reset")
	}

	// Concurrent committed work after the safety check: @ is no longer the
	// working-copy commit whose ancestry was verified, so the reset must refuse.
	writeFile(t, filepath.Join(wtPath, "unlanded.txt"), "keep\n")
	mustJJ(t, wtPath, "commit", "-m", "unlanded after check")
	changed, err := worktreeHead(wtPath)
	if err != nil {
		t.Fatalf("resolve changed HEAD: %v", err)
	}
	if changed == head {
		t.Fatal("expected working-copy commit to change after the concurrent commit")
	}

	if err := b.ResetWorktreeToRef(wtPath, resetRef, head, true); err == nil {
		t.Fatal("expected ResetWorktreeToRef to refuse after HEAD changed")
	}
	got, err := worktreeHead(wtPath)
	if err != nil {
		t.Fatalf("resolve preserved HEAD: %v", err)
	}
	if got != changed {
		t.Fatalf("expected unlanded HEAD %s preserved, got %s", changed, got)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "unlanded.txt")); err != nil {
		t.Fatalf("expected concurrent commit preserved on disk: %v", err)
	}
}

func TestResetWorktreeToRefRefusesWhenDirtyAfterSafetyCheck(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	wtPath := addWorkspace(t, repoDir)

	b := New()
	safe, resetRef, head, err := b.IsWorktreeSafeToReset(wtPath, "main")
	if err != nil {
		t.Fatalf("IsWorktreeSafeToReset: %v", err)
	}
	if !safe {
		t.Fatal("expected fresh workspace to be safe to reset")
	}

	// Record the working-copy identity jj actually preserves. The moment
	// any jj command snapshots the dirtied tree, @ is amended in place:
	// its commit id changes but its change id and parent do not, and head
	// (the commit id pinned at check time) is what makes the reset refuse.
	changeBefore := strings.TrimSpace(mustJJ(t, wtPath, "log", "-r", "@", "--no-graph", "-T", "change_id"))
	parentBefore := strings.TrimSpace(mustJJ(t, wtPath, "log", "-r", "@-", "--no-graph", "-T", "commit_id"))

	scratch := filepath.Join(wtPath, "scratch.txt")
	writeFile(t, scratch, "keep\n")

	err = b.ResetWorktreeToRef(wtPath, resetRef, head, true)
	if err == nil {
		t.Fatal("expected ResetWorktreeToRef to refuse after the tree became dirty")
	}
	if !strings.Contains(err.Error(), "became dirty after safety check") {
		t.Fatalf("expected dirty-after-check error, got %v", err)
	}

	// Commit-id equality across a dirtying event is not a jj invariant:
	// snapshotting amends @'s commit id. Assert what the refusal
	// guarantees instead - same change, same parent, work intact.
	changeAfter := strings.TrimSpace(mustJJ(t, wtPath, "log", "-r", "@", "--no-graph", "-T", "change_id"))
	if changeAfter != changeBefore {
		t.Fatalf("expected working-copy change %s preserved, got %s", changeBefore, changeAfter)
	}
	parentAfter := strings.TrimSpace(mustJJ(t, wtPath, "log", "-r", "@-", "--no-graph", "-T", "commit_id"))
	if parentAfter != parentBefore {
		t.Fatalf("expected parent %s preserved, got %s", parentBefore, parentAfter)
	}
	contents, err := os.ReadFile(scratch)
	if err != nil {
		t.Fatalf("expected concurrent uncommitted work preserved on disk: %v", err)
	}
	if string(contents) != "keep\n" {
		t.Fatalf("expected scratch contents preserved, got %q", contents)
	}
}

func TestIsHeadMergedIntoRef(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	wtPath := addWorkspace(t, repoDir)

	b := New()
	// Fresh workspace sits directly on main: merged.
	merged, err := b.IsHeadMergedIntoRef(wtPath, "main")
	if err != nil {
		t.Fatalf("IsHeadMergedIntoRef failed: %v", err)
	}
	if !merged {
		t.Fatal("expected fresh workspace to be merged into main")
	}

	// Commit new work in the workspace: no longer merged.
	writeFile(t, filepath.Join(wtPath, "feature.txt"), "feature\n")
	mustJJ(t, wtPath, "commit", "-m", "feature")
	merged, err = b.IsHeadMergedIntoRef(wtPath, "main")
	if err != nil {
		t.Fatalf("IsHeadMergedIntoRef failed: %v", err)
	}
	if merged {
		t.Fatal("expected workspace with unmerged commit to report unmerged")
	}

	// Move main forward to include the work: merged again.
	mustJJ(t, wtPath, "bookmark", "set", "main", "-r", "@-")
	merged, err = b.IsHeadMergedIntoRef(wtPath, "main")
	if err != nil {
		t.Fatalf("IsHeadMergedIntoRef failed: %v", err)
	}
	if !merged {
		t.Fatal("expected workspace to be merged after main advanced")
	}
}

func TestRemoveCleanWorktreeRejectsDirtyWorkspace(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	wtPath := addWorkspace(t, repoDir)

	dirtyPath := filepath.Join(wtPath, "uncommitted.txt")
	writeFile(t, dirtyPath, "keep me\n")

	b := New()
	if err := b.RemoveCleanWorktree(repoDir, wtPath); err == nil {
		t.Fatal("expected clean removal to reject dirty workspace")
	}
	if _, err := os.Stat(dirtyPath); err != nil {
		t.Fatalf("expected dirty workspace to remain: %v", err)
	}

	if err := b.RemoveWorktree(repoDir, wtPath); err != nil {
		t.Fatalf("forced removal failed: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatal("expected workspace directory to be removed")
	}
}

func TestFetchWithoutRemoteIsNoOp(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	if err := New().Fetch(repoDir); err != nil {
		t.Fatalf("expected fetch without remote to be a no-op, got %v", err)
	}
}

func TestDefaultBranchMergeRefPrefersOrigin(t *testing.T) {
	requireJJ(t)
	repoDir, _ := newRemoteRepo(t)

	ref, err := New().DefaultBranchMergeRef(repoDir)
	if err != nil {
		t.Fatalf("DefaultBranchMergeRef failed: %v", err)
	}
	if ref != "main@origin" {
		t.Fatalf("expected main@origin, got %q", ref)
	}
}

func TestDefaultBranchMergeRefLocalOnly(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)

	ref, err := New().DefaultBranchMergeRef(repoDir)
	if err != nil {
		t.Fatalf("DefaultBranchMergeRef failed: %v", err)
	}
	if ref != "main" {
		t.Fatalf("expected main, got %q", ref)
	}
}

// TestRemoveWorktreeRefusesNonJJDirectory pins the deletion guard: a
// directory that exists but is not a jj workspace must not be deleted.
func TestRemoveWorktreeRefusesNonJJDirectory(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	victim := filepath.Join(t.TempDir(), "not-a-workspace")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "data.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &Backend{}
	if err := b.RemoveWorktree(repoDir, victim); err == nil {
		t.Fatal("expected an error removing a non-jj directory")
	}
	if _, err := os.Stat(filepath.Join(victim, "data.txt")); err != nil {
		t.Fatalf("directory contents must survive a refused removal: %v", err)
	}
}

// TestRemoveWorktreeRefusesMainWorkspace pins the other side of the deletion
// guard: the main workspace, whose .jj/repo is the repository store itself,
// must never be deleted as if it were a pooled secondary workspace.
func TestRemoveWorktreeRefusesMainWorkspace(t *testing.T) {
	requireJJ(t)
	repoDir := newLocalRepo(t)
	b := &Backend{}
	if err := b.RemoveWorktree(repoDir, repoDir); err == nil {
		t.Fatal("expected an error removing the main workspace")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "README.md")); err != nil {
		t.Fatalf("repository contents must survive a refused removal: %v", err)
	}
}

// TestSymlinkedRepoPathResolvesOneRootIdentity pins the symlink
// canonicalization contract: a repository reached through a symlinked path
// (like macOS's /tmp -> /private/tmp) must resolve to the same root string
// from every route - `jj workspace root` in the main repo and the .jj/repo
// pointer inside a pooled workspace - because the pool identity is derived
// from that string. Before canonicalization the two routes disagreed and
// treehouse status inside a workspace resolved a phantom, empty pool.
func TestSymlinkedRepoPathResolvesOneRootIdentity(t *testing.T) {
	requireJJ(t)
	isolateJJConfig(t)
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(base, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	// Init and use the repository exclusively through the symlinked path.
	repoVia := filepath.Join(linkDir, "repo")
	if err := os.MkdirAll(repoVia, 0o755); err != nil {
		t.Fatal(err)
	}
	mustJJ(t, repoVia, "git", "init")
	mustJJ(t, repoVia, "bookmark", "create", "main", "-r", "@")

	b := &Backend{}
	wsPath := filepath.Join(base, "ws")
	if err := b.AddWorktree(repoVia, wsPath, "main"); err != nil {
		t.Fatalf("AddWorktree through symlinked path: %v", err)
	}

	fromMain, err := b.FindRepoRootFrom(repoVia)
	if err != nil {
		t.Fatal(err)
	}
	fromWorkspace, err := b.FindMainRepoRootFrom(wsPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(realDir, "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if fromMain != want {
		t.Fatalf("main-repo route: got %q, want physical %q", fromMain, want)
	}
	if fromWorkspace != want {
		t.Fatalf("workspace route: got %q, want physical %q", fromWorkspace, want)
	}
}

// TestIsOriginAccessErrorJJ pins jj's unreachable-origin vocabulary, each
// string captured from a real jj git fetch failure (jj 0.43).
func TestIsOriginAccessErrorJJ(t *testing.T) {
	cases := []struct {
		detail string
		want   bool
	}{
		{"Error: Git process failed: External git program failed:", true},
		{"fatal: unable to access 'https://x/': Could not resolve host: x", true},
		{"Error: Could not find repository at '/nonexistent/repo.git'", true},
		{"Error: Workspace is stale", false},
		{"", false},
	}
	for _, c := range cases {
		got := IsOriginAccessError(errors.New(c.detail))
		if got != c.want {
			t.Errorf("IsOriginAccessError(%q) = %v, want %v", c.detail, got, c.want)
		}
	}
	if IsOriginAccessError(nil) {
		t.Error("nil error must not classify as an origin access error")
	}
}
