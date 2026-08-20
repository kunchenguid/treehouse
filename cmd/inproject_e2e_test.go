package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readExcludeFile returns the contents of the repo's .git/info/exclude, or "" if
// it does not exist.
func readExcludeFile(t *testing.T, repoDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoDir, ".git", "info", "exclude"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("failed to read .git/info/exclude: %v", err)
	}
	return string(data)
}

// globalPoolEntries returns the names of per-repo pools under the fake home's
// global ~/.treehouse store. In-project mode must never create any.
func globalPoolEntries(t *testing.T, homeDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(homeDir, ".treehouse"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("failed to read global pool dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// assertInProject runs "get --lease" with the given extra env / args and checks
// the in-project contract for the root override: the pool lands under
// <repo>/.treehouse, the worktree has repo content, and no orphan is created in
// the global store.
func assertInProject(t *testing.T, repoDir, homeDir string, extraEnv []string, args ...string) string {
	t.Helper()

	getArgs := append([]string{"get", "--lease"}, args...)
	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, extraEnv, getArgs...)
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	if wtPath == "" {
		t.Fatal("could not capture leased worktree path")
	}

	// The pool must live inside the repo, under <repo>/.treehouse.
	inProjectRoot := filepath.Join(repoDir, ".treehouse")
	if !strings.HasPrefix(wtPath, inProjectRoot+string(filepath.Separator)) {
		t.Fatalf("expected in-project worktree under %s, got %s", inProjectRoot, wtPath)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Fatalf("expected in-project worktree to contain repo content: %v", err)
	}

	// No global orphan may be created under ~/.treehouse.
	if names := globalPoolEntries(t, homeDir); len(names) != 0 {
		t.Fatalf("expected no global pool entries in in-project mode, got: %v", names)
	}

	return wtPath
}

func TestInProjectViaFlag(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	assertInProject(t, repoDir, homeDir, nil, "--root", ".")
}

func TestInProjectViaEnv(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	assertInProject(t, repoDir, homeDir, []string{"TREEHOUSE_ROOT=."})
}

func TestInProjectViaConfig(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("root = \".\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repoDir, "add", "treehouse.toml")
	gitCmd(t, repoDir, "commit", "-m", "configure in-project root")

	assertInProject(t, repoDir, homeDir, nil)
}

// TestInProjectRemovalLeavesNoGlobalOrphan mirrors deleting the project: after
// removing the repo directory, nothing is left behind in the global store.
func TestInProjectRemovalLeavesNoGlobalOrphan(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	assertInProject(t, repoDir, homeDir, nil, "--root", ".")

	if err := os.RemoveAll(repoDir); err != nil {
		t.Fatalf("failed to remove project: %v", err)
	}
	if names := globalPoolEntries(t, homeDir); len(names) != 0 {
		t.Fatalf("expected no global orphan after project removal, got: %v", names)
	}
}

// TestRootFlagWinsOverEnv verifies the documented precedence: the --root flag
// overrides TREEHOUSE_ROOT. The env points at an absolute global-style root, but
// the flag selects in-project, so the pool must land in the repo.
func TestRootFlagWinsOverEnv(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	envRoot := t.TempDir()
	assertInProject(t, repoDir, homeDir, []string{"TREEHOUSE_ROOT=" + envRoot}, "--root", ".")

	// The env-pointed root must not have been used.
	if _, err := os.Stat(filepath.Join(envRoot, ".treehouse")); !os.IsNotExist(err) {
		t.Fatalf("expected env root to be ignored when --root is set, stat err: %v", err)
	}
}

// TestDefaultRootUnchanged asserts the default (unset) behavior is untouched:
// the pool lands under the global ~/.treehouse, and the repo working tree is not
// modified.
func TestDefaultRootUnchanged(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)

	globalRoot := filepath.Join(homeDir, ".treehouse")
	if !strings.HasPrefix(wtPath, globalRoot+string(filepath.Separator)) {
		t.Fatalf("expected default worktree under %s, got %s", globalRoot, wtPath)
	}

	// The repo must be untouched: no in-project pool, no ignore edits.
	if _, err := os.Stat(filepath.Join(repoDir, ".treehouse")); !os.IsNotExist(err) {
		t.Fatalf("expected no in-project pool for the default root, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("expected no .gitignore for the default root, stat err: %v", err)
	}
	if exclude := readExcludeFile(t, repoDir); strings.Contains(exclude, "/.treehouse") {
		t.Fatalf("expected no exclude entry for the default global root, got:\n%s", exclude)
	}
	if status := gitCmd(t, repoDir, "status", "--porcelain"); status != "" {
		t.Fatalf("expected clean git status for the default root, got:\n%s", status)
	}
}
