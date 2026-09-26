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
	t.Run("clean base head frees", func(t *testing.T) {
		_, poolDir, path := recoveredFixture(t)
		if st := statusOf(t, poolDir, path); st.Status != StatusAvailable {
			t.Fatalf("status = %s", st.Status)
		}
	})
	t.Run("live process remains leased", func(t *testing.T) {
		_, poolDir, path := recoveredFixture(t)
		oldScan, oldFilter := findProcessesInWorktree, dropProtectedProcesses
		findProcessesInWorktree = func(string) ([]process.ProcessInfo, error) {
			return []process.ProcessInfo{{PID: 42, Name: "worker"}}, nil
		}
		dropProtectedProcesses = func(p []process.ProcessInfo) ([]process.ProcessInfo, error) { return p, nil }
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
