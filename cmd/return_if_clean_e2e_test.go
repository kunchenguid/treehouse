package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/treehouse/v3/internal/pool"
)

// returnLeaseIfClean runs the clean-only return a lease manager issues: pinned to
// the lease it holds and refusing to lose anything.
func returnLeaseIfClean(t *testing.T, repoDir, homeDir string, lease leaseJSONResult) (stdout, stderr string, code int) {
	t.Helper()
	return runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path, "--if-lease-id", lease.LeaseID, "--if-clean")
}

// assertRefusedAndKept checks the whole refusal contract: exit 4, the blocker
// alone on stdout, and the slot still carrying the very same lease.
func assertRefusedAndKept(t *testing.T, repoDir, homeDir string, lease leaseJSONResult, want pool.CleanBlocker, stdout, stderr string, code int) {
	t.Helper()
	if code != ExitNotClean {
		t.Fatalf("expected exit %d, got %d\nstdout: %s\nstderr: %s", ExitNotClean, code, stdout, stderr)
	}
	if got := strings.TrimSpace(stdout); got != string(want) {
		t.Fatalf("expected stdout %q, got %q (stderr: %s)", want, got, stderr)
	}
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Path == lease.Path {
			if entry.Status != "leased" || entry.LeaseID != lease.LeaseID {
				t.Fatalf("refusal must keep the lease: got status %q lease %q, want leased %q", entry.Status, entry.LeaseID, lease.LeaseID)
			}
			return
		}
	}
	t.Fatalf("refused worktree %s vanished from status", lease.Path)
}

func assertReturnedAvailable(t *testing.T, repoDir, homeDir string, lease leaseJSONResult, stdout, stderr string, code int) {
	t.Helper()
	if code != 0 {
		t.Fatalf("expected the clean slot to be returned, exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("a successful return prints nothing on stdout, got %q", stdout)
	}
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Path == lease.Path && entry.Status != "available" {
			t.Fatalf("expected %s available after the return, got %q", lease.Path, entry.Status)
		}
	}
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	return gitCmd(t, dir, "rev-parse", "HEAD")
}

func TestReturnIfCleanReturnsACleanLandedSlot(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")

	stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
	assertReturnedAvailable(t, repoDir, homeDir, lease, stdout, stderr, code)
}

func TestReturnIfCleanRefusesUncommittedChanges(t *testing.T) {
	cases := map[string]func(t *testing.T, wt string){
		"untracked file": func(t *testing.T, wt string) {
			if err := os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("wip\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"tracked edit": func(t *testing.T, wt string) {
			if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("edited\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		// The edit is hidden from `git status`, so only a check that refuses to
		// trust the flag sees it.
		"edit hidden by assume-unchanged": func(t *testing.T, wt string) {
			gitCmd(t, wt, "update-index", "--assume-unchanged", "README.md")
			if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("hidden\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, dirty := range cases {
		t.Run(name, func(t *testing.T) {
			repoDir, homeDir := setupTestRepo(t)
			lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
			dirty(t, lease.Path)
			before, err := os.ReadFile(filepath.Join(lease.Path, "README.md"))
			if err != nil {
				t.Fatal(err)
			}

			stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
			assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByDirty, stdout, stderr, code)
			after, err := os.ReadFile(filepath.Join(lease.Path, "README.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("refusal touched the worktree: README.md %q -> %q", before, after)
			}

			// Plain return is unchanged: --force still cleans and returns it.
			_, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path, "--force")
			if code != 0 {
				t.Fatalf("return --force failed (exit %d): %s", code, stderr)
			}
		})
	}
}

// TestReturnIfCleanRefusesUnpushedCommitsUntilTheyLand covers both commit
// shapes the issue names: a commit on a detached HEAD, reachable only from the
// reflog once reset, and one on a branch created in the slot. Pushing the
// work is what makes the same return succeed.
func TestReturnIfCleanRefusesUnpushedCommitsUntilTheyLand(t *testing.T) {
	for _, branch := range []string{"", "agent/feature"} {
		name := "detached"
		if branch != "" {
			name = "branch"
		}
		t.Run(name, func(t *testing.T) {
			repoDir, homeDir := setupTestRepo(t)
			lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
			if branch != "" {
				gitCmd(t, lease.Path, "switch", "-c", branch)
			}
			if err := os.WriteFile(filepath.Join(lease.Path, "feature.txt"), []byte("work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitCmd(t, lease.Path, "add", "feature.txt")
			gitCmd(t, lease.Path, "commit", "-m", "agent work")
			head := headOf(t, lease.Path)

			stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
			assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByUnpushed, stdout, stderr, code)
			if got := headOf(t, lease.Path); got != head {
				t.Fatalf("refusal moved HEAD from %s to %s", head, got)
			}

			gitCmd(t, lease.Path, "push", "origin", "HEAD:refs/heads/landed")
			stdout, stderr, code = returnLeaseIfClean(t, repoDir, homeDir, lease)
			assertReturnedAvailable(t, repoDir, homeDir, lease, stdout, stderr, code)
		})
	}
}

// TestReturnIfCleanRefusesALiveProcessWithoutTerminatingIt pins the most
// conservative choice: an agent still attached means the slot is still in
// use, so the return neither kills it nor resets under it.
func TestReturnIfCleanRefusesALiveProcessWithoutTerminatingIt(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")

	signals := t.TempDir()
	readyFile := filepath.Join(signals, "ready")
	releaseFile := filepath.Join(signals, "release")
	agent := exec.Command(waitShellBin)
	agent.Dir = lease.Path
	agent.Env = append(os.Environ(), "TREEHOUSE_TEST_READY="+readyFile, "TREEHOUSE_TEST_RELEASE="+releaseFile)
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	var agentErr error
	exited := make(chan struct{})
	go func() { agentErr = agent.Wait(); close(exited) }()
	t.Cleanup(func() {
		os.WriteFile(releaseFile, nil, 0o644)
		<-exited
	})
	deadline := time.Now().Add(60 * time.Second)
	for _, err := os.Stat(readyFile); err != nil; _, err = os.Stat(readyFile) {
		if time.Now().After(deadline) {
			t.Fatal("the agent process never started in the worktree")
		}
		time.Sleep(20 * time.Millisecond)
	}

	stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
	assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByProcesses, stdout, stderr, code)
	select {
	case <-exited:
		t.Fatalf("the clean-only return terminated the agent: %v", agentErr)
	default:
	}

	if err := os.WriteFile(releaseFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	<-exited
	if agentErr != nil {
		t.Fatalf("agent exited with %v", agentErr)
	}
	stdout, stderr, code = returnLeaseIfClean(t, repoDir, homeDir, lease)
	assertReturnedAvailable(t, repoDir, homeDir, lease, stdout, stderr, code)
}

// TestReturnIfCleanSeesACommitThatLandsWhileItWaitsForTheLock is the race the
// feature exists to close: a caller's own checks pass, then a commit lands
// before the reset. Here the commit lands after the return was issued, while
// it is blocked behind another holder of the pool lock, so every check the
// caller could have made is stale. The return's own check, made once it holds
// the lock, must still see the commit. (Should a slow machine start the return
// late, the commit lands before any check at all and is refused just the same.)
func TestReturnIfCleanSeesACommitThatLandsWhileItWaitsForTheLock(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
	poolDir := filepath.Dir(filepath.Dir(lease.Path))

	var head string
	var out, errOut bytes.Buffer
	ret := exec.Command(treehouseBin, "return", lease.Path, "--if-lease-id", lease.LeaseID, "--if-clean")
	ret.Dir = repoDir
	ret.Env = buildEnv(homeDir)
	ret.Stdout, ret.Stderr = &out, &errOut
	var retErr error
	finished := make(chan struct{})
	err := pool.WithStateLock(poolDir, func() error {
		if err := ret.Start(); err != nil {
			return err
		}
		go func() { retErr = ret.Wait(); close(finished) }()
		time.Sleep(500 * time.Millisecond)
		select {
		case <-finished:
			t.Fatalf("return finished while the pool lock was held: %s", errOut.String())
		default:
		}
		if err := os.WriteFile(filepath.Join(lease.Path, "late.txt"), []byte("late\n"), 0o644); err != nil {
			return err
		}
		gitCmd(t, lease.Path, "add", "late.txt")
		gitCmd(t, lease.Path, "commit", "-m", "landed right before the return")
		head = headOf(t, lease.Path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-finished
	code := 0
	if retErr != nil {
		exitErr, ok := retErr.(*exec.ExitError)
		if !ok {
			t.Fatal(retErr)
		}
		code = exitErr.ExitCode()
	}

	assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByUnpushed, out.String(), errOut.String(), code)
	if got := headOf(t, lease.Path); got != head {
		t.Fatalf("the late commit was reset away: HEAD %s, want %s", got, head)
	}
}

// TestReturnIfCleanComposesWithTheLeaseCondition checks the lease condition
// is judged first: a stale caller gets the lease refusal, not a cleanliness
// verdict about a slot that is no longer its own.
func TestReturnIfCleanComposesWithTheLeaseCondition(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
	if err := os.WriteFile(filepath.Join(lease.Path, "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stale := lease
	stale.LeaseID = "not-the-current-lease"
	stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, stale)
	if code != ExitFailure || stdout != "" || !strings.Contains(stderr, "lease precondition failed") {
		t.Fatalf("expected the lease refusal (exit 1, empty stdout), got exit %d stdout %q stderr %s", code, stdout, stderr)
	}
	stdout, stderr, code = returnLeaseIfClean(t, repoDir, homeDir, lease)
	assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByDirty, stdout, stderr, code)
}

func TestReturnIfCleanRejectsForceAndAll(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")

	for _, args := range [][]string{
		{"return", lease.Path, "--if-clean", "--force"},
		{"return", "--all", "--if-clean"},
	} {
		stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, args...)
		if code != ExitFailure || !strings.Contains(stderr, "--if-clean") {
			t.Fatalf("%v: expected a usage refusal naming --if-clean, got exit %d stderr %s", args, code, stderr)
		}
		assertRefusedLeaseKept(t, repoDir, homeDir, lease, stdout)
	}
}

func assertRefusedLeaseKept(t *testing.T, repoDir, homeDir string, lease leaseJSONResult, stdout string) {
	t.Helper()
	if stdout != "" {
		t.Fatalf("a usage refusal prints nothing on stdout, got %q", stdout)
	}
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Path == lease.Path && entry.LeaseID != lease.LeaseID {
			t.Fatalf("a usage refusal released the lease on %s", lease.Path)
		}
	}
}

func TestJJReturnIfClean(t *testing.T) {
	requireJJ(t)
	repoDir, homeDir := setupJJTestRepo(t)

	t.Run("dirty working copy", func(t *testing.T) {
		lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
		if err := os.WriteFile(filepath.Join(lease.Path, "notes.txt"), []byte("wip\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByDirty, stdout, stderr, code)
		if _, err := os.Stat(filepath.Join(lease.Path, "notes.txt")); err != nil {
			t.Fatalf("refusal discarded the working-copy change: %v", err)
		}
		_, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path, "--force")
		if code != 0 {
			t.Fatalf("return --force failed (exit %d): %s", code, stderr)
		}
	})

	t.Run("unpushed commit until pushed", func(t *testing.T) {
		lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
		if err := os.WriteFile(filepath.Join(lease.Path, "feature.txt"), []byte("work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		jjCmd(t, lease.Path, "commit", "-m", "agent work")

		stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByUnpushed, stdout, stderr, code)

		// Pushed with git from the colocated main repository and fetched back,
		// because jj's own push flags differ across the versions it supports.
		work := jjCmd(t, lease.Path, "log", "-r", "@-", "--no-graph", "-T", "commit_id")
		gitCmd(t, repoDir, "push", "origin", work+":refs/heads/landed")
		jjCmd(t, lease.Path, "git", "fetch", "--remote", "origin")
		stdout, stderr, code = returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertReturnedAvailable(t, repoDir, homeDir, lease, stdout, stderr, code)
	})

	t.Run("clean slot", func(t *testing.T) {
		lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
		stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertReturnedAvailable(t, repoDir, homeDir, lease, stdout, stderr, code)
	})
}
