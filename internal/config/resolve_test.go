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

func TestResolveRoot_Precedence(t *testing.T) {
	cfg := Config{Root: "/from/config"}

	t.Run("flag wins over env and config", func(t *testing.T) {
		t.Setenv(RootEnvVar, "/from/env")
		if got := ResolveRoot("/from/flag", cfg); got != "/from/flag" {
			t.Errorf("expected flag to win, got %q", got)
		}
	})

	t.Run("env wins over config when flag is empty", func(t *testing.T) {
		t.Setenv(RootEnvVar, "/from/env")
		if got := ResolveRoot("", cfg); got != "/from/env" {
			t.Errorf("expected env to win, got %q", got)
		}
	})

	t.Run("config used when flag and env are empty", func(t *testing.T) {
		t.Setenv(RootEnvVar, "")
		if got := ResolveRoot("", cfg); got != "/from/config" {
			t.Errorf("expected config value, got %q", got)
		}
	})

	t.Run("default empty when nothing set", func(t *testing.T) {
		t.Setenv(RootEnvVar, "")
		if got := ResolveRoot("", Config{}); got != "" {
			t.Errorf("expected empty default root, got %q", got)
		}
	})
}

func TestResolveRoot_DefaultPathByteForByte(t *testing.T) {
	// The default (unset) resolution must be byte-for-byte identical to calling
	// ResolvePoolRoot with the raw config root, i.e. no override leaks in.
	t.Setenv(RootEnvVar, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	root := ResolveRoot("", DefaultConfig())
	poolRoot, err := ResolvePoolRoot("", root)
	if err != nil {
		t.Fatalf("ResolvePoolRoot failed: %v", err)
	}
	if expected := filepath.Join(home, ".treehouse"); poolRoot != expected {
		t.Fatalf("expected default pool root %s, got %s", expected, poolRoot)
	}
}

func TestResolveRoot_FlagDotSelectsInProject(t *testing.T) {
	repoDir := setupGitRepo(t)
	t.Setenv(RootEnvVar, "")

	root := ResolveRoot(".", Config{})
	poolDir, err := ResolvePoolDir(repoDir, root)
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	expected := filepath.Join(repoDir, ".treehouse", repoName)
	if !strings.HasPrefix(poolDir, expected) {
		t.Errorf("expected in-project pool dir under %s, got %s", expected, poolDir)
	}
}
