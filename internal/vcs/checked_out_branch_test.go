package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// P1-1 catch: a markerless (damaged) slot nested under another git repository
// must never report that repository's branch. Raw git resolves upward from the
// markerless directory, so the slot API has to refuse to read it at all.
func TestCheckedOutBranchMarkerlessSlotDoesNotInheritEnclosingBranch(t *testing.T) {
	isolateUserConfig(t)
	repo := gitRepoWithBranch(t, "")
	slot := filepath.Join(repo, "pool", "1", "slot")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Show the hazard is real: an ungated `git -C <slot> symbolic-ref` answers
	// with the enclosing repository's branch.
	out, err := exec.Command("git", "-C", slot, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		t.Fatalf("raw git on the markerless slot failed: %v", err)
	}
	if enclosing := strings.TrimSpace(string(out)); enclosing != "main" {
		t.Fatalf("test setup: expected raw git to inherit %q, got %q", "main", enclosing)
	}

	branch, detached, err := CheckedOutBranch(slot)
	if err != nil {
		t.Fatalf("a markerless slot must not fail status: %v", err)
	}
	if branch != "" {
		t.Fatalf("markerless slot inherited a branch: got %q, want empty", branch)
	}
	if detached {
		t.Fatalf("markerless slot must not be reported detached")
	}
}

// P1-1 false positive: a healthy git worktree slot still reports its own
// branch. The marker gate must not swallow valid slots.
func TestCheckedOutBranchHealthyGitSlotReportsOwnBranch(t *testing.T) {
	isolateUserConfig(t)
	repo := gitRepoWithBranch(t, "")
	slot := filepath.Join(repo, "pool", "1", "slot")
	mustRun(t, repo, "git", "worktree", "add", "-b", "feature", slot)

	branch, detached, err := CheckedOutBranch(slot)
	if err != nil {
		t.Fatalf("healthy slot: unexpected error: %v", err)
	}
	if branch != "feature" {
		t.Fatalf("healthy slot: got branch %q, want %q", branch, "feature")
	}
	if detached {
		t.Fatalf("healthy slot on a branch must not be reported detached")
	}
}

// A healthy pooled slot is detached by default and must read as detached, not
// as a failed read.
func TestCheckedOutBranchHealthyDetachedSlotIsDetached(t *testing.T) {
	isolateUserConfig(t)
	repo := gitRepoWithBranch(t, "")
	slot := filepath.Join(repo, "pool", "1", "slot")
	mustRun(t, repo, "git", "worktree", "add", "--detach", slot)

	branch, detached, err := CheckedOutBranch(slot)
	if err != nil {
		t.Fatalf("detached slot must not be a read error: %v", err)
	}
	if branch != "" {
		t.Fatalf("detached slot: got branch %q, want empty", branch)
	}
	if !detached {
		t.Fatalf("detached slot must be reported detached")
	}
}

// A slot that carries a .git marker but cannot be read is a genuine error, not
// an empty branch: this is the read failure P1-2 requires consumers to see.
func TestCheckedOutBranchBrokenGitMarkerIsAnError(t *testing.T) {
	isolateUserConfig(t)
	slot := filepath.Join(t.TempDir(), "slot")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	// A .git file pointing at a gitdir that does not exist.
	if err := os.WriteFile(filepath.Join(slot, ".git"), []byte("gitdir: /nonexistent/treehouse-test-gitdir\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	branch, detached, err := CheckedOutBranch(slot)
	if err == nil {
		t.Fatalf("broken git marker must be a read error, got branch %q detached=%v", branch, detached)
	}
	if branch != "" {
		t.Fatalf("failed read must not invent a branch, got %q", branch)
	}
	if detached {
		t.Fatalf("failed read must not be reported as detached")
	}
}

// A jj slot has no git branch to report; it answers empty with no error and no
// attempt to shell out to git.
func TestCheckedOutBranchJJSlotReportsNothing(t *testing.T) {
	isolateUserConfig(t)
	slot := fakeJJOnlyRepo(t)

	branch, detached, err := CheckedOutBranch(slot)
	if err != nil {
		t.Fatalf("jj slot must not be a read error: %v", err)
	}
	if branch != "" || detached {
		t.Fatalf("jj slot: got branch %q detached=%v, want empty and not detached", branch, detached)
	}
}
