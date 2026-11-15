package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/config"
)

func TestGetDefaultLayoutPlacesWorktreeInThePool(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	want := filepath.Join(homeDir, ".treehouse")
	if !strings.HasPrefix(wtPath, want) {
		t.Fatalf("acquired %s, want a path under %s", wtPath, want)
	}
	if filepath.Base(wtPath) != "myrepo" || filepath.Base(filepath.Dir(wtPath)) != "1" {
		t.Errorf("acquired %s, want the built-in <slot>/<repo> layout", wtPath)
	}
}

func TestGetWorktreePathFlagPlacesWorktreeBesideTheRepository(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil,
		"get", "--lease", "--worktree-path", "{repo_parent}/{repo}-{slot}")
	if code != 0 {
		t.Fatalf("get --lease --worktree-path failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	if want := filepath.Join(filepath.Dir(repoDir), "myrepo-1"); wtPath != want {
		t.Fatalf("acquired %s, want %s", wtPath, want)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Errorf("expected a populated worktree at %s: %v", wtPath, err)
	}

	statusOut, _, statusCode := runTreehouse(t, repoDir, homeDir, nil, "status")
	if statusCode != 0 {
		t.Fatalf("status failed: %s", statusOut)
	}
	if !strings.Contains(statusOut, "myrepo-1") {
		t.Errorf("expected status to report the templated worktree, got %q", statusOut)
	}

	_, returnStderr, returnCode := runTreehouse(t, repoDir, homeDir, nil, "return", wtPath)
	if returnCode != 0 {
		t.Fatalf("return failed (code %d): %s", returnCode, returnStderr)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("expected return to keep the worktree in place: %v", err)
	}
}

func TestGetWorktreePathFromConfigAndEnv(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	writeRepoConfig(t, repoDir, "worktree_path = \"{repo_parent}/{repo}-from-config-{slot}\"\n")

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	if want := filepath.Join(filepath.Dir(repoDir), "myrepo-from-config-1"); strings.TrimSpace(stdout) != want {
		t.Fatalf("config template gave %s, want %s", strings.TrimSpace(stdout), want)
	}

	env := []string{"TREEHOUSE_WORKTREE_PATH={repo_parent}/{repo}-from-env-{slot}"}
	stdout, stderr, code = runTreehouse(t, repoDir, homeDir, env, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease with TREEHOUSE_WORKTREE_PATH failed (code %d): %s", code, stderr)
	}
	if want := filepath.Join(filepath.Dir(repoDir), "myrepo-from-env-2"); strings.TrimSpace(stdout) != want {
		t.Fatalf("env template gave %s, want %s", strings.TrimSpace(stdout), want)
	}

	stdout, stderr, code = runTreehouse(t, repoDir, homeDir, env,
		"get", "--lease", "--worktree-path", "{repo_parent}/{repo}-from-flag-{slot}")
	if code != 0 {
		t.Fatalf("get --lease --worktree-path failed (code %d): %s", code, stderr)
	}
	if want := filepath.Join(filepath.Dir(repoDir), "myrepo-from-flag-3"); strings.TrimSpace(stdout) != want {
		t.Fatalf("flag template gave %s, want %s", strings.TrimSpace(stdout), want)
	}
}

func TestInitDocumentsWorktreePathWithoutSettingIt(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	_, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "init")
	if code != 0 {
		t.Fatalf("init failed (code %d): %s", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(repoDir, "treehouse.toml"))
	if err != nil {
		t.Fatalf("treehouse.toml not created: %v", err)
	}
	if !strings.Contains(string(data), "worktree_path") {
		t.Errorf("generated config does not document worktree_path: %s", data)
	}

	cfg, err := config.Load(repoDir)
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if cfg.WorktreePath != "" {
		t.Errorf("generated config sets worktree_path to %q instead of documenting it", cfg.WorktreePath)
	}
}

func TestGetInvalidWorktreePathFailsClosed(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil,
		"get", "--lease", "--worktree-path", "{repo_parent}/{repo}-fixed")
	if code == 0 {
		t.Fatalf("a template without {slot} succeeded: stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stderr, "{slot}") {
		t.Errorf("expected the error to name {slot}, got %q", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected no path on stdout for a failed acquire, got %q", stdout)
	}

	statusOut, _, _ := runTreehouse(t, repoDir, homeDir, nil, "status")
	if strings.Contains(statusOut, "myrepo-fixed") {
		t.Errorf("expected no worktree to be registered, got %q", statusOut)
	}
}

func TestIgnoredRepositoryLocalWorktreeLifecycle(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, ".git", "info", "exclude"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	template := "{repo_parent}/{repo}/.worktrees/{slot}"
	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil,
		"get", "--lease", "--worktree-path", template)
	if code != 0 {
		t.Fatalf("acquire failed (%d): %s", code, stderr)
	}
	path := strings.TrimSpace(stdout)
	if want := filepath.Join(repoDir, ".worktrees", "1"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(filepath.Join(path, "README.md")); err != nil {
		t.Fatal(err)
	}
	status, _, code := runTreehouse(t, repoDir, homeDir, nil, "status", "--json")
	var entries []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(status), &entries); err != nil {
		t.Fatal(err)
	}
	if code != 0 || len(entries) != 1 || entries[0].Path != path {
		t.Fatalf("status = %s (%d)", status, code)
	}
	_, stderr, code = runTreehouse(t, homeDir, homeDir, nil, "return", path)
	if code != 0 {
		t.Fatalf("return from outside repository failed (%d): %s", code, stderr)
	}
	stdout, stderr, code = runTreehouse(t, repoDir, homeDir, nil,
		"get", "--lease", "--worktree-path", template)
	if code != 0 || strings.TrimSpace(stdout) != path {
		t.Fatalf("reuse = %q (%d): %s", stdout, code, stderr)
	}
	_, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "return", path)
	if code != 0 {
		t.Fatalf("return failed (%d): %s", code, stderr)
	}
	_, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "prune", "--yes")
	if code != 0 {
		t.Fatalf("prune failed (%d): %s", code, stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pruned checkout remains: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("parent was removed: %v", err)
	}
}
