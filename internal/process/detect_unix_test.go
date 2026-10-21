//go:build !windows

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// FindProcessesInWorktree should match a process whose cwd resolves to the
// same real path as the worktree, even when the caller passes a symlinked
// worktree path. This also covers macOS /tmp -> /private/tmp.
func TestFindProcessesInWorktrees_ResolvesSymlinksAndSeparatesRoots(t *testing.T) {
	realDir := t.TempDir()

	linkDir := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cmd := exec.Command("sleep", "60")
	cmd.Dir = realDir
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	time.Sleep(200 * time.Millisecond)

	roots := []string{linkDir, realDir, filepath.Dir(realDir), realDir + "-sibling"}
	byWorktree, err := FindProcessesInWorktrees(roots)
	if err != nil {
		t.Fatalf("FindProcessesInWorktrees: %v", err)
	}
	for i, root := range roots {
		found := slices.ContainsFunc(byWorktree[root], func(p ProcessInfo) bool {
			return int(p.PID) == cmd.Process.Pid
		})
		if want := i < 3; found != want {
			t.Fatalf("pid %d in %q: found=%v want=%v; processes=%v", cmd.Process.Pid, root, found, want, byWorktree[root])
		}
	}
}

// UnprotectedProcessesInWorktree must report a foreign process in the worktree
// while excluding the caller, so a return run from inside its own worktree is
// not blocked by itself yet still refuses when another writer is present.
func TestUnprotectedProcessesInWorktree_ExcludesCallerIncludesForeign(t *testing.T) {
	worktreeDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sleep", "60")
	cmd.Dir = worktreeDir
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	time.Sleep(200 * time.Millisecond)

	// Stand the caller inside the worktree so the exclusion is exercised.
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })

	procs, err := UnprotectedProcessesInWorktree(worktreeDir)
	if err != nil {
		t.Fatalf("UnprotectedProcessesInWorktree: %v", err)
	}

	var foundForeign, foundCaller bool
	for _, p := range procs {
		if int(p.PID) == cmd.Process.Pid {
			foundForeign = true
		}
		if int(p.PID) == os.Getpid() {
			foundCaller = true
		}
	}
	if !foundForeign {
		t.Fatalf("expected foreign process %d reported, got %v", cmd.Process.Pid, procs)
	}
	if foundCaller {
		t.Fatalf("expected the caller %d excluded, got %v", os.Getpid(), procs)
	}
}

func TestFindProcessesInWorktree_IncludesDotDotPrefixedChild(t *testing.T) {
	worktreeDir := t.TempDir()
	childDir := filepath.Join(worktreeDir, "..cache")
	if err := os.Mkdir(childDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sleep", "60")
	cmd.Dir = childDir
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	time.Sleep(200 * time.Millisecond)

	procs, err := FindProcessesInWorktree(worktreeDir)
	if err != nil {
		t.Fatalf("FindProcessesInWorktree: %v", err)
	}

	var found bool
	for _, p := range procs {
		if int(p.PID) == cmd.Process.Pid {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected to find pid %d in dot-dot-prefixed child, got %v", cmd.Process.Pid, procs)
	}
}
