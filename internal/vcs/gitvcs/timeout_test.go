package gitvcs

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGitTimeoutFromEnv(t *testing.T) {
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
