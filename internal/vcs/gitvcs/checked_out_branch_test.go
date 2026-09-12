package gitvcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "--initial-branch=main")
	gitRun(t, dir, "commit", "--allow-empty", "-m", "one")
	return dir
}

// State (a): a worktree on a branch reports that branch and no error. This is
// also the false-positive guard: a healthy checkout must never be misreported
// as detached or as a read failure.
func TestCheckedOutBranchReadsBranch(t *testing.T) {
	dir := initRepo(t)

	branch, err := CheckedOutBranch(dir)
	if err != nil {
		t.Fatalf("on a branch: unexpected error: %v", err)
	}
	if branch != "main" {
		t.Fatalf("on a branch: got %q, want %q", branch, "main")
	}
}

// A pooled slot is a linked worktree, not a plain repository; the read must
// follow the worktree's own HEAD.
func TestCheckedOutBranchInLinkedWorktree(t *testing.T) {
	dir := initRepo(t)
	slot := filepath.Join(dir, "slot")
	gitRun(t, dir, "worktree", "add", "-b", "feature", slot)

	branch, err := CheckedOutBranch(slot)
	if err != nil {
		t.Fatalf("linked worktree on a branch: unexpected error: %v", err)
	}
	if branch != "feature" {
		t.Fatalf("linked worktree on a branch: got %q, want %q", branch, "feature")
	}
}

// State (b): a detached HEAD answers empty with no error, because naming a tag
// or commit there would be a checkout nobody can resume from.
func TestCheckedOutBranchDetachedIsEmptyNotError(t *testing.T) {
	dir := initRepo(t)
	gitRun(t, dir, "checkout", "--detach")

	branch, err := CheckedOutBranch(dir)
	if err != nil {
		t.Fatalf("detached HEAD must not be an error, got: %v", err)
	}
	if branch != "" {
		t.Fatalf("detached HEAD: got %q, want empty", branch)
	}
}

// State (c): a genuine read failure - the path is not a repository - is an
// error, not an empty string.
func TestCheckedOutBranchReadFailureIsAnError(t *testing.T) {
	notRepo := filepath.Join(t.TempDir(), "not-a-repo")
	if err := os.Mkdir(notRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	branch, err := CheckedOutBranch(notRepo)
	if err == nil {
		t.Fatalf("a path outside any repository must be a read error, got branch %q", branch)
	}
	if branch != "" {
		t.Fatalf("a failed read must not invent a branch, got %q", branch)
	}
}

// The whole point of P1-2: detached HEAD (b) and a read failure (c) must be
// distinguishable. If both collapsed to the same value, a consumer could not
// tell a healthy detached slot from an unreadable one.
func TestCheckedOutBranchDetachedAndReadFailureDiffer(t *testing.T) {
	detached := initRepo(t)
	gitRun(t, detached, "checkout", "--detach")
	detachedBranch, detachedErr := CheckedOutBranch(detached)

	notRepo := filepath.Join(t.TempDir(), "not-a-repo")
	if err := os.Mkdir(notRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	failedBranch, failedErr := CheckedOutBranch(notRepo)

	if detachedErr != nil {
		t.Fatalf("detached HEAD must not error: %v", detachedErr)
	}
	if failedErr == nil {
		t.Fatal("read failure must error")
	}
	if detachedBranch != failedBranch {
		t.Fatalf("test setup: expected both to report no branch name, got %q and %q", detachedBranch, failedBranch)
	}
	// Same empty branch name, but the error is the distinguishing signal and
	// must be present for exactly one of them.
	if (detachedErr == nil) == (failedErr == nil) {
		t.Fatalf("detached and read failure are indistinguishable: detachedErr=%v failedErr=%v", detachedErr, failedErr)
	}
}
