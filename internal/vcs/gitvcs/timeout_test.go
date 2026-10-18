package gitvcs

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

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
		t.Setenv(GitTimeoutEnvVar, tc.value)
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
	t.Setenv(GitTimeoutEnvVar, "300ms")
	old := gitWaitDelay
	gitWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { gitWaitDelay = old })

	start := time.Now()
	// A shell alias stands in for any git subprocess that never returns: a
	// hung credential helper, hook, or network stall.
	_, err := runGit(repo, "-c", "alias.stall=!sleep 30", "stall")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("stalled git returned after %s; want the deadline to stop it", elapsed)
	}
	if !errors.Is(err, ErrGitTimeout) {
		t.Fatalf("got %v, want ErrGitTimeout", err)
	}
	for _, want := range []string{"index.lock", GitTimeoutEnvVar} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("timeout error %q does not mention %s", err, want)
		}
	}
}

func TestGitWithinDeadlineIsUnaffected(t *testing.T) {
	repo := recoveryRepo(t)
	t.Setenv(GitTimeoutEnvVar, "1m")
	branch, err := CheckedOutBranch(repo)
	if err != nil || branch != "feature" {
		t.Fatalf("CheckedOutBranch = %q, %v; want feature, nil", branch, err)
	}
}
