package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/process"
)

// commitInSlot leaves a slot clean but holding one commit that is not merged
// into main, with its owner reservation gone (a pane closed under the owner).
func commitInSlot(t *testing.T, poolDir, wtPath string) string {
	t.Helper()
	runGit(t, wtPath, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "feature.txt")
	runGit(t, wtPath, "commit", "-m", "wip")
	clearOwnerReservation(t, poolDir, wtPath)
	return gitOut(t, wtPath, "rev-parse", "HEAD")
}

func quietListProcesses(t *testing.T) {
	t.Helper()
	t.Cleanup(swapListProcessSeams(
		func(string) ([]process.ProcessInfo, error) { return nil, nil },
		func([]process.ProcessInfo) ([]process.ProcessInfo, error) { return nil, nil },
	))
}

func listOne(t *testing.T, poolDir string) WorktreeStatus {
	t.Helper()
	statuses, err := List(poolDir)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("expected one worktree, got %#v", statuses)
	}
	return statuses[0]
}

// status and get must agree: a clean slot holding commits that are not merged
// into the base is skipped by get, so status may not call it available.
func TestList_ReportsUnlandedSlotThatAcquireSkips(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	quietListProcesses(t)

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	head := commitInSlot(t, poolDir, wtPath)

	if got := listOne(t, poolDir).Status; got != StatusUnlanded {
		t.Fatalf("status = %q, want %q", got, StatusUnlanded)
	}

	_, err = Acquire(repoDir, poolDir, 1, nil)
	if err == nil {
		t.Fatal("expected acquire to skip the unlanded slot and report a full pool")
	}
	for _, want := range []string{"1 unlanded", `"main"`, StatusUnlanded} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("pool-full error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "in use or dirty (max_trees") {
		t.Errorf("pool-full error fell back to the generic message: %v", err)
	}
	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("unlanded HEAD %s was moved to %s", head, got)
	}
}

// A slot that was merely parked, or whose work has since landed on the base,
// is still available, and get still reuses it.
func TestList_ReportsLandedSlotAvailable(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	quietListProcesses(t)

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	clearOwnerReservation(t, poolDir, wtPath)
	if got := listOne(t, poolDir).Status; got != StatusAvailable {
		t.Fatalf("pristine slot status = %q, want %q", got, StatusAvailable)
	}

	head := commitInSlot(t, poolDir, wtPath)
	runGit(t, repoDir, "merge", "--ff-only", head)
	runGit(t, repoDir, "push", "origin", "main")
	if got := listOne(t, poolDir).Status; got != StatusAvailable {
		t.Fatalf("landed slot status = %q, want %q", got, StatusAvailable)
	}
	reused, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("expected the landed slot to be reused: %v", err)
	}
	if reused != wtPath {
		t.Fatalf("reused %s, want %s", reused, wtPath)
	}
}

// Dirty keeps its own, more urgent, label.
func TestList_DirtyOutranksUnlanded(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	quietListProcesses(t)

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	commitInSlot(t, poolDir, wtPath)
	if err := os.WriteFile(filepath.Join(wtPath, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := listOne(t, poolDir).Status; got != StatusDirty {
		t.Fatalf("status = %q, want %q", got, StatusDirty)
	}
}

// Standing in an unlanded slot must not make it a bulk-return target: it was
// "held only by cwd" while it read available, and still is.
func TestList_UnlandedSlotUnderCwdIsHeldOnlyByCwd(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	quietListProcesses(t)

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	commitInSlot(t, poolDir, wtPath)
	t.Chdir(wtPath)

	got := listOne(t, poolDir)
	if got.Status != StatusHere || !got.HeldOnlyByCwd {
		t.Fatalf("got status %q, HeldOnlyByCwd %v; want %q and true", got.Status, got.HeldOnlyByCwd, StatusHere)
	}
}
