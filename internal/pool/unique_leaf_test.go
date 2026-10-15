package pool

import (
	"path/filepath"
	"testing"
)

// canonicalDir resolves path to the single spelling the running platform uses
// for that directory. filepath.EvalSymlinks cleans the separators, resolves
// symlinks (macOS /var -> /private/var), and on Windows expands 8.3 short
// components and normalizes component case, so a path git printed and a path
// filepath.Join built compare equal when they name the same directory.
func canonicalDir(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

func TestAcquire_WithoutUniqueLeafKeepsRepositoryName(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	want := filepath.Join(poolDir, "1", filepath.Base(repoDir))
	if wtPath != want {
		t.Errorf("worktree path = %s, want the unchanged default %s", wtPath, want)
	}
}

func TestAcquire_UniqueLeafNamesWorktreeAfterItsSlot(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{UniqueLeaf: true})
	if err != nil {
		t.Fatalf("AcquireWithOptions failed: %v", err)
	}

	want := filepath.Join(poolDir, "1", filepath.Base(repoDir)+"-1")
	if wtPath != want {
		t.Errorf("worktree path = %s, want %s", wtPath, want)
	}
	// git and Go spell the same directory differently -- git prints forward
	// slashes on every platform, and on Windows the two can also disagree on
	// drive-letter case and 8.3 short components -- so canonicalize both
	// spellings before comparing them.
	toplevel := gitOut(t, wtPath, "rev-parse", "--show-toplevel")
	if got, want := canonicalDir(t, toplevel), canonicalDir(t, wtPath); got != want {
		t.Errorf("git toplevel = %s, want the worktree directory %s", got, want)
	}
}

func TestAcquire_UniqueLeafGivesEachSlotADistinctLeaf(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	first, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{UniqueLeaf: true})
	if err != nil {
		t.Fatalf("first AcquireWithOptions failed: %v", err)
	}
	second, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{UniqueLeaf: true})
	if err != nil {
		t.Fatalf("second AcquireWithOptions failed: %v", err)
	}

	// The point of the option: the last path segment, not just the parent
	// directory, tells the two checkouts apart.
	if filepath.Base(first) == filepath.Base(second) {
		t.Errorf("expected distinct leaf names, got %s for both slots", filepath.Base(first))
	}
}

func TestAcquire_UniqueLeafDoesNotMoveAnExistingWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	shared, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	clearOwnerReservation(t, poolDir, shared)

	// Recycling the slot with the option on must hand back the path already in
	// state: enabling it never renames or invalidates a worktree that exists.
	recycled, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{UniqueLeaf: true})
	if err != nil {
		t.Fatalf("AcquireWithOptions failed: %v", err)
	}

	if recycled != shared {
		t.Errorf("recycled worktree path = %s, want the existing %s", recycled, shared)
	}
}

func TestAcquire_UniqueLeafSlotKeepsItsPathAcrossRecycles(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	first, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{UniqueLeaf: true})
	if err != nil {
		t.Fatalf("first AcquireWithOptions failed: %v", err)
	}
	if err := Release(poolDir, first); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	second, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{UniqueLeaf: true})
	if err != nil {
		t.Fatalf("second AcquireWithOptions failed: %v", err)
	}

	if second != first {
		t.Errorf("recycled worktree path = %s, want the stable %s", second, first)
	}
}

func TestAcquire_UniqueLeafAndSharedLeafSlotsCoexist(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	shared, err := Acquire(repoDir, poolDir, 2, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	unique, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{UniqueLeaf: true})
	if err != nil {
		t.Fatalf("AcquireWithOptions failed: %v", err)
	}

	if shared == unique {
		t.Fatalf("expected two distinct slots, got %s twice", shared)
	}

	statuses, err := List(poolDir)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 worktrees in the pool, got %d", len(statuses))
	}
	paths := map[string]bool{}
	for _, s := range statuses {
		paths[s.Path] = true
	}
	for _, want := range []string{shared, unique} {
		if !paths[want] {
			t.Errorf("expected %s in pool status, got %v", want, paths)
		}
	}
}

func TestAcquireLeaseInfo_UniqueLeafReportsTheUniquePath(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	lease, err := AcquireLeaseInfoWithOptions(repoDir, poolDir, 1, nil, "holder", AcquireOptions{UniqueLeaf: true})
	if err != nil {
		t.Fatalf("AcquireLeaseInfoWithOptions failed: %v", err)
	}

	want := filepath.Join(poolDir, "1", filepath.Base(repoDir)+"-1")
	if lease.Path != want {
		t.Errorf("lease path = %s, want %s", lease.Path, want)
	}
}
