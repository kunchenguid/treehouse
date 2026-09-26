package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/internal/process"
)

func recoveredFixture(t *testing.T) (string, string, string) {
	t.Helper()
	repo, poolDir := setupLocalRepo(t)
	path := idleSlots(t, repo, poolDir, 1)[0]
	writeRawState(t, poolDir, State{Version: stateVersion, Worktrees: []WorktreeEntry{{Name: "1", Path: path, Leased: true, LeaseHolder: RecoveredLeaseHolder}}})
	return repo, poolDir, path
}

func TestRecoveredAutoFreeGates(t *testing.T) {
	t.Run("clean base head frees for good", func(t *testing.T) {
		_, poolDir, path := recoveredFixture(t)
		for range 2 {
			if st := statusOf(t, poolDir, path); st.Status != StatusAvailable || st.LeaseHolder != "" || st.RecoveryReason != "" {
				t.Fatalf("status = %+v", st)
			}
		}
		if wt := entryFor(t, poolDir, path); wt.Leased || !wt.SeedInventoryKnown {
			t.Fatalf("freed entry reads back as %#v", wt)
		}
	})
	t.Run("caller's own shell in the slot remains leased", func(t *testing.T) {
		_, poolDir, path := recoveredFixture(t)
		oldScan, oldFilter := findProcessesInWorktree, dropProtectedProcesses
		findProcessesInWorktree = func(string) ([]process.ProcessInfo, error) {
			return []process.ProcessInfo{{PID: 42, Name: "zsh"}}, nil
		}
		dropProtectedProcesses = func([]process.ProcessInfo) ([]process.ProcessInfo, error) { return nil, nil }
		t.Cleanup(func() { findProcessesInWorktree, dropProtectedProcesses = oldScan, oldFilter })
		st := statusOf(t, poolDir, path)
		if st.Status != StatusLeased || !strings.Contains(st.RecoveryReason, "process is using") {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("tracked changes remain leased", func(t *testing.T) {
		_, poolDir, path := recoveredFixture(t)
		if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st := statusOf(t, poolDir, path)
		if st.Status != StatusLeased || !strings.Contains(st.RecoveryReason, "tracked changes") {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("unpushed head remains leased", func(t *testing.T) {
		_, poolDir, path := recoveredFixture(t)
		if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("commit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "new.txt"}, {"commit", "-m", "unpushed"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = path
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
		st := statusOf(t, poolDir, path)
		if st.Status != StatusLeased || !strings.Contains(st.RecoveryReason, "not contained") {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("untracked contents are retained in backup then freed", func(t *testing.T) {
		_, poolDir, path := recoveredFixture(t)
		file := filepath.Join(path, "scratch", "notes.txt")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("keep me\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		st := statusOf(t, poolDir, path)
		if st.Status != StatusAvailable {
			t.Fatalf("status = %+v", st)
		}
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("source untracked file remains; err=%v", err)
		}
		matches, err := filepath.Glob(filepath.Join(filepath.Dir(poolDir), "treehouse-recovered-backup-*", "scratch", "notes.txt"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("backup matches %v, err %v", matches, err)
		}
		got, err := os.ReadFile(matches[0])
		if err != nil || string(got) != "keep me\n" {
			t.Fatalf("backup content %q, err %v", got, err)
		}
	})
}

func TestRecoveredAutoFreeOnReturnOfSibling(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	paths := idleSlots(t, repo, poolDir, 2)
	writeRawState(t, poolDir, State{Version: stateVersion, Worktrees: []WorktreeEntry{
		{Name: "1", Path: paths[0], Leased: true, LeaseHolder: RecoveredLeaseHolder},
		{Name: "2", Path: paths[1], Leased: true, LeaseID: "0123456789abcdef0123456789abcdef", LeaseHolder: "agent"},
	}})
	if err := Release(poolDir, paths[1]); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if wt := entryFor(t, poolDir, path); wt.Leased {
			t.Fatalf("%s still leased after returning its sibling: %#v", path, wt)
		}
	}
}

func TestRecoveryReasonClearedByNamedReturn(t *testing.T) {
	_, poolDir, path := recoveredFixture(t)
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t, poolDir, path); st.RecoveryReason == "" {
		t.Fatalf("quarantined slot has no reason: %+v", st)
	}
	if err := Release(poolDir, path); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t, poolDir, path); st.Status != StatusAvailable || st.RecoveryReason != "" {
		t.Fatalf("returned slot reads %+v", st)
	}
}

func TestDamagedRecoveredEntryExplainsReason(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	path := idleSlots(t, repo, poolDir, 1)[0]
	writeRawState(t, poolDir, State{Version: stateVersion, Worktrees: []WorktreeEntry{{Name: "1", Path: path, Leased: true, LeaseHolder: RecoveredLeaseHolder, RecoveryError: "marker loop"}}})
	st := statusOf(t, poolDir, path)
	if st.Status != StatusDamaged || st.LeaseHolder != RecoveredLeaseHolder || !strings.Contains(st.RecoveryReason, "marker could not be read") {
		t.Fatalf("status = %+v", st)
	}
}
