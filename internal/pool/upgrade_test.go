package pool

import (
	"encoding/json"
	"errors"
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

// writeStateKey writes a pool state key whose modification time says when it
// was created.
func writeStateKey(t *testing.T, poolDir string, created time.Time) {
	t.Helper()
	if err := os.WriteFile(stateKeyPath(poolDir), make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stateKeyPath(poolDir), created, created); err != nil {
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

// TestReadState_QuarantinesUnversionedStateBesideKey covers state that an older
// binary rewrote after 3.0 had already run in the pool: the key proves a 3.0
// build wrote here, so the unversioned rewrite may have dropped a real seed
// inventory and must stay quarantined.
func TestReadState_QuarantinesUnversionedStateBesideKey(t *testing.T) {
	poolDir := t.TempDir()
	path := makeFakeWorktree(t, poolDir, "1", "myrepo")
	writeRawState(t, poolDir, State{Worktrees: []WorktreeEntry{{Name: "1", Path: path, CreatedAt: time.Now()}}})
	writeStateKey(t, poolDir, time.Now())

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	entry := state.Worktrees[0]
	if !entry.Leased || entry.SeedInventoryKnown || entry.LeaseHolder != recoveredLeaseHolder {
		t.Fatalf("unversioned state beside a key was not quarantined: %#v", entry)
	}
}

// TestReadState_RecognizesThreeZeroUpgradeQuarantine pins which version-4
// entries count as quarantined by 3.0.0's misreading of pre-3.0 state. 3.0.0
// relabeled every pre-3.0 entry, stamped a lease time on the ones without one,
// then created the state key on its first write. Whether the entry was idle or
// leased before the upgrade, it gets the same returnable label.
func TestReadState_RecognizesThreeZeroUpgradeQuarantine(t *testing.T) {
	stamp := time.Now().Add(-time.Hour).Round(0)
	created := stamp.Add(-48 * time.Hour)
	recovered := func(leasedAt time.Time) WorktreeEntry {
		return WorktreeEntry{CreatedAt: created, Leased: true, LeaseHolder: recoveredLeaseHolder, LeasedAt: leasedAt}
	}
	withLeaseID := recovered(stamp)
	withLeaseID.LeaseID = "0123456789abcdef0123456789abcdef"
	scanned := recovered(stamp)
	scanned.CreatedAt = stamp
	damaged := recovered(stamp)
	damaged.RecoveryError = "unreadable marker"

	cases := []struct {
		name       string
		version    int
		entry      WorktreeEntry
		wantHolder string
	}{
		{"stamped by the upgrade", 4, recovered(stamp), UpgradeLeaseHolder},
		{"leased just before the upgrade", 4, recovered(stamp.Add(-500 * time.Millisecond)), UpgradeLeaseHolder},
		{"leased long before the upgrade", 4, recovered(stamp.Add(-time.Hour)), UpgradeLeaseHolder},
		{"quarantined after the key existed", 4, recovered(stamp.Add(time.Minute)), recoveredLeaseHolder},
		{"lease identity kept by 3.0.0", 4, withLeaseID, recoveredLeaseHolder},
		{"rebuilt by a recovery scan", 4, scanned, recoveredLeaseHolder},
		{"recovered with an unreadable marker", 4, damaged, recoveredLeaseHolder},
		{"written by this build", stateVersion, recovered(stamp), recoveredLeaseHolder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			poolDir := t.TempDir()
			entry := tc.entry
			entry.Name = "1"
			entry.Path = makeFakeWorktree(t, poolDir, "1", "myrepo")
			writeRawState(t, poolDir, State{Version: tc.version, Worktrees: []WorktreeEntry{entry}})
			writeStateKey(t, poolDir, stamp.Add(200*time.Millisecond))

			state, err := ReadState(poolDir)
			if err != nil {
				t.Fatal(err)
			}
			got := state.Worktrees[0]
			if !got.Leased || got.LeaseHolder != tc.wantHolder {
				t.Fatalf("entry read as leased=%v holder %q, want leased by %q", got.Leased, got.LeaseHolder, tc.wantHolder)
			}
			if wantKnown := tc.wantHolder != recoveredLeaseHolder; got.SeedInventoryKnown != wantKnown {
				t.Fatalf("seed inventory known = %v, want %v", got.SeedInventoryKnown, wantKnown)
			}
		})
	}
}

// TestUpgrade_KeepsThreeZeroQuarantineUntilReturned covers pools treehouse
// 3.0.0 already rewrote: every pre-3.0 entry leased as "recovered", with a
// state key created right after. A slot that was idle and one leased just
// before the upgrade look alike, so neither is freed automatically, even once
// detached, clean, merged, and idle; `return` frees both after the operator
// checks them, while a genuinely recovered slot still refuses.
func TestUpgrade_KeepsThreeZeroQuarantineUntilReturned(t *testing.T) {
	repoDir, poolDir := setupLocalRepo(t)
	paths := idleSlots(t, repoDir, poolDir, 3)
	idle, lease, scanned := paths[0], paths[1], paths[2]

	stamp := time.Now().Add(-time.Hour).Round(0)
	created := stamp.Add(-48 * time.Hour)
	var entries []WorktreeEntry
	for _, path := range paths {
		entry := WorktreeEntry{Name: filepath.Base(filepath.Dir(path)), Path: path, CreatedAt: created,
			Leased: true, LeaseHolder: recoveredLeaseHolder, LeasedAt: stamp}
		switch path {
		case lease:
			entry.LeasedAt = stamp.Add(-500 * time.Millisecond)
		case scanned:
			entry.CreatedAt = stamp
		}
		entries = append(entries, entry)
	}
	writeRawState(t, poolDir, State{Version: upgradeQuarantineStateVersion, Worktrees: entries})
	writeStateKey(t, poolDir, stamp.Add(200*time.Millisecond))

	for _, path := range []string{idle, lease} {
		if st := statusOf(t, poolDir, path); st.Status != StatusLeased || st.LeaseHolder != UpgradeLeaseHolder {
			t.Fatalf("slot %s quarantined by the upgrade reads %s held by %q", path, st.Status, st.LeaseHolder)
		}
	}
	got, err := AcquireLease(repoDir, poolDir, len(paths)+1, nil, "after-fix")
	if err != nil {
		t.Fatalf("acquire over a pool 3.0.0 quarantined: %v", err)
	}
	for _, path := range paths {
		if got == path {
			t.Fatalf("acquire reused %s, which the 3.0.0 upgrade quarantined", path)
		}
	}
	for _, path := range []string{idle, lease} {
		if wt := entryFor(t, poolDir, path); !wt.Leased || wt.LeaseHolder != UpgradeLeaseHolder {
			t.Fatalf("slot %s was freed without a return: leased=%v holder %q", path, wt.Leased, wt.LeaseHolder)
		}
	}

	for _, path := range []string{idle, lease} {
		if err := Release(poolDir, path); err != nil {
			t.Fatalf("return of %s, quarantined by the upgrade: %v", path, err)
		}
		if st := statusOf(t, poolDir, path); st.Status != StatusAvailable {
			t.Fatalf("returned slot %s reads %s, want available", path, st.Status)
		}
	}
	if err := Release(poolDir, scanned); !errors.Is(err, ErrSeedInventoryUntrusted) {
		t.Fatalf("return of a genuinely recovered slot = %v, want %v", err, ErrSeedInventoryUntrusted)
	}
}
