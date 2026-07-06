//go:build !windows

package pool

import (
	"os"
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
