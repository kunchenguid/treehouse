package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func writeApprovalHooks(t *testing.T, repo, marker string, paths []string) {
	t.Helper()
	table := map[string]any{
		"approval_files": paths,
		"post_create":    []string{"echo create >> hook-events.txt"},
		"post_get":       []string{"echo get >> hook-events.txt"},
		"post_return":    []string{"echo return >> hook-events.txt"},
		"pre_destroy":    []string{"echo destroy > " + quoteReturnPath(marker)},
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(map[string]any{"hooks": table}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "treehouse.toml"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupApprovalFilesRepo(t *testing.T) (repo, home, marker string) {
	t.Helper()
	repo, home = setupTestRepo(t)
	// Approval compares dependency bytes between the main and execution
	// worktrees, so keep checkouts free of the host's line-ending conversion.
	gitCmd(t, repo, "config", "core.autocrlf", "false")
	for _, path := range []string{"setup.sh", "package.json"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte("approved\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prepareHookLog(t, repo, "setup.sh", "package.json")
	marker = filepath.Join(t.TempDir(), "destroy-ran")
	writeApprovalHooks(t, repo, marker, []string{"setup.sh", "package.json"})
	return repo, home, marker
}

// Fixtures already have their local base refs; these tests exercise hook
// approval, not fetch behavior (covered by the baseline get tests).
func getApprovalLease(t *testing.T, repo, home string, flags ...string) string {
	t.Helper()
	out, _ := requireTreehouseOK(t, repo, home, append([]string{"get", "--lease", "--no-fetch"}, flags...)...)
	return strings.TrimSpace(out)
}

func assertNoApprovalEvents(t *testing.T, worktree string) {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(worktree, "hook-events.txt")); !os.IsNotExist(err) {
		t.Fatalf("unexpected repository hook events %q: %v", data, err)
	}
}

// Every way of reducing the declared list (removing an entry, emptying it,
// deleting the key) is covered by internal/config's
// TestApprovalFileListCannotBeDowngradedWithoutTrust. This is the command-level
// slice: one representative downgrade must disable every lifecycle entry point
// until an explicit trust re-approves it.
func TestApprovalFileDowngradesDisableEveryLifecycleUntilTrust(t *testing.T) {
	t.Parallel()
	repo, home, marker := setupApprovalFilesRepo(t)
	requireTreehouseOK(t, repo, home, "trust")
	wt := getApprovalLease(t, repo, home)
	assertHookEvents(t, wt, "create", "get")
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertHookEvents(t, wt, "create", "get", "return")

	writeApprovalHooks(t, repo, marker, []string{"setup.sh"})
	reused := getApprovalLease(t, repo, home)
	if reused != wt {
		t.Fatal("expected slot reuse")
	}
	assertHookEvents(t, wt, "create", "get", "return")
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertHookEvents(t, wt, "create", "get", "return")
	requireTreehouseOK(t, repo, home, "destroy", wt, "--yes")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("downgraded approval ran deletion hook")
	}

	requireTreehouseOK(t, repo, home, "trust")
	wt = getApprovalLease(t, repo, home)
	assertHookEvents(t, wt, "create", "get")
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertHookEvents(t, wt, "create", "get", "return")
	requireTreehouseOK(t, repo, home, "destroy", wt, "--yes")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("reapproved deletion hook did not run: %v", err)
	}
}

// Changed and missing dependencies in the execution directory are covered by
// internal/config's TestRunWorktreeHooksChecksExecutionWorktreeContents, and
// the deletion entry points by TestTrustedPreDestroyTargetsOwningClone. What
// only the command level can show is that the check happens after the return
// reset has replaced the worktree's contents.
func TestApprovalFilesCheckedInExecutionWorktree(t *testing.T) {
	t.Parallel()
	repo, home, marker := setupApprovalFilesRepo(t)
	gitCmd(t, repo, "checkout", "-b", "alternate")
	if err := os.WriteFile(filepath.Join(repo, "setup.sh"), []byte("different\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "setup.sh")
	gitCmd(t, repo, "commit", "-m", "Change hook dependency on alternate base")
	gitCmd(t, repo, "checkout", "main")
	requireTreehouseOK(t, repo, home, "trust")
	wt := getApprovalLease(t, repo, home, "--base", "alternate")
	assertNoApprovalEvents(t, wt)
	// Before return the file matches approval, but the reset restores the
	// alternate base. Verification must occur AFTER that reset.
	if err := os.WriteFile(filepath.Join(wt, "setup.sh"), []byte("approved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertNoApprovalEvents(t, wt)
	requireTreehouseOK(t, repo, home, "destroy", wt, "--yes")
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("expected deletion to complete")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("mismatched execution files ran deletion hook")
	}
}

func TestApprovalFilesRequireMainAndWorktreeContents(t *testing.T) {
	t.Parallel()
	repo, home, _ := setupApprovalFilesRepo(t)
	requireTreehouseOK(t, repo, home, "trust")
	// Main changes while a new worktree still checks out the approved commit.
	if err := os.WriteFile(filepath.Join(repo, "setup.sh"), []byte("new contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := getApprovalLease(t, repo, home)
	assertNoApprovalEvents(t, wt)
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertNoApprovalEvents(t, wt)
	// Reapproving main is insufficient while the execution worktree differs.
	requireTreehouseOK(t, repo, home, "trust")
	wt = getApprovalLease(t, repo, home)
	assertNoApprovalEvents(t, wt)
	gitCmd(t, repo, "add", "setup.sh")
	gitCmd(t, repo, "commit", "-m", "Land approved dependency contents")
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertHookEvents(t, wt, "return")
	wt = getApprovalLease(t, repo, home)
	assertHookEvents(t, wt, "return", "create", "get")
}
