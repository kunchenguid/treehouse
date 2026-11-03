package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupReftableRepo(t *testing.T) (repoDir, poolDir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", "--ref-format=reftable", t.TempDir()).CombinedOutput(); err != nil {
		t.Skipf("this git cannot create a reftable repository: %v: %s", err, out)
	}
	return setupRepo(t, "--ref-format=reftable")
}

func commitAndPushMain(t *testing.T, repoDir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", name)
	runGit(t, repoDir, "commit", "-m", "add "+name)
	runGit(t, repoDir, "push", "origin", "main")
	return gitOut(t, repoDir, "rev-parse", "HEAD")
}

func assertSlotOnCommit(t *testing.T, wtPath, want string) {
	t.Helper()
	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != want {
		t.Fatalf("slot HEAD = %s, want %s", got, want)
	}
	if status := gitOut(t, wtPath, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("expected a clean slot, got %q", status)
	}
}

func TestRelease_ReftableRepoParksHeadOnBaseAndRecycles(t *testing.T) {
	repoDir, poolDir := setupReftableRepo(t)
	mainTip := gitOut(t, repoDir, "rev-parse", "HEAD")

	wtPath, err := AcquireLease(repoDir, poolDir, 1, nil, "agent")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	runGit(t, wtPath, "switch", "-c", "work")
	if err := os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "work.txt")
	runGit(t, wtPath, "commit", "-m", "work")

	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release: %v", err)
	}
	assertSlotOnCommit(t, wtPath, mainTip)

	again, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("reacquire with max_trees 1: %v", err)
	}
	if again != wtPath {
		t.Fatalf("reacquire created %s instead of recycling %s", again, wtPath)
	}
}

func TestAcquire_ReftableRepoReusedSlotMovesHeadToAdvancedBase(t *testing.T) {
	repoDir, poolDir := setupReftableRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release: %v", err)
	}
	newTip := commitAndPushMain(t, repoDir, "advanced.txt")

	again, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if again != wtPath {
		t.Fatalf("reacquire created %s instead of recycling %s", again, wtPath)
	}
	assertSlotOnCommit(t, wtPath, newTip)
}
