//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestApprovalFilesRejectFIFOWithoutBlocking(t *testing.T) {
	repo := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(repo, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := hookFingerprint("table", []string{"pipe"}, repo)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("approved a FIFO")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening an unsupported FIFO blocked approval")
	}
}

// A listed file can be replaced after approvalFileHash has checked it and
// before it opens it. readApprovalFile is that open, so calling it with the
// stat of the regular file that was there reproduces the window exactly: it
// must reject the FIFO rather than wait for a writer while post_return holds
// the pool lock.
func TestApprovalFilesRejectFIFOSubstitutedAfterCheck(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "deps.txt")
	if err := os.WriteFile(path, []byte("deps"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	checked, err := root.Stat("deps.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readApprovalFile(root, "deps.txt", checked)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("approved a FIFO that replaced the checked file")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening a FIFO substituted after the check blocked approval")
	}
}
