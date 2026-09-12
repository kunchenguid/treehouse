package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_WorktreePathDefaultsToEmpty(t *testing.T) {
	setUserHome(t, t.TempDir())

	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.WorktreePath != "" {
		t.Errorf("WorktreePath: got %q, want empty", cfg.WorktreePath)
	}
}

func TestLoad_WorktreePathFromRepoConfig(t *testing.T) {
	repoDir := t.TempDir()
	setUserHome(t, t.TempDir())

	cfgTOML := "max_trees = 4\nworktree_path = \"{repo_parent}/{repo}-{slot}\"\n"
	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte(cfgTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.WorktreePath != "{repo_parent}/{repo}-{slot}" {
		t.Errorf("WorktreePath: got %q, want the repo-level template", cfg.WorktreePath)
	}
}

func TestLoad_WorktreePathFromUserConfig(t *testing.T) {
	repoDir := t.TempDir()
	userHome := t.TempDir()
	setUserHome(t, userHome)

	configDir := filepath.Join(userHome, ".config", "treehouse")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("worktree_path = \"{pool}/{slot}/{repo}-{slot}\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.WorktreePath != "{pool}/{slot}/{repo}-{slot}" {
		t.Errorf("WorktreePath: got %q, want the user-level template", cfg.WorktreePath)
	}
}

func TestResolveWorktreePath_Precedence(t *testing.T) {
	cfg := Config{WorktreePath: "{pool}/{slot}/from-config"}

	t.Run("flag wins over env and config", func(t *testing.T) {
		t.Setenv(WorktreePathEnvVar, "{pool}/{slot}/from-env")
		if got := ResolveWorktreePath("{pool}/{slot}/from-flag", cfg); got != "{pool}/{slot}/from-flag" {
			t.Errorf("expected flag to win, got %q", got)
		}
	})

	t.Run("env wins over config when flag is empty", func(t *testing.T) {
		t.Setenv(WorktreePathEnvVar, "{pool}/{slot}/from-env")
		if got := ResolveWorktreePath("", cfg); got != "{pool}/{slot}/from-env" {
			t.Errorf("expected env to win, got %q", got)
		}
	})

	t.Run("config used when flag and env are empty", func(t *testing.T) {
		t.Setenv(WorktreePathEnvVar, "")
		if got := ResolveWorktreePath("", cfg); got != "{pool}/{slot}/from-config" {
			t.Errorf("expected config value, got %q", got)
		}
	})

	t.Run("empty default when nothing set", func(t *testing.T) {
		t.Setenv(WorktreePathEnvVar, "")
		if got := ResolveWorktreePath("", Config{}); got != "" {
			t.Errorf("expected empty default template, got %q", got)
		}
	})

	t.Run("default config sets no template", func(t *testing.T) {
		t.Setenv(WorktreePathEnvVar, "")
		if got := ResolveWorktreePath("", DefaultConfig()); got != "" {
			t.Errorf("expected the default config to leave the layout alone, got %q", got)
		}
	})
}
