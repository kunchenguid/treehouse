package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAPFSSharingResolution(t *testing.T) {
	for _, tc := range []struct {
		name, flag, env, configured string
		enabled, invalid            bool
	}{
		{name: "default off"},
		{name: "config opt in", configured: "fresh", enabled: true},
		{name: "environment opt out", configured: "fresh", env: "off"},
		{name: "environment opt in", configured: "off", env: "fresh", enabled: true},
		{name: "flag opt out", env: "fresh", flag: "off"},
		{name: "flag opt in", env: "off", flag: "fresh", enabled: true},
		{name: "bad config", configured: "auto", invalid: true},
		{name: "bad env", env: "yes", invalid: true},
		{name: "bad flag", flag: "true", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(APFSSharingEnvVar, tc.env)
			got, err := ResolveAPFSSharing(tc.flag, Config{APFSSharing: tc.configured})
			if (err != nil) != tc.invalid || got != tc.enabled {
				t.Fatalf("got %t, %v; want enabled=%t invalid=%t", got, err, tc.enabled, tc.invalid)
			}
		})
	}
}

func TestAPFSSharingLoadsThroughConfig(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	setUserHome(t, home)
	t.Setenv(APFSSharingEnvVar, "")
	userDir := filepath.Join(home, ".config", "treehouse")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "config.toml"), []byte("apfs_sharing = \"fresh\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if enabled, err := ResolveAPFSSharing("", cfg); err != nil || !enabled {
		t.Fatalf("user config: %+v %v", cfg, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "treehouse.toml"), []byte("apfs_sharing = \"off\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if enabled, err := ResolveAPFSSharing("", cfg); err != nil || enabled {
		t.Fatalf("repo opt-out: %+v %v", cfg, err)
	}
}
