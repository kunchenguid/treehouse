package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/treehouse/v3/internal/process"
)

// A worktree that still has a foreign live writer after termination must not be
// handed back to the pool. The point-in-time termination scan misses a process
// that started during the grace period, so killLingeringProcesses re-scans and
// refuses rather than resetting a slot another agent is writing into.
func TestKillLingeringProcesses_RefusesSurvivingLiveWriter(t *testing.T) {
	restore := swapProcessSeams(
		func(string, time.Duration) ([]process.ProcessInfo, error) { return nil, nil },
		func(string) ([]process.ProcessInfo, error) {
			return []process.ProcessInfo{{PID: 4321, Name: "agent"}}, nil
		},
	)
	t.Cleanup(restore)

	err := killLingeringProcesses("/pool/1/repo")
	if err == nil {
		t.Fatal("expected return to refuse a worktree that still has a live writer")
	}
	if !strings.Contains(err.Error(), "still has live processes") {
		t.Fatalf("expected live-writer refusal, got: %v", err)
	}
	if !strings.Contains(err.Error(), "agent (4321)") {
		t.Fatalf("expected the surviving writer named in the refusal, got: %v", err)
	}
}

// A termination failure (e.g. an ancestry-lookup error that would otherwise
// silently protect every process) must surface, and the re-scan must not run:
// the worktree cannot be proven quiet.
func TestKillLingeringProcesses_SurfacesTerminationFailure(t *testing.T) {
	restore := swapProcessSeams(
		func(string, time.Duration) ([]process.ProcessInfo, error) {
			return nil, errors.New("cannot resolve ancestry of process 200")
		},
		func(string) ([]process.ProcessInfo, error) {
			t.Fatal("re-scan must not run when termination fails")
			return nil, nil
		},
	)
	t.Cleanup(restore)

	err := killLingeringProcesses("/pool/1/repo")
	if err == nil {
		t.Fatal("expected a termination failure to be surfaced")
	}
	if !strings.Contains(err.Error(), "could not terminate worktree processes") {
		t.Fatalf("expected termination-failure wrapping, got: %v", err)
	}
}

// The happy path: nothing to terminate and nothing left behind returns cleanly.
func TestKillLingeringProcesses_QuietWorktreeSucceeds(t *testing.T) {
	restore := swapProcessSeams(
		func(string, time.Duration) ([]process.ProcessInfo, error) { return nil, nil },
		func(string) ([]process.ProcessInfo, error) { return nil, nil },
	)
	t.Cleanup(restore)

	if err := killLingeringProcesses("/pool/1/repo"); err != nil {
		t.Fatalf("expected a quiet worktree to return cleanly, got: %v", err)
	}
}

func swapProcessSeams(
	terminate func(string, time.Duration) ([]process.ProcessInfo, error),
	rescan func(string) ([]process.ProcessInfo, error),
) func() {
	origTerminate, origRescan := terminateWorktreeProcesses, unprotectedProcessesInWorktree
	terminateWorktreeProcesses = terminate
	unprotectedProcessesInWorktree = rescan
	return func() {
		terminateWorktreeProcesses = origTerminate
		unprotectedProcessesInWorktree = origRescan
	}
}
