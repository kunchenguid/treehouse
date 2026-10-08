package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const approvalCommands = "post_create=['echo create']\npost_get=['echo get']\npost_return=['echo return']\npre_destroy=['echo destroy']\n"

func approvalFixture(t *testing.T) string {
	t.Helper()
	setUserHome(t, t.TempDir())
	repo := t.TempDir()
	for _, name := range []string{"setup.sh", "package.json"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeRepoConfig(t, repo, "[hooks]\napproval_files=['setup.sh','package.json']\n"+approvalCommands)
	return repo
}

func assertRepositoryHooksDisabled(t *testing.T, repo string) {
	t.Helper()
	cfg, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks.PostCreate)+len(cfg.Hooks.PostGet)+len(cfg.Hooks.PostReturn)+len(cfg.Hooks.PreDestroy) != 0 {
		t.Fatalf("unapproved repository hooks loaded: %+v", cfg.Hooks)
	}
}

func TestApprovalFileListCannotBeDowngradedWithoutTrust(t *testing.T) {
	for name, list := range map[string]string{
		"remove one": "approval_files=['setup.sh']\n",
		"empty list": "approval_files=[]\n",
		"delete key": "",
	} {
		t.Run(name, func(t *testing.T) {
			repo := approvalFixture(t)
			if _, _, err := Trust(repo); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(userConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			writeRepoConfig(t, repo, "[hooks]\n"+list+approvalCommands)
			assertRepositoryHooksDisabled(t, repo)
			after, err := os.ReadFile(userConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("repository edit rewrote approval")
			}
			if _, _, err := Trust(repo); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(repo)
			if err != nil || len(cfg.Hooks.PostCreate) != 1 || len(cfg.Hooks.PostReturn) != 1 {
				t.Fatalf("explicit approval of reduced list failed: %+v, %v", cfg, err)
			}
		})
	}
}

func TestApprovalFilesChangedContentsAndMissingFile(t *testing.T) {
	for _, name := range []string{"setup.sh", "package.json"} {
		t.Run(name, func(t *testing.T) {
			repo := approvalFixture(t)
			if _, _, err := Trust(repo); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(repo, name)
			if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
				t.Fatal(err)
			}
			assertRepositoryHooksDisabled(t, repo)
			if _, _, err := Trust(repo); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(repo)
			if err != nil || len(cfg.Hooks.PostGet) != 1 {
				t.Fatalf("content reapproval failed: %+v, %v", cfg, err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			assertRepositoryHooksDisabled(t, repo)
			if _, _, err := Trust(repo); err == nil {
				t.Fatal("approved missing dependency")
			}
		})
	}
}

func TestApprovalFilesUpgradeRequiresContentApproval(t *testing.T) {
	repo := approvalFixture(t)
	// A previous version could approve this as an unknown hook key. That
	// command-only fingerprint must not authorize the new file-checking mode.
	_, _, tableHash, err := readRepoConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateTrust(RepoIdentity(repo), tableHash); err != nil {
		t.Fatal(err)
	}
	assertRepositoryHooksDisabled(t, repo)
	if _, _, err := Trust(repo); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(repo)
	if err != nil || len(cfg.Hooks.PostGet) != 1 {
		t.Fatalf("file approval failed: %+v, %v", cfg, err)
	}
}

func TestApprovalFilesRejectInvalidPathsAndTypes(t *testing.T) {
	for _, name := range []string{"escape", "absolute", "empty", "directory", "missing"} {
		t.Run(name, func(t *testing.T) {
			repo := approvalFixture(t)
			path := map[string]string{"escape": "../setup.sh", "absolute": filepath.Join(repo, "setup.sh"), "empty": "", "directory": ".", "missing": "missing.sh"}[name]
			// Encode paths correctly on Windows too.
			var buf bytes.Buffer
			cfg := Config{Hooks: Hooks{ApprovalFiles: []string{path}, PostGet: []string{"echo get"}}}
			if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
				t.Fatal(err)
			}
			writeRepoConfig(t, repo, buf.String())
			if _, _, err := Trust(repo); err == nil {
				t.Fatalf("approved %q", path)
			}
		})
	}
}

func TestApprovalFilesConfineSymlinks(t *testing.T) {
	repo := approvalFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, target, path string
		allowed            bool
	}{
		{"inside file", "setup.sh", "link", true},
		{"absolute inside file", filepath.Join(repo, "setup.sh"), "link", false},
		{"outside file", filepath.Join(outside, "file"), "link", false},
		{"outside directory", outside, "link/file", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := filepath.Join(repo, "link")
			if err := os.Symlink(tc.target, link); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			defer os.Remove(link)
			_, err := hookFingerprint("table-hash", []string{tc.path}, repo)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
}

func TestApprovalFilesRejectUnreadable(t *testing.T) {
	repo := approvalFixture(t)
	if _, _, err := Trust(repo); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "setup.sh")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o644)
	if file, err := os.Open(path); err == nil {
		_ = file.Close()
		t.Skip("filesystem or current user can read mode-000 files")
	}
	assertRepositoryHooksDisabled(t, repo)
	if _, _, err := Trust(repo); err == nil {
		t.Fatal("approved unreadable file")
	}
}

func TestApprovalFilesRejectCaseAliases(t *testing.T) {
	repo := approvalFixture(t)
	writeRepoConfig(t, repo, "[hooks]\nApproval_Files=['setup.sh']\n"+approvalCommands)
	if _, _, err := Trust(repo); err == nil || !strings.Contains(err.Error(), "approval_files") {
		t.Fatalf("expected canonical-key rejection, got %v", err)
	}
}
