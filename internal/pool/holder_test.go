//go:build !windows

package pool

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kunchenguid/treehouse/v3/internal/process"
)

// startHolder starts a process that can stand in for a lease holder.
func startHolder(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// exitedHolder returns the identity of a process that has since exited.
func exitedHolder(t *testing.T) (int32, int64) {
	t.Helper()
	cmd := startHolder(t)
	pid := int32(cmd.Process.Pid)
	stamp, err := process.StartStamp(pid)
	if err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return pid, stamp
}

// leaseWithLiveHolder acquires a lease held by the test's parent process, the
// one ancestor that outlives every test.
func leaseWithLiveHolder(t *testing.T, repo, poolDir string, poolSize int) LeaseInfo {
	t.Helper()
	lease, err := AcquireLeaseInfoWithOptions(repo, poolDir, poolSize, nil, "agent", AcquireOptions{LeaseHolderPID: int32(os.Getppid())})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

// setHolder rewrites the recorded holder of the lease on path, as if that
// lease had been taken by the process with this identity.
func setHolder(t *testing.T, poolDir, path string, pid int32, stamp int64) {
	t.Helper()
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Worktrees {
		if state.Worktrees[i].Path == path {
			state.Worktrees[i].LeaseHolderPID = pid
			state.Worktrees[i].LeaseHolderStart = stamp
		}
	}
	if err := WriteState(poolDir, state); err != nil {
		t.Fatal(err)
	}
}

// exitedHolderFixture is a slot at its base, leased by a holder that exited.
func exitedHolderFixture(t *testing.T) (repo, poolDir, path string) {
	t.Helper()
	repo, poolDir = setupLocalRepo(t)
	path = leaseWithLiveHolder(t, repo, poolDir, 1).Path
	pid, stamp := exitedHolder(t)
	setHolder(t, poolDir, path, pid, stamp)
	return repo, poolDir, path
}

func TestAcquireLeaseRecordsVerifiedHolder(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	parent := int32(os.Getppid())
	lease := leaseWithLiveHolder(t, repo, poolDir, 1)
	want, err := process.StartStamp(parent)
	if err != nil {
		t.Fatal(err)
	}
	if lease.LeaseHolderPID != parent {
		t.Fatalf("lease holder PID = %d, want %d", lease.LeaseHolderPID, parent)
	}
	if wt := entryFor(t, poolDir, lease.Path); wt.LeaseHolderPID != parent || wt.LeaseHolderStart != want {
		t.Fatalf("recorded holder = %d/%d, want %d/%d", wt.LeaseHolderPID, wt.LeaseHolderStart, parent, want)
	}
	if st := statusOf(t, poolDir, lease.Path); st.Status != StatusLeased || st.LeaseHolderPID != parent {
		t.Fatalf("status = %+v", st)
	}
}

func TestAcquireLeaseRefusesUnverifiableHolderBeforeTouchingThePool(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	stranger := startHolder(t)
	for name, pid := range map[string]int32{
		"not an ancestor": int32(stranger.Process.Pid),
		"this process":    int32(os.Getpid()),
		"negative":        -1,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := AcquireLeaseInfoWithOptions(repo, poolDir, 1, nil, "agent", AcquireOptions{LeaseHolderPID: pid})
			if err == nil {
				t.Fatal("acquisition with an unverifiable holder succeeded")
			}
			if _, statErr := os.Stat(poolDir); !os.IsNotExist(statErr) {
				t.Fatalf("refused acquisition touched the pool: %v", statErr)
			}
		})
	}
}

func TestHolderPIDRequiresALease(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	if _, err := AcquireWithOptions(repo, poolDir, 1, nil, AcquireOptions{LeaseHolderPID: int32(os.Getppid())}); err == nil {
		t.Fatal("a holder PID without a lease was accepted")
	}
}

func TestExitedHolderLeaseIsFreedWhenSafe(t *testing.T) {
	_, poolDir, path := exitedHolderFixture(t)
	for range 2 {
		if st := statusOf(t, poolDir, path); st.Status != StatusAvailable || st.LeaseID != "" || st.LeaseHolderPID != 0 || st.RecoveryReason != "" {
			t.Fatalf("status = %+v", st)
		}
	}
	if wt := entryFor(t, poolDir, path); wt.Leased || wt.LeaseHolderPID != 0 || wt.LeaseHolderStart != 0 || !wt.SeedInventoryKnown {
		t.Fatalf("freed entry reads back as %#v", wt)
	}
}

func TestHolderLeasesThatAreNotProvenGoneStayLeased(t *testing.T) {
	t.Run("live holder", func(t *testing.T) {
		repo, poolDir := setupLocalRepo(t)
		lease := leaseWithLiveHolder(t, repo, poolDir, 1)
		assertStillLeased(t, poolDir, lease.Path, lease.LeaseID)
	})
	t.Run("lease without a holder PID", func(t *testing.T) {
		repo, poolDir := setupLocalRepo(t)
		lease, err := AcquireLeaseInfo(repo, poolDir, 1, nil, "agent")
		if err != nil {
			t.Fatal(err)
		}
		assertStillLeased(t, poolDir, lease.Path, lease.LeaseID)
	})
	t.Run("holder PID without a recorded start", func(t *testing.T) {
		repo, poolDir := setupLocalRepo(t)
		lease := leaseWithLiveHolder(t, repo, poolDir, 1)
		pid, _ := exitedHolder(t)
		setHolder(t, poolDir, lease.Path, pid, 0)
		assertStillLeased(t, poolDir, lease.Path, lease.LeaseID)
	})
	t.Run("liveness that cannot be read", func(t *testing.T) {
		_, poolDir, path := exitedHolderFixture(t)
		lease := entryFor(t, poolDir, path)
		old := holderLiveness
		holderLiveness = func(int32, int64) process.Liveness { return process.LivenessUnknown }
		t.Cleanup(func() { holderLiveness = old })
		assertStillLeased(t, poolDir, path, lease.LeaseID)
	})
	t.Run("legacy lease from a state file without holder fields", func(t *testing.T) {
		repo, poolDir := setupLocalRepo(t)
		lease, err := AcquireLeaseInfo(repo, poolDir, 1, nil, "agent")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(stateFilePath(poolDir))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "lease_holder_pid") || strings.Contains(string(data), "lease_holder_start") {
			t.Fatalf("a lease without a holder wrote holder fields:\n%s", data)
		}
		assertStillLeased(t, poolDir, lease.Path, lease.LeaseID)
	})
}

func assertStillLeased(t *testing.T, poolDir, path, leaseID string) {
	t.Helper()
	for range 2 {
		st := statusOf(t, poolDir, path)
		if st.Status != StatusLeased || st.LeaseID != leaseID || st.RecoveryReason != "" {
			t.Fatalf("status = %+v, want lease %s kept without a reason", st, leaseID)
		}
	}
}

func TestRecycledHolderPIDIsTreatedAsExited(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	lease := leaseWithLiveHolder(t, repo, poolDir, 1)
	recycled := startHolder(t)
	pid := int32(recycled.Process.Pid)
	stamp, err := process.StartStamp(pid)
	if err != nil {
		t.Fatal(err)
	}
	// The lease was taken by an earlier process that had this PID.
	setHolder(t, poolDir, lease.Path, pid, stamp-1)
	if st := statusOf(t, poolDir, lease.Path); st.Status != StatusAvailable {
		t.Fatalf("status = %+v, want the lease of the recycled PID freed", st)
	}
}

func TestExitedHolderLeaseStaysLeasedUnlessSafe(t *testing.T) {
	commit := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, path, "add", "work.txt")
		runGit(t, path, "commit", "-m", "unpushed work")
	}
	for name, tc := range map[string]struct {
		mutate func(t *testing.T, path string)
		reason string
	}{
		"tracked changes": {
			mutate: func(t *testing.T, path string) {
				if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("edited\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			reason: "tracked changes",
		},
		"unlanded HEAD": {mutate: commit, reason: "not contained"},
	} {
		t.Run(name, func(t *testing.T) {
			_, poolDir, path := exitedHolderFixture(t)
			lease := entryFor(t, poolDir, path)
			tc.mutate(t, path)
			st := statusOf(t, poolDir, path)
			if st.Status != StatusLeased || st.LeaseID != lease.LeaseID || !strings.Contains(st.RecoveryReason, tc.reason) || !strings.Contains(st.RecoveryReason, "holder process") {
				t.Fatalf("status = %+v", st)
			}
		})
	}
}

func TestExitedHolderUntrackedFilesAreKeptInBackupThenFreed(t *testing.T) {
	_, poolDir, path := exitedHolderFixture(t)
	file := filepath.Join(path, "scratch.txt")
	if err := os.WriteFile(file, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := statusOf(t, poolDir, path)
	if st.Status != StatusAvailable || st.RecoveryBackup == "" {
		t.Fatalf("status = %+v", st)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("untracked file left in the freed slot: %v", err)
	}
	assertFileContents(t, filepath.Join(st.RecoveryBackup, "scratch.txt"), "keep me\n")
}

func TestExitedHolderBusySlotIsFreedOnlyOnceIdle(t *testing.T) {
	_, poolDir, path := exitedHolderFixture(t)
	resident := exec.Command("sleep", "60")
	resident.Dir = path
	if err := resident.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = resident.Process.Kill()
		_ = resident.Wait()
	})
	time.Sleep(200 * time.Millisecond)
	if st := statusOf(t, poolDir, path); st.Status != StatusLeased || !strings.Contains(st.RecoveryReason, "process is using") {
		t.Fatalf("status with a resident process = %+v", st)
	}
	_ = resident.Process.Kill()
	_ = resident.Wait()
	if st := statusOf(t, poolDir, path); st.Status != StatusAvailable || st.RecoveryReason != "" {
		t.Fatalf("status once idle = %+v", st)
	}
}

func TestHolderChangeBeforeReclamationIsRespected(t *testing.T) {
	t.Run("a slot returned and re-leased to a live holder keeps the new lease", func(t *testing.T) {
		repo, poolDir := setupLocalRepo(t)
		lease := leaseWithLiveHolder(t, repo, poolDir, 1)
		if err := Release(poolDir, lease.Path); err != nil {
			t.Fatal(err)
		}
		if wt := entryFor(t, poolDir, lease.Path); wt.LeaseHolderPID != 0 || wt.LeaseHolderStart != 0 {
			t.Fatalf("return kept the holder: %#v", wt)
		}
		again := leaseWithLiveHolder(t, repo, poolDir, 1)
		if again.Path != lease.Path {
			t.Fatalf("re-lease got %s, want %s", again.Path, lease.Path)
		}
		assertStillLeased(t, poolDir, again.Path, again.LeaseID)
	})
	t.Run("a release naming the reclaimed lease is refused", func(t *testing.T) {
		_, poolDir, path := exitedHolderFixture(t)
		old := entryFor(t, poolDir, path).LeaseID
		if st := statusOf(t, poolDir, path); st.Status != StatusAvailable {
			t.Fatalf("status = %+v", st)
		}
		err := ReleaseConditional(poolDir, path, "", ReleasePreconditions{ExpectedLeaseID: &old}, nil)
		if !errors.Is(err, ErrLeasePreconditionFailed) {
			t.Fatalf("release of the reclaimed lease = %v, want ErrLeasePreconditionFailed", err)
		}
	})
	t.Run("the predicate judges the slot as it is at reclamation", func(t *testing.T) {
		_, poolDir, path := exitedHolderFixture(t)
		old := holderLiveness
		holderLiveness = func(pid int32, stamp int64) process.Liveness {
			// The slot changes after the holder was judged gone.
			if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("late edit\n"), 0o644); err != nil {
				t.Error(err)
			}
			return old(pid, stamp)
		}
		t.Cleanup(func() { holderLiveness = old })
		if st := statusOf(t, poolDir, path); st.Status != StatusLeased || !strings.Contains(st.RecoveryReason, "tracked changes") {
			t.Fatalf("status = %+v", st)
		}
		assertFileContents(t, filepath.Join(path, "README.md"), "late edit\n")
	})
}

func TestExitedHolderLeaseIsReclaimedOnceUnderConcurrentAcquires(t *testing.T) {
	repo, poolDir, path := exitedHolderFixture(t)
	const callers = 6
	var wg sync.WaitGroup
	results := make([]LeaseInfo, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = AcquireLeaseInfoWithOptions(repo, poolDir, 1, nil, "agent", AcquireOptions{SkipFetch: true, LeaseHolderPID: int32(os.Getppid())})
		}()
	}
	wg.Wait()
	won := -1
	for i, err := range errs {
		if err != nil {
			if !strings.Contains(err.Error(), "in use") {
				t.Fatalf("caller %d failed for another reason: %v", i, err)
			}
			continue
		}
		if won >= 0 {
			t.Fatalf("callers %d and %d both acquired %s and %s", won, i, results[won].Path, results[i].Path)
		}
		won = i
	}
	if won < 0 || results[won].Path != path {
		t.Fatalf("winner = %d (%+v), want the reclaimed slot %s", won, results, path)
	}
	assertStillLeased(t, poolDir, path, results[won].LeaseID)
}
