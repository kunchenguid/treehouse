//go:build !windows

package config

import (
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
