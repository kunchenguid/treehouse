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
