package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/treehouse/v3/internal/pool"
	"github.com/kunchenguid/treehouse/v3/internal/process"
)

// setupAtomicFixture builds a real local repo and pool, acquires one worktree,
// and returns the pool directory and the acquired worktree path.
func setupAtomicFixture(t *testing.T) (poolDir, wtPath string) {
	t.Helper()
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(base, "myrepo")
	poolDir = filepath.Join(base, "pool")

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if dir != "" {
			cmd.Dir = dir
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("", "init", "--initial-branch=main", repoDir)
	git(repoDir, "config", "user.email", "test@test.com")
	git(repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repoDir, "add", ".")
	git(repoDir, "commit", "-m", "initial")

	wtPath, err = pool.AcquireWithOptions(repoDir, poolDir, 4, nil, pool.AcquireOptions{SkipFetch: true})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	return poolDir, wtPath
}

// A writer that is still live when the worktree is about to be reset must not
// have the slot reset from under it. The emptiness check runs as the release's
// beforeReset step - under the same state lock and immediately before the reset -
// so a surviving writer fails the whole return without the reset ever running.
func TestReturnWorktreeToPool_SurvivingWriterBlocksReset(t *testing.T) {
	poolDir, wtPath := setupAtomicFixture(t)

	// An untracked file that only the reset (git clean -fd) would remove.
	marker := filepath.Join(wtPath, "writer-scratch.txt")
	if err := os.WriteFile(marker, []byte("in use\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := swapProcessSeams(
		func(string, time.Duration) ([]process.ProcessInfo, error) { return nil, nil },
		func(string) ([]process.ProcessInfo, error) {
			return []process.ProcessInfo{{PID: 4321, Name: "agent"}}, nil
		},
	)
	t.Cleanup(restore)

	err := returnWorktreeToPool(poolDir, wtPath, "", pool.ReleasePreconditions{})
	if err == nil {
		t.Fatal("expected returnWorktreeToPool to fail when a live writer remains")
	}
	if !strings.Contains(err.Error(), "still has live processes") {
		t.Fatalf("expected a live-writer refusal, got: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("worktree was reset despite a surviving writer: %v", statErr)
	}
}

// The happy path: a quiet worktree is reset and returned, proving the same
// locked transaction still resets once the emptiness check passes.
func TestReturnWorktreeToPool_QuietWorktreeResetsAndReturns(t *testing.T) {
	poolDir, wtPath := setupAtomicFixture(t)

	marker := filepath.Join(wtPath, "writer-scratch.txt")
	if err := os.WriteFile(marker, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := swapProcessSeams(
		func(string, time.Duration) ([]process.ProcessInfo, error) { return nil, nil },
		func(string) ([]process.ProcessInfo, error) { return nil, nil },
	)
	t.Cleanup(restore)

	if err := returnWorktreeToPool(poolDir, wtPath, "", pool.ReleasePreconditions{}); err != nil {
		t.Fatalf("expected a quiet worktree to return cleanly, got: %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("expected the reset to remove untracked files, stat err: %v", statErr)
	}
}
