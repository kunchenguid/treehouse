package pool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCurrent_InsideWorktreeReturnsIt(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	got, err := Current(poolDir, wtPath)
	if err != nil {
		t.Fatalf("Current failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected Current to find the acquired worktree, got nil")
	}
	if got.Path != wtPath {
		t.Fatalf("expected path %s, got %s", wtPath, got.Path)
	}
}

func TestCurrent_InsideWorktreeSubdirReturnsIt(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	sub := filepath.Join(wtPath, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Current(poolDir, sub)
	if err != nil {
		t.Fatalf("Current failed: %v", err)
	}
	if got == nil || got.Path != wtPath {
		t.Fatalf("expected subdir to resolve to worktree %s, got %#v", wtPath, got)
	}
}

func TestCurrent_OutsideWorktreeReturnsNil(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	if _, err := Acquire(repoDir, poolDir, 4, nil); err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// The main repo checkout is not inside any managed worktree.
	got, err := Current(poolDir, repoDir)
	if err != nil {
		t.Fatalf("Current failed: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for main repo dir, got %#v", got)
	}
}

func TestCurrent_UnmanagedPoolDirHasNoSideEffects(t *testing.T) {
	base := t.TempDir()
	poolDir := filepath.Join(base, "does-not-exist")

	got, err := Current(poolDir, base)
	if err != nil {
		t.Fatalf("Current failed: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for unmanaged pool, got %#v", got)
	}
	if _, err := os.Stat(poolDir); !os.IsNotExist(err) {
		t.Fatalf("expected Current not to create poolDir, stat err = %v", err)
	}
}
