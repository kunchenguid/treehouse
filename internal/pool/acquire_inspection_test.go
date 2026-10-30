package pool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/process"
)

func idleAcquisitionSlots(t *testing.T, count int) (repo, poolDir string, paths []string) {
	t.Helper()
	repo, poolDir = setupRepo(t)
	for range count {
		path, err := AcquireLease(repo, poolDir, count, nil, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	for _, path := range paths {
		if err := Release(poolDir, path); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestAcquireSkipsGitLockedCandidateBeforeInspection(t *testing.T) {
	repo, poolDir, paths := idleAcquisitionSlots(t, 2)
	lockPath := gitOut(t, paths[0], "rev-parse", "--path-format=absolute", "--git-path", "index.lock")
	if err := os.WriteFile(lockPath, []byte("keep existing lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "git-trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	original := findProcessesInWorktrees
	t.Cleanup(func() { findProcessesInWorktrees = original })
	findProcessesInWorktrees = func(roots []string) (map[string][]process.ProcessInfo, error) {
		if slices.Contains(roots, paths[0]) {
			t.Errorf("locked candidate reached process inspection: %v", roots)
		}
		return nil, nil
	}
	acquired, err := AcquireLeaseInfo(repo, poolDir, 2, nil, "test")
	if err != nil || acquired.Path != paths[1] || acquired.LeaseID == "" {
		t.Fatalf("acquisition=%+v err=%v; want second slot", acquired, err)
	}
	if data, err := os.ReadFile(lockPath); err != nil || string(data) != "keep existing lock\n" {
		t.Fatalf("existing lock changed: %q, %v", data, err)
	}
	if gitRanInWorktree(t, trace, paths[0], "status") {
		t.Fatalf("locked candidate reached dirty inspection: %s", paths[0])
	}
}

func gitRanInWorktree(t *testing.T, trace, worktree, subcommand string) bool {
	t.Helper()
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	sessions := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event struct {
			Event    string   `json:"event"`
			SID      string   `json:"sid"`
			Argv     []string `json:"argv"`
			Worktree string   `json:"worktree"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event == "start" {
			sessions[event.SID] = slices.Contains(event.Argv, subcommand)
		}
		if event.Event == "def_repo" && sessions[event.SID] && filepath.Clean(event.Worktree) == filepath.Clean(worktree) {
			return true
		}
	}
	return false
}

func TestAcquireResolvesCloneIdentityLazily(t *testing.T) {
	repo, poolDir, paths := idleAcquisitionSlots(t, 3)
	trace := filepath.Join(t.TempDir(), "git-trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	acquired, err := AcquireLeaseInfo(repo, poolDir, 3, nil, "test")
	if err != nil || acquired.Path != paths[0] {
		t.Fatalf("acquisition=%+v err=%v; want first slot", acquired, err)
	}
	if !gitRanInWorktree(t, trace, paths[0], "rev-parse") {
		t.Fatalf("reused slot %s was not identity-checked", paths[0])
	}
	for _, path := range paths[1:] {
		if gitRanInWorktree(t, trace, path, "rev-parse") {
			t.Errorf("unselected slot %s was identity-checked", path)
		}
	}
}

func TestAcquireBatchesProcessInspection(t *testing.T) {
	t.Run("first candidate reused", func(t *testing.T) {
		repo, poolDir, paths := idleAcquisitionSlots(t, 2)
		original := findProcessesInWorktrees
		t.Cleanup(func() { findProcessesInWorktrees = original })
		var scans [][]string
		findProcessesInWorktrees = func(roots []string) (map[string][]process.ProcessInfo, error) {
			scans = append(scans, slices.Clone(roots))
			return nil, nil
		}
		acquired, err := AcquireLeaseInfo(repo, poolDir, 2, nil, "test")
		if err != nil || acquired.Path != paths[0] {
			t.Fatalf("acquisition=%+v err=%v; want first slot", acquired, err)
		}
		if want := [][]string{paths}; !reflect.DeepEqual(scans, want) {
			t.Fatalf("process scans=%v; want only the candidate batch %v", scans, want)
		}
	})
	t.Run("earlier candidate inspected", func(t *testing.T) {
		repo, poolDir, paths := idleAcquisitionSlots(t, 3)
		if err := os.WriteFile(filepath.Join(paths[0], "untracked"), []byte("preserve\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		original := findProcessesInWorktrees
		t.Cleanup(func() { findProcessesInWorktrees = original })
		var scans [][]string
		findProcessesInWorktrees = func(roots []string) (map[string][]process.ProcessInfo, error) {
			scans = append(scans, slices.Clone(roots))
			return map[string][]process.ProcessInfo{paths[1]: {{PID: 123, Name: "codex"}}}, nil
		}
		acquired, err := AcquireLeaseInfo(repo, poolDir, 3, nil, "test")
		if err != nil || acquired.Path != paths[2] {
			t.Fatalf("acquisition=%+v err=%v; want third slot", acquired, err)
		}
		if want := [][]string{paths, {paths[2]}}; !reflect.DeepEqual(scans, want) {
			t.Fatalf("process scans=%v; want one candidate batch and one selected-slot revalidation %v", scans, want)
		}
	})
}

func TestAcquireRecheckSkipsNewProcess(t *testing.T) {
	repo, poolDir, paths := idleAcquisitionSlots(t, 2)
	if err := os.WriteFile(filepath.Join(paths[0], "untracked"), []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := findProcessesInWorktrees
	t.Cleanup(func() { findProcessesInWorktrees = original })
	calls := 0
	findProcessesInWorktrees = func(roots []string) (map[string][]process.ProcessInfo, error) {
		calls++
		if calls > 1 {
			return map[string][]process.ProcessInfo{paths[1]: {{PID: 123, Name: "writer"}}}, nil
		}
		return nil, nil
	}
	if acquired, err := AcquireLeaseInfo(repo, poolDir, 2, nil, "test"); err == nil {
		t.Errorf("in-use candidate acquired: %+v", acquired)
	}
	if calls != 2 {
		t.Errorf("process scans=%d; want batch plus selected-slot revalidation", calls)
	}
	state, err := ReadState(poolDir)
	if err != nil || len(state.Worktrees) != 2 {
		t.Fatalf("failed acquisition altered membership: %+v, %v", state, err)
	}
	for _, wt := range state.Worktrees {
		if wt.Leased || wt.OwnerPID != 0 {
			t.Fatalf("failed acquisition reserved a slot: %+v", wt)
		}
	}
}

func TestAcquireIgnoresProcessScanErrors(t *testing.T) {
	for _, stage := range []string{"batch error", "revalidation error"} {
		t.Run(stage, func(t *testing.T) {
			repo, poolDir, paths := idleAcquisitionSlots(t, 2)
			want := paths[0]
			if stage == "revalidation error" {
				if err := os.WriteFile(filepath.Join(paths[0], "untracked"), []byte("preserve\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = paths[1]
			}
			original := findProcessesInWorktrees
			t.Cleanup(func() { findProcessesInWorktrees = original })
			calls := 0
			findProcessesInWorktrees = func(roots []string) (map[string][]process.ProcessInfo, error) {
				calls++
				if stage == "batch error" || calls > 1 {
					return nil, errors.New("process enumeration unavailable")
				}
				return nil, nil
			}
			stderr := captureStderr(t, func() {
				acquired, err := AcquireLeaseInfo(repo, poolDir, 2, nil, "test")
				if err != nil || acquired.Path != want {
					t.Errorf("acquisition=%+v err=%v; want reuse of %s", acquired, err, want)
				}
			})
			if strings.Contains(stderr, "process enumeration unavailable") {
				t.Errorf("unexpected scan failure diagnostic: %s", stderr)
			}
		})
	}
}

func TestAcquireReportsResetFailure(t *testing.T) {
	repo, poolDir, paths := idleAcquisitionSlots(t, 2)
	lockPath := gitOut(t, paths[0], "rev-parse", "--path-format=absolute", "--git-path", "index.lock")
	original := findProcessesInWorktrees
	t.Cleanup(func() { findProcessesInWorktrees = original })
	calls := 0
	findProcessesInWorktrees = func(roots []string) (map[string][]process.ProcessInfo, error) {
		calls++
		if calls == 1 {
			if err := os.WriteFile(lockPath, []byte("late lock\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return nil, nil
	}
	stderr := captureStderr(t, func() {
		acquired, err := AcquireLeaseInfo(repo, poolDir, 2, nil, "test")
		if err != nil || acquired.Path != paths[1] {
			t.Errorf("acquisition=%+v err=%v; want second slot", acquired, err)
		}
	})
	if !strings.Contains(stderr, "slot 1") || !strings.Contains(stderr, "reset") || !strings.Contains(stderr, "index.lock") {
		t.Fatalf("missing reset failure diagnostic: %s", stderr)
	}
	if data, err := os.ReadFile(lockPath); err != nil || string(data) != "late lock\n" {
		t.Fatalf("late lock changed: %q, %v", data, err)
	}
}
