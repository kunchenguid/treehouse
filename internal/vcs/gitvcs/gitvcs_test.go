package gitvcs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

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

func TestCommonGitDirEnvFallsBackForOlderGitWithSourceEnvironment(t *testing.T) {
	repo := t.TempDir()
	mustGit(t, "", "init", repo)
	logPath, _ := installGitWrapper(t, true)
	metadata := filepath.Join(repo, ".git")
	t.Setenv("PATH", filepath.Dir(logPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
	env := append(os.Environ(), "GIT_DIR="+metadata)
	got, err := commonGitDirEnv(t.TempDir(), env)
	if err != nil {
		t.Fatalf("commonGitDirEnv failed through older-Git fallback: %v", err)
	}
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("common Git directory = %q, want source GIT_DIR %q", got, metadata)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(calls) != 2 || strings.Contains(calls[1], "--path-format=absolute") {
		t.Fatalf("expected one rejected path-format call followed by the source-environment fallback, got %q", data)
	}
}

func TestSeedLayersReusesKnownCommonDirectory(t *testing.T) {
	isolateIncludeConfig(t)
	repo, worktree := setupSeedWorktree(t, "selected.env\n")
	writeTestFile(t, repo, "selected.env", "selected\n")
	common, err := CommonGitDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	logPath, _ := installGitWrapper(t, true)
	t.Setenv("PATH", filepath.Dir(logPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := selectedSeedPathsEnvWithCommonDir(repo, worktree, nil, "", nil, common)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "selected.env\x00" {
		t.Fatalf("selected paths = %q, want selected.env", got)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "rev-parse") {
		t.Fatalf("known source common directory still triggered Git discovery: %s", data)
	}
	configCalls := 0
	for _, call := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(call, "config ") {
			configCalls++
		}
	}
	if configCalls != 1 {
		t.Fatalf("layer resolution used %d Git config lookups, want 1: %s", configCalls, data)
	}
}

func TestMainRepoRootUsesOneCommonDirectoryQuery(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "")
	logPath, _ := installGitWrapper(t, false)
	t.Setenv("PATH", filepath.Dir(logPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, olderGit := range []bool{false, true} {
		t.Run(fmt.Sprintf("olderGit=%t", olderGit), func(t *testing.T) {
			if olderGit {
				t.Setenv("TREEHOUSE_REJECT_PATH_FORMAT", "1")
			}
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			got := mainRepoRoot(worktree)
			gotInfo, err := os.Stat(got)
			if err != nil {
				t.Fatal(err)
			}
			wantInfo, err := os.Stat(repo)
			if err != nil || !os.SameFile(gotInfo, wantInfo) {
				t.Fatalf("main root = %q, want %q: %v", got, repo, err)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Split(strings.TrimSpace(string(data)), "\n")
			wantCalls := 1
			if olderGit {
				wantCalls = 2
			}
			if len(calls) != wantCalls || !strings.Contains(calls[0], "--path-format=absolute") {
				t.Fatalf("common-directory discovery = %q, want absolute query first and %d calls", data, wantCalls)
			}
			if olderGit && strings.Contains(calls[1], "--path-format=absolute") {
				t.Fatalf("older Git did not fall back to the relative query: %s", data)
			}
		})
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

func TestSeedWorktreeUsesGitExcludePatterns(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     []string
		notWant  []string
	}{
		{
			name:     "wildcard",
			manifest: "*.env\n",
			want:     []string{".env", "nested/app.env"},
			notWant:  []string{"app.txt"},
		},
		{
			name:     "negation",
			manifest: "*.env\n!important.env\n",
			want:     []string{"app.env"},
			notWant:  []string{"important.env"},
		},
		{
			name:     "anchored",
			manifest: "/root.env\n",
			want:     []string{"root.env"},
			notWant:  []string{"nested/root.env"},
		},
		{
			name:     "directory",
			manifest: "config/\n",
			want:     []string{"config/dev/settings.json"},
			notWant:  []string{"other/settings.json"},
		},
		{
			name:     "double star",
			manifest: "build/**/cache/*.json\n",
			want:     []string{"build/cache/a.json", "build/one/two/cache/b.json"},
			notWant:  []string{"build/one/data.json"},
		},
		{
			name:     "escaped leading marker",
			manifest: "\\!literal\n\\#literal\n",
			want:     []string{"!literal", "#literal"},
			notWant:  []string{"literal"},
		},
		{
			name:     "spaces and comments",
			manifest: "# ignored comment\nname with space\n",
			want:     []string{"name with space"},
			notWant:  []string{"ignored comment"},
		},
	}

	allFiles := []string{
		".env", "nested/app.env", "app.txt", "app.env", "important.env",
		"root.env", "nested/root.env", "config/dev/settings.json",
		"other/settings.json", "build/cache/a.json",
		"build/one/two/cache/b.json", "build/one/data.json",
		"!literal", "#literal", "literal", "name with space", "ignored comment",
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, worktree := setupSeedWorktree(t, tt.manifest)
			for _, name := range allFiles {
				writeTestFile(t, repo, name, name+"\n")
			}

			if err := SeedWorktree(repo, worktree, nil); err != nil {
				t.Fatal(err)
			}
			for _, name := range tt.want {
				assertTestFile(t, worktree, name, name+"\n")
			}
			for _, name := range tt.notWant {
				if _, err := os.Lstat(filepath.Join(worktree, filepath.FromSlash(name))); !os.IsNotExist(err) {
					t.Fatalf("%s was unexpectedly seeded: %v", name, err)
				}
			}
		})
	}
}

func TestSeedWorktreeCopiesOnlyManifestSelections(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "selected.env\n")
	writeTestFile(t, repo, ".gitignore", "*.env\n*.secret\n")
	mustGit(t, repo, "add", "-f", ".gitignore")
	mustGit(t, repo, "commit", "-m", "narrow ignored files")
	writeTestFile(t, repo, "selected.env", "selected\n")
	writeTestFile(t, repo, "unrelated.secret", "secret\n")

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, worktree, "selected.env", "selected\n")
	if _, err := os.Lstat(filepath.Join(worktree, "unrelated.secret")); !os.IsNotExist(err) {
		t.Fatalf("unselected ignored file was unexpectedly seeded: %v", err)
	}
}

func TestSeedWorktreeDoesNotCopyManifestSelectedUnignoredFile(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "selected.env\ncredentials.txt\n")
	writeTestFile(t, repo, ".gitignore", "*.env\n")
	mustGit(t, repo, "add", "-f", ".gitignore")
	mustGit(t, repo, "commit", "-m", "narrow ignored files")
	writeTestFile(t, repo, "selected.env", "selected\n")
	writeTestFile(t, repo, "credentials.txt", "credentials\n")

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, worktree, "selected.env", "selected\n")
	if _, err := os.Lstat(filepath.Join(worktree, "credentials.txt")); !os.IsNotExist(err) {
		t.Fatalf("unignored file was unexpectedly seeded: %v", err)
	}
}

func TestSeedWorktreeFlattensSourceSymlink(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "linked.env\n")
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "linked.env")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	seeded := filepath.Join(worktree, "linked.env")
	if info, err := os.Lstat(seeded); err != nil {
		t.Fatal(err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("source symlink was recreated in the worktree")
	}
	assertTestFile(t, worktree, "linked.env", outside)
}

func TestSeedWorktreePreservesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	repo, worktree := setupSeedWorktree(t, "private.env\n")
	source := filepath.Join(repo, "private.env")
	if err := os.WriteFile(source, []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(worktree, "private.env"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("seeded permissions = %o, want 600", got)
	}
}

func TestSeedWorktreeDoesNotExecuteGitFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test filter command uses a Unix shell")
	}
	repo, worktree := setupSeedWorktree(t, "filtered.env\n")
	marker := filepath.Join(t.TempDir(), "filter-ran")
	writeTestFile(t, repo, ".gitattributes", "*.env filter=seed-test\n")
	mustGit(t, repo, "add", "-f", ".gitattributes")
	mustGit(t, repo, "commit", "-m", "add seed filter")
	mustGit(t, repo, "config", "filter.seed-test.clean", "sh -c 'touch \"$1\"; cat' - "+marker)
	mustGit(t, repo, "config", "filter.seed-test.smudge", "cat")
	writeTestFile(t, repo, "filtered.env", "source\n")

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, worktree, "filtered.env", "source\n")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Git content filter executed during seeding: %v", err)
	}
}

func TestSeedWorktreeDoesNotCopyFilesIgnoredOutsideManifest(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "selected.env\n")
	writeTestFile(t, repo, ".gitignore", "*.env\n")
	writeTestFile(t, repo, "selected.env", "selected\n")
	writeTestFile(t, repo, "unselected.env", "unselected\n")

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, worktree, "selected.env", "selected\n")
	if _, err := os.Stat(filepath.Join(worktree, "unselected.env")); !os.IsNotExist(err) {
		t.Fatalf("file ignored outside .worktreeinclude was seeded: %v", err)
	}
}

func TestSeedWorktreeHonorsCaseInsensitiveTrackedPathsWhenGitConfigIsFalse(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "worktree")
	mustGit(t, "", "init", "--initial-branch=main", repo)
	mustGit(t, repo, "config", "user.email", "test@test.com")
	mustGit(t, repo, "config", "user.name", "Test")
	writeTestFile(t, repo, "Config.env", "tracked\n")
	mustGit(t, repo, "add", "Config.env")
	mustGit(t, repo, "commit", "-m", "track config")
	mustGit(t, repo, "checkout", "-b", "seed-source")
	mustGit(t, repo, "rm", "Config.env")
	writeTestFile(t, repo, ".gitignore", "config.env\n")
	writeTestFile(t, repo, ".worktreeinclude", "config.env\n")
	mustGit(t, repo, "add", ".gitignore", ".worktreeinclude")
	mustGit(t, repo, "commit", "-m", "seed differently cased config")
	writeTestFile(t, repo, "config.env", "ignored\n")
	mustGit(t, repo, "worktree", "add", "--detach", worktree, "main")
	trackedInfo, err := os.Stat(filepath.Join(worktree, "Config.env"))
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(filepath.Join(worktree, "config.env"))
	if err != nil || !os.SameFile(trackedInfo, aliasInfo) {
		t.Skip("case-insensitive filesystem required")
	}
	mustGit(t, worktree, "config", "core.ignoreCase", "false")

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	assertGitCheckoutFile(t, worktree, "Config.env", "tracked\n")
	if dirty, err := IsDirty(worktree); err != nil {
		t.Fatal(err)
	} else if dirty {
		t.Fatal("case-folded tracked path was overwritten")
	}
}

func TestSeedWorktreePreservesTrackedAncestor(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "worktree")
	mustGit(t, "", "init", "--initial-branch=main", repo)
	mustGit(t, repo, "config", "user.email", "test@test.com")
	mustGit(t, repo, "config", "user.name", "Test")
	writeTestFile(t, repo, "config", "tracked\n")
	mustGit(t, repo, "add", "config")
	mustGit(t, repo, "commit", "-m", "track config file")
	mustGit(t, repo, "checkout", "-b", "seed-source")
	mustGit(t, repo, "rm", "config")
	writeTestFile(t, repo, ".gitignore", "config/\n")
	writeTestFile(t, repo, ".worktreeinclude", "config/settings.env\n")
	mustGit(t, repo, "add", ".gitignore", ".worktreeinclude")
	mustGit(t, repo, "commit", "-m", "seed nested config")
	writeTestFile(t, repo, "config/settings.env", "ignored\n")
	mustGit(t, repo, "worktree", "add", "--detach", worktree, "main")

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	assertGitCheckoutFile(t, worktree, "config", "tracked\n")
	if dirty, err := IsDirty(worktree); err != nil {
		t.Fatal(err)
	} else if dirty {
		t.Fatal("seeding made the target worktree dirty")
	}
}

func TestSeedWorktreePreservesTrackedDescendant(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "worktree")
	mustGit(t, "", "init", "--initial-branch=main", repo)
	mustGit(t, repo, "config", "user.email", "test@test.com")
	mustGit(t, repo, "config", "user.name", "Test")
	writeTestFile(t, repo, "config/settings.json", "tracked\n")
	mustGit(t, repo, "add", "config/settings.json")
	mustGit(t, repo, "commit", "-m", "track config file")
	mustGit(t, repo, "checkout", "-b", "seed-source")
	mustGit(t, repo, "rm", "config/settings.json")
	writeTestFile(t, repo, ".gitignore", "config\n")
	writeTestFile(t, repo, ".worktreeinclude", "config\n")
	mustGit(t, repo, "add", ".gitignore", ".worktreeinclude")
	mustGit(t, repo, "commit", "-m", "seed config file")
	writeTestFile(t, repo, "config", "ignored\n")
	mustGit(t, repo, "worktree", "add", "--detach", worktree, "main")

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	assertGitCheckoutFile(t, worktree, "config/settings.json", "tracked\n")
	if dirty, err := IsDirty(worktree); err != nil {
		t.Fatal(err)
	} else if dirty {
		t.Fatal("seeding made the target worktree dirty")
	}
}

func TestResetWorktreeRemovesSeedHiddenByModifiedManifest(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "secret.env\n")
	writeTestFile(t, repo, "secret.env", "secret\n")
	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, worktree, ".worktreeinclude", "")

	if err := ResetWorktreeWithSeededPaths(worktree, "main", []string{"secret.env"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "secret.env")); !os.IsNotExist(err) {
		t.Fatalf("obsolete seed survived reset: %v", err)
	}
}

func TestResetWorktreeRemovesInventoriedSeedWhenCurrentRevisionDoesNotIgnoreIt(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "secret.env\n")
	writeTestFile(t, repo, "secret.env", "secret\n")
	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, worktree, ".gitignore", "other.env\n")
	mustGit(t, worktree, "add", ".gitignore")
	mustGit(t, worktree, "commit", "-m", "stop ignoring seed")

	if err := ResetWorktreeWithSeededPaths(worktree, "main", []string{"secret.env"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "secret.env")); !os.IsNotExist(err) {
		t.Fatalf("legacy seed survived reset after ignore rules changed: %v", err)
	}
}

func TestEmptySeedInventoryPreservesUnignoredManifestSelection(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "notes.txt\n")
	writeTestFile(t, repo, ".gitignore", "*.env\n")
	mustGit(t, repo, "add", ".gitignore")
	mustGit(t, repo, "commit", "-m", "stop ignoring notes")
	mustGit(t, worktree, "reset", "--hard", "main")
	writeTestFile(t, worktree, "notes.txt", "keep me\n")

	identity, err := os.Stat(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeSeededPaths(worktree, []string{}, identity); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, worktree, "notes.txt", "keep me\n")
}

func TestSeedInventoryRemovesSeedWhoseSourceWasDeleted(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "secret.env\n")
	writeTestFile(t, repo, "secret.env", "secret\n")
	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "secret.env")); err != nil {
		t.Fatal(err)
	}

	identity, err := os.Stat(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeSeededPaths(worktree, []string{"secret.env"}, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "secret.env")); !os.IsNotExist(err) {
		t.Fatalf("obsolete seed survived after its source was deleted: %v", err)
	}
}

func TestResetWorktreeRejectsControlPathSeedInventory(t *testing.T) {
	worktree, _, _ := setupSafeResetWorktree(t)

	if err := ResetWorktreeWithSeededPaths(worktree, "main", []string{".git"}); err == nil {
		t.Fatal("reset accepted a control path as seeded inventory")
	}
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		t.Fatalf("invalid inventory damaged worktree marker: %v", err)
	}
}

func TestRemoveSeededPathsRejectsSymlinkedRoot(t *testing.T) {
	outside := t.TempDir()
	writeTestFile(t, outside, "secret.env", "keep me\n")
	alias := filepath.Join(t.TempDir(), "worktree")
	if err := os.Symlink(outside, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	identity, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeSeededPaths(alias, []string{"secret.env"}, identity); err == nil {
		t.Fatal("expected symlinked worktree root to be rejected")
	}
	assertTestFile(t, outside, "secret.env", "keep me\n")
}

func TestRemoveSeededPathsRejectsReplacementDirectory(t *testing.T) {
	parent := t.TempDir()
	worktree := filepath.Join(parent, "worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	originalHandle, err := openDirectoryNoFollow(worktree)
	if err != nil {
		t.Fatal(err)
	}
	defer originalHandle.Close()
	original, err := originalHandle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(worktree, worktree+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, worktree, "secret.env", "keep me\n")

	if err := removeSeededPaths(worktree, []string{"secret.env"}, original); err == nil {
		t.Fatal("expected replaced worktree root to be rejected")
	}
	assertTestFile(t, worktree, "secret.env", "keep me\n")
}

func TestRemoveSeededPathsRejectsUnregisteredReplacementDirectory(t *testing.T) {
	replacement := t.TempDir()
	writeTestFile(t, replacement, "secret.env", "keep me\n")

	if err := RemoveSeededPaths(replacement, []string{"secret.env"}); err == nil {
		t.Fatal("expected cleanup to reject an unregistered replacement directory")
	}
	assertTestFile(t, replacement, "secret.env", "keep me\n")
}

func TestRemoveSeededPathsRejectsCopiedJJWorkspaceMarker(t *testing.T) {
	parent := t.TempDir()
	worktree := filepath.Join(parent, "worktree")
	marker := filepath.Join(worktree, ".jj", "repo")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("store"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareJJSeededCleanup(worktree); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("store"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, worktree, "secret.env", "keep me\n")

	if err := RemoveSeededPathsFromJJWorkspace(worktree, []string{"secret.env"}); err == nil {
		t.Fatal("expected cleanup to reject a copied jj workspace marker")
	}
	assertTestFile(t, worktree, "secret.env", "keep me\n")
}

func TestPrepareJJSeededCleanupDoesNotClaimAdjacentUserFile(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	marker := filepath.Join(worktree, ".jj", "repo")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("store"), 0o644); err != nil {
		t.Fatal(err)
	}
	adjacent := worktree + ".treehouse-jj-seed-auth"
	if err := os.WriteFile(adjacent, []byte("user data\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := PrepareJJSeededCleanup(worktree); err != nil {
		t.Fatalf("PrepareJJSeededCleanup failed: %v", err)
	}
	assertTestFile(t, filepath.Dir(adjacent), filepath.Base(adjacent), "user data\n")
}

// TestPrepareJJSeededCleanupRelinksOverALeftoverEntry pins that a removed
// workspace whose authentication was never unlinked does not make its path
// unusable. The entry is keyed on the worktree path, so a leftover authenticates
// nothing and the new workspace's marker replaces it.
func TestPrepareJJSeededCleanupRelinksOverALeftoverEntry(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	marker := filepath.Join(worktree, ".jj", "repo")
	writeMarker := func(contents string) {
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeMarker("first store")
	if err := PrepareJJSeededCleanup(worktree); err != nil {
		t.Fatal(err)
	}

	// The workspace goes away with its authentication still linked - exactly what
	// a cleanup failure on the removal routes leaves behind.
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	authPath := jjSeedAuthenticationPath(worktree)
	if _, err := os.Stat(authPath); err != nil {
		t.Fatalf("no leftover entry, so this test proves nothing: %v", err)
	}

	writeMarker("second store")
	if err := PrepareJJSeededCleanup(worktree); err != nil {
		t.Fatalf("PrepareJJSeededCleanup refused a path holding a leftover entry: %v", err)
	}
	if err := AuthenticateJJSeededCleanup(worktree); err != nil {
		t.Fatalf("the relinked entry does not authenticate the new workspace: %v", err)
	}
}

// TestPrepareJJSeededCleanupRefusesAnOccupiedNonEntry keeps the relink narrow: it
// replaces a plain file treehouse could have written and nothing else.
func TestPrepareJJSeededCleanupRefusesAnOccupiedNonEntry(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	marker := filepath.Join(worktree, ".jj", "repo")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("store"), 0o644); err != nil {
		t.Fatal(err)
	}
	authPath := jjSeedAuthenticationPath(worktree)
	if err := os.MkdirAll(authPath, 0o700); err != nil {
		t.Fatal(err)
	}

	err := PrepareJJSeededCleanup(worktree)
	if err == nil {
		t.Fatal("expected an occupied path that is not an authentication entry to be refused")
	}
	if !strings.Contains(err.Error(), authPath) {
		t.Errorf("error %q does not name %q", err, authPath)
	}
	if info, statErr := os.Lstat(authPath); statErr != nil || !info.IsDir() {
		t.Errorf("the occupied path was not left alone: %v", statErr)
	}
}

func TestRemoveJJSeedAuthenticationRejectsUnownedFile(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	marker := filepath.Join(worktree, ".jj", "repo")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("store"), 0o644); err != nil {
		t.Fatal(err)
	}
	authPath := jjSeedAuthenticationPath(worktree)
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("user data\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RemoveJJSeedAuthentication(worktree); err == nil {
		t.Fatal("expected unowned authentication file to be rejected")
	}
	assertTestFile(t, filepath.Dir(authPath), filepath.Base(authPath), "user data\n")
}

func TestRemoveStaleJJSeedAuthenticationAllowsWorkspaceRecreation(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	store := filepath.Join(repo, ".jj", "repo")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(parent, "worktree")
	marker := filepath.Join(worktree, ".jj", "repo")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(store), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareJJSeededCleanup(worktree); err != nil {
		t.Fatal(err)
	}
	identity, err := JJSeedAuthenticationIdentity(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}

	if err := RemoveStaleJJSeedAuthentication(worktree, identity); err != nil {
		t.Fatalf("RemoveStaleJJSeedAuthentication failed: %v", err)
	}
	if _, err := os.Stat(jjSeedAuthenticationPath(worktree)); !os.IsNotExist(err) {
		t.Fatalf("stale authentication still exists: %v", err)
	}
}

func TestRemoveJJSeedAuthenticationPreservesConcurrentReplacement(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	marker := filepath.Join(worktree, ".jj", "repo")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("store"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareJJSeededCleanup(worktree); err != nil {
		t.Fatal(err)
	}
	oldHook := jjAuthenticationQuarantined
	jjAuthenticationQuarantined = func(path string) {
		if err := os.WriteFile(path, []byte("replacement\n"), 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { jjAuthenticationQuarantined = oldHook })

	if err := RemoveJJSeedAuthentication(worktree); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, filepath.Dir(jjSeedAuthenticationPath(worktree)), filepath.Base(jjSeedAuthenticationPath(worktree)), "replacement\n")
}

func TestRemoveSeededPathsFromJJWorkspacePreservesConcurrentReplacement(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	marker := filepath.Join(worktree, ".jj", "repo")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("store"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, worktree, "secret.env", "seeded\n")
	if err := PrepareJJSeededCleanup(worktree); err != nil {
		t.Fatal(err)
	}
	oldHook := jjAuthenticationQuarantined
	jjAuthenticationQuarantined = func(path string) {
		if err := os.WriteFile(path, []byte("replacement\n"), 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { jjAuthenticationQuarantined = oldHook })

	if err := RemoveSeededPathsFromJJWorkspace(worktree, []string{"secret.env"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "secret.env")); !os.IsNotExist(err) {
		t.Fatalf("seeded file still exists: %v", err)
	}
	assertTestFile(t, filepath.Dir(jjSeedAuthenticationPath(worktree)), filepath.Base(jjSeedAuthenticationPath(worktree)), "replacement\n")
}

func TestRemoveStaleJJSeedAuthenticationPreservesConcurrentReplacement(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	marker := filepath.Join(worktree, ".jj", "repo")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("store"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareJJSeededCleanup(worktree); err != nil {
		t.Fatal(err)
	}
	identity, err := JJSeedAuthenticationIdentity(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	oldHook := jjAuthenticationQuarantined
	jjAuthenticationQuarantined = func(path string) {
		if err := os.WriteFile(path, []byte("replacement\n"), 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { jjAuthenticationQuarantined = oldHook })

	if err := RemoveStaleJJSeedAuthentication(worktree, identity); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, filepath.Dir(jjSeedAuthenticationPath(worktree)), filepath.Base(jjSeedAuthenticationPath(worktree)), "replacement\n")
}

func TestSeedWorktreeRefusesDestinationSymlink(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "config/settings.env\n")
	writeTestFile(t, repo, "config/settings.env", "seeded\n")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(worktree, "config")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := SeedWorktree(repo, worktree, nil); err == nil {
		t.Fatal("expected destination symlink to reject seeding")
	}
	if _, err := os.Stat(filepath.Join(outside, "settings.env")); !os.IsNotExist(err) {
		t.Fatalf("seed escaped through destination symlink: %v", err)
	}
}

func TestSeedWorktreeRefusesToOverwriteUnmanagedDestination(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "secret.env\n")
	writeTestFile(t, repo, "secret.env", "seeded\n")
	writeTestFile(t, worktree, "secret.env", "local\n")

	if err := SeedWorktree(repo, worktree, nil); err == nil {
		t.Fatal("expected existing unmanaged file to reject seeding")
	}
	assertTestFile(t, worktree, "secret.env", "local\n")
}

func TestSeedWorktreeRefusesToOverwriteUnmanagedAncestor(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "config/secret.env\n")
	writeTestFile(t, repo, "config/secret.env", "seeded\n")
	writeTestFile(t, worktree, "config", "local\n")

	if err := SeedWorktree(repo, worktree, nil); err == nil {
		t.Fatal("expected existing unmanaged ancestor to reject seeding")
	}
	assertTestFile(t, worktree, "config", "local\n")
}

func TestSeedWorktreeRejectsSymlinkedRoot(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, ".env\n")
	writeTestFile(t, repo, ".env", "seeded\n")
	alias := filepath.Join(t.TempDir(), "worktree")
	if err := os.Symlink(worktree, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := SeedWorktree(repo, alias, nil); err == nil {
		t.Fatal("expected symlinked worktree root to be rejected")
	}
}

func TestOpenRootUnchangedRejectsReplacedDirectory(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "worktree")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	originalHandle, err := openDirectoryNoFollow(path)
	if err != nil {
		t.Fatal(err)
	}
	defer originalHandle.Close()
	original, err := originalHandle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := openRootUnchanged(path, original)
	if root != nil {
		root.Close()
	}
	if err == nil {
		t.Fatal("expected replaced worktree root to be rejected")
	}
}

func TestOpenRootUnchangedRejectsSymlinkToOriginalDirectory(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "worktree")
	oldPath := path + ".old"
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	originalHandle, err := openDirectoryNoFollow(path)
	if err != nil {
		t.Fatal(err)
	}
	defer originalHandle.Close()
	original, err := originalHandle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldPath, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	root, err := openRootUnchanged(path, original)
	if root != nil {
		root.Close()
	}
	if err == nil {
		t.Fatal("expected symlink to original worktree root to be rejected")
	}
}

func TestSeedWorktreeIgnoresUntrackedManifest(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "worktree")
	mustGit(t, "", "init", "--initial-branch=main", repo)
	mustGit(t, repo, "config", "user.email", "test@test.com")
	mustGit(t, repo, "config", "user.name", "Test")
	writeTestFile(t, repo, ".gitignore", "*.env\n")
	mustGit(t, repo, "add", "-f", ".gitignore")
	mustGit(t, repo, "commit", "-m", "no manifest")
	mustGit(t, repo, "worktree", "add", "--detach", worktree)

	writeTestFile(t, repo, ".worktreeinclude", "secret.env\n")
	writeTestFile(t, repo, "secret.env", "secret\n")

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "secret.env")); !os.IsNotExist(err) {
		t.Fatal("untracked .worktreeinclude selected a seed")
	}
}

func TestSeedWorktreeIgnoresDirtyCommittedManifest(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, "selected.env\n")
	writeTestFile(t, repo, "selected.env", "selected\n")
	writeTestFile(t, repo, "extra.env", "extra\n")
	writeTestFile(t, repo, ".worktreeinclude", "selected.env\nextra.env\n")

	if err := SeedWorktree(repo, worktree, nil); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, worktree, "selected.env", "selected\n")
	if _, err := os.Stat(filepath.Join(worktree, "extra.env")); !os.IsNotExist(err) {
		t.Fatal("dirty .worktreeinclude selected an extra seed")
	}
}

// isolateIncludeConfig prevents host policy from authorizing seeds in fixtures.
func isolateIncludeConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_CONFIG", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_COUNT", "0")
	return home
}

func assertSeedInventory(t *testing.T, repo, worktree string, manifest []byte, want ...string) {
	t.Helper()
	got, err := SeedWorktreeWithInventory(repo, worktree, manifest)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("seed inventory = %q, want %q", got, want)
	}
	for _, name := range got {
		source, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		assertTestFile(t, worktree, name, string(source))
	}
}

func TestSeedLayersUnconfiguredParity(t *testing.T) {
	isolateIncludeConfig(t)
	repo, worktree := setupSeedWorktree(t, "selected.env\nnested/**\n!nested/private.env\n")
	for _, name := range []string{"selected.env", "nested/public.env", "nested/private.env", "unselected.env"} {
		writeTestFile(t, repo, name, "bytes for "+name+"\x00\r\n")
	}
	assertSeedInventory(t, repo, worktree, nil, "nested/public.env", "selected.env")
	for _, name := range []string{"nested/public.env", "selected.env"} {
		data, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %x", name, sha256.Sum256(data))
	}
}

func TestSeedLayersDiscoveryAndPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, user, clone, repository string
		want                          []string
	}{
		{"absent", "", "", "", nil},
		{"user alone", "user.env", "", "", []string{"user.env"}},
		{"clone alone", "", "clone.env", "", []string{"clone.env"}},
		{"clone negates user", "conflict.env", "!conflict.env", "", nil},
		{"repository negates user", "conflict.env", "", "!conflict.env", nil},
		{"repository negates clone", "", "conflict.env", "!conflict.env", nil},
		{"repository readds", "conflict.env", "!conflict.env", "conflict.env", []string{"conflict.env"}},
		{"additions", "user.env", "clone.env", "repository.env", []string{"user.env", "clone.env", "repository.env"}},
		{"excluded parent", "nested/", "!nested/private.env", "", []string{"nested/private.env", "nested/public.env"}},
		{"traversable parent", "nested/**", "!nested/private.env", "", []string{"nested/public.env"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateIncludeConfig(t)
			writeTestFile(t, home, "xdg/git/worktreeinclude", "secret.env\n")
			repo, worktree := setupSeedWorktree(t, tc.repository)
			if tc.repository == "" {
				mustGit(t, repo, "rm", "-f", ".worktreeinclude")
				mustGit(t, repo, "commit", "-m", "no committed manifest")
				mustGit(t, worktree, "reset", "--hard", "main")
			}
			// Neither an untracked nor a dirty source manifest supplies policy.
			writeTestFile(t, repo, ".worktreeinclude", "secret.env\n")
			for _, name := range []string{"user.env", "clone.env", "repository.env", "conflict.env", "secret.env", "nested/private.env", "nested/public.env"} {
				writeTestFile(t, repo, name, name+"\n")
			}
			if tc.user != "" {
				writeTestFile(t, repo, "personal include", tc.user)
				mustGit(t, repo, "config", "worktree.includeFile", "personal include")
			}
			if tc.clone != "" {
				common, err := CommonGitDir(repo)
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, common, "info/worktreeinclude", tc.clone)
			}
			assertSeedInventory(t, repo, worktree, nil, tc.want...)
		})
	}
}

func TestSeedLayersMatchOrderedGitFiles(t *testing.T) {
	isolateIncludeConfig(t)
	layers := []string{
		"\xef\xbb\xbf# comment\r\n/space name.env\r\n/anchored.env\r\nnested/**\r\n*break.env\r\nremove.env", // no LF
		"\xef\xbb\xbf!remove.env\r\n!nested/private.env\r\n\\#hash.env\r\n\\!bang.env",                       // per-file BOM
		"\xef\xbb\xbfrepository.env\ntrailing\\ \n",
	}
	repo, worktree := setupSeedWorktree(t, layers[2])
	mustGit(t, repo, "config", "core.autocrlf", "false")
	for _, name := range []string{"space name.env", "anchored.env", "nested/anchored.env", "nested/public.env", "nested/private.env", "remove.env", "repository.env", "#hash.env", "!bang.env", "line\nbreak.env", "trailing "} {
		if runtime.GOOS == "windows" && (strings.Contains(name, "\n") || strings.HasSuffix(name, " ")) {
			continue // Windows filenames cannot contain LF or end with spaces.
		}
		writeTestFile(t, repo, name, "raw\x00\r\n"+name)
	}
	paths := make([]string, len(layers))
	args := []string{"ls-files", "-z", "--others", "--ignored"}
	for i, layer := range layers {
		paths[i] = filepath.Join(t.TempDir(), "include")
		if err := os.WriteFile(paths[i], []byte(layer), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--exclude-from="+paths[i])
	}
	mustGit(t, repo, "config", "worktree.includeFile", paths[0])
	common, err := CommonGitDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, common, "info/worktreeinclude", layers[1])
	oracle, err := gitOutput(repo, nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	got, err := selectedSeedPathsEnv(repo, worktree, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, oracle) {
		t.Fatalf("combined matcher = %q, ordered Git files = %q", got, oracle)
	}
}

func TestSeedLayersInvalidDestinationRevisionRemainsAnError(t *testing.T) {
	isolateIncludeConfig(t)
	repo, worktree := setupSeedWorktree(t, "")
	if _, err := selectedSeedPathsEnv(repo, worktree, nil, "refs/heads/missing", nil); err == nil {
		t.Fatal("missing destination revision must remain a seeding error")
	}
}

func TestSeedLayersDanglingCloneSidecarIsAnError(t *testing.T) {
	isolateIncludeConfig(t)
	repo, worktree := setupSeedWorktree(t, "")
	common, err := CommonGitDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(common, "info", "worktreeinclude")
	if err := os.Symlink(filepath.Join(common, "missing-worktreeinclude"), sidecar); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := selectedSeedPathsEnv(repo, worktree, nil, "", nil); err == nil || !strings.Contains(err.Error(), "read clone include") {
		t.Fatalf("dangling clone sidecar error = %v, want persistent-layer read error", err)
	}
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	got, err := selectedSeedPathsEnv(repo, worktree, nil, "", nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("absent clone sidecar = %q, %v; want no rules", got, err)
	}
	if err := os.Symlink(filepath.Join(common, "missing-worktreeinclude"), sidecar); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err = selectedSeedPathsEnv(repo, worktree, nil, "", []byte{})
	if err != nil || len(got) != 0 {
		t.Fatalf("empty explicit override = %q, %v; want sidecar bypass", got, err)
	}
}

func TestSeedLayersConfigLookup(t *testing.T) {
	for _, mode := range []string{"tilde", "relative spaces", "include", "includeIf", "scope", "command config", "empty file", "missing", "empty value", "directory", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			home := isolateIncludeConfig(t)
			repo, worktree := setupSeedWorktree(t, "")
			writeTestFile(t, repo, "selected.env", "selected\n")
			writeTestFile(t, home, "personal include", "selected.env")
			path := filepath.Join(home, "personal include")
			wantError := ""
			switch mode {
			case "tilde":
				path = "~/personal include"
			case "relative spaces":
				writeTestFile(t, repo, " policy file", "selected.env")
				path = " policy file" // whitespace is part of the path, not padding.
			case "include", "includeIf":
				included := filepath.Join(home, "included config")
				mustGit(t, repo, "config", "--file", included, "worktree.includeFile", path)
				key := "include.path"
				if mode == "includeIf" {
					common, err := CommonGitDir(repo)
					if err != nil {
						t.Fatal(err)
					}
					common, err = filepath.EvalSymlinks(common)
					if err != nil {
						t.Fatal(err)
					}
					// Git appends ** to a trailing /; match the gitdir itself instead.
					key = "includeIf.gitdir:" + filepath.ToSlash(common) + ".path"
				}
				mustGit(t, repo, "config", "--global", key, included)
			case "scope":
				mustGit(t, repo, "config", "--global", "worktree.includeFile", "missing-global")
			case "command config":
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "worktree.includeFile")
				t.Setenv("GIT_CONFIG_VALUE_0", path)
				path = "missing-local"
			case "empty file":
				writeTestFile(t, home, "personal include", "")
			case "missing":
				path += "-missing"
				wantError = "worktree.includeFile"
			case "empty value":
				path = ""
				wantError = "worktree.includeFile"
			case "directory":
				path = home // deterministic unreadability even as root.
				wantError = "worktree.includeFile"
			case "malformed":
				writeTestFile(t, home, ".gitconfig", "[invalid\n")
				wantError = "worktree.includeFile"
			}
			if mode != "include" && mode != "includeIf" && mode != "malformed" {
				mustGit(t, repo, "config", "worktree.includeFile", path)
			}
			got, err := SeedWorktreeWithInventory(repo, worktree, nil)
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("expected %s error, got inventory %q, error %v", wantError, got, err)
				}
				return
			}
			want := []string{"selected.env"}
			if mode == "empty file" {
				want = nil
			}
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("inventory %q, error %v; want %q", got, err, want)
			}
		})
	}
}

func TestSeedLayersSourceContextAndDestinationRevision(t *testing.T) {
	isolateIncludeConfig(t)
	repo, worktree := setupSeedWorktree(t, "old.env\n")
	writeTestFile(t, repo, ".worktreeinclude", "new.env\n")
	mustGit(t, repo, "add", "-f", ".worktreeinclude")
	mustGit(t, repo, "commit", "-m", "new policy")
	mustGit(t, repo, "config", "extensions.worktreeConfig", "true")
	writeTestFile(t, repo, "source.include", "personal.env\n")
	mustGit(t, repo, "config", "--worktree", "worktree.includeFile", "source.include")
	mustGit(t, worktree, "config", "--worktree", "worktree.includeFile", "broken-destination")
	common, err := CommonGitDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, common, "info/worktreeinclude", "clone.env\n")
	// The private linked-worktree gitdir must not be used as the clone layer.
	private, err := gitOutput(worktree, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, strings.TrimSpace(string(private)), "info/worktreeinclude", "secret.env\n")
	for _, name := range []string{"old.env", "new.env", "personal.env", "clone.env", "secret.env"} {
		writeTestFile(t, repo, name, name)
	}
	nested := filepath.Join(worktree, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := FindMainRepoRootFrom(nested)
	if err != nil {
		t.Fatal(err)
	}
	assertSeedInventory(t, root, worktree, nil, "old.env", "personal.env", "clone.env")
}

func TestSeedLayersNonstandardMetadataAndIndependentIgnore(t *testing.T) {
	isolateIncludeConfig(t)
	repo, worktree := setupSeedWorktree(t, "")
	// Relocate the source metadata and let Git rewrite its gitfile.
	metadata := filepath.Join(t.TempDir(), "shared metadata")
	mustGit(t, "", "init", "--separate-git-dir="+metadata, repo)
	writeTestFile(t, repo, ".gitignore", "repository.env\n!unignored.env\n")
	writeTestFile(t, metadata, "info/exclude", "clone.env\nunselected.env\n")
	globalIgnore := filepath.Join(t.TempDir(), "global ignore")
	if err := os.WriteFile(globalIgnore, []byte("global.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "config", "core.excludesFile", globalIgnore)
	writeTestFile(t, metadata, "info/worktreeinclude", "*.env\n!unselected.env\n")
	writeTestFile(t, repo, "tracked.env", "indexed")
	mustGit(t, repo, "add", "-f", "tracked.env")
	writeTestFile(t, repo, "tracked.env", "modified")
	for _, name := range []string{"repository.env", "clone.env", "global.env", "unselected.env", "unignored.env"} {
		writeTestFile(t, repo, name, name)
	}
	// Selection can run independently of the old worktree backlink after relocation.
	got, err := selectedSeedPathsEnv(repo, "", nil, "HEAD", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "clone.env\x00global.env\x00repository.env\x00"
	if string(got) != want {
		t.Fatalf("eligible paths %q, want %q (worktree %s)", got, want, worktree)
	}
}

func TestSeedLayersOverrideBypassesBrokenDefaults(t *testing.T) {
	for _, broken := range []string{"configured file", "clone directory", "committed directory"} {
		t.Run(broken, func(t *testing.T) {
			isolateIncludeConfig(t)
			repo, worktree := setupSeedWorktree(t, "default.env\n")
			switch broken {
			case "configured file":
				mustGit(t, repo, "config", "worktree.includeFile", "missing")
			case "clone directory":
				common, err := CommonGitDir(repo)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(common, "info", "worktreeinclude"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "committed directory":
				mustGit(t, repo, "rm", "-f", ".worktreeinclude")
				writeTestFile(t, repo, ".worktreeinclude/child", "not a blob")
				mustGit(t, repo, "add", "-f", ".worktreeinclude/child")
				mustGit(t, repo, "commit", "-m", "invalid manifest")
				mustGit(t, worktree, "reset", "--hard", "main")
			}
			writeTestFile(t, repo, "explicit.env", "explicit\n")
			assertSeedInventory(t, repo, worktree, []byte{})
			assertSeedInventory(t, repo, worktree, []byte("explicit.env"), "explicit.env")
			if _, err := selectedSeedPathsEnv(repo, worktree, nil, "", nil); err == nil {
				t.Fatal("broken default must fail when override is removed")
			}
		})
	}
}

func setupSeedWorktree(t *testing.T, manifest string) (string, string) {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "worktree")
	mustGit(t, "", "init", "--initial-branch=main", repo)
	mustGit(t, repo, "config", "user.email", "test@test.com")
	mustGit(t, repo, "config", "user.name", "Test")
	writeTestFile(t, repo, ".gitignore", "*\n")
	writeTestFile(t, repo, ".worktreeinclude", manifest)
	mustGit(t, repo, "add", "-f", ".gitignore", ".worktreeinclude")
	mustGit(t, repo, "commit", "-m", "seed manifest")
	mustGit(t, repo, "worktree", "add", "--detach", worktree)
	return repo, worktree
}

func writeTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", name, data, want)
	}
}

func assertGitCheckoutFile(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ReplaceAll(string(data), "\r\n", "\n"); got != want {
		t.Fatalf("%s = %q, want %q", name, data, want)
	}
}
func installGitWrapper(t *testing.T, rejectPathFormat bool) (string, string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "main.go")
	code := `package main
import (
  "fmt"
  "os"
  "os/exec"
  "strings"
)
func main() {
  args := os.Args[1:]
  if log := os.Getenv("TREEHOUSE_GIT_LOG"); log != "" {
    f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
    if err != nil { panic(err) }
    _, _ = fmt.Fprintln(f, strings.Join(args, " "))
    _ = f.Close()
  }
  if os.Getenv("TREEHOUSE_REJECT_PATH_FORMAT") == "1" {
    for _, arg := range args {
      if arg == "--path-format=absolute" { os.Exit(129) }
    }
  }
  cmd := exec.Command(os.Getenv("TREEHOUSE_REAL_GIT"), args...)
  cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
  if err := cmd.Run(); err != nil {
    if exit, ok := err.(*exec.ExitError); ok { os.Exit(exit.ExitCode()) }
    fmt.Fprintln(os.Stderr, err)
    os.Exit(1)
  }
}`
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	wrapper := filepath.Join(root, name)
	cmd := exec.Command("go", "build", "-o", wrapper, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build git wrapper: %v\n%s", err, output)
	}
	logPath := filepath.Join(root, "git.log")
	t.Setenv("TREEHOUSE_REAL_GIT", realGit)
	t.Setenv("TREEHOUSE_GIT_LOG", logPath)
	if rejectPathFormat {
		t.Setenv("TREEHOUSE_REJECT_PATH_FORMAT", "1")
	}
	return logPath, wrapper
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
