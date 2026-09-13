package pool

import (
	"os"
	"path/filepath"
	"testing"
)

// makeUnreadableMarkerWorktree creates a slot whose .git marker is a
// self-referential symlink. Stat follows the link and fails with ELOOP, which
// WorktreeBackendNameChecked reports as a genuine read failure rather than a
// missing marker, deterministically and independent of the running user's
// privileges. Symlink-less platforms (or hosts that forbid them) skip.
func makeUnreadableMarkerWorktree(t *testing.T, poolDir, slot, repoName string) string {
	t.Helper()
	wtPath := filepath.Join(poolDir, slot, repoName)
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Symlink(".git", filepath.Join(wtPath, ".git")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return wtPath
}

// findEntryByPath returns a pointer to the WorktreeEntry at path, or nil.
func findEntryByPath(state State, path string) *WorktreeEntry {
	for i := range state.Worktrees {
		if state.Worktrees[i].Path == path {
			return &state.Worktrees[i]
		}
	}
	return nil
}

// TestReadState_RecoverySkipsUnreadableMarkerSlot is the corrupt-state half of
// Greptile P1: a truncated state file plus one healthy slot and one slot whose
// .git marker cannot be resolved must not abort recovery. The healthy slot is
// recovered normally, and the broken slot is recorded (leased, with the reason)
// instead of silently disappearing or failing the whole scan.
func TestReadState_RecoverySkipsUnreadableMarkerSlot(t *testing.T) {
	poolDir := t.TempDir()
	healthy := makeFakeWorktree(t, poolDir, "1", "myrepo")
	broken := makeUnreadableMarkerWorktree(t, poolDir, "2", "myrepo")

	if err := os.WriteFile(stateFilePath(poolDir), []byte("{ corrupt"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("recovery aborted because of one broken slot: %v", err)
	}

	healthyEntry := findEntryByPath(got, healthy)
	if healthyEntry == nil {
		t.Fatalf("healthy slot %s was not recovered", healthy)
	}
	if !healthyEntry.Leased || healthyEntry.RecoveryError != "" {
		t.Fatalf("healthy slot misclassified: %#v", *healthyEntry)
	}

	brokenEntry := findEntryByPath(got, broken)
	if brokenEntry == nil {
		t.Fatalf("broken slot %s vanished from recovery", broken)
	}
	if !brokenEntry.Leased || brokenEntry.RecoveryError == "" {
		t.Fatalf("broken slot must be leased with a recorded reason: %#v", *brokenEntry)
	}
}

// TestReadState_MissingStateSkipsUnreadableMarkerSlot is the missing-state half
// of Greptile P1: with no state file at all, the same scan (via
// recoverMissingStateEntries) must skip the broken slot rather than abort.
func TestReadState_MissingStateSkipsUnreadableMarkerSlot(t *testing.T) {
	poolDir := t.TempDir()
	healthy := makeFakeWorktree(t, poolDir, "1", "myrepo")
	broken := makeUnreadableMarkerWorktree(t, poolDir, "2", "myrepo")
	// No state file is written: ReadState takes the missing-file path.

	got, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("recovery aborted because of one broken slot: %v", err)
	}

	if healthyEntry := findEntryByPath(got, healthy); healthyEntry == nil || healthyEntry.RecoveryError != "" {
		t.Fatalf("healthy slot was not recovered cleanly: %#v", healthyEntry)
	}
	if brokenEntry := findEntryByPath(got, broken); brokenEntry == nil || !brokenEntry.Leased || brokenEntry.RecoveryError == "" {
		t.Fatalf("broken slot must be recorded leased with a reason: %#v", brokenEntry)
	}
}

// TestReadState_RecoveryWithoutBrokenSlotUnchanged pins the regression
// guarantee: when no slot's marker is unreadable, recovery behaves exactly as
// before (healthy slots recovered leased, no recovery_error recorded).
func TestReadState_RecoveryWithoutBrokenSlotUnchanged(t *testing.T) {
	poolDir := t.TempDir()
	healthy := makeFakeWorktree(t, poolDir, "1", "myrepo")
	if err := os.WriteFile(stateFilePath(poolDir), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if len(got.Worktrees) != 1 {
		t.Fatalf("recovered %d entries, want 1", len(got.Worktrees))
	}
	if got.Worktrees[0].Path != healthy || !got.Worktrees[0].Leased || got.Worktrees[0].RecoveryError != "" {
		t.Fatalf("healthy recovery changed: %#v", got.Worktrees[0])
	}
}

// TestList_RecoveryReportsBrokenMarkerSlotDamaged exercises the status surface:
// after corrupt-state recovery, `List` (the backing call for `treehouse status`)
// must not abort, must list the healthy slot, and must report the broken slot
// as damaged with the recorded reason.
func TestList_RecoveryReportsBrokenMarkerSlotDamaged(t *testing.T) {
	repoDir, poolDir := setupLocalRepo(t)
	healthy, err := Acquire(repoDir, poolDir, 3, nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	clearOwnerReservation(t, poolDir, healthy)
	broken := makeUnreadableMarkerWorktree(t, poolDir, "2", "myrepo")

	if err := os.WriteFile(stateFilePath(poolDir), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	statuses, err := List(poolDir)
	if err != nil {
		t.Fatalf("List aborted because of one broken slot: %v", err)
	}

	var healthySt, brokenSt *WorktreeStatus
	for i := range statuses {
		switch statuses[i].Path {
		case healthy:
			healthySt = &statuses[i]
		case broken:
			brokenSt = &statuses[i]
		}
	}
	if healthySt == nil {
		t.Fatalf("healthy slot %s not listed", healthy)
	}
	if brokenSt == nil {
		t.Fatalf("broken slot %s not listed", broken)
	}
	if brokenSt.Status != StatusDamaged {
		t.Fatalf("broken slot status = %q, want %q", brokenSt.Status, StatusDamaged)
	}
	if brokenSt.BranchErr == "" {
		t.Fatal("broken slot must report the recorded reason, got empty BranchErr")
	}
}

// TestAcquire_DoesNotHandOutUnreadableMarkerRecoveredSlot pins the
// "unusable" half: a broken slot recovered from a corrupt state file is leased
// (RecoveryError set), so Acquire must not return it. With headroom in the
// pool it creates a fresh slot at a different path instead.
func TestAcquire_DoesNotHandOutUnreadableMarkerRecoveredSlot(t *testing.T) {
	repoDir, poolDir := setupLocalRepo(t)
	broken := makeUnreadableMarkerWorktree(t, poolDir, "1", "myrepo")
	if err := os.WriteFile(stateFilePath(poolDir), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	acquired, err := Acquire(repoDir, poolDir, 3, nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if acquired == broken {
		t.Fatalf("Acquire handed out the unreadable-marker recovered slot %s", broken)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	brokenEntry := findEntryByPath(state, broken)
	if brokenEntry == nil || !brokenEntry.Leased || brokenEntry.RecoveryError == "" {
		t.Fatalf("broken slot was not quarantined as leased: %#v", brokenEntry)
	}
}
