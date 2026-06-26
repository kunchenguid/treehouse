package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoRootFromCommonGitDirHandlesForwardSlashPath(t *testing.T) {
	root, ok := repoRootFromCommonGitDir("C:/Users/runner/AppData/Local/Temp/repo/.git")
	if !ok {
		t.Fatal("expected .git common dir to resolve to a repo root")
	}

	want := filepath.Clean(filepath.FromSlash("C:/Users/runner/AppData/Local/Temp/repo"))
	if root != want {
		t.Fatalf("expected repo root %q, got %q", want, root)
	}
}

func TestGetDefaultBranchFromDetachedLinkedWorktreeUsesMainRepoHead(t *testing.T) {
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	mustGit(t, repoDir, "config", "init.defaultBranch", "wrong")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	branch, err := GetDefaultBranch(wtPath)
	if err != nil {
		t.Fatalf("GetDefaultBranch failed: %v", err)
	}
	if branch != "main" {
		t.Fatalf("expected default branch main from main repo HEAD, got %q", branch)
	}
}

func TestFindMainRepoRootFromLinkedWorktree(t *testing.T) {
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	mainRoot, err := FindMainRepoRootFrom(wtPath)
	if err != nil {
		t.Fatalf("FindMainRepoRootFrom failed: %v", err)
	}
	if mainRoot != repoDir {
		t.Fatalf("expected main repo root %s, got %s", repoDir, mainRoot)
	}
}

func TestRemoveCleanWorktreeRejectsDirtyWorktree(t *testing.T) {
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	dirtyPath := filepath.Join(wtPath, "uncommitted.txt")
	if err := os.WriteFile(dirtyPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveCleanWorktree(repoDir, wtPath); err == nil {
		t.Fatal("expected clean worktree removal to reject dirty worktree")
	}
	if _, err := os.Stat(dirtyPath); err != nil {
		t.Fatalf("expected dirty worktree to remain: %v", err)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestRepoNameFromCommonDir(t *testing.T) {
	cases := []struct{ in, want string }{
		{filepath.FromSlash("/a/b/repo/.git"), "repo"},
		{filepath.FromSlash("/a/b/proj/.bare"), "proj"},
		{filepath.FromSlash("/a/b/repo.git"), "repo"},
		{filepath.FromSlash("/a/b/repo"), "repo"},
	}
	for _, c := range cases {
		if got := RepoNameFromCommonDir(c.in); got != c.want {
			t.Errorf("RepoNameFromCommonDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMainRootFromCommonDir(t *testing.T) {
	cases := []struct{ in, want string }{
		{filepath.FromSlash("/a/b/repo/.git"), filepath.FromSlash("/a/b/repo")},
		{filepath.FromSlash("/a/b/proj/.bare"), filepath.FromSlash("/a/b/proj")},
		{filepath.FromSlash("/a/b/repo.git"), filepath.FromSlash("/a/b/repo.git")},
	}
	for _, c := range cases {
		if got := MainRootFromCommonDir(c.in); got != c.want {
			t.Errorf("MainRootFromCommonDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// setupBareLayout builds the bare-repo worktree layout this tool must support:
//
//	base/origin.git        bare remote
//	base/proj/.bare        bare clone (the common git dir)
//	base/proj/.git         gitdir file pointing at .bare
//	base/proj/main         linked worktree
//	base/proj/feature      linked worktree
func setupBareLayout(t *testing.T) (bareDir, wtMain, wtFeature, projDir string) {
	t.Helper()
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}

	originDir := filepath.Join(base, "origin.git")
	seedDir := filepath.Join(base, "seed")
	projDir = filepath.Join(base, "proj")
	bareDir = filepath.Join(projDir, ".bare")
	wtMain = filepath.Join(projDir, "main")
	wtFeature = filepath.Join(projDir, "feature")

	mustGit(t, "", "init", "--bare", "--initial-branch=main", originDir)
	mustGit(t, "", "init", "--initial-branch=main", seedDir)
	mustGit(t, seedDir, "config", "user.email", "test@test.com")
	mustGit(t, seedDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, seedDir, "add", ".")
	mustGit(t, seedDir, "commit", "-m", "initial")
	mustGit(t, seedDir, "remote", "add", "origin", originDir)
	mustGit(t, seedDir, "push", "-u", "origin", "main")

	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, "", "clone", "--bare", originDir, bareDir)
	mustGit(t, bareDir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	mustGit(t, bareDir, "fetch", "origin")
	if err := os.WriteFile(filepath.Join(projDir, ".git"), []byte("gitdir: ./.bare\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, projDir, "worktree", "add", wtMain, "main")
	mustGit(t, projDir, "worktree", "add", "-b", "feature", wtFeature, "main")
	return bareDir, wtMain, wtFeature, projDir
}

func TestCommonGitDirStableAcrossBareLayout(t *testing.T) {
	bareDir, wtMain, wtFeature, projDir := setupBareLayout(t)

	// Every worktree, the bare dir, and the gitdir-file parent resolve to the
	// same common git dir - the stable repository identity anchor.
	for _, dir := range []string{wtMain, wtFeature, bareDir, projDir} {
		got, err := CommonGitDir(dir)
		if err != nil {
			t.Fatalf("CommonGitDir(%s): %v", dir, err)
		}
		if got != bareDir {
			t.Errorf("CommonGitDir(%s) = %q, want %q", dir, got, bareDir)
		}
	}
}

func TestResolveWorkDirWorktreeAndBare(t *testing.T) {
	bareDir, wtMain, _, projDir := setupBareLayout(t)

	// In a worktree, operations run from the working-tree root.
	if got, err := ResolveWorkDir(wtMain); err != nil || got != wtMain {
		t.Fatalf("ResolveWorkDir(%s) = %q, %v; want %q", wtMain, got, err, wtMain)
	}
	// In the bare dir (no work tree), operations run from the common git dir.
	if got, err := ResolveWorkDir(bareDir); err != nil || got != bareDir {
		t.Fatalf("ResolveWorkDir(%s) = %q, %v; want %q", bareDir, got, err, bareDir)
	}
	// From the gitdir-file parent (also no work tree), likewise.
	if got, err := ResolveWorkDir(projDir); err != nil || got != bareDir {
		t.Fatalf("ResolveWorkDir(%s) = %q, %v; want %q", projDir, got, err, bareDir)
	}
}
