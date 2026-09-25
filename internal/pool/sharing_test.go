package pool

import (
	"errors"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/internal/fileclone"
)

func TestSharingOnlyRunsForOptedInFreshSlotAfterBranch(t *testing.T) {
	repo, dir := setupRepo(t)
	original := shareWorktreeFiles
	t.Cleanup(func() { shareWorktreeFiles = original })
	calls := 0
	shareWorktreeFiles = func(source, target string) (fileclone.Report, error) {
		calls++
		if source != repo {
			t.Fatalf("source=%s", source)
		}
		if got := gitOut(t, target, "branch", "--show-current"); got != "feature" {
			t.Fatalf("sharing before branch checkout: %s", got)
		}
		state, err := ReadState(dir)
		if err != nil || len(state.Worktrees) != 1 || !state.Worktrees[0].Leased || state.Worktrees[0].LeaseHolder != acquisitionIncompleteLeaseHolder {
			t.Fatalf("not provisionally reserved: %+v %v", state, err)
		}
		return fileclone.Report{Reason: "test filesystem skip"}, nil
	}
	path, err := AcquireWithOptions(repo, dir, 1, nil, AcquireOptions{APFSSharing: true, Branch: "feature"})
	if err != nil || calls != 1 {
		t.Fatalf("fresh: path=%s calls=%d err=%v", path, calls, err)
	}
	if err := Release(dir, path); err != nil {
		t.Fatal(err)
	}
	reused, err := AcquireWithOptions(repo, dir, 1, nil, AcquireOptions{APFSSharing: true})
	if err != nil || reused != path || calls != 1 {
		t.Fatalf("reuse ran sharing: %s %d %v", reused, calls, err)
	}
}

func TestSharingDefaultDoesNotCallOptimizer(t *testing.T) {
	repo, dir := setupRepo(t)
	original := shareWorktreeFiles
	t.Cleanup(func() { shareWorktreeFiles = original })
	shareWorktreeFiles = func(string, string) (fileclone.Report, error) {
		t.Fatal("default acquisition called sharing")
		return fileclone.Report{}, nil
	}
	if _, err := Acquire(repo, dir, 1, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSharingFailureQuarantinesBeforePublication(t *testing.T) {
	repo, dir := setupRepo(t)
	original := shareWorktreeFiles
	t.Cleanup(func() { shareWorktreeFiles = original })
	shareWorktreeFiles = func(string, string) (fileclone.Report, error) {
		return fileclone.Report{}, errors.New("destination changed")
	}
	lease, err := AcquireLeaseInfoWithOptions(repo, dir, 1, nil, "worker", AcquireOptions{APFSSharing: true})
	if err == nil || !strings.Contains(err.Error(), "quarantined") || lease.Path != "" {
		t.Fatalf("unsafe handoff: %+v %v", lease, err)
	}
	state, err := ReadState(dir)
	if err != nil || len(state.Worktrees) != 1 || !state.Worktrees[0].Leased || !strings.Contains(state.Worktrees[0].LeaseHolder, "APFS") {
		t.Fatalf("quarantine lost: %+v %v", state, err)
	}
	if _, err := Acquire(repo, dir, 1, nil); err == nil {
		t.Fatal("quarantined slot reused")
	}
}
