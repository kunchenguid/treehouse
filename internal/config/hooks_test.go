package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoad_Hooks(t *testing.T) {
	repoDir := t.TempDir()

	cfgTOML := `max_trees = 4

[hooks]
post_create = ["echo a", "echo b"]
pre_destroy = ["echo c"]
`
	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte(cfgTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	wantPost := []string{"echo a", "echo b"}
	wantPre := []string{"echo c"}
	if !reflect.DeepEqual(cfg.Hooks.PostCreate, wantPost) {
		t.Errorf("PostCreate: got %v, want %v", cfg.Hooks.PostCreate, wantPost)
	}
	if !reflect.DeepEqual(cfg.Hooks.PreDestroy, wantPre) {
		t.Errorf("PreDestroy: got %v, want %v", cfg.Hooks.PreDestroy, wantPre)
	}
}

func TestLoad_HooksDefaultEmpty(t *testing.T) {
	repoDir := t.TempDir()

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(cfg.Hooks.PostCreate) != 0 {
		t.Errorf("expected empty PostCreate, got %v", cfg.Hooks.PostCreate)
	}
	if len(cfg.Hooks.PreDestroy) != 0 {
		t.Errorf("expected empty PreDestroy, got %v", cfg.Hooks.PreDestroy)
	}
}
