package pool

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPoolPathIdentityScanStatsRecordedPathsOnce(t *testing.T) {
	poolDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "pool")
	if err := os.Symlink(poolDir, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	var state State
	for _, name := range []string{"1", "2", "3"} {
		makeFakeWorktree(t, poolDir, name, "repo")
		state.Worktrees = append(state.Worktrees, WorktreeEntry{
			Name: name, Path: filepath.Join(alias, name, "repo"),
			Leased: true, LeaseID: "lease-" + name, LeaseHolder: "holder-" + name,
		})
	}
	for _, tc := range []struct {
		name      string
		root      string
		wantStats int
	}{
		{"physical-root", poolDir, 1},
		{"recorded-root", alias, 0},
		{"physical-retry", poolDir, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := make(map[string]int)
			stat := func(path string) (os.FileInfo, error) {
				calls[path]++
				return os.Stat(path)
			}
			got, err := recoverMissingStateEntriesWithStat(tc.root, state, stat)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Worktrees) != len(state.Worktrees) {
				t.Fatalf("scan registered %d entries for %d directories", len(got.Worktrees), len(state.Worktrees))
			}
			for i, wt := range state.Worktrees {
				if calls[wt.Path] != tc.wantStats {
					t.Errorf("recorded path %s statted %d times, want %d", wt.Path, calls[wt.Path], tc.wantStats)
				}
				if entry := got.Worktrees[i]; entry.Path != wt.Path || !entry.Leased || entry.LeaseID != wt.LeaseID || entry.LeaseHolder != wt.LeaseHolder {
					t.Fatalf("scan changed recorded lease: %+v", entry)
				}
			}
		})
	}
}

func TestPoolPathIdentity(t *testing.T) {
	poolDir := t.TempDir()
	path := makeFakeWorktree(t, poolDir, "1", "repo")
	alias := filepath.Join(t.TempDir(), "pool")
	if err := os.Symlink(poolDir, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	recorded := filepath.Join(alias, "1", "repo")
	want := WorktreeEntry{Name: "1", Path: recorded, Leased: true, LeaseID: "lease-one", LeaseHolder: "holder"}
	if err := WriteState(poolDir, State{Worktrees: []WorktreeEntry{want}}); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{poolDir, alias, poolDir} {
		state, err := ReadState(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Worktrees) != 1 {
			t.Fatalf("ReadState(%s) registered %d entries for one directory", root, len(state.Worktrees))
		}
		if state.Worktrees[0].Path != recorded || state.Worktrees[0].LeaseID != want.LeaseID {
			t.Fatalf("ReadState changed the recorded lease: %+v", state.Worktrees)
		}
		if err := WriteState(root, state); err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{path, recorded} {
			got, err := FindByPath(root, target)
			if err != nil || got == nil || got.Path != recorded || got.LeaseID != want.LeaseID {
				t.Fatalf("FindByPath(%s, %s) = %+v, %v", root, target, got, err)
			}
			got, err = releasableWorktree(&state, target, ReleasePreconditions{ExpectedLeaseID: &want.LeaseID})
			if err != nil || got == nil || got.Path != recorded {
				t.Fatalf("release lookup(%s) = %+v, %v", target, got, err)
			}
		}
	}
}

func TestPoolPathIdentityUnmatched(t *testing.T) {
	poolDir := t.TempDir()
	missing := filepath.Join(poolDir, "missing")
	existing := t.TempDir()
	state := State{Worktrees: []WorktreeEntry{{Name: "missing", Path: missing}, {Name: "existing", Path: existing}}}
	if err := WriteState(poolDir, state); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{filepath.Join(poolDir, "other-missing"), t.TempDir()} {
		got, err := FindByPath(poolDir, target)
		if err != nil || got != nil {
			t.Fatalf("unmanaged FindByPath(%s) = %+v, %v", target, got, err)
		}
		if got, err := releasableWorktree(&state, target, ReleasePreconditions{}); err == nil || got != nil {
			t.Fatalf("unmanaged release lookup(%s) = %+v, %v", target, got, err)
		}
	}
	// Exact recorded paths still identify damaged or missing entries.
	got, err := FindByPath(poolDir, missing)
	if err != nil || got == nil || got.Path != missing {
		t.Fatalf("missing recorded path lookup = %+v, %v", got, err)
	}
}

func TestReleasePathIdentity(t *testing.T) {
	repoDir, _ := setupRepo(t)
	poolDir := filepath.Join(t.TempDir(), "pool")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(poolDir, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	path, err := AcquireLease(repoDir, poolDir, 1, nil, "holder")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(poolDir, path)
	if err != nil {
		t.Fatal(err)
	}
	aliasPath := filepath.Join(alias, rel)
	entry, err := FindByPath(alias, aliasPath)
	if err != nil || entry == nil || entry.Path != path {
		t.Fatalf("canonical recorded path lookup = %+v, %v", entry, err)
	}
	wrongID := "not-the-lease"
	if err := ReleaseConditional(alias, aliasPath, "", ReleasePreconditions{ExpectedLeaseID: &wrongID}, nil); err == nil {
		t.Fatal("alias bypassed the lease identity precondition")
	}
	if err := Release(alias, filepath.Join(poolDir, "missing")); err == nil {
		t.Fatal("released a nonexistent unmanaged path")
	}
	if err := Release(alias, t.TempDir()); err == nil {
		t.Fatal("released a distinct directory")
	}
	if err := ReleaseConditional(alias, aliasPath, "", ReleasePreconditions{ExpectedLeaseID: &entry.LeaseID}, nil); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(poolDir)
	if err != nil || len(state.Worktrees) != 1 || state.Worktrees[0].Path != path || state.Worktrees[0].Leased {
		t.Fatalf("release did not preserve the canonical entry: %+v, %v", state, err)
	}
}

// aliasedPool returns a pool directory and a second spelling of it, standing in
// for a symlinked root or a second mount of the same filesystem.
func aliasedPool(t *testing.T) (string, string) {
	t.Helper()
	poolDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "pool")
	if err := os.Symlink(poolDir, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	return poolDir, alias
}

func TestCollapseAliasedEntriesKeepsTheReservation(t *testing.T) {
	poolDir, alias := aliasedPool(t)
	makeFakeWorktree(t, poolDir, "1", "repo")
	makeFakeWorktree(t, poolDir, "2", "repo")
	older := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	newer := older.Add(24 * time.Hour)
	for _, tc := range []struct {
		name     string
		physical WorktreeEntry
		aliased  WorktreeEntry
		wantPath string
		wantID   string
	}{
		{"leased original beats free twin",
			WorktreeEntry{Leased: true, LeaseID: "a", LeaseHolder: "session-a", CreatedAt: older},
			WorktreeEntry{CreatedAt: newer}, poolDir, "a"},
		{"leased twin beats free original",
			WorktreeEntry{CreatedAt: older},
			WorktreeEntry{Leased: true, LeaseID: "b", LeaseHolder: "session-b", CreatedAt: newer}, alias, "b"},
		{"explicit lease beats recovered lease",
			WorktreeEntry{Leased: true, LeaseHolder: RecoveredLeaseHolder, CreatedAt: older},
			WorktreeEntry{Leased: true, LeaseID: "c", LeaseHolder: "session-c", CreatedAt: newer}, alias, "c"},
		{"recovered lease beats free twin",
			WorktreeEntry{CreatedAt: older},
			WorktreeEntry{Leased: true, LeaseHolder: RecoveredLeaseHolder, CreatedAt: newer}, alias, ""},
		{"two free entries keep the older",
			WorktreeEntry{CreatedAt: newer},
			WorktreeEntry{CreatedAt: older}, alias, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			physical, aliased := tc.physical, tc.aliased
			physical.Name, physical.Path = "1", filepath.Join(poolDir, "1", "repo")
			aliased.Name, aliased.Path = "1", filepath.Join(alias, "1", "repo")
			other := WorktreeEntry{Name: "2", Path: filepath.Join(poolDir, "2", "repo"), CreatedAt: older}
			got := collapseAliasedEntries(State{Worktrees: []WorktreeEntry{physical, other, aliased}}, os.Stat)
			if len(got.Worktrees) != 2 {
				t.Fatalf("got %d entries, want 2: %+v", len(got.Worktrees), got.Worktrees)
			}
			kept := got.Worktrees[0]
			if kept.Name != "1" {
				kept = got.Worktrees[1]
			}
			if want := filepath.Join(tc.wantPath, "1", "repo"); kept.Path != want || kept.LeaseID != tc.wantID {
				t.Fatalf("kept %s lease %q, want %s lease %q", kept.Path, kept.LeaseID, want, tc.wantID)
			}
		})
	}
}

func TestCollapseAliasedEntriesLeavesDistinctSlotsAlone(t *testing.T) {
	poolDir := t.TempDir()
	otherPool := t.TempDir()
	makeFakeWorktree(t, poolDir, "1", "repo")
	makeFakeWorktree(t, otherPool, "1", "repo")
	state := State{Worktrees: []WorktreeEntry{
		{Name: "1", Path: filepath.Join(poolDir, "1", "repo"), Leased: true, LeaseID: "a"},
		{Name: "1", Path: filepath.Join(otherPool, "1", "repo")},
		{Name: "1", Path: filepath.Join(poolDir, "missing", "repo")},
	}}
	if got := collapseAliasedEntries(state, os.Stat); len(got.Worktrees) != 3 {
		t.Fatalf("merged entries that are different or unprovable directories: %+v", got.Worktrees)
	}
}

func TestReadStateDropsFreeTwinOfLeasedSlot(t *testing.T) {
	poolDir, alias := aliasedPool(t)
	makeFakeWorktree(t, poolDir, "1", "repo")
	leased := WorktreeEntry{Name: "1", Path: filepath.Join(poolDir, "1", "repo"), CreatedAt: time.Now().Add(-time.Hour).UTC(),
		Leased: true, LeaseID: "lease-1", LeaseHolder: "session-a", LeasedAt: time.Now().Add(-time.Hour).UTC()}
	twin := WorktreeEntry{Name: "1", Path: filepath.Join(alias, "1", "repo"), CreatedAt: time.Now().UTC()}
	setSeedInventory(&leased, nil, true)
	setSeedInventory(&twin, nil, true)
	if err := WriteState(poolDir, State{Worktrees: []WorktreeEntry{leased, twin}}); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{poolDir, alias} {
		got, err := ReadState(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Worktrees) != 1 || !got.Worktrees[0].Leased || got.Worktrees[0].LeaseID != "lease-1" || got.Worktrees[0].Path != leased.Path {
			t.Fatalf("ReadState(%s) = %+v, want only the leased entry", root, got.Worktrees)
		}
	}
}
