package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteState_RoundTrip(t *testing.T) {
	poolDir := t.TempDir()
	want := State{Worktrees: []WorktreeEntry{
		{Name: "1", Path: filepath.Join(poolDir, "1", "myrepo"), CreatedAt: time.Now().UTC().Truncate(time.Second)},
	}}

	if err := WriteState(poolDir, want); err != nil {
		t.Fatalf("WriteState: %v", err)
	}

	got, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if len(got.Worktrees) != 1 || got.Worktrees[0].Path != want.Worktrees[0].Path {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestReadState_LoadsPreIdentityLease(t *testing.T) {
	poolDir := t.TempDir()
	stateJSON := `{
  "worktrees": [{
    "name": "1",
    "path": "legacy-worktree",
    "created_at": "2026-07-20T12:00:00Z",
    "leased": true,
    "lease_holder": "legacy-automation",
    "leased_at": "2026-07-20T12:01:00Z"
  }]
}`
	if err := os.WriteFile(stateFilePath(poolDir), []byte(stateJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState rejected pre-identity lease: %v", err)
	}
	if len(state.Worktrees) != 1 {
		t.Fatalf("ReadState returned %d entries, want 1", len(state.Worktrees))
	}
	lease := state.Worktrees[0]
	if !lease.Leased || lease.LeaseID != "" || lease.LeaseHolder != "legacy-automation" || lease.LeasedAt.IsZero() {
		t.Fatalf("pre-identity lease loaded incorrectly: %#v", lease)
	}
}

// TestWriteState_LeavesNoTempFileBehind guards the temp-then-rename approach:
// a successful write must not leave its staging file lying around next to the
// real state file.
func TestWriteState_LeavesNoTempFileBehind(t *testing.T) {
	poolDir := t.TempDir()
	if err := WriteState(poolDir, State{Worktrees: []WorktreeEntry{{Name: "1", Path: "/tmp/x"}}}); err != nil {
		t.Fatalf("WriteState: %v", err)
	}

	entries, err := os.ReadDir(poolDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "treehouse-state.json" {
			t.Fatalf("unexpected leftover file %q in pool dir", e.Name())
		}
	}
}

// TestWriteState_InterruptedWriteNeverTouchesLiveFile simulates a crash that
// happens after the temp file is created but before the atomic rename - the
// same failure mode a killed process or power loss would produce. The
// previously committed state file must be completely unaffected: with the old
// truncate-then-write approach this same interruption would have already
// destroyed the live file's contents.
func TestWriteState_InterruptedWriteNeverTouchesLiveFile(t *testing.T) {
	poolDir := t.TempDir()
	original := State{Worktrees: []WorktreeEntry{
		{Name: "1", Path: filepath.Join(poolDir, "1", "myrepo")},
	}}
	if err := WriteState(poolDir, original); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	before, err := os.ReadFile(stateFilePath(poolDir))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Reproduce WriteState's staging step only, then "crash" before the rename.
	tmp, err := os.CreateTemp(poolDir, filepath.Base(stateFilePath(poolDir))+".tmp-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := tmp.Write([]byte(`{"worktrees": [{"name": "2"`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	tmp.Close()
	// No rename: this is the crash.

	after, err := os.ReadFile(stateFilePath(poolDir))
	if err != nil {
		t.Fatalf("ReadFile after interrupted write: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("live state file changed after interrupted write:\nbefore: %s\nafter:  %s", before, after)
	}

	got, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState after interrupted write: %v", err)
	}
	if len(got.Worktrees) != 1 || got.Worktrees[0].Name != "1" {
		t.Fatalf("ReadState after interrupted write = %+v, want original entry preserved", got)
	}
}

// TestReadState_RecoversFromEmptyFile covers the actual incident: a crash mid
// os.WriteFile truncates the state file to zero bytes. ReadState must not
// error - it must reconcile from the worktree directories still on disk and
// mark them leased so nothing gets silently handed out or destroyed.
func TestReadState_RecoversFromEmptyFile(t *testing.T) {
	poolDir := t.TempDir()
	makeFakeWorktree(t, poolDir, "1", "myrepo")
	makeFakeWorktree(t, poolDir, "2", "myrepo")

	if err := os.WriteFile(stateFilePath(poolDir), nil, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState on empty file returned error: %v", err)
	}
	if len(got.Worktrees) != 2 {
		t.Fatalf("recovered %d worktrees, want 2: %+v", len(got.Worktrees), got.Worktrees)
	}
	for _, wt := range got.Worktrees {
		if !wt.Leased {
			t.Errorf("recovered worktree %s not marked leased", wt.Path)
		}
		if wt.LeaseHolder != recoveredLeaseHolder {
			t.Errorf("recovered worktree %s has lease holder %q, want %q", wt.Path, wt.LeaseHolder, recoveredLeaseHolder)
		}
	}
}

// TestReadState_RecoversFromPartiallyWrittenFile covers a crash that lands
// mid-write with some, but not all, bytes flushed - a truncated-but-nonempty
// file, which also fails json.Unmarshal and must take the same recovery path
// as a fully empty file.
func TestReadState_RecoversFromPartiallyWrittenFile(t *testing.T) {
	poolDir := t.TempDir()
	makeFakeWorktree(t, poolDir, "1", "myrepo")

	full, err := json.MarshalIndent(State{Worktrees: []WorktreeEntry{
		{Name: "1", Path: filepath.Join(poolDir, "1", "myrepo")},
	}}, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	partial := full[:len(full)/2]
	if err := os.WriteFile(stateFilePath(poolDir), partial, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState on partially written file returned error: %v", err)
	}
	if len(got.Worktrees) != 1 || !got.Worktrees[0].Leased {
		t.Fatalf("recovered state = %+v, want 1 leased worktree", got.Worktrees)
	}
}

// TestReadState_RecoveredWorktreesBlockAcquire ensures the recovery path
// actually protects against the failure mode the incident hit: after a
// corrupt state file, Acquire must not hand out (or silently recreate at the
// same path as) a worktree that still exists on disk.
func TestReadState_RecoveredWorktreesBlockAcquire(t *testing.T) {
	repoDir, poolDir := setupLocalRepo(t)
	if _, err := Acquire(repoDir, poolDir, 1, nil); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	if err := os.WriteFile(stateFilePath(poolDir), nil, 0644); err != nil {
		t.Fatalf("truncate state file: %v", err)
	}

	if _, err := Acquire(repoDir, poolDir, 1, nil); err == nil {
		t.Fatal("Acquire after state corruption should refuse to hand out the pool's only worktree, got nil error")
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if len(state.Worktrees) != 1 || !state.Worktrees[0].Leased {
		t.Fatalf("state after recovery = %+v, want the recovered worktree marked leased", state.Worktrees)
	}
}

func makeFakeWorktree(t *testing.T, poolDir, slot, repoName string) string {
	t.Helper()
	wtPath := filepath.Join(poolDir, slot, repoName)
	if err := os.MkdirAll(wtPath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, ".git"), []byte("gitdir: ../../fake.git\n"), 0644); err != nil {
		t.Fatalf("WriteFile .git: %v", err)
	}
	return wtPath
}
