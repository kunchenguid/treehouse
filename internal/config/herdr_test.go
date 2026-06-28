package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_IgnoresRepoHerdr(t *testing.T) {
	repoDir := t.TempDir()
	setUserHome(t, t.TempDir())

	cfgTOML := `max_trees = 4

[herdr]
enabled = false
split = "down"
focus = false
`
	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte(cfgTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Repo-level herdr settings must be ignored, leaving the safe defaults.
	if !cfg.Herdr.IsEnabled() {
		t.Error("expected repo herdr.enabled=false to be ignored (default enabled)")
	}
	if cfg.Herdr.SplitDirection() != "right" {
		t.Errorf("expected repo herdr.split to be ignored (default right), got %q", cfg.Herdr.SplitDirection())
	}
	if !cfg.Herdr.FocusNewPane() {
		t.Error("expected repo herdr.focus=false to be ignored (default focus)")
	}
}

func TestLoad_UserHerdr(t *testing.T) {
	repoDir := t.TempDir()
	userHome := t.TempDir()
	setUserHome(t, userHome)

	configDir := filepath.Join(userHome, ".config", "treehouse")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgTOML := `[herdr]
enabled = false
split = "down"
focus = false
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(cfgTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Herdr.IsEnabled() {
		t.Error("expected user herdr.enabled=false to be honored")
	}
	if cfg.Herdr.SplitDirection() != "down" {
		t.Errorf("expected user herdr.split=down, got %q", cfg.Herdr.SplitDirection())
	}
	if cfg.Herdr.FocusNewPane() {
		t.Error("expected user herdr.focus=false to be honored")
	}
}

func TestHerdrDefaults(t *testing.T) {
	var h Herdr
	if !h.IsEnabled() {
		t.Error("zero-value Herdr should be enabled by default")
	}
	if !h.FocusNewPane() {
		t.Error("zero-value Herdr should focus the new pane by default")
	}
	if h.SplitDirection() != "right" {
		t.Errorf("zero-value Herdr split default = %q, want right", h.SplitDirection())
	}
}
