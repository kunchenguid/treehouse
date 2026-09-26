package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestRecoveredSubmoduleChangesRemainLeased(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	sub := filepath.Join(filepath.Dir(repo), "sub")
	runGit(t, "", "init", "--initial-branch=main", sub)
	runGit(t, sub, "config", "user.email", "test@test.com")
	runGit(t, sub, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, sub, "add", ".")
	runGit(t, sub, "commit", "-m", "sub")
	runGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", sub, "sub")
	runGit(t, repo, "config", "-f", ".gitmodules", "submodule.sub.ignore", "all")
	runGit(t, repo, "add", ".gitmodules")
	runGit(t, repo, "commit", "-m", "add ignored submodule")

	path := idleSlots(t, repo, poolDir, 1)[0]
	runGit(t, path, "-c", "protocol.file.allow=always", "submodule", "update", "--init")
	if err := os.WriteFile(filepath.Join(path, "sub", "f.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRawState(t, poolDir, State{Version: stateVersion, Worktrees: []WorktreeEntry{{Name: "1", Path: path, Leased: true, LeaseHolder: RecoveredLeaseHolder}}})
	st := statusOf(t, poolDir, path)
	if st.Status != StatusLeased || !strings.Contains(st.RecoveryReason, "tracked changes") {
		t.Fatalf("status = %+v", st)
	}
}

func TestRecoveredPartialBackupRetriesIntoSameBackup(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory whose entries cannot be renamed")
	}
	_, poolDir, path := recoveredFixture(t)
	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(path, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "b.txt"), []byte("stuck\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	st := statusOf(t, poolDir, path)
	backup := st.RecoveryBackup
	if st.Status != StatusLeased || backup == "" || !strings.Contains(st.RecoveryReason, backup) {
		t.Fatalf("status after partial backup = %+v", st)
	}
	assertFileContents(t, filepath.Join(backup, "a.txt"), "first\n")

	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	st = statusOf(t, poolDir, path)
	if st.Status != StatusAvailable || st.RecoveryBackup != backup {
		t.Fatalf("status after retry = %+v, want available reporting backup %s", st, backup)
	}
	assertFileContents(t, filepath.Join(backup, "a.txt"), "first\n")
	assertFileContents(t, filepath.Join(backup, "a.txt.1"), "second\n")
	assertFileContents(t, filepath.Join(backup, "locked", "b.txt"), "stuck\n")
	if dirs, _ := filepath.Glob(filepath.Join(filepath.Dir(poolDir), "treehouse-recovered-backup-*")); len(dirs) != 1 {
		t.Fatalf("backup folders = %v, want only %s", dirs, backup)
	}
}

func TestRecoveredBackupNamedWhileStateWriteFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a pool directory the state cannot be written into")
	}
	_, poolDir, path := recoveredFixture(t)
	if err := os.WriteFile(filepath.Join(path, "notes.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(poolDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(poolDir, 0o755) })
	backup := filepath.Join(filepath.Dir(poolDir), "treehouse-recovered-backup-"+filepath.Base(poolDir)+"-1")
	for range 2 {
		_, err := List(poolDir)
		if err == nil || !strings.Contains(err.Error(), backup) {
			t.Fatalf("List error = %v, want it to name backup %s", err, backup)
		}
	}
	if wt := entryFor(t, poolDir, path); !wt.Leased {
		t.Fatalf("entry was persisted as freed: %#v", wt)
	}
	assertFileContents(t, filepath.Join(backup, "notes.txt"), "keep\n")
}

func TestRecoveredBackupRefusesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	for name, link := range map[string]func(backup, elsewhere string) error{
		"backup folder": func(backup, elsewhere string) error { return os.Symlink(elsewhere, backup) },
		"folder inside backup": func(backup, elsewhere string) error {
			if err := os.Mkdir(backup, 0o700); err != nil {
				return err
			}
			return os.Symlink(elsewhere, filepath.Join(backup, "scratch"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, poolDir, path := recoveredFixture(t)
			file := filepath.Join(path, "scratch", "notes.txt")
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("keep\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			elsewhere := t.TempDir()
			backup := filepath.Join(filepath.Dir(poolDir), "treehouse-recovered-backup-"+filepath.Base(poolDir)+"-1")
			if err := link(backup, elsewhere); err != nil {
				t.Fatal(err)
			}
			st := statusOf(t, poolDir, path)
			if st.Status != StatusLeased || !strings.Contains(st.RecoveryReason, "not a real directory") {
				t.Fatalf("status = %+v", st)
			}
			assertFileContents(t, file, "keep\n")
			if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
				t.Fatalf("files were moved through the symlink: %v", entries)
			}
		})
	}
}
