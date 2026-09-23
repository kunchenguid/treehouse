package gitvcs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCreateBranchChecksHeadEvenAfterSuccessfulCheckout(t *testing.T) {
	var calls [][]string
	run := func(dir string, args ...string) (string, error) {
		calls = append(calls, append([]string{dir}, args...))
		switch args[0] {
		case "rev-parse":
			return "initial-commit", nil
		case "symbolic-ref":
			return "feature", nil
		}
		return "", nil
	}
	if err := createBranch("worktree", "feature", run); err != nil {
		t.Fatalf("createBranch failed: %v", err)
	}
	want := [][]string{
		{"worktree", "rev-parse", "--verify", "HEAD^{commit}"},
		{"worktree", "branch", "--", "feature", "initial-commit"},
		{"worktree", "checkout", "feature"},
		{"worktree", "symbolic-ref", "-q", "--short", "HEAD"},
		{"worktree", "rev-parse", "--verify", "HEAD^{commit}"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestCreateBranchAcceptsCompletedCheckoutAfterHookFailure(t *testing.T) {
	hookErr := errors.New("post-checkout failed")
	var calls int
	err := createBranch("worktree", "feature", func(_ string, args ...string) (string, error) {
		calls++
		switch args[0] {
		case "rev-parse":
			return "initial-commit", nil
		case "branch":
			return "", nil
		case "checkout":
			return "", hookErr
		case "symbolic-ref":
			return "feature", nil
		default:
			t.Fatalf("unexpected git command: %v", args)
			return "", nil
		}
	})
	if err != nil {
		t.Fatalf("createBranch rejected the completed checkout: %v", err)
	}
	if calls != 5 {
		t.Fatalf("git called %d times, want initial HEAD, branch reservation, checkout, and both postcondition checks", calls)
	}
}

func TestCreateBranchReturnsFailureWhenCheckoutDidNotSelectBranch(t *testing.T) {
	checkoutErr := errors.New("checkout failed")
	err := createBranch("worktree", "feature", func(_ string, args ...string) (string, error) {
		switch args[0] {
		case "symbolic-ref":
			return "", errors.New("detached")
		case "rev-parse":
			return "initial-commit", nil
		case "checkout":
			return "", checkoutErr
		case "branch":
			return "", nil
		default:
			t.Fatalf("unexpected git command: %v", args)
			return "", nil
		}
	})
	if !errors.Is(err, checkoutErr) {
		t.Fatalf("error = %v, want original checkout error", err)
	}
	if !strings.Contains(err.Error(), "branch \"feature\" was created and left in place for manual inspection/removal") {
		t.Fatalf("error does not explain retained ref: %v", err)
	}
}

func TestCreateBranchDoesNotOwnPreexistingBranch(t *testing.T) {
	branchErr := errors.New("branch already exists")
	var calls int
	err := createBranch("worktree", "feature", func(_ string, args ...string) (string, error) {
		calls++
		switch args[0] {
		case "rev-parse":
			return "initial-commit", nil
		case "branch":
			return "", branchErr
		default:
			t.Fatalf("unexpected git command: %v", args)
			return "", nil
		}
	})
	if !errors.Is(err, branchErr) {
		t.Fatalf("error = %v, want existing-branch error", err)
	}
	if calls != 2 {
		t.Fatalf("git called %d times, want no checkout after rejected reservation", calls)
	}
	if strings.Contains(err.Error(), "manual inspection/removal") {
		t.Fatal("preexisting branch marked as newly created")
	}
}

func TestCreateBranchRejectsOptionLikeNameWithoutChangingBranch(t *testing.T) {
	dir := initRepo(t)
	before, err := runGit(dir, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateBranch(dir, "-D"); err == nil {
		t.Fatal("option-like branch name unexpectedly succeeded")
	}
	if got, err := runGit(dir, "rev-parse", "refs/heads/main"); err != nil || got != before {
		t.Fatalf("main changed from %s to %s", before, got)
	}
	if got, err := CheckedOutBranch(dir); err != nil || got != "main" {
		t.Fatalf("HEAD changed to %q", got)
	}
}

func TestCreateBranchReconcilesRealPostCheckoutHookFailure(t *testing.T) {
	dir := initRepo(t)
	gitRun(t, dir, "checkout", "--detach")
	installFailingPostCheckoutHook(t, dir)

	if err := CreateBranch(dir, "feature"); err != nil {
		t.Fatalf("CreateBranch rejected a branch Git created before its hook failed: %v", err)
	}
	branch, err := CheckedOutBranch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "feature" {
		t.Fatalf("checked-out branch = %q, want feature", branch)
	}
}

func TestDetachWorktreeReconcilesRealPostCheckoutHookFailure(t *testing.T) {
	dir := initRepo(t)
	installFailingPostCheckoutHook(t, dir)

	if err := DetachWorktree(dir); err != nil {
		t.Fatalf("DetachWorktree rejected a detach Git completed before its hook failed: %v", err)
	}
	branch, err := CheckedOutBranch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		t.Fatalf("checked-out branch = %q, want detached HEAD", branch)
	}
}

func installFailingPostCheckoutHook(t *testing.T, repoDir string) {
	t.Helper()
	hook := filepath.Join(repoDir, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
