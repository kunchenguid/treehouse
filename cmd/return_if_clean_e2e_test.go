package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// work is what makes the same return succeed. It is pushed to a feature
// branch, never merged into the base: work on any remote-tracking ref has
// landed (a Greptile round-2 regression refused exactly this).
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
		before := jjCmd(t, lease.Path, "log", "-r", "@", "--no-graph", "-T", "commit_id")
		stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByUnsupported, stdout, stderr, code)
		if _, err := os.Stat(filepath.Join(lease.Path, "notes.txt")); err != nil {
			t.Fatalf("refusal discarded the working-copy change: %v", err)
		}
		if after := jjCmd(t, lease.Path, "log", "-r", "@", "--no-graph", "-T", "commit_id"); after != before {
			t.Fatalf("refusal moved @ from %s to %s", before, after)
		}
		// Plain return is unchanged: on a dirty workspace it still asks before
		// cleaning, so --force answers it.
		_, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path, "--force")
		if code != 0 {
			t.Fatalf("plain return --force failed (exit %d): %s", code, stderr)
		}
	})

	t.Run("unpushed commit", func(t *testing.T) {
		lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
		if err := os.WriteFile(filepath.Join(lease.Path, "feature.txt"), []byte("work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		jjCmd(t, lease.Path, "commit", "-m", "agent work")

		stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
		before := jjCmd(t, lease.Path, "log", "-r", "@", "--no-graph", "-T", "commit_id")
		assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByUnsupported, stdout, stderr, code)
		if subject := jjCmd(t, lease.Path, "log", "-r", "@-", "--no-graph", "-T", "description"); subject != "agent work" {
			t.Fatalf("refusal discarded committed work: %q", subject)
		}
		if after := jjCmd(t, lease.Path, "log", "-r", "@", "--no-graph", "-T", "commit_id"); after != before {
			t.Fatalf("refusal moved @ from %s to %s", before, after)
		}
		_, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path)
		if code != 0 {
			t.Fatalf("plain return failed (exit %d): %s", code, stderr)
		}
	})

	t.Run("clean slot", func(t *testing.T) {
		lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
		before := jjCmd(t, lease.Path, "log", "-r", "@", "--no-graph", "-T", "commit_id")
		stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByUnsupported, stdout, stderr, code)
		if after := jjCmd(t, lease.Path, "log", "-r", "@", "--no-graph", "-T", "commit_id"); after != before {
			t.Fatalf("refusal moved @ from %s to %s", before, after)
		}
		_, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path)
		if code != 0 {
			t.Fatalf("plain return failed (exit %d): %s", code, stderr)
		}
	})
}

// seededRepo is setupTestRepo plus a committed .worktreeinclude that seeds one
// ignored file, local.seed, into every acquired worktree.
func seededRepo(t *testing.T) (repoDir, homeDir string) {
	t.Helper()
	repoDir, homeDir = setupTestRepo(t)
	gitCmd(t, repoDir, "config", "core.autocrlf", "false")
	for name, contents := range map[string]string{
		".gitignore":       "*.seed\n",
		".worktreeinclude": "local.seed\n",
		"local.seed":       "secret=1\n",
	} {
		if err := os.WriteFile(filepath.Join(repoDir, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, repoDir, "add", ".gitignore", ".worktreeinclude")
	gitCmd(t, repoDir, "commit", "-m", "seed local.seed")
	gitCmd(t, repoDir, "push", "origin", "main")
	return repoDir, homeDir
}

// Greptile, rounds 1 to 3: the reset deletes every seeded file without reading
// it, and seeded files are ignored, so an edit to one was invisible to the
// dirtiness checks and deleted by a successful --if-clean.
func TestReturnIfCleanRefusesAnEditedOrUnverifiableSeededFile(t *testing.T) {
	t.Run("symlink source", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require elevated privileges")
		}
		repoDir, homeDir := seededRepo(t)
		source := filepath.Join(repoDir, "local.seed")
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target=1\\n", source); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
		seed := filepath.Join(lease.Path, "local.seed")
		stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertReturnedAvailable(t, repoDir, homeDir, lease, stdout, stderr, code)

		repoDir, homeDir = seededRepo(t)
		source = filepath.Join(repoDir, "local.seed")
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target=1\\n", source); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		lease = acquireLeaseJSON(t, repoDir, homeDir, "dibs")
		seed = filepath.Join(lease.Path, "local.seed")
		if err := os.WriteFile(seed, []byte("edited"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code = returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByDirty, stdout, stderr, code)
	})
	t.Run("edited", func(t *testing.T) {
		repoDir, homeDir := seededRepo(t)
		lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
		seed := filepath.Join(lease.Path, "local.seed")
		if err := os.WriteFile(seed, []byte("secret=2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByDirty, stdout, stderr, code)
		if got, err := os.ReadFile(seed); err != nil || string(got) != "secret=2\n" {
			t.Fatalf("the edited seeded file was not kept: %q, %v", got, err)
		}
	})

	t.Run("source gone", func(t *testing.T) {
		repoDir, homeDir := seededRepo(t)
		lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
		if err := os.Remove(filepath.Join(repoDir, "local.seed")); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertRefusedAndKept(t, repoDir, homeDir, lease, pool.BlockedByDirty, stdout, stderr, code)
		if _, err := os.Stat(filepath.Join(lease.Path, "local.seed")); err != nil {
			t.Fatalf("a seeded file that could not be verified was deleted: %v", err)
		}
	})

	t.Run("untouched", func(t *testing.T) {
		repoDir, homeDir := seededRepo(t)
		lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
		stdout, stderr, code := returnLeaseIfClean(t, repoDir, homeDir, lease)
		assertReturnedAvailable(t, repoDir, homeDir, lease, stdout, stderr, code)
		if _, err := os.Stat(filepath.Join(lease.Path, "local.seed")); !os.IsNotExist(err) {
			t.Fatalf("an unchanged seeded file should be cleaned up as usual: %v", err)
		}
	})
}

// A refusal leaves the checkout exactly as found: the branch an agent has
// checked out stays checked out, so its next commit still lands on it.
func TestReturnIfCleanRefusalKeepsTheCheckedOutBranch(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
	gitCmd(t, lease.Path, "switch", "-c", "agent/feature")
	gitCmd(t, lease.Path, "push", "origin", "agent/feature")

	signals := t.TempDir()
	readyFile := filepath.Join(signals, "ready")
	releaseFile := filepath.Join(signals, "release")
	agent := exec.Command(waitShellBin)
	agent.Dir = lease.Path
	agent.Env = append(os.Environ(), "TREEHOUSE_TEST_READY="+readyFile, "TREEHOUSE_TEST_RELEASE="+releaseFile)
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.WriteFile(releaseFile, nil, 0o644)
		agent.Wait()
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
	if branch := gitCmd(t, lease.Path, "symbolic-ref", "--short", "HEAD"); branch != "agent/feature" {
		t.Fatalf("the refusal changed the checkout: HEAD is on %q, want agent/feature", branch)
	}
}

// Greptile, rounds 1 and 3: the return detached HEAD with `git checkout`
// after its first check. The post-checkout hook could create work the reset
// then discarded (round 1), or start a worker the final process check refused
// after the branch had already been detached (round 3). A clean-only return
// runs no checkout: the guarded reset moves HEAD itself, so no checkout hook
// runs and the branch it leaves is not moved. The hook here does both.
func TestReturnIfCleanRunsNoCheckoutHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-checkout fixture requires /bin/sh")
	}
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
	gitCmd(t, lease.Path, "switch", "-c", "agent/feature")
	gitCmd(t, lease.Path, "push", "origin", "agent/feature")
	branchTip := gitCmd(t, lease.Path, "rev-parse", "agent/feature")

	hooks := t.TempDir()
	hook := "#!/bin/sh\nprintf ran > hook-ran\nif [ -n \"$TREEHOUSE_TEST_HOOK_WORKER\" ]; then sleep 30 </dev/null >/dev/null 2>&1 & fi\n"
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repoDir, "config", "core.hooksPath", hooks)
	// An ambient GIT_CONFIG_* hooks override would silently disable the hook
	// and make this test vacuous, so the environment pins it off.
	hookEnv := []string{"GIT_CONFIG_COUNT=0"}
	marker := filepath.Join(lease.Path, "hook-ran")

	// Prove the hook is live for a real checkout in this worktree.
	probe := exec.Command("git", "checkout", "-q", "agent/feature")
	probe.Dir = lease.Path
	probe.Env = append(os.Environ(), hookEnv...)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("probe checkout failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("test setup: the post-checkout hook did not run: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, append(hookEnv, "TREEHOUSE_TEST_HOOK_WORKER=1"), "return", lease.Path, "--if-lease-id", lease.LeaseID, "--if-clean")
	assertReturnedAvailable(t, repoDir, homeDir, lease, stdout, stderr, code)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a clean-only return ran a checkout hook: %v", err)
	}
	if tip := gitCmd(t, repoDir, "rev-parse", "agent/feature"); tip != branchTip {
		t.Fatalf("the return moved branch agent/feature from %s to %s", branchTip, tip)
	}
}
