package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/internal/git"
)

func TestResolvePoolDir_EmptyRoot(t *testing.T) {
	// With empty root, pool dir should be under $HOME/.treehouse/{repoName}-{hash}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	// We need a real repo for GetRemoteURL. Use a fake approach by creating
	// a temp git repo with a remote.
	repoDir := setupGitRepo(t)

	poolDir, err := ResolvePoolDir(repoDir, "")
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	if !strings.HasPrefix(poolDir, filepath.Join(home, ".treehouse", repoName)) {
		t.Errorf("expected pool dir under %s/.treehouse/%s-*, got %s", home, repoName, poolDir)
	}
}

func TestResolvePoolDir_RelativeRoot(t *testing.T) {
	repoDir := setupGitRepo(t)

	poolDir, err := ResolvePoolDir(repoDir, ".worktrees")
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	expected := filepath.Join(repoDir, ".worktrees", ".treehouse", repoName)
	if !strings.HasPrefix(poolDir, expected) {
		t.Errorf("expected pool dir to start with %s, got %s", expected, poolDir)
	}
}

func TestResolvePoolDir_AbsoluteRoot(t *testing.T) {
	repoDir := setupGitRepo(t)
	absRoot := t.TempDir()

	poolDir, err := ResolvePoolDir(repoDir, absRoot)
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	expected := filepath.Join(absRoot, ".treehouse", repoName)
	if !strings.HasPrefix(poolDir, expected) {
		t.Errorf("expected pool dir to start with %s, got %s", expected, poolDir)
	}
}

func TestResolvePoolDir_DotSlashRoot(t *testing.T) {
	repoDir := setupGitRepo(t)

	poolDir, err := ResolvePoolDir(repoDir, "./")
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	expected := filepath.Join(repoDir, ".treehouse", repoName)
	if !strings.HasPrefix(poolDir, expected) {
		t.Errorf("expected pool dir to start with %s, got %s", expected, poolDir)
	}
}

func TestResolvePoolDir_EnvVarExpansion(t *testing.T) {
	repoDir := setupGitRepo(t)
	absRoot := t.TempDir()

	t.Setenv("TEST_TREEHOUSE_ROOT", absRoot)

	poolDir, err := ResolvePoolDir(repoDir, "$TEST_TREEHOUSE_ROOT")
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	expected := filepath.Join(absRoot, ".treehouse", repoName)
	if !strings.HasPrefix(poolDir, expected) {
		t.Errorf("expected pool dir to start with %s, got %s", expected, poolDir)
	}
}

// setupBareLayoutConfig builds a bare clone with two linked worktrees plus a
// gitdir-file parent, matching the layout treehouse must support.
func setupBareLayoutConfig(t *testing.T) (bareDir, wtMain, wtFeature, projDir string) {
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

	run(t, "", "git", "init", "--bare", "--initial-branch=main", originDir)
	run(t, "", "git", "init", "--initial-branch=main", seedDir)
	run(t, seedDir, "git", "config", "user.email", "test@test.com")
	run(t, seedDir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seedDir, "git", "add", ".")
	run(t, seedDir, "git", "commit", "-m", "initial")
	run(t, seedDir, "git", "remote", "add", "origin", originDir)
	run(t, seedDir, "git", "push", "-u", "origin", "main")

	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, "", "git", "clone", "--bare", originDir, bareDir)
	run(t, bareDir, "git", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	run(t, bareDir, "git", "fetch", "origin")
	if err := os.WriteFile(filepath.Join(projDir, ".git"), []byte("gitdir: ./.bare\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, projDir, "git", "worktree", "add", wtMain, "main")
	run(t, projDir, "git", "worktree", "add", "-b", "feature", wtFeature, "main")
	return bareDir, wtMain, wtFeature, projDir
}

// All worktrees of one repo - and the bare repo itself - must resolve to a
// single shared pool rather than one pool per checkout.
func TestResolvePoolDir_SharedAcrossWorktreesAndBare(t *testing.T) {
	bareDir, wtMain, wtFeature, projDir := setupBareLayoutConfig(t)
	root := t.TempDir() // absolute root => shared pool root regardless of caller dir

	var want string
	for i, dir := range []string{wtMain, wtFeature, bareDir, projDir} {
		got, err := ResolvePoolDir(dir, root)
		if err != nil {
			t.Fatalf("ResolvePoolDir(%s): %v", dir, err)
		}
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Errorf("ResolvePoolDir(%s) = %q, want shared %q", dir, got, want)
		}
	}

	// The pool name is keyed on the project, not a per-worktree basename.
	if name := filepath.Base(want); !strings.HasPrefix(name, "proj-") {
		t.Errorf("pool name %q should start with project name %q", name, "proj-")
	}
}

// A purely-local repo (no remote) must also share one pool across worktrees;
// the hash falls back to the common git dir, which is stable across worktrees.
func TestResolvePoolDir_LocalOnlySharedAcrossWorktrees(t *testing.T) {
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "wt")

	run(t, "", "git", "init", "--initial-branch=main", repoDir)
	run(t, repoDir, "git", "config", "user.email", "test@test.com")
	run(t, repoDir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repoDir, "git", "add", ".")
	run(t, repoDir, "git", "commit", "-m", "initial")
	run(t, repoDir, "git", "worktree", "add", "--detach", wtPath, "main")

	root := t.TempDir()
	fromRepo, err := ResolvePoolDir(repoDir, root)
	if err != nil {
		t.Fatalf("ResolvePoolDir(repo): %v", err)
	}
	fromWt, err := ResolvePoolDir(wtPath, root)
	if err != nil {
		t.Fatalf("ResolvePoolDir(worktree): %v", err)
	}
	if fromRepo != fromWt {
		t.Errorf("local-only repo: main %q and worktree %q must share one pool", fromRepo, fromWt)
	}

	// The hash is keyed on the main repo root (not the common git dir), which
	// reproduces the pre-change worktree-toplevel value so a classic
	// single-checkout local-only repo keeps its existing pool on upgrade.
	wantName := "repo-" + git.ShortHash(repoDir)
	if got := filepath.Base(fromRepo); got != wantName {
		t.Errorf("local-only pool name = %q, want %q (hash must key on main root)", got, wantName)
	}
}

func TestResolvePoolRoot_EmptyRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	poolRoot, err := ResolvePoolRoot("", "")
	if err != nil {
		t.Fatalf("ResolvePoolRoot failed: %v", err)
	}

	expected := filepath.Join(home, ".treehouse")
	if poolRoot != expected {
		t.Fatalf("expected pool root %s, got %s", expected, poolRoot)
	}
}

func TestResolvePoolRoot_RelativeRootRequiresRepo(t *testing.T) {
	if _, err := ResolvePoolRoot("", ".worktrees"); err == nil {
		t.Fatal("expected relative root without repo to fail")
	}
}
