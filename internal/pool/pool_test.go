package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupRepo(t *testing.T) (repoDir, poolDir string) {
	t.Helper()
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}

	bareDir := filepath.Join(base, "remote.git")
	repoDir = filepath.Join(base, "myrepo")
	poolDir = filepath.Join(base, "pool")

	runGit(t, "", "init", "--bare", "--initial-branch=main", bareDir)
	runGit(t, "", "init", "--initial-branch=main", repoDir)
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "remote", "add", "origin", bareDir)

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "initial")
	runGit(t, repoDir, "push", "-u", "origin", "main")
	return repoDir, poolDir
}

func runGit(t *testing.T, dir string, args ...string) {
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

func TestAcquire_RunsPostCreateHookInWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	// `echo X > sentinel.txt` works in both /bin/sh and cmd.exe.
	hook := "echo created > hook-sentinel.txt"

	wtPath, err := Acquire(repoDir, poolDir, 4, []string{hook})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if wtPath == "" {
		t.Fatal("Acquire returned empty path")
	}

	sentinel := filepath.Join(wtPath, "hook-sentinel.txt")
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected post_create hook to create %s: %v", sentinel, err)
	}
}

func TestAcquire_HookFailureDoesNotFailAcquire(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	hooks := []string{
		"this-command-does-not-exist-xyzzy",
		"echo ok > second-ran.txt",
	}

	wtPath, err := Acquire(repoDir, poolDir, 4, hooks)
	if err != nil {
		t.Fatalf("Acquire should not fail when a hook fails: %v", err)
	}
	if wtPath == "" {
		t.Fatal("Acquire returned empty path")
	}

	// The second hook must still have run despite the first failing.
	if _, err := os.Stat(filepath.Join(wtPath, "second-ran.txt")); err != nil {
		t.Fatalf("expected second hook to run despite first failing: %v", err)
	}
}

func TestDestroy_RunsPreDestroyHook(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// Capture the location to verify after destroy. We write a sentinel into
	// the *parent* of the worktree, since the worktree itself is removed.
	sentinelDir := filepath.Dir(filepath.Dir(wtPath))
	sentinel := filepath.Join(sentinelDir, "predestroy-ran.txt")

	// Hook writes to an absolute path so it survives worktree removal.
	hook := "echo bye > " + quoteForShell(sentinel)

	if err := Destroy(repoDir, poolDir, wtPath, true, []string{hook}); err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected pre_destroy hook to create %s: %v", sentinel, err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir to be removed after Destroy")
	}
}

// quoteForShell wraps a path so it survives splitting by /bin/sh or cmd.exe.
// Tests only use temp-dir paths which don't contain quotes, so simple quoting
// is sufficient.
func quoteForShell(p string) string {
	// Double-quote works in both sh and cmd.exe for paths without quotes.
	return `"` + p + `"`
}
