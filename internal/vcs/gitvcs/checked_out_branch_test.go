package gitvcs

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// A slot on a branch reports that branch; a detached HEAD reports empty with no
// error, because naming a tag or commit there would be a checkout nobody can
// resume from.
func TestCheckedOutBranchReportsBranchAndDetachedHonestly(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
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
	run("init", "--initial-branch=main")
	run("commit", "--allow-empty", "-m", "one")

	branch, err := CheckedOutBranch(dir)
	if err != nil {
		t.Fatalf("on a branch: unexpected error: %v", err)
	}
	if branch != "main" {
		t.Fatalf("on a branch: got %q, want %q", branch, "main")
	}

	run("checkout", "--detach")

	branch, err = CheckedOutBranch(dir)
	if err != nil {
		t.Fatalf("detached HEAD must not be an error, got: %v", err)
	}
	if branch != "" {
		t.Fatalf("detached HEAD: got %q, want empty", branch)
	}
}

// A path that is not a repository answers empty rather than failing status.
func TestCheckedOutBranchOutsideRepoIsEmpty(t *testing.T) {
	branch, err := CheckedOutBranch(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "" {
		t.Fatalf("got %q, want empty", branch)
	}
}
