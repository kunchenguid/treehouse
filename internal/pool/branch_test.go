package pool

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireBranchCreatesAtAcquiredCommitBeforeHook(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	developTip := addBranch(t, repoDir, "develop", "develop-only.txt")
	observed := filepath.Join(t.TempDir(), "hook-ran")
	hook := "git branch --show-current > " + quoteForShell(observed)

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, []string{hook}, AcquireOptions{BaseBranch: "develop", Branch: "feature"})
	if err != nil {
		t.Fatalf("AcquireWithOptions failed: %v", err)
	}
	if got := gitOut(t, wtPath, "branch", "--show-current"); got != "feature" {
		t.Fatalf("checked-out branch = %q, want feature", got)
	}
	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != developTip {
		t.Fatalf("feature starts at %s, want %s", got, developTip)
	}
	contents, err := os.ReadFile(observed)
	if err != nil {
		t.Fatalf("post-create hook did not run: %v", err)
	}
	if string(contents) != "feature\n" {
		t.Fatalf("post-create hook observed %q, want feature", contents)
	}
}

func TestAcquireBranchFailureOnRecycledSlotLeavesItDetachedAndReusable(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearOwnerReservation(t, poolDir, wtPath)
	// A collision before checkout must not invoke a post-checkout hook.

	if _, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "main"}); err == nil {
		t.Fatal("expected creating an existing branch to fail")
	}
	if got := gitOut(t, wtPath, "branch", "--show-current"); got != "" {
		t.Fatalf("failed acquisition left recycled slot on %q, want detached", got)
	}

	reused, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("failed branch creation stranded capacity: %v", err)
	}
	if reused != wtPath {
		t.Fatalf("got new slot %s, want recycled %s", reused, wtPath)
	}
}

func TestAcquireBranchPostCheckoutHookFailureKeepsCompletedAcquisition(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearOwnerReservation(t, poolDir, wtPath)
	installFailingPostCheckoutHook(t, repoDir)

	acquired, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "feature"})
	if err != nil {
		t.Fatalf("AcquireWithOptions rejected a completed branch checkout: %v", err)
	}
	if acquired != wtPath {
		t.Fatalf("acquired %s, want recycled slot %s", acquired, wtPath)
	}
	if got := gitOut(t, wtPath, "branch", "--show-current"); got != "feature" {
		t.Fatalf("checked-out branch = %q, want feature", got)
	}
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0].Leased {
		t.Fatalf("completed acquisition was quarantined: %#v", state.Worktrees)
	}
}

func TestAcquireBranchCollisionDoesNotRunRedundantDetachHook(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearOwnerReservation(t, poolDir, wtPath)
	hook := filepath.Join(repoDir, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'unexpected hook output\\n' > hook-output.tmp\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".git", "info", "exclude"), []byte("hook-output.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "main"}); err == nil {
		t.Fatal("expected branch collision")
	}
	if _, err := os.Stat(filepath.Join(wtPath, "hook-output.tmp")); !os.IsNotExist(err) {
		t.Fatalf("collision invoked a redundant detach hook: %v", err)
	}
	reused, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil || reused != wtPath {
		t.Fatalf("collision stranded recycled slot: path %q, error %v", reused, err)
	}
}

func TestAcquireBranchRejectedReferenceTransactionPreservesIgnoredOutput(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearOwnerReservation(t, poolDir, wtPath)
	hook := filepath.Join(repoDir, ".git", "hooks", "reference-transaction")
	script := "#!/bin/sh\n" +
		"[ \"$1\" = prepared ] || exit 0\n" +
		"input=$(cat)\n" +
		"case \"$input\" in *' refs/heads/feature'*) ;; *) exit 0 ;; esac\n" +
		"printf 'hook work\\n' > ignored.tmp\n" +
		"exit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".git", "info", "exclude"), []byte("ignored.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "feature"})
	if err == nil || !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("rejected branch creation = %v, want quarantine", err)
	}
	if got := gitOut(t, wtPath, "branch", "--show-current"); got != "" {
		t.Fatalf("failed creation left HEAD on %q, want detached", got)
	}
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0].Path != wtPath || !state.Worktrees[0].Leased || state.Worktrees[0].LeaseHolder != "quarantined: branch creation cleanup failed" {
		t.Fatalf("hook output slot was not quarantined: %#v", state.Worktrees)
	}
	content, err := os.ReadFile(filepath.Join(wtPath, "ignored.tmp"))
	if err != nil || string(content) != "hook work\n" {
		t.Fatalf("ignored hook output = %q, error %v", content, err)
	}
	if _, err := Acquire(repoDir, poolDir, 1, nil); err == nil {
		t.Fatal("quarantined slot was reused")
	}
}

func TestAcquireBranchCleanupFailureQuarantinesRecycledSlot(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearOwnerReservation(t, poolDir, wtPath)

	oldCreateBranch := createBranch
	createBranch = func(path, branch string) error {
		// An unexpected symbolic HEAD must never be released as reusable.
		cmd := exec.Command("git", "symbolic-ref", "HEAD", "refs/heads/main")
		cmd.Dir = path
		if err := cmd.Run(); err != nil {
			return err
		}
		return errors.New("branch failed")
	}
	t.Cleanup(func() { createBranch = oldCreateBranch })

	_, err = AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "feature"})
	if err == nil || !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("AcquireWithOptions error = %v, want quarantine", err)
	}
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Worktrees) != 1 || !state.Worktrees[0].Leased || state.Worktrees[0].LeaseHolder != "quarantined: branch creation cleanup failed" {
		t.Fatalf("failed cleanup was not quarantined: %#v", state.Worktrees)
	}
}

func TestAcquireInvalidBranchOnNewSlotRemovesWorktreeAndState(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	if _, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "invalid branch"}); err == nil {
		t.Fatal("expected invalid branch to fail")
	}
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Worktrees) != 0 {
		t.Fatalf("failed branch creation registered %d worktrees", len(state.Worktrees))
	}
	if _, err := os.Stat(filepath.Join(poolDir, "1", filepath.Base(repoDir))); !os.IsNotExist(err) {
		t.Fatalf("failed branch creation left a worktree directory: %v", err)
	}
	if _, err := Acquire(repoDir, poolDir, 1, nil); err != nil {
		t.Fatalf("invalid branch stranded capacity: %v", err)
	}
}

func TestAcquireBranchFailedCheckoutPreservesIgnoredHookOutput(t *testing.T) {
	for _, recycled := range []bool{false, true} {
		name := "new"
		if recycled {
			name = "recycled"
		}
		t.Run(name, func(t *testing.T) {
			repoDir, poolDir := setupRepo(t)
			var wtPath string
			if recycled {
				var err error
				wtPath, err = Acquire(repoDir, poolDir, 1, nil)
				if err != nil {
					t.Fatal(err)
				}
				clearOwnerReservation(t, poolDir, wtPath)
			}
			hook := filepath.Join(repoDir, ".git", "hooks", "post-checkout")
			script := "#!/bin/sh\n" +
				"[ \"$(git symbolic-ref -q --short HEAD)\" = feature ] || exit 0\n" +
				"printf 'hook work\\n' > hook-output.tmp\n" +
				"git symbolic-ref HEAD refs/heads/main\n" +
				"exit 1\n"
			if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repoDir, ".git", "info", "exclude"), []byte("hook-output.tmp\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "feature"})
			if err == nil || !strings.Contains(err.Error(), "quarantined") {
				t.Fatalf("failed checkout error = %v, want quarantine", err)
			}
			state, err := ReadState(poolDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Worktrees) != 1 || !state.Worktrees[0].Leased || state.Worktrees[0].LeaseHolder != "quarantined: branch checkout failed" {
				t.Fatalf("failed checkout did not quarantine slot: %#v", state.Worktrees)
			}
			if recycled && state.Worktrees[0].Path != wtPath {
				t.Fatalf("quarantined %s, want recycled slot %s", state.Worktrees[0].Path, wtPath)
			}
			content, err := os.ReadFile(filepath.Join(state.Worktrees[0].Path, "hook-output.tmp"))
			if err != nil || string(content) != "hook work\n" {
				t.Fatalf("hook output = %q, err %v", content, err)
			}
			if got := gitOut(t, state.Worktrees[0].Path, "check-ignore", "hook-output.tmp"); got != "hook-output.tmp" {
				t.Fatalf("hook output is not ignored: %q", got)
			}
			if got := gitOut(t, repoDir, "show-ref", "--verify", "refs/heads/feature"); got == "" {
				t.Fatal("created branch was lost")
			}
			if _, err := Acquire(repoDir, poolDir, 1, nil); err == nil {
				t.Fatal("quarantined slot was reused")
			}
		})
	}
}

func TestAcquireBranchCollisionAfterAddPreservesHookOutput(t *testing.T) {
	for _, tc := range []struct {
		name, output    string
		custom, ignored bool
	}{
		{name: "ignored", output: "hook-output.tmp", ignored: true},
		{name: "untracked", output: "hook-output.txt"},
		{name: "custom hooks path", output: "hook-output.tmp", ignored: true, custom: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoDir, poolDir := setupRepo(t)
			hookDir := filepath.Join(repoDir, ".git", "hooks")
			if tc.custom {
				hookDir = filepath.Join(t.TempDir(), "configured-hooks")
				if err := os.Mkdir(hookDir, 0o755); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("git", "config", "core.hooksPath", hookDir)
				cmd.Dir = repoDir
				if err := cmd.Run(); err != nil {
					t.Fatal(err)
				}
			}
			hook := filepath.Join(hookDir, "post-checkout")
			if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'worktree add output\\n' > "+tc.output+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.ignored {
				if err := os.WriteFile(filepath.Join(repoDir, ".git", "info", "exclude"), []byte(tc.output+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "main"})
			if err == nil || !strings.Contains(err.Error(), "quarantined") {
				t.Fatalf("branch collision error = %v, want quarantine", err)
			}
			state, err := ReadState(poolDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Worktrees) != 1 || !state.Worktrees[0].Leased {
				t.Fatalf("hook output slot was not quarantined: %#v", state.Worktrees)
			}
			content, err := os.ReadFile(filepath.Join(state.Worktrees[0].Path, tc.output))
			if err != nil || string(content) != "worktree add output\n" {
				t.Fatalf("hook output = %q, error %v", content, err)
			}
			if tc.ignored {
				if got := gitOut(t, state.Worktrees[0].Path, "check-ignore", tc.output); got != tc.output {
					t.Fatalf("hook output is not ignored: %q", got)
				}
			}
			if _, err := Acquire(repoDir, poolDir, 1, nil); err == nil {
				t.Fatal("quarantined slot was reused")
			}
		})
	}
}

func TestAcquireBranchCleanupFailureQuarantinesNewSlot(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	oldRemoveWorktree := removeWorktree
	removeWorktree = func(string, string) error { return errors.New("cleanup failed") }
	t.Cleanup(func() { removeWorktree = oldRemoveWorktree })

	_, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "invalid branch"})
	if err == nil || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("AcquireWithOptions error = %v, want cleanup failure", err)
	}
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Worktrees) != 1 || !state.Worktrees[0].Leased || state.Worktrees[0].LeaseHolder != "quarantined: branch creation cleanup failed" {
		t.Fatalf("failed cleanup was not quarantined: %#v", state.Worktrees)
	}
}

func TestAcquireBranchHookSwitchesHeadAndRetainsRef(t *testing.T) {
	for _, recycled := range []bool{false, true} {
		for _, exitCode := range []string{"0", "1"} {
			name := "new"
			if recycled {
				name = "recycled"
			}
			t.Run(name+"/exit-"+exitCode, func(t *testing.T) {
				repoDir, poolDir := setupRepo(t)
				if recycled {
					path, err := Acquire(repoDir, poolDir, 1, nil)
					if err != nil {
						t.Fatal(err)
					}
					clearOwnerReservation(t, poolDir, path)
				}
				hook := filepath.Join(repoDir, ".git", "hooks", "post-checkout")
				script := "#!/bin/sh\n[ \"$(git symbolic-ref -q --short HEAD)\" = feature ] || exit 0\ngit symbolic-ref HEAD refs/heads/main\nexit " + exitCode + "\n"
				if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				_, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "feature"})
				if err == nil || !strings.Contains(err.Error(), "branch \"feature\" was created and left in place for manual inspection/removal") {
					t.Fatalf("failed checkout error = %v", err)
				}
				if got := gitOut(t, repoDir, "rev-parse", "refs/heads/feature"); got != gitOut(t, repoDir, "rev-parse", "HEAD") {
					t.Fatalf("retained branch tip = %s", got)
				}
				if err := os.Remove(hook); err != nil {
					t.Fatal(err)
				}
				other := filepath.Join(t.TempDir(), "other")
				gitOut(t, repoDir, "worktree", "add", other, "feature")
				if got := gitOut(t, other, "branch", "--show-current"); got != "feature" {
					t.Fatalf("other worktree checked out %q", got)
				}
				state, err := ReadState(poolDir)
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Worktrees) != 1 || !state.Worktrees[0].Leased {
					t.Fatalf("failed checkout was not quarantined: %#v", state.Worktrees)
				}
				if _, err := Acquire(repoDir, poolDir, 1, nil); err == nil {
					t.Fatal("failed checkout slot was reused")
				}
			})
		}
	}
}

func TestAcquireBranchHookAdvancedRefIsPreserved(t *testing.T) {
	for _, recycled := range []bool{false, true} {
		name := "new"
		if recycled {
			name = "recycled"
		}
		t.Run(name, func(t *testing.T) {
			repoDir, poolDir := setupRepo(t)
			if recycled {
				path, err := Acquire(repoDir, poolDir, 1, nil)
				if err != nil {
					t.Fatal(err)
				}
				clearOwnerReservation(t, poolDir, path)
			}
			hook := filepath.Join(repoDir, ".git", "hooks", "post-checkout")
			tipFile := filepath.Join(t.TempDir(), "advanced-tip")
			script := "#!/bin/sh\n[ \"$(git symbolic-ref -q --short HEAD)\" = feature ] || exit 0\nnew=$(git commit-tree HEAD^{tree} -p HEAD -m hook-advanced) || exit 1\ngit update-ref refs/heads/feature \"$new\" || exit 1\nprintf '%s\\n' \"$new\" > " + quoteForShell(tipFile) + "\n"
			if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "feature"}); err == nil {
				t.Fatal("hook advanced branch tip but acquisition succeeded")
			} else if !strings.Contains(err.Error(), "manual inspection/removal") {
				t.Fatalf("failed checkout did not explain retained branch: %v", err)
			}
			recorded, err := os.ReadFile(tipFile)
			if err != nil {
				t.Fatalf("hook did not advance branch: %v", err)
			}
			if got := gitOut(t, repoDir, "rev-parse", "refs/heads/feature"); got != strings.TrimSpace(string(recorded)) {
				t.Fatalf("advanced branch tip = %s, want %s", got, strings.TrimSpace(string(recorded)))
			}
			if err := os.Remove(hook); err != nil {
				t.Fatal(err)
			}
			if _, err := Acquire(repoDir, poolDir, 1, nil); err == nil {
				t.Fatal("slot with an unmerged hook commit was reused")
			}
		})
	}
}

func TestAcquireBranchDoesNotDeletePreexistingSameCommit(t *testing.T) {
	for _, recycled := range []bool{false, true} {
		name := "new"
		if recycled {
			name = "recycled"
		}
		t.Run(name, func(t *testing.T) {
			repoDir, poolDir := setupRepo(t)
			if recycled {
				path, err := Acquire(repoDir, poolDir, 1, nil)
				if err != nil {
					t.Fatal(err)
				}
				clearOwnerReservation(t, poolDir, path)
			}
			gitOut(t, repoDir, "branch", "feature", "HEAD")
			want := gitOut(t, repoDir, "rev-parse", "refs/heads/feature")
			if _, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{Branch: "feature"}); err == nil {
				t.Fatal("preexisting feature branch was accepted")
			}
			if got := gitOut(t, repoDir, "rev-parse", "refs/heads/feature"); got != want {
				t.Fatalf("preexisting branch tip = %s, want %s", got, want)
			}
			if _, err := Acquire(repoDir, poolDir, 1, nil); err != nil {
				t.Fatalf("branch collision stranded capacity: %v", err)
			}
		})
	}
}

func installFailingPostCheckoutHook(t *testing.T, repoDir string) {
	t.Helper()
	hook := filepath.Join(repoDir, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
