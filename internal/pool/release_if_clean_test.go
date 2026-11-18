package pool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kunchenguid/treehouse/v3/internal/process"
)

// These tests pin the RequireClean release as snapshot, verify, then act. The
// process scan is the first and last thing each check runs, so replacing it
// lets a test land work at an exact point: during the first check (after HEAD
// was recorded), or during the final check inside the guarded reset.

func leasedSlotForCleanRelease(t *testing.T) (poolDir, wtPath string) {
	t.Helper()
	repoDir, poolDir := setupRepo(t)
	wtPath, err := AcquireLease(repoDir, poolDir, 4, nil, "dibs")
	if err != nil {
		t.Fatalf("AcquireLease failed: %v", err)
	}
	return poolDir, wtPath
}

// scanDuringCheck replaces the process scan for one test. onScan receives the
// 1-based number of the scan and returns the processes that scan reports.
func scanDuringCheck(t *testing.T, onScan func(n int) []process.ProcessInfo) *int {
	t.Helper()
	var calls int
	old := findProcessesInWorktree
	findProcessesInWorktree = func(string) ([]process.ProcessInfo, error) {
		calls++
		return onScan(calls), nil
	}
	t.Cleanup(func() { findProcessesInWorktree = old })
	return &calls
}

func releaseClean(poolDir, wtPath string) error {
	return ReleaseConditional(poolDir, wtPath, "", ReleasePreconditions{RequireClean: true}, nil)
}

func requireNotClean(t *testing.T, err error, want CleanBlocker) {
	t.Helper()
	var notClean *NotCleanError
	if !errors.As(err, &notClean) {
		t.Fatalf("expected a %s refusal, got %v", want, err)
	}
	if notClean.Blocker != want {
		t.Fatalf("expected blocker %s, got %s (%s)", want, notClean.Blocker, notClean.Detail)
	}
}

func requireStillLeased(t *testing.T, poolDir, wtPath string) {
	t.Helper()
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	wt := findWorktreeByPath(&state, wtPath)
	if wt == nil || !wt.Leased {
		t.Fatalf("a refused release must keep the lease on %s, got %+v", wtPath, wt)
	}
}

// Greptile, round 1: work created after the clean check but before the reset
// was discarded. The final check runs while the reset holds HEAD's lock, after
// which nothing is removed until it passes.
func TestReleaseIfCleanRefusesAnEditMadeAfterTheFirstCheck(t *testing.T) {
	poolDir, wtPath := leasedSlotForCleanRelease(t)
	headLock := gitOut(t, wtPath, "rev-parse", "--path-format=absolute", "--git-path", "HEAD.lock")
	late := filepath.Join(wtPath, "late.txt")
	lockedDuringFinalCheck := false
	calls := scanDuringCheck(t, func(n int) []process.ProcessInfo {
		if n == 2 {
			_, err := os.Stat(headLock)
			lockedDuringFinalCheck = err == nil
			if err := os.WriteFile(late, []byte("late\n"), 0o644); err != nil {
				t.Error(err)
			}
		}
		return nil
	})

	err := releaseClean(poolDir, wtPath)
	requireNotClean(t, err, BlockedByDirty)
	if *calls < 2 {
		t.Fatalf("expected the checks to run again before the reset, ran %d time(s)", *calls)
	}
	if !lockedDuringFinalCheck {
		t.Fatal("the final check must run while the reset holds HEAD's lock")
	}
	if got, err := os.ReadFile(late); err != nil || string(got) != "late\n" {
		t.Fatalf("the late edit was discarded: %q, %v", got, err)
	}
	requireStillLeased(t, poolDir, wtPath)
}

// Greptile, round 3: HEAD was read after the landed check, so a commit landing
// between the two became the expected head and was reset away. HEAD is now
// recorded before any check, the landed check judges that recorded commit, and
// the reset refuses a HEAD that moved off it. The commit here lands after HEAD
// was recorded, so the landed verdict is about the old, landed commit and only
// the snapshot comparison stands between the new commit and the reset.
func TestReleaseIfCleanRefusesACommitThatLandsAfterHEADIsRecorded(t *testing.T) {
	poolDir, wtPath := leasedSlotForCleanRelease(t)
	recorded := gitOut(t, wtPath, "rev-parse", "HEAD")
	scanDuringCheck(t, func(n int) []process.ProcessInfo {
		if n == 1 {
			if err := os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("work\n"), 0o644); err != nil {
				t.Error(err)
			}
			gitOut(t, wtPath, "add", "work.txt")
			gitOut(t, wtPath, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "landed mid-check")
		}
		return nil
	})

	err := releaseClean(poolDir, wtPath)
	requireNotClean(t, err, BlockedByUnpushed)
	now := gitOut(t, wtPath, "rev-parse", "HEAD")
	if now == recorded {
		t.Fatal("test setup: the commit did not land")
	}
	if subject := gitOut(t, wtPath, "log", "-1", "--format=%s"); subject != "landed mid-check" {
		t.Fatalf("the commit that landed mid-check was reset away; HEAD is now %q", subject)
	}
	requireStillLeased(t, poolDir, wtPath)
}

// Greptile, outside the diff: a process that started after the first scan was
// terminated instead of refused. The final check refuses it, and nothing is
// ever signalled.
func TestReleaseIfCleanRefusesAProcessThatStartsAfterTheFirstScan(t *testing.T) {
	poolDir, wtPath := leasedSlotForCleanRelease(t)
	head := gitOut(t, wtPath, "rev-parse", "HEAD")
	oldTerminate := terminateWorktreeProcesses
	terminateWorktreeProcesses = func(string, time.Duration) ([]process.ProcessInfo, error) {
		t.Error("a clean-only release must never terminate processes")
		return nil, nil
	}
	t.Cleanup(func() { terminateWorktreeProcesses = oldTerminate })
	late := process.ProcessInfo{PID: 2147480000, Name: "late-agent"}
	scanDuringCheck(t, func(n int) []process.ProcessInfo {
		if n >= 2 {
			return []process.ProcessInfo{late}
		}
		return nil
	})

	err := releaseClean(poolDir, wtPath)
	requireNotClean(t, err, BlockedByProcesses)
	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("a refusal moved HEAD from %s to %s", head, got)
	}
	requireStillLeased(t, poolDir, wtPath)
}

// Greptile, round 2: a refused guarded reset was swallowed and the return
// reported success. A reset that fails for any reason - here a HEAD.lock the
// pool does not own - must surface and keep the lease.
func TestReleaseIfCleanNeverReportsSuccessWhenTheResetFails(t *testing.T) {
	poolDir, wtPath := leasedSlotForCleanRelease(t)
	headLock := gitOut(t, wtPath, "rev-parse", "--path-format=absolute", "--git-path", "HEAD.lock")
	if err := os.WriteFile(headLock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(headLock) })

	if err := releaseClean(poolDir, wtPath); err == nil {
		t.Fatal("a release whose reset could not run must not report success")
	}
	requireStillLeased(t, poolDir, wtPath)
}
