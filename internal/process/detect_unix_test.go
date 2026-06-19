//go:build !windows

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// FindProcessesInWorktree should match a process whose cwd resolves to the
// same real path as the worktree, even when the caller passes a symlinked
// worktree path. This also covers macOS /tmp -> /private/tmp.
func TestFindProcessesInWorktree_ResolvesSymlinks(t *testing.T) {
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

	procs, err := FindProcessesInWorktree(linkDir)
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
		t.Fatalf("expected to find pid %d via symlinked path %q, got %v",
			cmd.Process.Pid, linkDir, procs)
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
