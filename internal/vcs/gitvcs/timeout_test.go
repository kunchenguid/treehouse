package gitvcs

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func setGitTimeoutEnv(t *testing.T, value string) {
	t.Helper()
	t.Setenv(GitTimeoutEnvVar, value)
	resetGitTimeout()
	t.Cleanup(resetGitTimeout)
}

func TestGitTimeoutFromEnv(t *testing.T) {
	var warnings bytes.Buffer
	old := gitTimeoutWarnings
	gitTimeoutWarnings = &warnings
	t.Cleanup(func() { gitTimeoutWarnings = old })
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", defaultGitTimeout},
		{"90s", 90 * time.Second},
		{"0", 0},
		{"not-a-duration", defaultGitTimeout},
		{"-5s", defaultGitTimeout},
	} {
		setGitTimeoutEnv(t, tc.value)
		if got := gitTimeout(); got != tc.want {
			t.Errorf("%s=%q: got %s, want %s", GitTimeoutEnvVar, tc.value, got, tc.want)
		}
	}
	if defaultGitTimeout != 30*time.Minute {
		t.Errorf("defaultGitTimeout = %s, want 30m", defaultGitTimeout)
	}
	for _, bad := range []string{"not-a-duration", "-5s"} {
		if !strings.Contains(warnings.String(), bad) {
			t.Errorf("no warning for %s=%q; got %q", GitTimeoutEnvVar, bad, warnings.String())
		}
	}
	if n := strings.Count(warnings.String(), "\n"); n != 2 {
		t.Errorf("got %d warning lines, want 2 (valid values must not warn): %q", n, warnings.String())
	}
}

func TestGitTimeoutWarnsOncePerProcess(t *testing.T) {
	var warnings bytes.Buffer
	old := gitTimeoutWarnings
	gitTimeoutWarnings = &warnings
	t.Cleanup(func() { gitTimeoutWarnings = old })
	setGitTimeoutEnv(t, "not-a-duration")

	repo := recoveryRepo(t)
	for range 3 {
		if _, err := CheckedOutBranch(repo); err != nil {
			t.Fatalf("CheckedOutBranch: %v", err)
		}
	}
	if n := strings.Count(warnings.String(), "\n"); n != 1 {
		t.Errorf("got %d warning lines across several git commands, want 1: %q", n, warnings.String())
	}
}

func TestDeadlineErrIgnoresCommandThatSucceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	cmd := &gitCmd{ctx: ctx, cancel: cancel, args: []string{"status"}, timeout: time.Nanosecond}
	if err := cmd.deadlineErr(nil); err != nil {
		t.Fatalf("deadlineErr(nil) after the deadline = %v; want nil for a command that finished", err)
	}
	if err := cmd.deadlineErr(errors.New("signal: killed")); !errors.Is(err, ErrGitTimeout) {
		t.Fatalf("deadlineErr(killed) = %v; want ErrGitTimeout", err)
	}
}

func TestStalledGitFailsWithTimeoutDiagnostic(t *testing.T) {
	repo := recoveryRepo(t)
	setGitTimeoutEnv(t, "300ms")
	old := gitWaitDelay
	gitWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { gitWaitDelay = old })

	const stall = 4 * time.Second
	start := time.Now()
	// The deadline kills git but not the sleep it started, which keeps the
	// repository as its working directory; Windows cannot remove the temp
	// repository until it exits.
	t.Cleanup(func() { time.Sleep(time.Until(start.Add(stall + 500*time.Millisecond))) })
	// A shell alias stands in for any git subprocess that never returns: a
	// hung credential helper, hook, or network stall.
	_, err := runGit(repo, "-c", "alias.stall=!sleep 4", "stall")
	if elapsed := time.Since(start); elapsed > stall-time.Second {
		t.Fatalf("stalled git returned after %s; want the deadline to stop it", elapsed)
	}
	if !errors.Is(err, ErrGitTimeout) {
		t.Fatalf("got %v, want ErrGitTimeout", err)
	}
	if strings.Contains(err.Error(), "submodule.recurse") {
		t.Errorf("timeout error %q reports the injected -c pair instead of the caller's command", err)
	}
	for _, want := range []string{"index.lock", GitTimeoutEnvVar} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("timeout error %q does not mention %s", err, want)
		}
	}
}

func TestGitWithinDeadlineIsUnaffected(t *testing.T) {
	repo := recoveryRepo(t)
	setGitTimeoutEnv(t, "1m")
	branch, err := CheckedOutBranch(repo)
	if err != nil || branch != "feature" {
		t.Fatalf("CheckedOutBranch = %q, %v; want feature, nil", branch, err)
	}
}

func TestTimedOutLsRemoteIsOriginAccessError(t *testing.T) {
	repo := recoveryRepo(t)
	mustGit(t, repo, "config", "protocol.ext.allow", "always")
	setGitTimeoutEnv(t, "300ms")
	old := gitWaitDelay
	gitWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { gitWaitDelay = old })

	start := time.Now()
	t.Cleanup(func() { time.Sleep(time.Until(start.Add(4500 * time.Millisecond))) })
	_, _, err := remoteDefaultBranch(repo, "ext::sleep 4")
	if !errors.Is(err, ErrGitTimeout) {
		t.Fatalf("got %v, want ErrGitTimeout", err)
	}
	if !IsOriginAccessError(err) {
		t.Errorf("IsOriginAccessError(%q) = false; a stalled ls-remote must read as an unreachable origin", err)
	}
}

func TestSuccessfulGitOutlivedByPipeHolderSucceeds(t *testing.T) {
	repo := recoveryRepo(t)
	for _, timeout := range []string{"1m", "0"} {
		setGitTimeoutEnv(t, timeout)
		old := gitWaitDelay
		gitWaitDelay = 100 * time.Millisecond
		t.Cleanup(func() { gitWaitDelay = old })

		// Git exits at once, but the background sleep it left behind, like a
		// post-checkout hook's child, holds stdout open past the wait delay.
		out, err := runGit(repo, "-c", "alias.linger=!sleep 1 & echo ok", "linger")
		if err != nil || out != "ok" {
			t.Errorf("%s=%s: got %q, %v; want ok, nil for a git that exited 0 within its deadline", GitTimeoutEnvVar, timeout, out, err)
		}
	}
}

func TestPipeHolderOutlivingDeadlineFailsWithTimeout(t *testing.T) {
	repo := recoveryRepo(t)
	setGitTimeoutEnv(t, "2s")

	const hold = 4 * time.Second
	start := time.Now()
	t.Cleanup(func() { time.Sleep(time.Until(start.Add(time.Second + hold + 500*time.Millisecond))) })
	// Git uses part of the budget, then exits. The background sleep it left
	// behind, like a hook child, holds stdout past the original deadline.
	// A WaitDelay restarted at git's exit would still be running here.
	_, err := runGit(repo, "-c", "alias.linger=!sleep 1; sleep 4 & echo ok", "linger")
	if elapsed := time.Since(start); elapsed > 2700*time.Millisecond {
		t.Fatalf("git returned after %s; want the original deadline to bound a pipe held open after git exited", elapsed)
	}
	if !errors.Is(err, ErrGitTimeout) {
		t.Fatalf("got %v, want ErrGitTimeout", err)
	}
}
