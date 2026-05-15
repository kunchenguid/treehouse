package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
