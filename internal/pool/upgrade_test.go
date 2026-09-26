package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// idleSlots acquires n real worktrees and returns them to the pool, leaving
// each detached, clean, and merged, as a pre-3.0 release parked it.
func idleSlots(t *testing.T, repoDir, poolDir string, n int) []string {
	t.Helper()
	var paths []string
	for range n {
		path, err := AcquireLease(repoDir, poolDir, n, nil, "setup")
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	for _, path := range paths {
		if err := Release(poolDir, path); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

// writeRawState writes state exactly as another treehouse release would have
// left it, bypassing WriteState's signing and versioning.
func writeRawState(t *testing.T, poolDir string, s State) {
	t.Helper()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFilePath(poolDir), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func statusOf(t *testing.T, poolDir, path string) WorktreeStatus {
	t.Helper()
	statuses, err := List(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range statuses {
		if st.Path == path {
			return st
		}
	}
	t.Fatalf("worktree %s missing from status %#v", path, statuses)
	return WorktreeStatus{}
}

func entryFor(t *testing.T, poolDir, path string) WorktreeEntry {
	t.Helper()
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, wt := range state.Worktrees {
		if wt.Path == path {
			return wt
		}
	}
	t.Fatalf("worktree %s missing from state %#v", path, state.Worktrees)
	return WorktreeEntry{}
}

// TestUpgrade_AdoptsPre30State reproduces the first run over a pool written by
// treehouse 2.x: no version, no seed inventory, no state key. Every idle slot
// must stay reusable and every lease must keep its holder. A pool that is full
// makes the difference observable: quarantining the idle slot leaves nothing
// for Acquire to hand out.
func TestUpgrade_AdoptsPre30State(t *testing.T) {
	repoDir, poolDir := setupLocalRepo(t)
	paths := idleSlots(t, repoDir, poolDir, 2)
	idle, leased := paths[0], paths[1]
	created := time.Now().Add(-48 * time.Hour).Round(0)
	writeRawState(t, poolDir, State{Worktrees: []WorktreeEntry{
		{Name: "1", Path: idle, CreatedAt: created},
		{Name: "2", Path: leased, CreatedAt: created, Leased: true, LeaseHolder: "agent-7", LeasedAt: created.Add(time.Hour)},
	}})
	if err := os.Remove(stateKeyPath(poolDir)); err != nil {
		t.Fatal(err)
	}

	if st := statusOf(t, poolDir, idle); st.Status != StatusAvailable {
		t.Fatalf("idle pre-3.0 slot reads %s, want available", st.Status)
	}
	if st := statusOf(t, poolDir, leased); st.Status != StatusLeased || st.LeaseHolder != "agent-7" {
		t.Fatalf("pre-3.0 lease reads %s held by %q, want leased by agent-7", st.Status, st.LeaseHolder)
	}
	got, err := AcquireLease(repoDir, poolDir, 2, nil, "after-upgrade")
	if err != nil {
		t.Fatalf("acquire over an upgraded full pool: %v", err)
	}
	if got != idle {
		t.Fatalf("acquired %s, want the idle pre-3.0 slot %s", got, idle)
	}
	if err := Release(poolDir, leased); err != nil {
		t.Fatalf("return of a pre-3.0 lease after upgrade: %v", err)
	}

	data, err := os.ReadFile(stateFilePath(poolDir))
	if err != nil {
		t.Fatal(err)
	}
	var persisted State
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Version != stateVersion {
		t.Fatalf("upgraded state written as version %d, want %d", persisted.Version, stateVersion)
	}
	if _, err := readStateKey(poolDir); err != nil {
		t.Fatalf("upgraded pool has no state key: %v", err)
	}
}

// TestUpgrade_AdoptsTwoXRewriteBesideKey covers issue #151: a 2.x session is
// live across the upgrade, 3.x adopts the pool and writes versioned state plus
// a key, then the still-running 2.x binary rewrites the file without version or
// seed fields. The next 3.x read must adopt that rewrite as well, not
// quarantine the pool.
func TestUpgrade_AdoptsTwoXRewriteBesideKey(t *testing.T) {
	repoDir, poolDir := setupLocalRepo(t)
	paths := idleSlots(t, repoDir, poolDir, 2)
	first, second := paths[0], paths[1]
	created := time.Now().Add(-48 * time.Hour).Round(0)
	writeRawState(t, poolDir, State{Worktrees: []WorktreeEntry{
		{Name: "1", Path: first, CreatedAt: created},
		{Name: "2", Path: second, CreatedAt: created, Leased: true, LeaseHolder: "agent-7", LeasedAt: created.Add(time.Hour)},
	}})
	if err := os.Remove(stateKeyPath(poolDir)); err != nil {
		t.Fatal(err)
	}

	if st := statusOf(t, poolDir, second); st.Status != StatusLeased || st.LeaseHolder != "agent-7" {
		t.Fatalf("pre-3.0 lease reads %s held by %q, want leased by agent-7", st.Status, st.LeaseHolder)
	}
	if _, err := readStateKey(poolDir); err != nil {
		t.Fatalf("3.x did not write a state key while adopting: %v", err)
	}

	leasedAt := created.Add(2 * time.Hour)
	writeRawState(t, poolDir, State{Worktrees: []WorktreeEntry{
		{Name: "1", Path: first, CreatedAt: created, Leased: true, LeaseHolder: "agent-8", LeasedAt: leasedAt},
		{Name: "2", Path: second, CreatedAt: created},
	}})

	if st := statusOf(t, poolDir, second); st.Status != StatusAvailable {
		t.Fatalf("slot released by 2.x beside a key reads %s held by %q, want available", st.Status, st.LeaseHolder)
	}
	if st := statusOf(t, poolDir, first); st.Status != StatusLeased || st.LeaseHolder != "agent-8" {
		t.Fatalf("2.x lease beside a key reads %s held by %q, want leased by agent-8", st.Status, st.LeaseHolder)
	}
	got, err := AcquireLease(repoDir, poolDir, 2, nil, "after-rewrite")
	if err != nil {
		t.Fatalf("acquire over a full pool rewritten by 2.x: %v", err)
	}
	if got != second {
		t.Fatalf("acquired %s, want the slot 2.x released %s", got, second)
	}
	if wt := entryFor(t, poolDir, first); !wt.Leased || wt.LeaseHolder != "agent-8" {
		t.Fatalf("2.x lease changed without a return: %#v", wt)
	}
}

// TestUpgrade_KeepsThreeZeroQuarantineUntilNamedReturn covers pools treehouse
// 3.0.0 already rewrote: every pre-3.0 entry leased as recovered, with or
// without the lease identity it had, and a state key beside it. Nothing tells
// a formerly idle slot from a durable lease, so every one stays leased and is
// never handed out, and a return naming it releases it despite its unknown
// seed inventory.
func TestUpgrade_KeepsThreeZeroQuarantineUntilNamedReturn(t *testing.T) {
	repoDir, poolDir := setupLocalRepo(t)
	paths := idleSlots(t, repoDir, poolDir, 2)
	stamp := time.Now().Add(-time.Hour).Round(0)
	var entries []WorktreeEntry
	for i, path := range paths {
		entry := WorktreeEntry{Name: filepath.Base(filepath.Dir(path)), Path: path, CreatedAt: stamp.Add(-48 * time.Hour),
			Leased: true, LeaseHolder: RecoveredLeaseHolder, LeasedAt: stamp}
		if i == 1 {
			entry.LeaseID = "0123456789abcdef0123456789abcdef"
			entry.LeasedAt = stamp.Add(-time.Hour)
		}
		entries = append(entries, entry)
	}
	writeRawState(t, poolDir, State{Version: stateVersion, Worktrees: entries})

	for _, path := range paths {
		if st := statusOf(t, poolDir, path); st.Status != StatusLeased || st.LeaseHolder != RecoveredLeaseHolder {
			t.Fatalf("slot %s quarantined by 3.0.0 reads %s held by %q", path, st.Status, st.LeaseHolder)
		}
	}
	got, err := AcquireLease(repoDir, poolDir, len(paths)+1, nil, "after-fix")
	if err != nil {
		t.Fatalf("acquire over a pool 3.0.0 quarantined: %v", err)
	}
	for _, path := range paths {
		if got == path {
			t.Fatalf("acquire reused %s, which 3.0.0 quarantined", path)
		}
		if wt := entryFor(t, poolDir, path); !wt.Leased || wt.LeaseHolder != RecoveredLeaseHolder || wt.SeedInventoryKnown {
			t.Fatalf("slot %s changed without a return: %#v", path, wt)
		}
	}

	for _, path := range paths {
		if err := Release(poolDir, path); err != nil {
			t.Fatalf("return naming %s: %v", path, err)
		}
		if st := statusOf(t, poolDir, path); st.Status != StatusAvailable {
			t.Fatalf("returned slot %s reads %s, want available", path, st.Status)
		}
	}
}
