package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/internal/fileclone"
)

func addSharingAsset(t *testing.T, repo string) []byte {
	t.Helper()
	data := bytes.Repeat([]byte("independent tracked asset\n"), 8192)
	if err := os.WriteFile(filepath.Join(repo, "asset.bin"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "asset.bin")
	gitCmd(t, repo, "commit", "-m", "tracked asset")
	return data
}

func TestGetAPFSSharingDefaultAndOptOut(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		env          []string
		args         []string
	}{
		{name: "default"},
		{name: "environment opt out", config: "apfs_sharing = \"fresh\"\n", env: []string{"TREEHOUSE_APFS_SHARING=off"}},
		{name: "flag opt out", env: []string{"TREEHOUSE_APFS_SHARING=fresh"}, args: []string{"--apfs-sharing", "off"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, home := setupTestRepo(t)
			addSharingAsset(t, repo)
			if tc.config != "" {
				writeRepoConfig(t, repo, tc.config)
			}
			out, stderr, code := runTreehouse(t, repo, home, tc.env, append([]string{"get", "--lease", "--no-fetch"}, tc.args...)...)
			if code != 0 || strings.Contains(stderr, "APFS sharing") {
				t.Fatalf("default/opt-out: %s %s %d", out, stderr, code)
			}
			if _, err := os.Stat(filepath.Join(strings.TrimSpace(out), "asset.bin")); err != nil {
				t.Fatalf("path-only stdout: %q %v", out, err)
			}
		})
	}
}

func TestGetAPFSSharingRejectsInvalidSettingBeforeAllocation(t *testing.T) {
	repo, home := setupTestRepo(t)
	out, stderr, code := runTreehouse(t, repo, home, nil, "get", "--lease", "--apfs-sharing", "auto")
	if code == 0 || out != "" || !strings.Contains(stderr, "off or fresh") {
		t.Fatalf("invalid setting: %q %q %d", out, stderr, code)
	}
	if _, err := os.Stat(filepath.Join(home, ".treehouse")); !os.IsNotExist(err) {
		t.Fatalf("invalid setting allocated pool: %v", err)
	}
}

func requireSharingVolume(t *testing.T, repo string) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		if reason := fileclone.FilesystemReason(repo, repo); reason != "" {
			t.Skip(reason)
		}
	}
}

func TestGetAPFSSharingFreshJSONAndReuse(t *testing.T) {
	repo, home := setupTestRepo(t)
	requireSharingVolume(t, repo)
	original := addSharingAsset(t, repo)
	out, stderr, code := runTreehouse(t, repo, home, nil, "get", "--lease", "--json", "--no-fetch", "--branch", "shared-feature", "--apfs-sharing", "fresh")
	if code != 0 {
		t.Fatalf("fresh sharing: %s %d", stderr, code)
	}
	var lease struct {
		Path    string `json:"path"`
		LeaseID string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(out), &lease); err != nil || lease.Path == "" || lease.LeaseID == "" {
		t.Fatalf("contaminated JSON: %q %v", out, err)
	}
	if runtime.GOOS == "darwin" {
		if !strings.Contains(stderr, "cloned=1") {
			t.Fatalf("APFS temporary filesystem required for this test: %s", stderr)
		}
		if !strings.Contains(stderr, "private_data_reduced_bytes=") {
			t.Fatal("allocation not distinguished from logical bytes")
		}
	} else if !strings.Contains(stderr, "requires macOS APFS") {
		t.Fatalf("unsupported OS did not skip: %s", stderr)
	}
	got, err := os.ReadFile(filepath.Join(lease.Path, "asset.bin"))
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("checkout content changed")
	}
	// Changing a clone must not change the main checkout.
	if err := os.WriteFile(filepath.Join(lease.Path, "asset.bin"), []byte("slot edit"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(repo, "asset.bin"))
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("slot edit propagated to main")
	}
	// Restore through Git in this disposable test slot, then return/reacquire.
	gitCmd(t, lease.Path, "checkout", "--", "asset.bin")
	if _, stderr, code := runTreehouse(t, repo, home, nil, "return", lease.Path); code != 0 {
		t.Fatalf("return: %s", stderr)
	}
	out, stderr, code = runTreehouse(t, repo, home, nil, "get", "--lease", "--no-fetch", "--apfs-sharing", "fresh")
	if code != 0 || strings.TrimSpace(out) != lease.Path || strings.Contains(stderr, "APFS sharing") {
		t.Fatalf("reused slot swept: %q %q %d", out, stderr, code)
	}
}

func TestGetAPFSSharingBeforeTreehouseHook(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("APFS and POSIX hook test")
	}
	repo, home := setupTestRepo(t)
	requireSharingVolume(t, repo)
	original := addSharingAsset(t, repo)
	configDir := filepath.Join(home, ".config", "treehouse")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "apfs_sharing = \"fresh\"\n[hooks]\npost_create = [\"printf hook-output > asset.bin\"]\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runTreehouse(t, repo, home, nil, "get", "--lease", "--no-fetch")
	if code != 0 || !strings.Contains(stderr, "cloned=1") {
		t.Fatalf("sharing before hook: %q %q %d", out, stderr, code)
	}
	got, err := os.ReadFile(filepath.Join(strings.TrimSpace(out), "asset.bin"))
	if err != nil || string(got) != "hook-output" {
		t.Fatalf("hook output lost: %q %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(repo, "asset.bin"))
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("hook changed donor")
	}
}

func TestGetAPFSSharingSkipsGitHooksAndFilters(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("APFS safety gates")
	}
	for _, kind := range []string{"hook", "filter", "fsmonitor"} {
		t.Run(kind, func(t *testing.T) {
			repo, home := setupTestRepo(t)
			requireSharingVolume(t, repo)
			original := addSharingAsset(t, repo)
			if kind == "fsmonitor" {
				gitCmd(t, repo, "config", "core.fsmonitor", "/usr/bin/false")
			} else if kind == "hook" {
				if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "post-checkout"), []byte("#!/bin/sh\nprintf hook-ran > hook-result\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("asset.bin filter=unconfigured\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitCmd(t, repo, "add", ".gitattributes")
				gitCmd(t, repo, "commit", "-m", "filter attribute")
			}
			out, stderr, code := runTreehouse(t, repo, home, nil, "get", "--lease", "--no-fetch", "--apfs-sharing", "fresh")
			if code != 0 || !strings.Contains(stderr, "may have started a writer") {
				t.Fatalf("unsafe setup not skipped: %q %q %d", out, stderr, code)
			}
			got, err := os.ReadFile(filepath.Join(strings.TrimSpace(out), "asset.bin"))
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("skip changed checkout")
			}
		})
	}
}
