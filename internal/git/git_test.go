package git

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
		filepath.Join(".git", "index.lock"),
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
