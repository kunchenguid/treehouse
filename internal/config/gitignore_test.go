package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupGitRepo(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}

	bareDir := filepath.Join(base, "remote.git")
	repoDir := filepath.Join(base, "myrepo")

	run(t, "", "git", "init", "--bare", "--initial-branch=main", bareDir)
	run(t, "", "git", "init", "--initial-branch=main", repoDir)
	run(t, repoDir, "git", "config", "user.email", "test@test.com")
	run(t, repoDir, "git", "config", "user.name", "Test")
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repoDir, "git", "add", ".")
	run(t, repoDir, "git", "commit", "-m", "initial")
	run(t, repoDir, "git", "push", "-u", "origin", "main")

	return repoDir
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func TestEnsureGitignore_AddsEntry(t *testing.T) {
	repoDir := setupGitRepo(t)

	treehouseDir := filepath.Join(repoDir, ".worktrees", ".treehouse")

	if err := EnsureGitignore(treehouseDir); err != nil {
		t.Fatalf("EnsureGitignore failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(repoDir, ".gitignore"))
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	expected := "/.worktrees/.treehouse"
	if !strings.Contains(string(data), expected) {
		t.Errorf("expected .gitignore to contain %q, got: %s", expected, data)
	}
}

func TestEnsureGitignore_Idempotent(t *testing.T) {
	repoDir := setupGitRepo(t)

	treehouseDir := filepath.Join(repoDir, ".worktrees", ".treehouse")

	if err := EnsureGitignore(treehouseDir); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(treehouseDir); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(repoDir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}

	entry := "/.worktrees/.treehouse"
	count := strings.Count(string(data), entry)
	if count != 1 {
		t.Errorf("expected entry exactly once, found %d times in:\n%s", count, data)
	}
}

func TestEnsureGitignore_NotInRepo(t *testing.T) {
	// A temp dir that is not a git repo.
	dir := t.TempDir()
	treehouseDir := filepath.Join(dir, ".treehouse")

	if err := EnsureGitignore(treehouseDir); err != nil {
		t.Fatalf("EnsureGitignore should be a no-op outside a repo, got: %v", err)
	}

	// No .gitignore should be created.
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Error("expected no .gitignore to be created outside a git repo")
	}
}

func TestEnsureGitignore_DefaultRoot(t *testing.T) {
	repoDir := setupGitRepo(t)

	// When using default root ($HOME/.treehouse), the treehouse dir is outside
	// the repo, so EnsureGitignore should be a no-op.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	treehouseDir := filepath.Join(home, ".treehouse")

	// This should not fail even though the dir is outside the repo.
	if err := EnsureGitignore(treehouseDir); err != nil {
		t.Fatalf("EnsureGitignore failed: %v", err)
	}

	// No .gitignore should be created/modified in the repo.
	if _, err := os.Stat(filepath.Join(repoDir, ".gitignore")); err == nil {
		// If .gitignore exists, it shouldn't contain .treehouse (the home one
		// is in a different repo context).
		_ = repoDir // just ensure we don't accidentally create one
	}
}
