package gitvcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunGitContextPreservesNormalOutputAndExitDiagnostics(t *testing.T) {
	repoDir := t.TempDir()
	repoDir, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, "", "init", "--initial-branch=main", repoDir)

	out, err := runGitContext(context.Background(), repoDir, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("runGitContext failed: %v", err)
	}
	// git reports --show-toplevel with forward slashes even on Windows.
	if filepath.FromSlash(out) != repoDir {
		t.Fatalf("expected trimmed repository path %q, got %q", repoDir, out)
	}

	_, err = runGitContext(context.Background(), repoDir, "rev-parse", "--verify", "missing-ref")
	if err == nil {
		t.Fatal("expected missing ref to fail")
	}
	if !strings.Contains(err.Error(), "git rev-parse --verify missing-ref:") {
		t.Fatalf("expected ordinary git exit diagnostic, got %q", err)
	}
}

func TestRunGitContextReportsActionableTimeout(t *testing.T) {
	repoDir := t.TempDir()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := runGitContext(ctx, repoDir, "checkout", "--detach")
	if err == nil {
		t.Fatal("expected expired context to fail")
	}

	message := err.Error()
	for _, want := range []string{
		"git checkout --detach timed out",
		repoDir,
		"git rev-parse --git-path index.lock",
		"credential",
		"network",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("expected timeout diagnostic to contain %q, got %q", want, message)
		}
	}
}

func TestRunGitContextDoesNotReportCancellationAsTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runGitContext(ctx, t.TempDir(), "status")
	if err == nil {
		t.Fatal("expected canceled context to fail")
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected cancellation to remain distinct from timeout, got %q", err)
	}
}

func TestRunGitContextBoundsDescendantHeldOutputPipe(t *testing.T) {
	helperDir := t.TempDir()
	helperName := "git-pipeholder"
	if filepath.Ext(os.Args[0]) == ".exe" {
		helperName += ".exe"
	}
	helperPath := filepath.Join(helperDir, helperName)
	testBinary, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helperPath, testBinary, 0o755); err != nil {
		t.Fatal(err)
	}

	readyPath := filepath.Join(t.TempDir(), "ready")
	stopPath := filepath.Join(t.TempDir(), "stop")
	donePath := filepath.Join(t.TempDir(), "done")
	t.Setenv("TREEHOUSE_GIT_PIPE_HOLDER", "1")
	t.Setenv("TREEHOUSE_GIT_PIPE_READY", readyPath)
	t.Setenv("TREEHOUSE_GIT_PIPE_STOP", stopPath)
	t.Setenv("TREEHOUSE_GIT_PIPE_DONE", donePath)
	defer os.WriteFile(stopPath, nil, 0o644) //nolint:errcheck -- best-effort helper cleanup

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = runGitContext(ctx, "", "--exec-path="+helperDir, "pipeholder", "-test.run=^TestGitPipeHolderHelper$")
	elapsed := time.Since(started)
	if writeErr := os.WriteFile(stopPath, nil, 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if !waitForFile(donePath, 2*time.Second) {
		t.Error("git helper did not exit after its output pipe was released")
	}
	if err == nil {
		t.Fatal("expected pipe-holding git command to time out")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout diagnostic, got %q", err)
	}
	if _, statErr := os.Stat(readyPath); statErr != nil {
		t.Fatalf("expected git helper and its descendant to start: %v", statErr)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("runGitContext remained blocked by an inherited output pipe for %v", elapsed)
	}
}

func TestGitPipeHolderHelper(t *testing.T) {
	if os.Getenv("TREEHOUSE_GIT_PIPE_HOLDER") != "1" {
		return
	}

	stopPath := os.Getenv("TREEHOUSE_GIT_PIPE_STOP")
	if os.Getenv("TREEHOUSE_GIT_PIPE_DESCENDANT") == "1" {
		waitForFile(stopPath, 15*time.Second)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestGitPipeHolderHelper$")
	cmd.Env = append(os.Environ(), "TREEHOUSE_GIT_PIPE_DESCENDANT=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("TREEHOUSE_GIT_PIPE_READY"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitForFile(stopPath, 15*time.Second)
	_ = cmd.Wait() // The command's inherited output pipe is intentionally closed on timeout.
	if err := os.WriteFile(os.Getenv("TREEHOUSE_GIT_PIPE_DONE"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitForFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestIsHeadMergedIntoRefPreservesUnmergedExitCode(t *testing.T) {
	repoDir := t.TempDir()
	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", "README.md")
	mustGit(t, repoDir, "commit", "-m", "main")
	mustGit(t, repoDir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", "feature.txt")
	mustGit(t, repoDir, "commit", "-m", "feature")

	merged, err := IsHeadMergedIntoRef(repoDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("IsHeadMergedIntoRef failed: %v", err)
	}
	if merged {
		t.Fatal("expected feature HEAD not to be merged into main")
	}
}

func TestIsHeadMergedIntoRefContextReportsTimeout(t *testing.T) {
	repoDir := t.TempDir()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := isHeadMergedIntoRefContext(ctx, repoDir, "refs/heads/main")
	if err == nil {
		t.Fatal("expected expired context to fail")
	}
	if !strings.Contains(err.Error(), "git merge-base --is-ancestor HEAD refs/heads/main timed out") {
		t.Fatalf("expected merge-base timeout diagnostic, got %q", err)
	}
}

func TestIsHeadContentMergedIntoRefContextReportsTimeout(t *testing.T) {
	repoDir := t.TempDir()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := isHeadContentMergedIntoRefContext(ctx, repoDir, "refs/heads/main")
	if err == nil {
		t.Fatal("expected expired context to fail")
	}
	if !strings.Contains(err.Error(), "git merge-base HEAD refs/heads/main timed out") {
		t.Fatalf("expected fallback merge-base timeout diagnostic, got %q", err)
	}
}

func TestRepoRootFromCommonGitDirHandlesForwardSlashPath(t *testing.T) {
	root, ok := repoRootFromCommonGitDir("C:/Users/runner/AppData/Local/Temp/repo/.git")
	if !ok {
		t.Fatal("expected .git common dir to resolve to a repo root")
	}

	want := filepath.Clean(filepath.FromSlash("C:/Users/runner/AppData/Local/Temp/repo"))
	if root != want {
		t.Fatalf("expected repo root %q, got %q", want, root)
	}
}

func TestGetDefaultBranchFromDetachedLinkedWorktreeUsesMainRepoHead(t *testing.T) {
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	mustGit(t, repoDir, "config", "init.defaultBranch", "wrong")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	branch, err := GetDefaultBranch(wtPath)
	if err != nil {
		t.Fatalf("GetDefaultBranch failed: %v", err)
	}
	if branch != "main" {
		t.Fatalf("expected default branch main from main repo HEAD, got %q", branch)
	}
}

func TestFindMainRepoRootFromLinkedWorktree(t *testing.T) {
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	mainRoot, err := FindMainRepoRootFrom(wtPath)
	if err != nil {
		t.Fatalf("FindMainRepoRootFrom failed: %v", err)
	}
	if mainRoot != repoDir {
		t.Fatalf("expected main repo root %s, got %s", repoDir, mainRoot)
	}
}

func TestRemoveCleanWorktreeRejectsDirtyWorktree(t *testing.T) {
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	dirtyPath := filepath.Join(wtPath, "uncommitted.txt")
	if err := os.WriteFile(dirtyPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveCleanWorktree(repoDir, wtPath); err == nil {
		t.Fatal("expected clean worktree removal to reject dirty worktree")
	}
	if _, err := os.Stat(dirtyPath); err != nil {
		t.Fatalf("expected dirty worktree to remain: %v", err)
	}
}

func TestIsHeadMergedIntoRef(t *testing.T) {
	tests := []struct {
		name                   string
		ordinaryMerge          bool
		squashMerge            bool
		laterUnrelated         bool
		targetFeatureContent   string
		emptyFeatureCommit     bool
		revertedFeatureContent bool
		wantMerged             bool
	}{
		{name: "ordinary ancestry merge", ordinaryMerge: true, wantMerged: true},
		{name: "squash merge", squashMerge: true, wantMerged: true},
		{name: "squash merge followed by unrelated target commit", squashMerge: true, laterUnrelated: true, wantMerged: true},
		{name: "squash merge missing final feature content", squashMerge: true, targetFeatureContent: "one\n", wantMerged: false},
		{name: "unique unmerged content", wantMerged: false},
		{name: "empty feature commit", emptyFeatureCommit: true, wantMerged: false},
		{name: "feature content fully reverted", revertedFeatureContent: true, wantMerged: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir := t.TempDir()
			mustGit(t, "", "init", "--initial-branch=main", repoDir)
			mustGit(t, repoDir, "config", "user.email", "test@test.com")
			mustGit(t, repoDir, "config", "user.name", "Test")

			if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			mustGit(t, repoDir, "add", ".")
			mustGit(t, repoDir, "commit", "-m", "initial")
			mustGit(t, repoDir, "checkout", "-b", "feature")

			switch {
			case tt.emptyFeatureCommit:
				mustGit(t, repoDir, "commit", "--allow-empty", "-m", "empty feature commit")
			case tt.revertedFeatureContent:
				if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("feature\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				mustGit(t, repoDir, "commit", "-am", "feature change")
				if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				mustGit(t, repoDir, "commit", "-am", "revert feature change")
			default:
				if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("one\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				mustGit(t, repoDir, "add", "feature.txt")
				mustGit(t, repoDir, "commit", "-m", "feature one")
				if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				mustGit(t, repoDir, "commit", "-am", "feature two")
			}

			mustGit(t, repoDir, "checkout", "main")
			switch {
			case tt.ordinaryMerge:
				mustGit(t, repoDir, "merge", "--no-ff", "feature", "-m", "merge feature")
			case tt.squashMerge:
				mustGit(t, repoDir, "merge", "--squash", "feature")
				if tt.targetFeatureContent != "" {
					if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte(tt.targetFeatureContent), 0o644); err != nil {
						t.Fatal(err)
					}
					mustGit(t, repoDir, "add", "feature.txt")
				}
				mustGit(t, repoDir, "commit", "-m", "squash feature")
			}
			if tt.laterUnrelated {
				if err := os.WriteFile(filepath.Join(repoDir, "unrelated.txt"), []byte("later\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				mustGit(t, repoDir, "add", "unrelated.txt")
				mustGit(t, repoDir, "commit", "-m", "unrelated target change")
			}
			mustGit(t, repoDir, "checkout", "feature")

			merged, err := IsHeadMergedIntoRef(repoDir, "refs/heads/main")
			if err != nil {
				t.Fatalf("IsHeadMergedIntoRef failed: %v", err)
			}
			if merged != tt.wantMerged {
				t.Fatalf("expected merged=%t, got %t", tt.wantMerged, merged)
			}
		})
	}
}

func TestIsHeadMergedIntoRefFailsClosedWhenTargetCannotBeVerified(t *testing.T) {
	repoDir := t.TempDir()
	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")

	if _, err := IsHeadMergedIntoRef(repoDir, "refs/heads/missing"); err == nil {
		t.Fatal("expected merge verification error for missing target ref")
	}
}

func TestIsWorktreeSafeToReset(t *testing.T) {
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	// At base: resetting discards nothing, so it is safe.
	safe, _, _, err := IsWorktreeSafeToReset(wtPath, "main")
	if err != nil {
		t.Fatalf("IsWorktreeSafeToReset at base: %v", err)
	}
	if !safe {
		t.Fatal("expected a worktree at base to be safe to reset")
	}

	// Ahead of base: resetting would discard the commit, so it is not safe.
	if err := os.WriteFile(filepath.Join(wtPath, "unlanded.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wtPath, "add", "unlanded.txt")
	mustGit(t, wtPath, "commit", "-m", "unlanded work")

	safe, _, _, err = IsWorktreeSafeToReset(wtPath, "main")
	if err != nil {
		t.Fatalf("IsWorktreeSafeToReset ahead of base: %v", err)
	}
	if safe {
		t.Fatal("expected a worktree ahead of base to be refused")
	}
}

func TestResetWorktreeUsesCommitVerifiedBySafetyCheck(t *testing.T) {
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	safe, resetRef, head, err := IsWorktreeSafeToReset(wtPath, "main")
	if err != nil {
		t.Fatalf("IsWorktreeSafeToReset: %v", err)
	}
	if !safe {
		t.Fatal("expected worktree at base to be safe to reset")
	}

	if err := os.WriteFile(filepath.Join(repoDir, "advanced.txt"), []byte("new base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", "advanced.txt")
	mustGit(t, repoDir, "commit", "-m", "advance main")

	if err := ResetWorktreeToRef(wtPath, resetRef, head, true); err != nil {
		t.Fatalf("ResetWorktreeToRef: %v", err)
	}
	got, err := runGit(wtPath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve reset HEAD: %v", err)
	}
	if got != resetRef {
		t.Fatalf("reset targeted %s, want verified commit %s", got, resetRef)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Fatalf("expected committed work to remain: %v", err)
	}
}

func TestResetWorktreeToRefRefusesWhenHeadChanged(t *testing.T) {
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	safe, resetRef, head, err := IsWorktreeSafeToReset(wtPath, "main")
	if err != nil {
		t.Fatalf("IsWorktreeSafeToReset: %v", err)
	}
	if !safe {
		t.Fatal("expected worktree at base to be safe to reset")
	}

	// Concurrent committed work after the safety check: HEAD is no longer the
	// one whose ancestry was verified, so the reset must refuse.
	if err := os.WriteFile(filepath.Join(wtPath, "unlanded.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wtPath, "add", "unlanded.txt")
	mustGit(t, wtPath, "commit", "-m", "unlanded after check")
	changed, err := runGit(wtPath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve changed HEAD: %v", err)
	}
	if changed == head {
		t.Fatal("expected HEAD to change after the concurrent commit")
	}

	if err := ResetWorktreeToRef(wtPath, resetRef, head, true); err == nil {
		t.Fatal("expected ResetWorktreeToRef to refuse after HEAD changed")
	}
	got, err := runGit(wtPath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve preserved HEAD: %v", err)
	}
	if got != changed {
		t.Fatalf("expected unlanded HEAD %s preserved, got %s", changed, got)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "unlanded.txt")); err != nil {
		t.Fatalf("expected concurrent commit preserved on disk: %v", err)
	}
}

func TestResetWorktreeToRefRefusesWhenHeadLockHeld(t *testing.T) {
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wtPath := filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	safe, resetRef, head, err := IsWorktreeSafeToReset(wtPath, "main")
	if err != nil {
		t.Fatalf("IsWorktreeSafeToReset: %v", err)
	}
	if !safe {
		t.Fatal("expected worktree at base to be safe to reset")
	}

	headPath, err := gitPath(wtPath, "HEAD")
	if err != nil {
		t.Fatalf("resolve HEAD path: %v", err)
	}
	lockPath := headPath + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		t.Fatalf("create HEAD.lock: %v", err)
	}
	defer func() {
		_ = lf.Close()
		_ = os.Remove(lockPath)
	}()

	if err := ResetWorktreeToRef(wtPath, resetRef, head, true); err == nil {
		t.Fatal("expected ResetWorktreeToRef to refuse when git HEAD.lock is held")
	}
	got, err := runGit(wtPath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve preserved HEAD: %v", err)
	}
	if got != head {
		t.Fatalf("expected HEAD %s preserved under HEAD.lock, got %s", head, got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "commit", "--allow-empty", "-m", "concurrent")
	cmd.Dir = wtPath
	if err := cmd.Run(); err == nil {
		t.Fatal("expected git commit to honor an existing HEAD.lock")
	}
	if got, err := runGit(wtPath, "rev-parse", "HEAD"); err != nil {
		t.Fatalf("resolve HEAD after concurrent commit: %v", err)
	} else if got != head {
		t.Fatalf("expected git commit not to move HEAD while locked, got %s", got)
	}
}

func TestResetWorktreeToRefRefusesWhenDirtyAfterSafetyCheck(t *testing.T) {
	cases := []struct {
		name  string
		dirty func(t *testing.T, wtPath string) (keepPath, keepContents string)
	}{
		{
			name: "untracked file",
			dirty: func(t *testing.T, wtPath string) (string, string) {
				path := filepath.Join(wtPath, "scratch.txt")
				if err := os.WriteFile(path, []byte("keep\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return path, "keep\n"
			},
		},
		{
			name: "tracked modification",
			dirty: func(t *testing.T, wtPath string) (string, string) {
				path := filepath.Join(wtPath, "README.md")
				if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return path, "changed\n"
			},
		},
		{
			name: "index update",
			dirty: func(t *testing.T, wtPath string) (string, string) {
				path := filepath.Join(wtPath, "staged.txt")
				if err := os.WriteFile(path, []byte("keep\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				mustGit(t, wtPath, "add", "staged.txt")
				return path, "keep\n"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wtPath, resetRef, head := setupSafeResetWorktree(t)
			keepPath, keepContents := tc.dirty(t, wtPath)

			err := ResetWorktreeToRef(wtPath, resetRef, head, true)
			if err == nil {
				t.Fatal("expected ResetWorktreeToRef to refuse after the tree became dirty")
			}
			if !strings.Contains(err.Error(), "became dirty after safety check") {
				t.Fatalf("expected dirty-after-check error, got %v", err)
			}

			got, err := runGit(wtPath, "rev-parse", "HEAD")
			if err != nil {
				t.Fatalf("resolve preserved HEAD: %v", err)
			}
			if got != head {
				t.Fatalf("expected HEAD %s preserved, got %s", head, got)
			}
			contents, err := os.ReadFile(keepPath)
			if err != nil {
				t.Fatalf("expected concurrent uncommitted work preserved on disk: %v", err)
			}
			if string(contents) != keepContents {
				t.Fatalf("expected %q preserved, got %q", keepContents, contents)
			}
		})
	}
}

func TestResetWorktreeToRefDiscardsDirtyWithoutRequireClean(t *testing.T) {
	wtPath, resetRef, head := setupSafeResetWorktree(t)
	scratch := filepath.Join(wtPath, "scratch.txt")
	if err := os.WriteFile(scratch, []byte("discard\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ResetWorktreeToRef(wtPath, resetRef, head, false); err != nil {
		t.Fatalf("ResetWorktreeToRef without requireClean: %v", err)
	}
	got, err := runGit(wtPath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve reset HEAD: %v", err)
	}
	if got != resetRef {
		t.Fatalf("reset targeted %s, want %s", got, resetRef)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatal("expected return-path reset to discard uncommitted work")
	}
}

func setupSafeResetWorktree(t *testing.T) (wtPath, resetRef, head string) {
	t.Helper()
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wtPath = filepath.Join(base, "worktree")

	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")

	safe, resetRef, head, err := IsWorktreeSafeToReset(wtPath, "main")
	if err != nil {
		t.Fatalf("IsWorktreeSafeToReset: %v", err)
	}
	if !safe {
		t.Fatal("expected worktree at base to be safe to reset")
	}
	return wtPath, resetRef, head
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestGitCommandTimeoutForSeparatesLongRunningCommands(t *testing.T) {
	cases := []struct {
		args []string
		want time.Duration
	}{
		{[]string{"rev-parse", "--show-toplevel"}, defaultGitCommandTimeout},
		{[]string{"status", "--porcelain"}, defaultGitCommandTimeout},
		{[]string{"worktree", "prune"}, defaultGitCommandTimeout},
		{[]string{"fetch", "origin"}, defaultGitLongCommandTimeout},
		{[]string{"ls-remote", "--symref", "origin", "HEAD"}, defaultGitLongCommandTimeout},
		{[]string{"worktree", "add", "--detach", "path", "ref"}, defaultGitLongCommandTimeout},
		{[]string{"worktree", "remove", "--force", "path"}, defaultGitLongCommandTimeout},
		{[]string{"read-tree", "--reset", "-u", "ref"}, defaultGitLongCommandTimeout},
		{[]string{"clean", "-fd"}, defaultGitLongCommandTimeout},
		{[]string{"checkout", "--detach"}, defaultGitLongCommandTimeout},
		{[]string{"-c", "fetch.parallel=1", "fetch", "origin"}, defaultGitLongCommandTimeout},
		{[]string{"-c", "core.pager=cat", "rev-parse", "HEAD"}, defaultGitCommandTimeout},
	}
	for _, tc := range cases {
		if got := gitCommandTimeoutFor(tc.args...); got != tc.want {
			t.Errorf("gitCommandTimeoutFor(%q) = %s, want %s", tc.args, got, tc.want)
		}
	}
}

func TestGitCommandTimeoutForHonorsEnvironmentOverrides(t *testing.T) {
	t.Setenv(gitTimeoutEnv, "45s")
	t.Setenv(gitLongTimeoutEnv, "3h")

	if got := gitCommandTimeoutFor("status"); got != 45*time.Second {
		t.Errorf("expected overridden standard budget, got %s", got)
	}
	if got := gitCommandTimeoutFor("fetch", "origin"); got != 3*time.Hour {
		t.Errorf("expected overridden long budget, got %s", got)
	}

	t.Setenv(gitTimeoutEnv, "not-a-duration")
	t.Setenv(gitLongTimeoutEnv, "0")
	if got := gitCommandTimeoutFor("status"); got != defaultGitCommandTimeout {
		t.Errorf("expected unparseable override to fall back, got %s", got)
	}
	if got := gitCommandTimeoutFor("fetch", "origin"); got != defaultGitLongCommandTimeout {
		t.Errorf("expected non-positive override to fall back, got %s", got)
	}
}

func TestGitTimeoutErrorNamesTheOverridableBudget(t *testing.T) {
	err := gitTimeoutError("/tmp/repo", []string{"fetch", "origin"})
	if !strings.Contains(err.Error(), gitLongTimeoutEnv) {
		t.Errorf("expected long-budget override hint, got %q", err)
	}
	err = gitTimeoutError("/tmp/repo", []string{"status"})
	if !strings.Contains(err.Error(), gitTimeoutEnv) {
		t.Errorf("expected standard-budget override hint, got %q", err)
	}
}

func TestRunGitRawContextIdentifiesNonExitFailures(t *testing.T) {
	repoDir := t.TempDir()
	missingGitDir := filepath.Join(repoDir, "gone")

	_, err := runGitRawContext(context.Background(), missingGitDir, "rev-parse", "--show-toplevel")
	if err == nil {
		t.Fatal("expected a missing working directory to fail")
	}
	if !strings.Contains(err.Error(), "git rev-parse --show-toplevel in") {
		t.Fatalf("expected the failing subcommand in the error, got %q", err)
	}
	if !strings.Contains(err.Error(), missingGitDir) {
		t.Fatalf("expected the working directory in the error, got %q", err)
	}
}

// TestPruneWorktreeAtClearsInterruptedCreationLock covers the recovery path
// for an interrupted "git worktree add" and its two guards: a lock a user
// took and a worktree still on disk are both left registered.
func TestPruneWorktreeAtClearsInterruptedCreationLock(t *testing.T) {
	tests := []struct {
		name        string
		lockReason  string
		removeDir   bool
		wantCleared bool
	}{
		{name: "interrupted creation", lockReason: worktreeInitializingLock, removeDir: true, wantCleared: true},
		{name: "unlocked stale registration", removeDir: true, wantCleared: true},
		{name: "user lock", lockReason: "on removable media", removeDir: true},
		{name: "creation still in flight", lockReason: worktreeInitializingLock},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			base, err := filepath.EvalSymlinks(base)
			if err != nil {
				t.Fatal(err)
			}
			repoDir := filepath.Join(base, "repo")
			wtPath := filepath.Join(base, "slot", "worktree")

			mustGit(t, "", "init", "--initial-branch=main", repoDir)
			mustGit(t, repoDir, "config", "user.email", "test@test.com")
			mustGit(t, repoDir, "config", "user.name", "Test")
			if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			mustGit(t, repoDir, "add", ".")
			mustGit(t, repoDir, "commit", "-m", "initial")
			mustGit(t, repoDir, "worktree", "add", "--detach", wtPath, "main")
			if tt.lockReason != "" {
				mustGit(t, repoDir, "worktree", "lock", "--reason", tt.lockReason, wtPath)
			}
			if tt.removeDir {
				if err := os.RemoveAll(filepath.Dir(wtPath)); err != nil {
					t.Fatal(err)
				}
			}

			if err := PruneWorktreeAt(repoDir, wtPath); err != nil {
				t.Fatalf("PruneWorktreeAt failed: %v", err)
			}

			out, err := exec.Command("git", "-C", repoDir, "worktree", "list", "--porcelain").CombinedOutput()
			if err != nil {
				t.Fatalf("git worktree list failed: %v\n%s", err, out)
			}
			if got := strings.Contains(string(out), wtPath); got == tt.wantCleared {
				t.Fatalf("registration cleared=%v, want cleared=%v; list:\n%s", !got, tt.wantCleared, out)
			}
		})
	}
}
