package pool

import (
	"os"
	"testing"
)

// TestReadState_ValidStateOmitsBrokenMarkerSlot covers the recoverMissingStateEntries
// half of Greptile P1 #2 with a state file that EXISTS and is valid but omits an
// on-disk slot whose marker cannot be read, plus a second healthy on-disk slot.
// ReadState must not abort: the two untracked on-disk slots are recovered (the
// broken one quarantined with a recorded reason, the healthy one cleanly
// leased), and the slot already recorded in the state is left alone.
func TestReadState_ValidStateOmitsBrokenMarkerSlot(t *testing.T) {
	poolDir := t.TempDir()
	tracked := makeFakeWorktree(t, poolDir, "1", "tracked")
	if err := WriteState(poolDir, State{Worktrees: []WorktreeEntry{{Name: "1", Path: tracked}}}); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	broken := makeUnreadableMarkerWorktree(t, poolDir, "2", "myrepo")
	healthy := makeFakeWorktree(t, poolDir, "3", "myrepo")

	got, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState aborted on an untracked broken-marker slot: %v", err)
	}

	if e := findEntryByPath(got, tracked); e == nil || e.RecoveryError != "" {
		t.Fatalf("tracked slot was disturbed: %#v", e)
	}
	if e := findEntryByPath(got, broken); e == nil || !e.Leased || e.RecoveryError == "" {
		t.Fatalf("broken untracked slot was not quarantined with a reason: %#v", e)
	}
	if e := findEntryByPath(got, healthy); e == nil || !e.Leased || e.RecoveryError != "" {
		t.Fatalf("healthy untracked slot was not recovered cleanly: %#v", e)
	}
}

// TestReadState_RecoveryPathsRecoverUnreadableMarkerSymmetrically pins the
// symmetry Greptile P1 #2 demanded: the same on-disk layout (a healthy slot and
// a slot whose marker cannot be read) must be recovered identically whether the
// state file is missing (recoverMissingStateEntries) or corrupt
// (recoverCorruptState). Both paths share recoverOneWorktree, so this test
// fails if either one starts skipping, dropping, or aborting on the broken slot.
func TestReadState_RecoveryPathsRecoverUnreadableMarkerSymmetrically(t *testing.T) {
	recover := func(condition func(t *testing.T, poolDir string)) (State, error) {
		poolDir := t.TempDir()
		makeFakeWorktree(t, poolDir, "1", "myrepo")
		makeUnreadableMarkerWorktree(t, poolDir, "2", "myrepo")
		condition(t, poolDir)
		return ReadState(poolDir)
	}

	missing, err := recover(func(t *testing.T, poolDir string) { /* no state file */ })
	if err != nil {
		t.Fatalf("missing-state recovery aborted: %v", err)
	}
	corrupt, err := recover(func(t *testing.T, poolDir string) {
		if err := os.WriteFile(stateFilePath(poolDir), []byte("{ corrupt"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("corrupt-state recovery aborted: %v", err)
	}

	// Both paths must agree: two slots, the healthy one leased with no reason,
	// the broken one leased with a recorded reason. Running the same assertion
	// against both is the symmetry check itself.
	assert := func(label string, s State) {
		t.Helper()
		if len(s.Worktrees) != 2 {
			t.Fatalf("%s: recovered %d entries, want 2", label, len(s.Worktrees))
		}
		byName := make(map[string]WorktreeEntry, len(s.Worktrees))
		for _, wt := range s.Worktrees {
			byName[wt.Name] = wt
		}
		healthy, ok := byName["1"]
		if !ok || !healthy.Leased || healthy.RecoveryError != "" {
			t.Fatalf("%s: healthy slot misclassified: %#v", label, healthy)
		}
		broken, ok := byName["2"]
		if !ok || !broken.Leased || broken.RecoveryError == "" {
			t.Fatalf("%s: broken slot misclassified: %#v", label, broken)
		}
	}
	assert("missing-state", missing)
	assert("corrupt-state", corrupt)
}
