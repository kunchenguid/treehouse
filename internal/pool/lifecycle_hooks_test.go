package pool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleasePostReturnCompletesBeforeReuse(t *testing.T) {
	repo, poolDir := setupRepo(t)
	lease, err := AcquireLeaseInfo(repo, poolDir, 4, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "probe")
	hook := quoteForShell(os.Args[0]) + " -test.run=^TestPostReturnLockProbe$ -- " + quoteForShell(poolDir) + " " + quoteForShell(sentinel)
	if err := ReleaseConditionalWithHooks(poolDir, lease.Path, "", ReleasePreconditions{}, nil, []string{hook}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "released and locked" {
		t.Fatalf("post_return protection: %q", data)
	}
}

// Invoked by a hook in a separate process so the probe exercises the actual
// cross-process state lock, not just goroutine scheduling.
func TestPostReturnLockProbe(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	poolDir, sentinel := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0].Leased || state.Worktrees[0].OwnerPID != 0 {
		t.Fatalf("hook ran before release was persisted: %+v", state)
	}
	done := make(chan error, 1)
	go func() { done <- WithStateLock(poolDir, func() error { return nil }) }()
	select {
	case err := <-done:
		t.Fatalf("pool lock available during cleanup: %v", err)
	case <-time.After(200 * time.Millisecond):
		if err := os.WriteFile(sentinel, []byte("released and locked"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostReturnSkipsFailedAndDamagedReleases(t *testing.T) {
	for _, damaged := range []bool{false, true} {
		name := "before reset failure"
		if damaged {
			name = "missing marker"
		}
		t.Run(name, func(t *testing.T) {
			repo, poolDir := setupRepo(t)
			lease, err := AcquireLeaseInfo(repo, poolDir, 4, nil, "test")
			if err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(t.TempDir(), "hook-ran")
			hook := "echo ran > " + quoteForShell(sentinel)
			failure := errors.New("cannot stop writers")
			beforeReset := func() error { return failure }
			if damaged {
				if err := os.Remove(filepath.Join(lease.Path, ".git")); err != nil {
					t.Fatal(err)
				}
				beforeReset = nil
			}
			err = ReleaseConditionalWithHooks(poolDir, lease.Path, "", ReleasePreconditions{}, beforeReset, []string{hook}, nil)
			if !damaged && !errors.Is(err, failure) {
				t.Fatalf("release error: %v", err)
			}
			if damaged && err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatal("post_return ran without a successful reset")
			}
		})
	}
}

func TestAcquirePostGetRunsOutsideStateLock(t *testing.T) {
	repo, poolDir := setupRepo(t)
	sentinel := filepath.Join(t.TempDir(), "probe")
	hook := quoteForShell(os.Args[0]) + " -test.run=^TestHookLockProbe$ -- " + quoteForShell(poolDir) + " " + quoteForShell(sentinel)
	if _, err := AcquireWithOptions(repo, poolDir, 4, nil, AcquireOptions{PostGet: []string{hook}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "unlocked" {
		t.Fatalf("post_get held state lock: %q", data)
	}
}
