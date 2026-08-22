//go:build !windows

package gitvcs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSeedWorktreeSkipsFIFOWithoutBlocking(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "secrets.pipe\n")
	if err := syscall.Mkfifo(filepath.Join(repo, "secrets.pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() { result <- SeedWorktree(repo, worktree) }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(worktree, "secrets.pipe")); !os.IsNotExist(err) {
			t.Fatalf("FIFO was unexpectedly seeded: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("seeding blocked while reading a FIFO")
	}
}
