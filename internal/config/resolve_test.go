package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
