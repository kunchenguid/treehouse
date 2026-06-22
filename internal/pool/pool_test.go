package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setupRepo(t *testing.T) (repoDir, poolDir string) {
	t.Helper()
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}

	bareDir := filepath.Join(base, "remote.git")
	repoDir = filepath.Join(base, "myrepo")
	poolDir = filepath.Join(base, "pool")

	runGit(t, "", "init", "--bare", "--initial-branch=main", bareDir)
	runGit(t, "", "init", "--initial-branch=main", repoDir)
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "remote", "add", "origin", bareDir)

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "initial")
	runGit(t, repoDir, "push", "-u", "origin", "main")
	return repoDir, poolDir
}

func setupLocalRepo(t *testing.T) (repoDir, poolDir string) {
	t.Helper()
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}

	repoDir = filepath.Join(base, "myrepo")
	poolDir = filepath.Join(base, "pool")

	runGit(t, "", "init", "--initial-branch=main", repoDir)
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "initial")
	return repoDir, poolDir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestAcquire_RunsPostCreateHookInWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	// `echo X > sentinel.txt` works in both /bin/sh and cmd.exe.
	hook := "echo created > hook-sentinel.txt"

	wtPath, err := Acquire(repoDir, poolDir, 4, []string{hook})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if wtPath == "" {
		t.Fatal("Acquire returned empty path")
	}

	sentinel := filepath.Join(wtPath, "hook-sentinel.txt")
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected post_create hook to create %s: %v", sentinel, err)
	}
}

func TestAcquire_HookFailureDoesNotFailAcquire(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	hooks := []string{
		"this-command-does-not-exist-xyzzy",
		"echo ok > second-ran.txt",
	}

	wtPath, err := Acquire(repoDir, poolDir, 4, hooks)
	if err != nil {
		t.Fatalf("Acquire should not fail when a hook fails: %v", err)
	}
	if wtPath == "" {
		t.Fatal("Acquire returned empty path")
	}

	// The second hook must still have run despite the first failing.
	if _, err := os.Stat(filepath.Join(wtPath, "second-ran.txt")); err != nil {
		t.Fatalf("expected second hook to run despite first failing: %v", err)
	}
}

func TestAcquire_RunsPostCreateHookAfterReleasingStateLock(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	sentinel := filepath.Join(t.TempDir(), "lock-probe.txt")
	hook := quoteForShell(os.Args[0]) + " -test.run=TestHookLockProbe -- " + quoteForShell(poolDir) + " " + quoteForShell(sentinel)

	if _, err := Acquire(repoDir, poolDir, 4, []string{hook}); err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("expected lock probe output: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "unlocked" {
		t.Fatalf("expected hook to run after state lock release, got %q", got)
	}
}

func TestAcquire_DoesNotReuseWorktreeReservedByPostCreateHook(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	sentinel := filepath.Join(t.TempDir(), "acquired.txt")
	hookCwd := t.TempDir()
	hook := quoteForShell(os.Args[0]) + " -test.run=TestAcquireDuringHookProbe -- " + quoteForShell(repoDir) + " " + quoteForShell(poolDir) + " " + quoteForShell(sentinel) + " " + quoteForShell(hookCwd)

	wtPath, err := Acquire(repoDir, poolDir, 4, []string{hook})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	acquiredData, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("expected hook acquire output: %v", err)
	}
	acquired := strings.TrimSpace(string(acquiredData))
	if acquired == wtPath {
		t.Fatalf("hook acquire reused reserved worktree %s", wtPath)
	}
}

func TestRelease_DoesNotDependOnCurrentWorkingDirectory(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalCwd); err != nil {
			t.Fatalf("restore cwd failed: %v", err)
		}
	})
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
}

func TestList_RecoversDestroyingWorktreeWhenOwnerIsGone(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	state.Worktrees[0].Destroying = true
	state.Worktrees[0].OwnerPID = 999999
	if err := WriteState(poolDir, state); err != nil {
		t.Fatalf("WriteState failed: %v", err)
	}

	statuses, err := List(poolDir)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Path != wtPath {
		t.Fatalf("expected stale destroying worktree to be visible, got %#v", statuses)
	}

	state, err = ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if state.Worktrees[0].Destroying || state.Worktrees[0].OwnerPID != 0 {
		t.Fatalf("expected stale destroy reservation to be cleared, got %#v", state.Worktrees[0])
	}
}

func TestList_RecoversDestroyingWorktreeWhenOwnerIdentityDoesNotMatch(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	state.Worktrees[0].Destroying = true
	state.Worktrees[0].OwnerPID = int32(os.Getpid())
	state.Worktrees[0].OwnerStartedAt = 1
	if err := WriteState(poolDir, state); err != nil {
		t.Fatalf("WriteState failed: %v", err)
	}

	statuses, err := List(poolDir)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Path != wtPath {
		t.Fatalf("expected stale destroying worktree to be visible, got %#v", statuses)
	}

	state, err = ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if state.Worktrees[0].Destroying || state.Worktrees[0].OwnerPID != 0 || state.Worktrees[0].OwnerStartedAt != 0 {
		t.Fatalf("expected stale owner reservation to be cleared, got %#v", state.Worktrees[0])
	}
}

func TestList_ShowsReservedWorktreeAsInUse(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	statuses, err := List(poolDir)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Path != wtPath || statuses[0].Status != StatusInUse {
		t.Fatalf("expected reserved worktree to be listed as in-use, got %#v", statuses)
	}
}

func TestHookLockProbe(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-3] != "--" {
		return
	}

	poolDir := os.Args[len(os.Args)-2]
	sentinel := os.Args[len(os.Args)-1]
	done := make(chan error, 1)
	go func() {
		done <- WithStateLock(poolDir, func() error {
			return os.WriteFile(sentinel, []byte("unlocked\n"), 0o644)
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		if err := os.WriteFile(sentinel, []byte("locked\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Fatal("state lock was still held while hook ran")
	}
}

func TestDestroy_RunsPreDestroyHook(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// Capture the location to verify after destroy. We write a sentinel into
	// the *parent* of the worktree, since the worktree itself is removed.
	sentinelDir := filepath.Dir(filepath.Dir(wtPath))
	sentinel := filepath.Join(sentinelDir, "predestroy-ran.txt")

	// Hook writes to an absolute path so it survives worktree removal.
	hook := "echo bye > " + quoteForShell(sentinel)

	if err := Destroy(repoDir, poolDir, wtPath, true, []string{hook}); err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected pre_destroy hook to create %s: %v", sentinel, err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir to be removed after Destroy")
	}
}

func TestDestroy_DoesNotAllowHookAcquireToReusePendingDestroyWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	sentinel := filepath.Join(t.TempDir(), "acquired.txt")
	hook := quoteForShell(os.Args[0]) + " -test.run=TestAcquireDuringHookProbe -- " + quoteForShell(repoDir) + " " + quoteForShell(poolDir) + " " + quoteForShell(sentinel)

	if err := Destroy(repoDir, poolDir, wtPath, true, []string{hook}); err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}

	acquiredData, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("expected hook acquire output: %v", err)
	}
	acquired := strings.TrimSpace(string(acquiredData))
	if acquired == wtPath {
		t.Fatalf("hook acquire reused pending destroy worktree %s", wtPath)
	}
	if _, err := os.Stat(acquired); err != nil {
		t.Fatalf("expected hook-acquired worktree to remain on disk: %v", err)
	}
}

func TestDestroy_NonForceRejectsReservedWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	err = Destroy(repoDir, poolDir, wtPath, false, nil)
	if err == nil {
		t.Fatal("expected non-force Destroy to reject reserved worktree")
	}
	if !strings.Contains(err.Error(), "is in use") {
		t.Fatalf("expected in-use error, got %v", err)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected reserved worktree to remain on disk: %v", err)
	}
}

func TestDestroy_PreservesSupersededReservationAfterHook(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	sentinel := filepath.Join(wtPath, "superseded.txt")
	hook := quoteForShell(os.Args[0]) + " -test.run=TestSupersedeDestroyReservationProbe -- " + quoteForShell(poolDir) + " " + quoteForShell(wtPath) + " " + quoteForShell(sentinel)

	if err := Destroy(repoDir, poolDir, wtPath, true, []string{hook}); err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected superseded worktree to remain on disk: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0].Path != wtPath || state.Worktrees[0].Destroying {
		t.Fatalf("expected superseded state entry to remain available, got %#v", state.Worktrees)
	}
}

func TestDestroyAll_PreservesWorktreeAcquiredByHook(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	if _, err := Acquire(repoDir, poolDir, 4, nil); err != nil {
		t.Fatalf("first Acquire failed: %v", err)
	}
	if _, err := Acquire(repoDir, poolDir, 4, nil); err != nil {
		t.Fatalf("second Acquire failed: %v", err)
	}

	sentinel := filepath.Join(t.TempDir(), "acquired.txt")
	hook := quoteForShell(os.Args[0]) + " -test.run=TestAcquireDuringHookProbe -- " + quoteForShell(repoDir) + " " + quoteForShell(poolDir) + " " + quoteForShell(sentinel)

	if err := DestroyAll(repoDir, poolDir, true, []string{hook}); err != nil {
		t.Fatalf("DestroyAll failed: %v", err)
	}

	acquiredData, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("expected hook acquire output: %v", err)
	}
	acquired := strings.TrimSpace(string(acquiredData))
	if _, err := os.Stat(acquired); err != nil {
		t.Fatalf("expected hook-acquired worktree to remain on disk: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0].Path != acquired {
		t.Fatalf("expected state to preserve hook-acquired worktree %s, got %#v", acquired, state.Worktrees)
	}
}

func TestDestroyAll_PreservesSupersededReservationAfterHook(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	sentinel := filepath.Join(wtPath, "superseded.txt")
	hook := quoteForShell(os.Args[0]) + " -test.run=TestSupersedeDestroyReservationProbe -- " + quoteForShell(poolDir) + " " + quoteForShell(wtPath) + " " + quoteForShell(sentinel)

	if err := DestroyAll(repoDir, poolDir, true, []string{hook}); err != nil {
		t.Fatalf("DestroyAll failed: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected superseded worktree to remain on disk: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0].Path != wtPath || state.Worktrees[0].Destroying {
		t.Fatalf("expected superseded state entry to remain available, got %#v", state.Worktrees)
	}
}

func TestDestroyAll_NonForceRejectsReservedWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	err = DestroyAll(repoDir, poolDir, false, nil)
	if err == nil {
		t.Fatal("expected non-force DestroyAll to reject reserved worktree")
	}
	if !strings.Contains(err.Error(), "is in use") {
		t.Fatalf("expected in-use error, got %v", err)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected reserved worktree to remain on disk: %v", err)
	}
}

func TestDestroyAll_NonForceRejectsLiveDestroyingWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	state.Worktrees[0].Destroying = true
	if err := reserveOwner(&state.Worktrees[0]); err != nil {
		t.Fatalf("reserveOwner failed: %v", err)
	}
	if err := WriteState(poolDir, state); err != nil {
		t.Fatalf("WriteState failed: %v", err)
	}

	err = DestroyAll(repoDir, poolDir, false, nil)
	if err == nil {
		t.Fatal("expected non-force DestroyAll to reject live destroying worktree")
	}
	if !strings.Contains(err.Error(), "is in use") {
		t.Fatalf("expected in-use error, got %v", err)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected live destroying worktree to remain on disk: %v", err)
	}
	state, err = ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0].Path != wtPath || state.Worktrees[0].OwnerPID != int32(os.Getpid()) {
		t.Fatalf("expected live destroy reservation to remain unchanged, got %#v", state.Worktrees)
	}
}

func TestPruneDryRunDoesNotDeleteAvailableWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	result, err := Prune(repoDir, poolDir, true, nil)
	if err != nil {
		t.Fatalf("Prune dry run failed: %v", err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Path != wtPath {
		t.Fatalf("expected dry run candidate %s, got %#v", wtPath, result.Candidates)
	}
	if len(result.Pruned) != 0 || result.ReclaimableBytes == 0 {
		t.Fatalf("expected dry run to report reclaimable space without pruning, got %#v", result)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("dry run removed worktree %s: %v", wtPath, err)
	}
}

func TestPruneRemovesAvailableWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 1 || result.Pruned[0].Path != wtPath {
		t.Fatalf("expected pruned worktree %s, got %#v", wtPath, result.Pruned)
	}
	if result.FreedBytes == 0 {
		t.Fatalf("expected freed bytes, got %#v", result)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree to be removed, stat err: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 0 {
		t.Fatalf("expected pruned worktree to be removed from state, got %#v", state.Worktrees)
	}
}

func TestPrunePoolDerivesRepoContextFromManagedWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalCwd); err != nil {
			t.Fatalf("restore cwd failed: %v", err)
		}
	})
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	result, err := PrunePool(poolDir, false, nil)
	if err != nil {
		t.Fatalf("PrunePool failed: %v", err)
	}
	if len(result.Pruned) != 1 || result.Pruned[0].Path != wtPath {
		t.Fatalf("expected pruned worktree %s, got %#v", wtPath, result.Pruned)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree to be removed, stat err: %v", err)
	}
}

func TestPruneAllReportsBackingMissingOrphanWithoutDeletingByDefault(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	if err := os.RemoveAll(repoDir); err != nil {
		t.Fatalf("RemoveAll repo failed: %v", err)
	}

	result, err := PruneAllWithOptions(filepath.Dir(poolDir), PruneOptions{DryRun: false})
	if err != nil {
		t.Fatalf("PruneAll failed: %v", err)
	}
	if len(result.Result.Pruned) != 0 {
		t.Fatalf("plain prune must not delete orphans, got %#v", result.Result.Pruned)
	}
	if !hasSkippedCategory(result.Result.Skipped, wtPath, PruneSkipOrphanedBackingRepo) {
		t.Fatalf("expected orphan skip, got %#v", result.Result.Skipped)
	}
	if !hasSkippedReason(result.Result.Skipped, wtPath, pruneOrphanUnverifiedWarning) {
		t.Fatalf("expected unverified-content warning, got %#v", result.Result.Skipped)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("plain prune removed orphan %s: %v", wtPath, err)
	}
}

func TestPruneAllPrunesBackingMissingOrphanOnlyWithExplicitFlag(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	if err := os.RemoveAll(repoDir); err != nil {
		t.Fatalf("RemoveAll repo failed: %v", err)
	}

	result, err := PruneAllWithOptions(filepath.Dir(poolDir), PruneOptions{
		DryRun:       true,
		PruneOrphans: true,
	})
	if err != nil {
		t.Fatalf("PruneAll dry run failed: %v", err)
	}
	if len(result.Result.Candidates) != 1 || result.Result.Candidates[0].Path != wtPath {
		t.Fatalf("expected orphan dry-run candidate %s, got %#v", wtPath, result.Result.Candidates)
	}
	if !result.Result.Candidates[0].Orphaned || result.Result.Candidates[0].Warning != pruneOrphanUnverifiedWarning {
		t.Fatalf("expected orphan warning on candidate, got %#v", result.Result.Candidates[0])
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("dry run removed orphan %s: %v", wtPath, err)
	}

	result, err = PruneAllWithOptions(filepath.Dir(poolDir), PruneOptions{PruneOrphans: true})
	if err != nil {
		t.Fatalf("PruneAll --prune-orphans failed: %v", err)
	}
	if len(result.Result.Pruned) != 1 || result.Result.Pruned[0].Path != wtPath {
		t.Fatalf("expected pruned orphan %s, got %#v", wtPath, result.Result.Pruned)
	}
	if !result.Result.Pruned[0].Orphaned || result.Result.Pruned[0].Warning != pruneOrphanUnverifiedWarning {
		t.Fatalf("expected orphan warning on pruned worktree, got %#v", result.Result.Pruned[0])
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("expected orphan worktree to be removed, stat err: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 0 {
		t.Fatalf("expected pruned orphan to be removed from state, got %#v", state.Worktrees)
	}
}

func TestPruneAllNeverDeletesOriginUnreachableWithPruneOrphans(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	remoteDir := filepath.Join(filepath.Dir(repoDir), "remote.git")
	if err := os.RemoveAll(remoteDir); err != nil {
		t.Fatalf("RemoveAll remote failed: %v", err)
	}

	result, err := PruneAllWithOptions(filepath.Dir(poolDir), PruneOptions{PruneOrphans: true})
	if err != nil {
		t.Fatalf("PruneAll failed: %v", err)
	}
	if len(result.Result.Pruned) != 0 {
		t.Fatalf("origin-unreachable worktree must not be pruned, got %#v", result.Result.Pruned)
	}
	if !hasSkippedCategory(result.Result.Skipped, wtPath, PruneSkipOriginUnreachable) {
		t.Fatalf("expected origin-unreachable skip, got %#v", result.Result.Skipped)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("origin-unreachable worktree was removed: %v", err)
	}
}

func TestPruneAllSkipsUnsafeWorktreesAcrossPools(t *testing.T) {
	poolRoot := t.TempDir()

	safeRepo, _ := setupRepo(t)
	safePool := filepath.Join(poolRoot, "safe")
	safePath, err := Acquire(safeRepo, safePool, 4, nil)
	if err != nil {
		t.Fatalf("Acquire safe failed: %v", err)
	}
	if err := Release(safePool, safePath); err != nil {
		t.Fatalf("Release safe failed: %v", err)
	}

	dirtyRepo, _ := setupRepo(t)
	dirtyPool := filepath.Join(poolRoot, "dirty")
	dirtyPath, err := Acquire(dirtyRepo, dirtyPool, 4, nil)
	if err != nil {
		t.Fatalf("Acquire dirty failed: %v", err)
	}
	if err := Release(dirtyPool, dirtyPath); err != nil {
		t.Fatalf("Release dirty failed: %v", err)
	}
	runGit(t, dirtyPath, "config", "status.showUntrackedFiles", "no")
	if err := os.WriteFile(filepath.Join(dirtyPath, "untracked.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("WriteFile dirty failed: %v", err)
	}

	inUseRepo, _ := setupRepo(t)
	inUsePool := filepath.Join(poolRoot, "in-use")
	inUsePath, err := Acquire(inUseRepo, inUsePool, 4, nil)
	if err != nil {
		t.Fatalf("Acquire in-use failed: %v", err)
	}

	unmergedRepo, _ := setupRepo(t)
	unmergedPool := filepath.Join(poolRoot, "unmerged")
	unmergedPath, err := Acquire(unmergedRepo, unmergedPool, 4, nil)
	if err != nil {
		t.Fatalf("Acquire unmerged failed: %v", err)
	}
	if err := Release(unmergedPool, unmergedPath); err != nil {
		t.Fatalf("Release unmerged failed: %v", err)
	}
	runGit(t, unmergedPath, "checkout", "-b", "unmerged-work")
	if err := os.WriteFile(filepath.Join(unmergedPath, "README.md"), []byte("unmerged\n"), 0o644); err != nil {
		t.Fatalf("WriteFile unmerged failed: %v", err)
	}
	runGit(t, unmergedPath, "commit", "-am", "unmerged work")

	result, err := PruneAll(poolRoot, false, nil)
	if err != nil {
		t.Fatalf("PruneAll failed: %v", err)
	}
	if len(result.Result.Pruned) != 1 || result.Result.Pruned[0].Path != safePath {
		t.Fatalf("expected only safe worktree to be pruned, got %#v", result.Result.Pruned)
	}
	if !hasSkippedReason(result.Result.Skipped, dirtyPath, "uncommitted changes") {
		t.Fatalf("expected dirty worktree skip, got %#v", result.Result.Skipped)
	}
	if !hasSkippedReason(result.Result.Skipped, unmergedPath, "not merged") {
		t.Fatalf("expected unmerged worktree skip, got %#v", result.Result.Skipped)
	}

	if _, err := os.Stat(safePath); !os.IsNotExist(err) {
		t.Fatalf("expected safe worktree to be removed, stat err: %v", err)
	}
	for _, path := range []string{dirtyPath, inUsePath, unmergedPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected unsafe worktree to remain at %s: %v", path, err)
		}
	}
}

func TestPruneInUseWorktreeDoesNotRequireOrigin(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	remoteDir := filepath.Join(filepath.Dir(repoDir), "remote.git")
	if err := os.RemoveAll(remoteDir); err != nil {
		t.Fatal(err)
	}

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed on in-use worktree with offline origin: %v", err)
	}
	if len(result.Candidates) != 0 || len(result.Pruned) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("expected in-use worktree to be ignored, got %#v", result)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected in-use worktree to remain: %v", err)
	}
}

func TestPruneSkipsDirtyWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	runGit(t, wtPath, "config", "status.showUntrackedFiles", "no")
	if err := os.WriteFile(filepath.Join(wtPath, "uncommitted.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 0 {
		t.Fatalf("dirty worktree must not be pruned, got %#v", result.Pruned)
	}
	if !hasSkippedReason(result.Skipped, wtPath, "uncommitted changes") {
		t.Fatalf("expected dirty worktree skip, got %#v", result.Skipped)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected dirty worktree to remain: %v", err)
	}
}

func TestPruneSkipsUnmergedCommit(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	runGit(t, wtPath, "checkout", "-b", "unmerged-work")
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("unmerged\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	runGit(t, wtPath, "commit", "-am", "unmerged work")

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 0 {
		t.Fatalf("unmerged worktree must not be pruned, got %#v", result.Pruned)
	}
	if !hasSkippedReason(result.Skipped, wtPath, "not merged") {
		t.Fatalf("expected unmerged worktree skip, got %#v", result.Skipped)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected unmerged worktree to remain: %v", err)
	}
}

func TestPruneRefreshesOriginBeforeMergeSafety(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	base := filepath.Dir(repoDir)
	rewriteDir := filepath.Join(base, "rewriter")
	runGit(t, "", "clone", filepath.Join(base, "remote.git"), rewriteDir)
	runGit(t, rewriteDir, "config", "user.email", "test@test.com")
	runGit(t, rewriteDir, "config", "user.name", "Test")
	runGit(t, rewriteDir, "checkout", "--orphan", "replacement")
	runGit(t, rewriteDir, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(rewriteDir, "README.md"), []byte("replacement\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	runGit(t, rewriteDir, "add", ".")
	runGit(t, rewriteDir, "commit", "-m", "replacement")
	runGit(t, rewriteDir, "push", "--force", "origin", "replacement:main")

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 0 {
		t.Fatalf("worktree with remotely unmerged HEAD must not be pruned, got %#v", result.Pruned)
	}
	if !hasSkippedReason(result.Skipped, wtPath, "not merged") {
		t.Fatalf("expected unmerged worktree skip after fetch, got %#v", result.Skipped)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected remotely unmerged worktree to remain: %v", err)
	}
}

func TestPruneUsesRemoteTrackingDefaultRefNotShadowingBranch(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	runGit(t, repoDir, "branch", "origin/main", "main")

	base := filepath.Dir(repoDir)
	rewriteDir := filepath.Join(base, "shadow-rewriter")
	runGit(t, "", "clone", filepath.Join(base, "remote.git"), rewriteDir)
	runGit(t, rewriteDir, "config", "user.email", "test@test.com")
	runGit(t, rewriteDir, "config", "user.name", "Test")
	runGit(t, rewriteDir, "checkout", "--orphan", "replacement")
	runGit(t, rewriteDir, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(rewriteDir, "README.md"), []byte("replacement\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	runGit(t, rewriteDir, "add", ".")
	runGit(t, rewriteDir, "commit", "-m", "replacement")
	runGit(t, rewriteDir, "push", "--force", "origin", "replacement:main")

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 0 {
		t.Fatalf("shadowed default ref must not prune unmerged worktree, got %#v", result.Pruned)
	}
	if !hasSkippedReason(result.Skipped, wtPath, "not merged") {
		t.Fatalf("expected unmerged worktree skip with shadowed ref, got %#v", result.Skipped)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected shadowed-ref worktree to remain: %v", err)
	}
}

func TestPruneUsesFullLocalDefaultRefWithoutOrigin(t *testing.T) {
	repoDir, poolDir := setupLocalRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	runGit(t, repoDir, "tag", "main", "HEAD")
	runGit(t, repoDir, "checkout", "--orphan", "replacement")
	runGit(t, repoDir, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("replacement\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "replacement")
	runGit(t, repoDir, "branch", "-M", "main")

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 0 {
		t.Fatalf("local shadowed default ref must not prune unmerged worktree, got %#v", result.Pruned)
	}
	if !hasSkippedReason(result.Skipped, wtPath, "refs/heads/main") {
		t.Fatalf("expected local default branch skip, got %#v", result.Skipped)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected local shadowed-ref worktree to remain: %v", err)
	}
}

func TestPruneIgnoresStaleOriginHeadWhenOriginIsAbsent(t *testing.T) {
	repoDir, poolDir := setupLocalRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	runGit(t, repoDir, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, repoDir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	runGit(t, repoDir, "checkout", "--orphan", "trunk")
	runGit(t, repoDir, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("trunk\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "trunk")

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 0 {
		t.Fatalf("stale origin HEAD must not choose local main, got %#v", result.Pruned)
	}
	if !hasSkippedReason(result.Skipped, wtPath, "refs/heads/trunk") {
		t.Fatalf("expected local HEAD branch skip, got %#v", result.Skipped)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected stale origin HEAD worktree to remain: %v", err)
	}
}

func TestPruneSkipsWhenRemoteDefaultTrackingRefIsStale(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	runGit(t, repoDir, "branch", "side")
	runGit(t, repoDir, "push", "origin", "side")
	runGit(t, repoDir, "config", "--unset-all", "remote.origin.fetch")
	runGit(t, repoDir, "config", "--add", "remote.origin.fetch", "+refs/heads/side:refs/remotes/origin/side")

	base := filepath.Dir(repoDir)
	rewriteDir := filepath.Join(base, "stale-default-rewriter")
	runGit(t, "", "clone", filepath.Join(base, "remote.git"), rewriteDir)
	runGit(t, rewriteDir, "config", "user.email", "test@test.com")
	runGit(t, rewriteDir, "config", "user.name", "Test")
	runGit(t, rewriteDir, "checkout", "--orphan", "replacement")
	runGit(t, rewriteDir, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(rewriteDir, "README.md"), []byte("replacement\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	runGit(t, rewriteDir, "add", ".")
	runGit(t, rewriteDir, "commit", "-m", "replacement")
	runGit(t, rewriteDir, "push", "--force", "origin", "replacement:main")

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 0 {
		t.Fatalf("stale remote default worktree must not be pruned, got %#v", result.Pruned)
	}
	if !hasSkippedCategory(result.Skipped, wtPath, pruneSkipCannotVerify) {
		t.Fatalf("expected cannot-verify skip, got %#v", result.Skipped)
	}
	if !hasSkippedDetail(result.Skipped, wtPath, "stale") {
		t.Fatalf("expected stale diagnostic detail, got %#v", result.Skipped)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected stale remote default worktree to remain: %v", err)
	}
}

func TestRelease_RejectsDestroyingWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	state.Worktrees[0].Destroying = true
	if err := reserveOwner(&state.Worktrees[0]); err != nil {
		t.Fatalf("reserveOwner failed: %v", err)
	}
	reserved := state.Worktrees[0]
	if err := WriteState(poolDir, state); err != nil {
		t.Fatalf("WriteState failed: %v", err)
	}
	dirtyPath := filepath.Join(wtPath, "pre-destroy-work.txt")
	if err := os.WriteFile(dirtyPath, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	err = Release(poolDir, wtPath)
	if err == nil {
		t.Fatal("expected Release to reject destroying worktree")
	}
	if !strings.Contains(err.Error(), "is being destroyed") {
		t.Fatalf("expected destroying error, got %v", err)
	}

	state, err = ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0] != reserved {
		t.Fatalf("expected destroy reservation to remain unchanged, got %#v", state.Worktrees)
	}
	if _, err := os.Stat(dirtyPath); err != nil {
		t.Fatalf("expected Release to leave destroying worktree untouched: %v", err)
	}
}

func TestAcquireLease_MarksWorktreeLeasedInState(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := AcquireLease(repoDir, poolDir, 4, nil, "secondmate-home")
	if err != nil {
		t.Fatalf("AcquireLease failed: %v", err)
	}
	if wtPath == "" {
		t.Fatal("AcquireLease returned empty path")
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 1 {
		t.Fatalf("expected one worktree, got %#v", state.Worktrees)
	}
	wt := state.Worktrees[0]
	if !wt.Leased || wt.LeaseHolder != "secondmate-home" {
		t.Fatalf("expected durable lease with holder, got %#v", wt)
	}
	// A lease must not depend on a live process: no owner reservation is kept.
	if wt.OwnerPID != 0 || wt.OwnerStartedAt != 0 {
		t.Fatalf("expected no owner reservation on a lease, got %#v", wt)
	}
	if wt.LeasedAt.IsZero() {
		t.Fatalf("expected LeasedAt to be set, got %#v", wt)
	}
}

func TestAcquireLease_NotHandedOutBySubsequentAcquire(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	leased, err := AcquireLease(repoDir, poolDir, 4, nil, "")
	if err != nil {
		t.Fatalf("AcquireLease failed: %v", err)
	}

	// A plain acquire must never reuse the leased worktree even though no
	// process runs inside it.
	next, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if next == leased {
		t.Fatalf("acquire reused leased worktree %s", leased)
	}
}

func TestAcquireLease_ExhaustsPoolWhenAllLeased(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	if _, err := AcquireLease(repoDir, poolDir, 1, nil, ""); err != nil {
		t.Fatalf("AcquireLease failed: %v", err)
	}

	// With pool size 1 and the only worktree leased, a second acquire cannot
	// find or create one.
	if _, err := Acquire(repoDir, poolDir, 1, nil); err == nil {
		t.Fatal("expected acquire to fail when the only worktree is leased")
	}
}

func TestPrune_NeverRemovesLeasedWorktreeWithoutProcess(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := AcquireLease(repoDir, poolDir, 4, nil, "home")
	if err != nil {
		t.Fatalf("AcquireLease failed: %v", err)
	}

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Candidates) != 0 || len(result.Pruned) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("expected leased worktree to be ignored by prune, got %#v", result)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("prune removed leased worktree %s: %v", wtPath, err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 1 || !state.Worktrees[0].Leased {
		t.Fatalf("expected lease to persist after prune, got %#v", state.Worktrees)
	}
}

func TestRelease_ClearsLease(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := AcquireLease(repoDir, poolDir, 4, nil, "home")
	if err != nil {
		t.Fatalf("AcquireLease failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 1 {
		t.Fatalf("expected one worktree, got %#v", state.Worktrees)
	}
	if state.Worktrees[0].Leased || state.Worktrees[0].LeaseHolder != "" || !state.Worktrees[0].LeasedAt.IsZero() {
		t.Fatalf("expected lease to be cleared, got %#v", state.Worktrees[0])
	}
	data, err := os.ReadFile(stateFilePath(poolDir))
	if err != nil {
		t.Fatalf("reading state file failed: %v", err)
	}
	stateJSON := string(data)
	for _, field := range []string{`"leased"`, `"lease_holder"`, `"leased_at"`} {
		if strings.Contains(stateJSON, field) {
			t.Fatalf("expected cleared lease field %s to be omitted from state file:\n%s", field, stateJSON)
		}
	}

	// After release the worktree becomes available for reuse.
	reused, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire after release failed: %v", err)
	}
	if reused != wtPath {
		t.Fatalf("expected released worktree %s to be reused, got %s", wtPath, reused)
	}
}

func TestList_ShowsLeasedState(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := AcquireLease(repoDir, poolDir, 4, nil, "secondmate-7")
	if err != nil {
		t.Fatalf("AcquireLease failed: %v", err)
	}

	statuses, err := List(poolDir)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Path != wtPath {
		t.Fatalf("expected one leased worktree, got %#v", statuses)
	}
	if statuses[0].Status != StatusLeased {
		t.Fatalf("expected leased status, got %q", statuses[0].Status)
	}
	if statuses[0].LeaseHolder != "secondmate-7" {
		t.Fatalf("expected lease holder to be reported, got %q", statuses[0].LeaseHolder)
	}
}

func TestHealState_PreservesLease(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := AcquireLease(repoDir, poolDir, 4, nil, "home")
	if err != nil {
		t.Fatalf("AcquireLease failed: %v", err)
	}

	// Simulate a stale owner pid alongside the lease; healing must clear the
	// dead owner reservation but keep the durable lease intact.
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	state.Worktrees[0].OwnerPID = 999999
	state.Worktrees[0].OwnerStartedAt = 1
	if err := WriteState(poolDir, state); err != nil {
		t.Fatalf("WriteState failed: %v", err)
	}

	statuses, err := List(poolDir)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Status != StatusLeased {
		t.Fatalf("expected lease to survive healing, got %#v", statuses)
	}

	state, err = ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if !state.Worktrees[0].Leased {
		t.Fatalf("expected lease to persist after healing, got %#v", state.Worktrees[0])
	}
	if state.Worktrees[0].OwnerPID != 0 {
		t.Fatalf("expected stale owner reservation to be cleared, got %#v", state.Worktrees[0])
	}
	_ = wtPath
}

func TestDestroy_NonForceRejectsLeasedWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := AcquireLease(repoDir, poolDir, 4, nil, "home")
	if err != nil {
		t.Fatalf("AcquireLease failed: %v", err)
	}

	err = Destroy(repoDir, poolDir, wtPath, false, nil)
	if err == nil {
		t.Fatal("expected non-force Destroy to reject leased worktree")
	}
	if !strings.Contains(err.Error(), "is in use") {
		t.Fatalf("expected in-use error, got %v", err)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected leased worktree to remain on disk: %v", err)
	}

	// --force overrides the lease.
	if err := Destroy(repoDir, poolDir, wtPath, true, nil); err != nil {
		t.Fatalf("force Destroy failed: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("expected leased worktree to be removed after force destroy, stat err: %v", err)
	}
}

func TestAcquireLease_ConcurrentAcquiresNeverDoubleLease(t *testing.T) {
	// A local repo has no origin, so AcquireLease skips git fetch and avoids
	// concurrent-fetch races; the state lock still serializes pool mutation.
	repoDir, poolDir := setupLocalRepo(t)

	const n = 6
	paths := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			paths[i], errs[i] = AcquireLease(repoDir, poolDir, n, nil, "")
		}(i)
	}
	wg.Wait()

	seen := make(map[string]int)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("AcquireLease %d failed: %v", i, errs[i])
		}
		if paths[i] == "" {
			t.Fatalf("AcquireLease %d returned empty path", i)
		}
		seen[paths[i]]++
	}
	for path, count := range seen {
		if count != 1 {
			t.Fatalf("worktree %s was leased %d times (double-lease)", path, count)
		}
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != n {
		t.Fatalf("expected %d distinct leased worktrees, got %d: %#v", n, len(state.Worktrees), state.Worktrees)
	}
	for _, wt := range state.Worktrees {
		if !wt.Leased {
			t.Fatalf("expected every concurrently acquired worktree to be leased, got %#v", wt)
		}
	}
}

func hasSkippedReason(skipped []PruneSkipped, path, reason string) bool {
	for _, wt := range skipped {
		if wt.Path == path && strings.Contains(wt.Reason, reason) {
			return true
		}
	}
	return false
}

func hasSkippedCategory(skipped []PruneSkipped, path, category string) bool {
	for _, wt := range skipped {
		if wt.Path == path && wt.Category == category {
			return true
		}
	}
	return false
}

func hasSkippedDetail(skipped []PruneSkipped, path, detail string) bool {
	for _, wt := range skipped {
		if wt.Path == path && strings.Contains(wt.Detail, detail) {
			return true
		}
	}
	return false
}

func TestAcquireDuringHookProbe(t *testing.T) {
	if len(os.Args) < 5 {
		return
	}
	argStart := -1
	for i := len(os.Args) - 1; i >= 0; i-- {
		if os.Args[i] == "--" {
			argStart = i
			break
		}
	}
	if argStart == -1 || len(os.Args)-argStart < 4 {
		return
	}

	repoDir := os.Args[argStart+1]
	poolDir := os.Args[argStart+2]
	sentinel := os.Args[argStart+3]
	if len(os.Args) > argStart+4 {
		if err := os.Chdir(os.Args[argStart+4]); err != nil {
			t.Fatal(err)
		}
	}
	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, []byte(wtPath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSupersedeDestroyReservationProbe(t *testing.T) {
	if len(os.Args) < 5 {
		return
	}
	argStart := -1
	for i := len(os.Args) - 1; i >= 0; i-- {
		if os.Args[i] == "--" {
			argStart = i
			break
		}
	}
	if argStart == -1 || len(os.Args)-argStart < 4 {
		return
	}

	poolDir := os.Args[argStart+1]
	wtPath := os.Args[argStart+2]
	sentinel := os.Args[argStart+3]
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Worktrees {
		if state.Worktrees[i].Path == wtPath {
			state.Worktrees[i].Destroying = false
			state.Worktrees[i].OwnerPID = 0
			state.Worktrees[i].OwnerStartedAt = 0
		}
	}
	if err := os.WriteFile(sentinel, []byte("superseded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteState(poolDir, state); err != nil {
		t.Fatal(err)
	}
}

// quoteForShell wraps a path so it survives splitting by /bin/sh or cmd.exe.
// Tests only use temp-dir paths which don't contain quotes, so simple quoting
// is sufficient.
func quoteForShell(p string) string {
	// Double-quote works in both sh and cmd.exe for paths without quotes.
	return `"` + p + `"`
}
