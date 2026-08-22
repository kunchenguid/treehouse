//go:build !windows

package pool

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteState_PreservesExistingFileMode(t *testing.T) {
	poolDir := t.TempDir()
	path := stateFilePath(poolDir)
	if err := os.WriteFile(path, []byte(`{"worktrees":[]}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if err := WriteState(poolDir, State{}); err != nil {
		t.Fatalf("WriteState: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state file mode = %v, want 0600", got)
	}
}

func TestWriteState_NewFileRespectsUmask(t *testing.T) {
	poolDir := t.TempDir()
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)

	if err := WriteState(poolDir, State{}); err != nil {
		t.Fatalf("WriteState: %v", err)
	}

	info, err := os.Stat(stateFilePath(poolDir))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state file mode = %v, want 0600", got)
	}
}

func TestList_CorruptRecoveryFailsClosedWhenSlotUnreadable(t *testing.T) {
	poolDir := t.TempDir()
	wtPath := makeFakeWorktree(t, poolDir, "1", "myrepo")
	slotDir := filepath.Dir(wtPath)
	if err := os.WriteFile(stateFilePath(poolDir), nil, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(slotDir, 0); err != nil {
		t.Fatalf("Chmod unreadable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(slotDir, 0755); err != nil {
			t.Fatalf("restore permissions: %v", err)
		}
	})

	if _, err := List(poolDir); err == nil {
		t.Fatal("List with unreadable corrupt-state recovery slot returned nil error")
	}

	data, err := os.ReadFile(stateFilePath(poolDir))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("state file was rewritten after incomplete recovery: %s", data)
	}
}

func TestPrepareWorktreeTargetPreservesUnreadableEmptyDirectory(t *testing.T) {
	poolDir := t.TempDir()
	slotDir := filepath.Join(poolDir, "1")
	wtPath := filepath.Join(slotDir, "myrepo")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wtPath, 0); err != nil {
		t.Fatalf("Chmod unreadable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(wtPath, 0o755); err != nil {
			t.Fatalf("restore permissions: %v", err)
		}
	})

	usable, obstaclePath, err := prepareWorktreeTarget(slotDir, wtPath)
	if usable || err == nil {
		t.Fatalf("unreadable target result = usable %v, path %q, err %v; want preserved obstacle", usable, obstaclePath, err)
	}
	if obstaclePath != wtPath {
		t.Fatalf("obstacle path = %q, want %q", obstaclePath, wtPath)
	}
	if _, err := os.Lstat(wtPath); err != nil {
		t.Fatalf("unreadable target was removed: %v", err)
	}
}

func TestPrepareWorktreeTargetDoesNotFollowSlotSymlink(t *testing.T) {
	poolDir := t.TempDir()
	externalSlot := filepath.Join(t.TempDir(), "external-slot")
	externalWorktree := filepath.Join(externalSlot, "myrepo")
	if err := os.MkdirAll(externalWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	slotDir := filepath.Join(poolDir, "1")
	if err := os.Symlink(externalSlot, slotDir); err != nil {
		t.Fatal(err)
	}

	usable, obstaclePath, err := prepareWorktreeTarget(slotDir, filepath.Join(slotDir, "myrepo"))
	if usable || err == nil {
		t.Fatalf("symlinked slot result = usable %v, path %q, err %v; want preserved obstacle", usable, obstaclePath, err)
	}
	if obstaclePath != slotDir {
		t.Fatalf("obstacle path = %q, want %q", obstaclePath, slotDir)
	}
	if _, err := os.Stat(externalWorktree); err != nil {
		t.Fatalf("external directory was removed through slot symlink: %v", err)
	}
}
