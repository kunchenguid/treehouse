package pool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWithStateLockTimeout_ExpiresWhileLockHeld(t *testing.T) {
	poolDir := t.TempDir()
	lockPath := filepath.Join(poolDir, "treehouse-state.lock")
	holder, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := lockFile(holder); err != nil {
		t.Fatal(err)
	}
	defer unlockFile(holder)

	started := time.Now()
	err = WithStateLockTimeout(poolDir, 200*time.Millisecond, func() error {
		t.Fatal("callback ran while the lock was held by another process")
		return nil
	})
	elapsed := time.Since(started)
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("expected ErrLockTimeout, got %v", err)
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("timed out too early: %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timed out too late: %v", elapsed)
	}
}

func TestWithStateLockTimeout_AcquiresWhenFree(t *testing.T) {
	poolDir := t.TempDir()
	ran := false
	if err := WithStateLockTimeout(poolDir, time.Second, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("callback did not run")
	}
}

func TestAcquireWithOptions_TimeoutOnHeldLock(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(poolDir, "treehouse-state.lock")
	holder, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := lockFile(holder); err != nil {
		t.Fatal(err)
	}
	defer unlockFile(holder)

	started := time.Now()
	_, err = AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{
		SkipFetch: true,
		Timeout:   300 * time.Millisecond,
	})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("expected ErrLockTimeout wrap, got %v", err)
	}
	if elapsed < 200*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("unexpected elapsed %v for 300ms timeout", elapsed)
	}
}
