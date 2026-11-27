package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeUserConfig(t *testing.T, homeDir, body string) string {
	t.Helper()
	dir := filepath.Join(homeDir, ".config", "treehouse")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestStatusShowsPoolLimitAndSource pins the human status contract: the
// occupied/max count, the layer that supplied max_trees, and the full-pool
// advice, while status --json stays a bare array.
func TestStatusShowsPoolLimitAndSource(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	// Built-in default, empty pool.
	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "status")
	if code != 0 {
		t.Fatalf("status failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "pool  0/16 worktrees  (max_trees from built-in default)") {
		t.Fatalf("default pool line missing: %q", stdout)
	}
	if strings.Contains(stdout, "full:") {
		t.Fatalf("empty pool must not claim to be full: %q", stdout)
	}

	// User-level config supplies it when there is no repo config.
	userCfg := writeUserConfig(t, homeDir, "max_trees = 3\n")
	stdout, _, _ = runTreehouse(t, repoDir, homeDir, nil, "status")
	if !strings.Contains(stdout, "pool  0/3 worktrees  (max_trees from user config "+userCfg+")") {
		t.Fatalf("user-sourced pool line missing: %q", stdout)
	}

	// Repo-level config wins over user config.
	repoCfg := filepath.Join(repoDir, "treehouse.toml")
	if err := os.WriteFile(repoCfg, []byte("max_trees = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, _ = runTreehouse(t, repoDir, homeDir, nil, "status")
	if !strings.Contains(stdout, "pool  0/2 worktrees  (max_trees from repo config "+repoCfg+")") {
		t.Fatalf("repo-sourced pool line missing: %q", stdout)
	}

	// Fill the pool with two durable leases.
	for i := 0; i < 2; i++ {
		if _, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--no-fetch"); code != 0 {
			t.Fatalf("lease %d failed (%d): %s", i, code, stderr)
		}
	}
	stdout, _, _ = runTreehouse(t, repoDir, homeDir, nil, "status")
	if !strings.Contains(stdout, "pool  2/2 worktrees") || !strings.Contains(stdout, "full:") ||
		!strings.Contains(stdout, "set max_trees to a higher value in "+repoCfg) {
		t.Fatalf("full pool line missing advice: %q", stdout)
	}

	// A third get is refused, keeps the allocator message first, and explains
	// the source and how to raise it, on stderr only.
	out, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--no-fetch")
	if code == 0 {
		t.Fatalf("third lease must fail, stdout=%q", out)
	}
	if out != "" {
		t.Fatalf("failed get must keep stdout empty, got %q", out)
	}
	if !strings.Contains(stderr, "all 2 worktrees are in use or dirty (max_trees = 2)") ||
		!strings.Contains(stderr, "max_trees = 2 comes from the repo config "+repoCfg) ||
		!strings.Contains(stderr, "set max_trees to a higher value in "+repoCfg) {
		t.Fatalf("pool-full get lacks source advice: %q", stderr)
	}

	// Machine output is unchanged: a bare array of worktrees.
	jsonOut, _, code := runTreehouse(t, repoDir, homeDir, nil, "status", "--json")
	if code != 0 {
		t.Fatalf("status --json failed")
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &items); err != nil || len(items) != 2 {
		t.Fatalf("status --json must stay a 2-element array, got %q (%v)", jsonOut, err)
	}
}

// TestStatusReportsDefaultWhenRepoConfigLacksMaxTrees covers a repo config
// without max_trees: it keeps the built-in default, and status must say so.
func TestStatusReportsDefaultWhenRepoConfigLacksMaxTrees(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("# no max_trees\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, _ := runTreehouse(t, repoDir, homeDir, nil, "status")
	if !strings.Contains(stdout, "pool  0/16 worktrees  (max_trees from built-in default)") {
		t.Fatalf("repo config without max_trees must report the default: %q", stdout)
	}
}
