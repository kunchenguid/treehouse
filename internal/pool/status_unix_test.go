//go:build !windows

package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kunchenguid/treehouse/v3/internal/process"
)

// The listing's own Git child runs inside the slot it inspects. A concurrent
// acquisition must not mistake that observer for an occupant and skip an
// available slot.
func TestListGitInspectionDoesNotMakeAcquireSkipSlot(t *testing.T) {
	repo, poolDir := setupRepo(t)
	path, err := Acquire(repo, poolDir, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearOwnerReservation(t, poolDir, path)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gate := filepath.Join(dir, "gate")
	inspecting := filepath.Join(dir, "inspecting")
	wrapper := `#!/bin/sh
for arg in "$@"; do
	if [ "$arg" = status ] && [ -e "$TEST_GIT_GATE" ]; then
		: > "$TEST_GIT_INSPECTING"
		while [ -e "$TEST_GIT_GATE" ]; do sleep 0.05; done
		break
	fi
done
exec "$TEST_REAL_GIT" "$@"
`
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_REAL_GIT", realGit)
	t.Setenv("TEST_GIT_GATE", gate)
	t.Setenv("TEST_GIT_INSPECTING", inspecting)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	listed := make(chan error, 1)
	go func() {
		_, err := List(poolDir)
		listed <- err
	}()
	t.Cleanup(func() { _ = os.Remove(gate) })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(inspecting); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listing never reaches the slot's Git status check")
		}
		time.Sleep(10 * time.Millisecond)
	}

	type acquired struct {
		path string
		err  error
	}
	acquiring := make(chan acquired, 1)
	go func() {
		p, err := Acquire(repo, poolDir, 2, nil)
		acquiring <- acquired{p, err}
	}()
	time.Sleep(300 * time.Millisecond)
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	if err := <-listed; err != nil {
		t.Fatal(err)
	}
	got := <-acquiring
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.path != path {
		t.Fatalf("acquisition skipped the available slot %s under inspection and got %s", path, got.path)
	}
}

// listWithSlotProcess runs List with a real process inside slot. When
// onlyDuringBatch is set, the process lives only for the unlocked batched scan,
// like a Git child that another lock holder ran in the slot.
func listWithSlotProcess(t *testing.T, poolDir, slot string, onlyDuringBatch bool) ([]WorktreeStatus, int) {
	t.Helper()
	start := func() *exec.Cmd {
		cmd := exec.Command("sleep", "30")
		cmd.Dir = slot
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		return cmd
	}
	stop := func(cmd *exec.Cmd) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	var occupant *exec.Cmd
	if !onlyDuringBatch {
		occupant = start()
	}
	originalScan := findProcessesInWorktrees
	t.Cleanup(func() { findProcessesInWorktrees = originalScan })
	scans := 0
	findProcessesInWorktrees = func(paths []string) (map[string][]process.ProcessInfo, error) {
		scans++
		if onlyDuringBatch && scans == 1 {
			transient := start()
			defer stop(transient)
		}
		return originalScan(paths)
	}
	statuses, err := List(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	if occupant != nil {
		return statuses, occupant.Process.Pid
	}
	return statuses, 0
}

func TestListDropsProcessGoneBeforeSlotIsLocked(t *testing.T) {
	repo, poolDir := setupRepo(t)
	path, err := Acquire(repo, poolDir, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearOwnerReservation(t, poolDir, path)
	statuses, _ := listWithSlotProcess(t, poolDir, path, true)
	if len(statuses) != 1 || statuses[0].Status != StatusAvailable || len(statuses[0].Processes) != 0 {
		t.Fatalf("a process gone before the slot was locked keeps it in use: %+v", statuses)
	}
}

func TestListKeepsProcessStillInSlotWhenLocked(t *testing.T) {
	repo, poolDir := setupRepo(t)
	path, err := Acquire(repo, poolDir, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearOwnerReservation(t, poolDir, path)
	statuses, pid := listWithSlotProcess(t, poolDir, path, false)
	if len(statuses) != 1 || statuses[0].Status != StatusInUse {
		t.Fatalf("a process still in the slot is not reported in use: %+v", statuses)
	}
	found := false
	for _, proc := range statuses[0].Processes {
		found = found || int(proc.PID) == pid
	}
	if !found {
		t.Fatalf("expected occupant %d in %+v", pid, statuses[0].Processes)
	}
}
