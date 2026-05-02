package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var (
	treehouseBin      string
	exitShellBin      string
	dirtyMainShellBin string
)

func TestMain(m *testing.M) {
	buildDir, err := os.MkdirTemp("", "treehouse-e2e-*")
	if err != nil {
		panic(err)
	}

	// Build the treehouse binary from the module root (parent of cmd/).
	treehouseBin = filepath.Join(buildDir, "treehouse")
	if runtime.GOOS == "windows" {
		treehouseBin += ".exe"
	}
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		panic(err)
	}
	build := exec.Command("go", "build", "-o", treehouseBin, ".")
	build.Dir = moduleRoot
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("failed to build treehouse: " + err.Error())
	}

	// Build a minimal program that exits 0 immediately, used as the shell
	// in tests so that "treehouse get" doesn't block waiting for input.
	exitShellBin = filepath.Join(buildDir, "exit-shell")
	if runtime.GOOS == "windows" {
		exitShellBin += ".exe"
	}
	exitSrcDir := filepath.Join(buildDir, "exit-shell-src")
	if err := os.MkdirAll(exitSrcDir, 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(exitSrcDir, "go.mod"), []byte("module exit-shell\n\ngo 1.21\n"), 0o644); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(exitSrcDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		panic(err)
	}
	buildShell := exec.Command("go", "build", "-o", exitShellBin, ".")
	buildShell.Dir = exitSrcDir
	buildShell.Stderr = os.Stderr
	if err := buildShell.Run(); err != nil {
		panic("failed to build exit-shell: " + err.Error())
	}

	dirtyMainShellBin = filepath.Join(buildDir, "dirty-main-shell")
	if runtime.GOOS == "windows" {
		dirtyMainShellBin += ".exe"
	}
	dirtyMainSrcDir := filepath.Join(buildDir, "dirty-main-shell-src")
	if err := os.MkdirAll(dirtyMainSrcDir, 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dirtyMainSrcDir, "go.mod"), []byte("module dirty-main-shell\n\ngo 1.21\n"), 0o644); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dirtyMainSrcDir, "main.go"), []byte(`package main

import (
	"os"
	"os/exec"
)

func main() {
	cmd := exec.Command("git", "checkout", "main")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
	if err := os.WriteFile("README.md", []byte("dirty\n"), 0o644); err != nil {
		os.Exit(1)
	}
}
`), 0o644); err != nil {
		panic(err)
	}
	buildDirtyMainShell := exec.Command("go", "build", "-o", dirtyMainShellBin, ".")
	buildDirtyMainShell.Dir = dirtyMainSrcDir
	buildDirtyMainShell.Stderr = os.Stderr
	if err := buildDirtyMainShell.Run(); err != nil {
		panic("failed to build dirty-main-shell: " + err.Error())
	}

	code := m.Run()
	os.RemoveAll(buildDir)
	os.Exit(code)
}

// setupTestRepo creates a git repo with a bare remote. Returns the repo
// directory and a fake home directory (used to isolate pool state from the
// real home). All paths are symlink-resolved for macOS (/tmp → /private/tmp).
func setupTestRepo(t *testing.T) (repoDir, homeDir string) {
	t.Helper()

	base := t.TempDir()
	// Resolve symlinks so paths match what git rev-parse returns.
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}

	homeDir = filepath.Join(base, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	bareDir := filepath.Join(base, "remote.git")
	repoDir = filepath.Join(base, "myrepo")

	gitCmd(t, "", "init", "--bare", "--initial-branch=main", bareDir)
	gitCmd(t, "", "init", "--initial-branch=main", repoDir)
	gitCmd(t, repoDir, "config", "user.email", "test@test.com")
	gitCmd(t, repoDir, "config", "user.name", "Test")
	gitCmd(t, repoDir, "remote", "add", "origin", bareDir)

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repoDir, "add", ".")
	gitCmd(t, repoDir, "commit", "-m", "initial commit")
	gitCmd(t, repoDir, "push", "-u", "origin", "main")

	return repoDir, homeDir
}

// runTreehouse runs the treehouse binary as a subprocess with the given args.
// HOME (or USERPROFILE on Windows) is set to homeDir so pool state is isolated.
func runTreehouse(t *testing.T, repoDir, homeDir string, extraEnv []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	return runTreehouseFromDir(t, repoDir, repoDir, homeDir, extraEnv, args...)
}

func runTreehouseFromDir(t *testing.T, repoDir, workDir, homeDir string, extraEnv []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmd := exec.Command(treehouseBin, args...)
	cmd.Dir = workDir
	cmd.Env = buildEnv(homeDir, extraEnv...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to execute treehouse %v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// buildEnv constructs an environment for a treehouse subprocess, overriding
// HOME/USERPROFILE to the test homeDir and suppressing update checks.
func buildEnv(homeDir string, extra ...string) []string {
	skip := map[string]bool{
		"HOME":        true,
		"USERPROFILE": true,
		"HOMEDRIVE":   true,
		"HOMEPATH":    true,
	}
	for _, kv := range extra {
		if k, _, ok := strings.Cut(kv, "="); ok {
			skip[strings.ToUpper(k)] = true
		}
	}

	var env []string
	for _, e := range os.Environ() {
		if k, _, ok := strings.Cut(e, "="); ok {
			if skip[strings.ToUpper(k)] {
				continue
			}
		}
		env = append(env, e)
	}

	if runtime.GOOS == "windows" {
		env = append(env, "USERPROFILE="+homeDir)
	} else {
		env = append(env, "HOME="+homeDir)
	}
	env = append(env, "TREEHOUSE_NO_UPDATE_CHECK=1")
	env = append(env, extra...)
	return env
}

// gitCmd runs a git command and returns trimmed stdout. Fails the test on error.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitCmdResult(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// extractWorktreePath parses the worktree path from "treehouse get" stderr.
// The output line looks like:
//
//	🌳 Entered worktree at ~/.treehouse/.../1/myrepo. Type 'exit' to return.
//
// The path is pretty-printed with ~ for the home directory, so we un-prettify
// it using homeDir.
func extractWorktreePath(stderr, homeDir string) string {
	const prefix = "Entered worktree at "
	idx := strings.Index(stderr, prefix)
	if idx == -1 {
		return ""
	}
	rest := stderr[idx+len(prefix):]
	endIdx := strings.Index(rest, ". Type")
	if endIdx == -1 {
		return ""
	}
	path := rest[:endIdx]
	if strings.HasPrefix(path, "~") {
		path = homeDir + path[1:]
	}
	return path
}

// --- Tests ---

func TestInit(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	_, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "init")
	if code != 0 {
		t.Fatalf("treehouse init failed (code %d): %s", code, stderr)
	}

	data, err := os.ReadFile(filepath.Join(repoDir, "treehouse.toml"))
	if err != nil {
		t.Fatalf("treehouse.toml not created: %v", err)
	}
	if !strings.Contains(string(data), "max_trees") {
		t.Errorf("treehouse.toml missing max_trees: %s", data)
	}
}

func TestInitAlreadyExists(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("max_trees = 8\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, code := runTreehouse(t, repoDir, homeDir, nil, "init")
	if code == 0 {
		t.Fatal("expected treehouse init to fail when treehouse.toml already exists")
	}
}

func TestStatusEmptyPool(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "status")
	if code != 0 {
		t.Fatalf("treehouse status failed (code %d): %s", code, stderr)
	}
	// Empty pool should print the "no worktrees" message, not any entries.
	if strings.Contains(stdout, "available") || strings.Contains(stdout, "in-use") {
		t.Errorf("expected empty status, got stdout: %s", stdout)
	}
}

func TestGetAndStatus(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	// Use exit-shell so the subshell exits immediately.
	env := []string{"SHELL=" + exitShellBin}
	_, getErr, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("treehouse get failed (code %d): %s", code, getErr)
	}

	if !strings.Contains(getErr, "Entered worktree at") {
		t.Errorf("expected 'Entered worktree at' in stderr: %s", getErr)
	}
	if !strings.Contains(getErr, "Worktree returned to pool") {
		t.Errorf("expected 'Worktree returned to pool' in stderr: %s", getErr)
	}

	wtPath := extractWorktreePath(getErr, homeDir)
	if wtPath == "" {
		t.Fatal("could not extract worktree path from stderr")
	}

	// Verify the worktree directory exists and has repo content.
	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Errorf("README.md not found in worktree %s: %v", wtPath, err)
	}

	// Verify status shows the worktree as available.
	statusOut, statusErr, code := runTreehouse(t, repoDir, homeDir, nil, "status")
	if code != 0 {
		t.Fatalf("treehouse status failed (code %d): %s", code, statusErr)
	}
	if !strings.Contains(statusOut, "available") {
		t.Errorf("expected 'available' in status output: %s", statusOut)
	}
}

func TestGetReusesWorktree(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	env := []string{"SHELL=" + exitShellBin}

	// First get: creates a new worktree, subshell exits, worktree returned.
	_, stderr1, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("first get failed (code %d): %s", code, stderr1)
	}
	path1 := extractWorktreePath(stderr1, homeDir)
	if path1 == "" {
		t.Fatal("could not extract first worktree path")
	}

	// Second get: should reuse the same (now available) worktree.
	_, stderr2, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("second get failed (code %d): %s", code, stderr2)
	}
	path2 := extractWorktreePath(stderr2, homeDir)
	if path2 == "" {
		t.Fatal("could not extract second worktree path")
	}

	if path1 != path2 {
		t.Errorf("expected worktree reuse, got different paths:\n  first:  %s\n  second: %s", path1, path2)
	}
}

func TestReturnFromInsideWorktreeDoesNotTerminateCaller(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	env := []string{"SHELL=" + exitShellBin}

	_, getErr, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("get failed (code %d): %s", code, getErr)
	}
	wtPath := extractWorktreePath(getErr, homeDir)
	if wtPath == "" {
		t.Fatal("could not extract worktree path")
	}

	_, returnErr, code := runTreehouseFromDir(t, repoDir, wtPath, homeDir, nil, "return", "--force")
	if code != 0 {
		t.Fatalf("return from inside worktree failed (code %d): %s", code, returnErr)
	}
	if !strings.Contains(returnErr, "Worktree returned to pool") {
		t.Fatalf("expected return confirmation, got: %s", returnErr)
	}
	if strings.Contains(returnErr, "Terminated lingering processes") && strings.Contains(returnErr, "treehouse") {
		t.Fatalf("return should not terminate its own process chain: %s", returnErr)
	}
}

func TestGetDetachesWorktreeWhenLeavingDirty(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	gitCmd(t, repoDir, "checkout", "-b", "feature")

	env := []string{"SHELL=" + dirtyMainShellBin}
	_, getErr, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("get failed (code %d): %s", code, getErr)
	}
	wtPath := extractWorktreePath(getErr, homeDir)
	if wtPath == "" {
		t.Fatal("could not extract worktree path")
	}
	if !strings.Contains(getErr, "Worktree left dirty") {
		t.Fatalf("expected get to leave dirty worktree for this regression, got: %s", getErr)
	}

	if branch, err := gitCmdResult(t, wtPath, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		t.Fatalf("expected worktree HEAD to be detached, got branch %q", branch)
	}
	if out, err := gitCmdResult(t, repoDir, "checkout", "main"); err != nil {
		t.Fatalf("expected main repo to checkout main after dirty worktree exit, got: %v\n%s", err, out)
	}
}

func TestReturnForceCleansAndDetachesCheckedOutBranch(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	gitCmd(t, repoDir, "checkout", "-b", "feature")

	env := []string{"SHELL=" + exitShellBin}
	_, getErr, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("get failed (code %d): %s", code, getErr)
	}
	wtPath := extractWorktreePath(getErr, homeDir)
	if wtPath == "" {
		t.Fatal("could not extract worktree path")
	}

	gitCmd(t, wtPath, "checkout", "main")
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, returnErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--force", wtPath)
	if code != 0 {
		t.Fatalf("return --force failed (code %d): %s", code, returnErr)
	}

	if branch, err := gitCmdResult(t, wtPath, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		t.Fatalf("expected returned worktree HEAD to be detached, got branch %q", branch)
	}
	if status := gitCmd(t, wtPath, "status", "--porcelain"); status != "" {
		t.Fatalf("expected return --force to clean tracked changes, got status:\n%s", status)
	}
	if out, err := gitCmdResult(t, repoDir, "checkout", "main"); err != nil {
		t.Fatalf("expected main repo to checkout main after return --force, got: %v\n%s", err, out)
	}
}

func TestDestroySpecific(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	env := []string{"SHELL=" + exitShellBin}

	_, getErr, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("get failed (code %d): %s", code, getErr)
	}
	wtPath := extractWorktreePath(getErr, homeDir)
	if wtPath == "" {
		t.Fatal("could not extract worktree path")
	}

	_, destroyErr, code := runTreehouse(t, repoDir, homeDir, nil, "destroy", "--force", wtPath)
	if code != 0 {
		t.Fatalf("destroy --force failed (code %d): %s", code, destroyErr)
	}

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree directory still exists after destroy: %s", wtPath)
	}

	// Status should show no worktrees.
	statusOut, _, _ := runTreehouse(t, repoDir, homeDir, nil, "status")
	if strings.Contains(statusOut, "available") {
		t.Errorf("expected empty status after destroy, got: %s", statusOut)
	}
}

func TestDestroyAll(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	env := []string{"SHELL=" + exitShellBin}

	// Create two worktrees by doing get→return twice with pool size > 1.
	// The second get reuses the first, so force a second by making the first
	// dirty between gets. Instead, just verify destroy --all works with one.
	_, getErr, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("get failed (code %d): %s", code, getErr)
	}
	wtPath := extractWorktreePath(getErr, homeDir)

	_, destroyErr, code := runTreehouse(t, repoDir, homeDir, nil, "destroy", "--all", "--force")
	if code != 0 {
		t.Fatalf("destroy --all --force failed (code %d): %s", code, destroyErr)
	}
	if !strings.Contains(destroyErr, "All worktrees destroyed") {
		t.Errorf("expected 'All worktrees destroyed' in stderr: %s", destroyErr)
	}

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree directory still exists after destroy --all: %s", wtPath)
	}
}

func TestDestroyNoArgs(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	_, _, code := runTreehouse(t, repoDir, homeDir, nil, "destroy")
	if code == 0 {
		t.Fatal("expected destroy with no args and no --all to fail")
	}
}
