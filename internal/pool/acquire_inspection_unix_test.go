//go:build !windows

package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/process"
)

func TestAcquireSkipsProcessInspectionWhenIdentityUnverifiable(t *testing.T) {
	repo, poolDir, _ := idleAcquisitionSlots(t, 2)
	requester, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := `#!/bin/sh
if [ "$(pwd -P)" = "$TEST_REQUESTER" ]; then
	for arg in "$@"; do
		if [ "$arg" = --git-common-dir ]; then
			echo "requester identity unavailable" >&2
			exit 128
		fi
	done
fi
exec "$TEST_REAL_GIT" "$@"
`
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_REAL_GIT", realGit)
	t.Setenv("TEST_REQUESTER", requester)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	original := findProcessesInWorktrees
	t.Cleanup(func() { findProcessesInWorktrees = original })
	var scanned []string
	findProcessesInWorktrees = func(roots []string) (map[string][]process.ProcessInfo, error) {
		scanned = append(scanned, roots...)
		return nil, nil
	}
	before, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := AcquireLeaseInfo(repo, poolDir, 2, nil, "test")
	if err == nil || !strings.Contains(err.Error(), "2 whose clone identity cannot be verified") {
		t.Fatalf("acquisition=%+v err=%v; want unverifiable-identity exhaustion", acquired, err)
	}
	if len(scanned) != 0 {
		t.Fatalf("process inspection ran for %v; reuse is impossible without a requester identity", scanned)
	}
	after, err := ReadState(poolDir)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("refused reuse changed state: %+v -> %+v (%v)", before, after, err)
	}
}
