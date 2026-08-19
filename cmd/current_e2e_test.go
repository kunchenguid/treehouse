package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// leaseWorktree acquires a durable lease and returns its absolute path. It gives
// current tests a persistent worktree to run inside without holding a subshell.
func leaseWorktree(t *testing.T, repoDir, homeDir string) string {
	t.Helper()
	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	if wtPath == "" {
		t.Fatal("could not capture leased worktree path")
	}
	return wtPath
}

func TestCurrentInsideWorktreeReportsIt(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	wtPath := leaseWorktree(t, repoDir, homeDir)

	// buildEnv strips TREEHOUSE_DIR, so a pass proves detection is cwd-based
	// rather than relying on the env var the get subshell would export.
	stdout, stderr, code := runTreehouseFromDir(t, repoDir, wtPath, homeDir, nil, "current")
	if code != 0 {
		t.Fatalf("expected exit 0 inside worktree, got %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, filepath.Base(wtPath)) {
		t.Fatalf("expected worktree name/path in stdout, got: %q", stdout)
	}
}

func TestCurrentPathPrintsOnlyPath(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	wtPath := leaseWorktree(t, repoDir, homeDir)

	stdout, stderr, code := runTreehouseFromDir(t, repoDir, wtPath, homeDir, nil, "current", "--path")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr: %s)", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 1 || lines[0] != wtPath {
		t.Fatalf("expected exactly the worktree path on stdout, got: %q", stdout)
	}
}

func TestCurrentJSONInsideWorktree(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	wtPath := leaseWorktree(t, repoDir, homeDir)

	stdout, stderr, code := runTreehouseFromDir(t, repoDir, wtPath, homeDir, nil, "current", "--json")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, `"in_worktree":true`) {
		t.Fatalf("expected in_worktree true, got: %q", stdout)
	}
	if !strings.Contains(stdout, wtPath) {
		t.Fatalf("expected worktree path in JSON, got: %q", stdout)
	}
}

func TestCurrentOutsideWorktreeExitsNonZero(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	// Create a pool, then run from the main repo checkout (not a worktree).
	_ = leaseWorktree(t, repoDir, homeDir)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "current")
	if code == 0 {
		t.Fatalf("expected non-zero exit outside a worktree, got 0")
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty stdout outside a worktree, got: %q", stdout)
	}
	if !strings.Contains(stderr, "Not in a treehouse worktree") {
		t.Fatalf("expected human notice on stderr, got: %q", stderr)
	}
}

func TestCurrentJSONOutsideWorktreeReportsFalse(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	_ = leaseWorktree(t, repoDir, homeDir)

	stdout, _, code := runTreehouse(t, repoDir, homeDir, nil, "current", "--json")
	if code == 0 {
		t.Fatalf("expected non-zero exit outside a worktree, got 0")
	}
	if !strings.Contains(stdout, `"in_worktree":false`) {
		t.Fatalf("expected in_worktree false JSON on stdout, got: %q", stdout)
	}
}

func TestCurrentPathOutsideWorktreeIsSilent(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	_ = leaseWorktree(t, repoDir, homeDir)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "current", "--path")
	if code == 0 {
		t.Fatalf("expected non-zero exit, got 0")
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty stdout in --path mode, got: %q", stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected empty stderr in --path mode, got: %q", stderr)
	}
}

func TestCurrentNotAGitRepoExitsNonZero(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	outside := t.TempDir()

	_, _, code := runTreehouseFromDir(t, repoDir, outside, homeDir, nil, "current")
	if code == 0 {
		t.Fatalf("expected non-zero exit outside a git repository, got 0")
	}
}

func TestCurrentRejectsConflictingFlags(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	wtPath := leaseWorktree(t, repoDir, homeDir)

	_, stderr, code := runTreehouseFromDir(t, repoDir, wtPath, homeDir, nil, "current", "--path", "--json")
	if code == 0 {
		t.Fatalf("expected non-zero exit for conflicting flags, got 0")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got: %q", stderr)
	}
}
