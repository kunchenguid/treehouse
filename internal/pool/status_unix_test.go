//go:build !windows

package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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
