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

func setupTestRepoWithHome(t *testing.T, homeDir, repoName string) string {
	t.Helper()

	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}

	bareDir := filepath.Join(base, repoName+"-remote.git")
	repoDir := filepath.Join(base, repoName)

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

	return repoDir
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
		"HOME":          true,
		"USERPROFILE":   true,
		"HOMEDRIVE":     true,
		"HOMEPATH":      true,
		"TREEHOUSE_DIR": true,
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

func containsRawGitFailure(output string) bool {
	return strings.Contains(output, "fatal:") ||
		strings.Contains(output, "not a git repository") ||
		strings.Contains(output, "Could not read from remote repository") ||
		strings.Contains(output, "does not appear to be a git repository")
}

func setupMixedStaleAndOrphanedWorktrees(t *testing.T) (repoDir, homeDir, stalePath, orphanPath string) {
	t.Helper()

	repoDir, homeDir = setupTestRepo(t)
	env := []string{"SHELL=" + exitShellBin}

	_, getErr, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("first get failed (code %d): %s", code, getErr)
	}
	stalePath = extractWorktreePath(getErr, homeDir)
	if stalePath == "" {
		t.Fatal("could not extract first worktree path")
	}

	dirtyPath := filepath.Join(stalePath, "dirty.txt")
	if err := os.WriteFile(dirtyPath, []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, getErr, code = runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("second get failed (code %d): %s", code, getErr)
	}
	orphanPath = extractWorktreePath(getErr, homeDir)
	if orphanPath == "" {
		t.Fatal("could not extract second worktree path")
	}
	if orphanPath == stalePath {
		t.Fatalf("expected dirty first worktree to force a second worktree, got %s", orphanPath)
	}

	if err := os.Remove(dirtyPath); err != nil {
		t.Fatal(err)
	}
	removeWorktreeBackingGitDir(t, orphanPath)

	return repoDir, homeDir, stalePath, orphanPath
}

func removeWorktreeBackingGitDir(t *testing.T, wtPath string) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(wtPath, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		t.Fatalf("expected linked worktree .git file, got %q", data)
	}
	gitDir = filepath.FromSlash(strings.TrimSpace(gitDir))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(wtPath, gitDir)
	}
	if err := os.RemoveAll(gitDir); err != nil {
		t.Fatal(err)
	}
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

func TestGetLeasePrintsOnlyPathToStdout(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("treehouse get --lease failed (code %d): %s", code, stderr)
	}

	// stdout must be exactly the worktree path on a single line, so scripts can
	// do path=$(treehouse get --lease).
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one stdout line, got %d:\n%q", len(lines), stdout)
	}
	wtPath := lines[0]
	if !filepath.IsAbs(wtPath) {
		t.Fatalf("expected an absolute path on stdout, got %q", wtPath)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Fatalf("expected leased worktree to contain repo content: %v", err)
	}

	// Human-facing banners go to stderr only.
	if strings.Contains(stdout, "🌳") || strings.Contains(stdout, "Leased worktree") {
		t.Fatalf("stdout must contain only the path, got:\n%q", stdout)
	}
	if !strings.Contains(stderr, "Leased worktree at") {
		t.Fatalf("expected lease banner on stderr, got:\n%s", stderr)
	}

	// status reports the durable lease as a distinct state.
	statusOut, statusErr, code := runTreehouse(t, repoDir, homeDir, nil, "status")
	if code != 0 {
		t.Fatalf("status failed (code %d): %s", code, statusErr)
	}
	if !strings.Contains(statusOut, "leased") {
		t.Fatalf("expected status to show leased state, got:\n%s", statusOut)
	}
}

func TestGetLeaseRecordsHolder(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	_, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--lease-holder", "secondmate-home")
	if code != 0 {
		t.Fatalf("treehouse get --lease failed (code %d): %s", code, stderr)
	}

	statusOut, statusErr, code := runTreehouse(t, repoDir, homeDir, nil, "status")
	if code != 0 {
		t.Fatalf("status failed (code %d): %s", code, statusErr)
	}
	if !strings.Contains(statusOut, "leased") || !strings.Contains(statusOut, "secondmate-home") {
		t.Fatalf("expected status to show lease holder, got:\n%s", statusOut)
	}
}

func TestLeasedWorktreeSkippedByGetAndPrune(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	leaseOut, leaseErr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, leaseErr)
	}
	leasedPath := strings.TrimSpace(leaseOut)
	if leasedPath == "" {
		t.Fatal("could not capture leased worktree path")
	}

	// A later interactive get must not hand out the leased worktree.
	env := []string{"SHELL=" + exitShellBin}
	_, getErr, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("get failed (code %d): %s", code, getErr)
	}
	otherPath := extractWorktreePath(getErr, homeDir)
	if otherPath == "" {
		t.Fatal("could not extract second worktree path")
	}
	if otherPath == leasedPath {
		t.Fatalf("get handed out the leased worktree %s", leasedPath)
	}

	// prune must never remove the leased worktree, even with no process inside.
	pruneOut, pruneErr, code := runTreehouse(t, repoDir, homeDir, nil, "prune", "--yes")
	if code != 0 {
		t.Fatalf("prune --yes failed (code %d): %s", code, pruneErr)
	}
	prettyLeased := "~" + leasedPath[len(homeDir):]
	if strings.Contains(pruneOut, prettyLeased) {
		t.Fatalf("prune listed the leased worktree %s:\n%s", prettyLeased, pruneOut)
	}
	if _, err := os.Stat(leasedPath); err != nil {
		t.Fatalf("prune removed leased worktree %s: %v", leasedPath, err)
	}
}

func TestReturnReleasesLease(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	leaseOut, leaseErr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, leaseErr)
	}
	leasedPath := strings.TrimSpace(leaseOut)
	if leasedPath == "" {
		t.Fatal("could not capture leased worktree path")
	}

	_, returnErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", leasedPath)
	if code != 0 {
		t.Fatalf("return failed (code %d): %s", code, returnErr)
	}
	if !strings.Contains(returnErr, "Worktree returned to pool") {
		t.Fatalf("expected return confirmation, got: %s", returnErr)
	}

	// Status no longer reports the worktree as leased.
	statusOut, statusErr, code := runTreehouse(t, repoDir, homeDir, nil, "status")
	if code != 0 {
		t.Fatalf("status failed (code %d): %s", code, statusErr)
	}
	if strings.Contains(statusOut, "leased") {
		t.Fatalf("expected lease to be released, got status:\n%s", statusOut)
	}
	if !strings.Contains(statusOut, "available") {
		t.Fatalf("expected released worktree to be available, got status:\n%s", statusOut)
	}

	// The released worktree is reusable by a normal get.
	env := []string{"SHELL=" + exitShellBin}
	_, getErr, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("get after release failed (code %d): %s", code, getErr)
	}
	reusedPath := extractWorktreePath(getErr, homeDir)
	if reusedPath != leasedPath {
		t.Fatalf("expected released worktree %s to be reused, got %s", leasedPath, reusedPath)
	}
}

func TestReturnExplicitPathFromOutsideRepoReleasesLease(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	leaseOut, leaseErr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, leaseErr)
	}
	leasedPath := strings.TrimSpace(leaseOut)
	if leasedPath == "" {
		t.Fatal("could not capture leased worktree path")
	}

	outsideDir := t.TempDir()
	_, returnErr, code := runTreehouseFromDir(t, repoDir, outsideDir, homeDir, nil, "return", leasedPath)
	if code != 0 {
		t.Fatalf("return from outside repo failed (code %d): %s", code, returnErr)
	}
	if !strings.Contains(returnErr, "Worktree returned to pool") {
		t.Fatalf("expected return confirmation, got: %s", returnErr)
	}

	statusOut, statusErr, code := runTreehouse(t, repoDir, homeDir, nil, "status")
	if code != 0 {
		t.Fatalf("status failed (code %d): %s", code, statusErr)
	}
	if strings.Contains(statusOut, "leased") || strings.Contains(statusOut, "in-use") {
		t.Fatalf("expected status to show lease released, got:\n%s", statusOut)
	}
	if !strings.Contains(statusOut, "available") {
		t.Fatalf("expected returned worktree to be available, got:\n%s", statusOut)
	}
}

func TestReturnExplicitPathFromLinkedWorktreePool(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("root = \"../treehouse-pool\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repoDir, "add", "treehouse.toml")
	gitCmd(t, repoDir, "commit", "-m", "configure treehouse root")

	linkedDir := filepath.Join(filepath.Dir(repoDir), "agent-home")
	gitCmd(t, repoDir, "worktree", "add", "-b", "agent-home", linkedDir, "main")

	leaseOut, leaseErr, code := runTreehouseFromDir(t, repoDir, linkedDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease from linked worktree failed (code %d): %s", code, leaseErr)
	}
	leasedPath := strings.TrimSpace(leaseOut)
	if leasedPath == "" {
		t.Fatal("could not capture leased worktree path")
	}

	outsideDir := t.TempDir()
	_, returnErr, code := runTreehouseFromDir(t, repoDir, outsideDir, homeDir, nil, "return", leasedPath)
	if code != 0 {
		t.Fatalf("return from outside repo failed (code %d): %s", code, returnErr)
	}
	if !strings.Contains(returnErr, "Worktree returned to pool") {
		t.Fatalf("expected return confirmation, got: %s", returnErr)
	}

	statusOut, statusErr, code := runTreehouseFromDir(t, repoDir, linkedDir, homeDir, nil, "status")
	if code != 0 {
		t.Fatalf("status failed (code %d): %s", code, statusErr)
	}
	if strings.Contains(statusOut, "leased") || strings.Contains(statusOut, "in-use") {
		t.Fatalf("expected status to show lease released, got:\n%s", statusOut)
	}
	if !strings.Contains(statusOut, "available") {
		t.Fatalf("expected returned worktree to be available, got:\n%s", statusOut)
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

func TestReturnForceCleansConflictedWorktree(t *testing.T) {
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

	gitCmd(t, repoDir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("main change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repoDir, "commit", "-am", "change main")
	gitCmd(t, repoDir, "push", "origin", "main")

	gitCmd(t, wtPath, "checkout", "-b", "conflict")
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("worktree change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, wtPath, "commit", "-am", "change worktree")
	if out, err := gitCmdResult(t, wtPath, "merge", "origin/main"); err == nil {
		t.Fatalf("expected merge conflict, got success:\n%s", out)
	}

	_, returnErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--force", wtPath)
	if code != 0 {
		t.Fatalf("return --force failed (code %d): %s", code, returnErr)
	}

	if branch, err := gitCmdResult(t, wtPath, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		t.Fatalf("expected returned worktree HEAD to be detached, got branch %q", branch)
	}
	if status := gitCmd(t, wtPath, "status", "--porcelain"); status != "" {
		t.Fatalf("expected return --force to clean conflicted worktree, got status:\n%s", status)
	}
	if out, err := gitCmdResult(t, repoDir, "checkout", "main"); err != nil {
		t.Fatalf("expected main repo to checkout main after return --force, got: %v\n%s", err, out)
	}
}

func TestDestroyDryRunByDefault(t *testing.T) {
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

	out, errOut, code := runTreehouse(t, repoDir, homeDir, nil, "destroy", wtPath)
	if code != 0 {
		t.Fatalf("destroy dry run failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "Dry run") || !strings.Contains(out, "would destroy 1 worktree") {
		t.Fatalf("expected dry-run preview, got stdout:\n%s", out)
	}
	if !strings.Contains(out, "[disposable]") {
		t.Fatalf("expected [disposable] status tag, got stdout:\n%s", out)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("dry run removed worktree %s: %v", wtPath, err)
	}
}

func TestDestroySpecificWithYes(t *testing.T) {
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

	out, errOut, code := runTreehouse(t, repoDir, homeDir, nil, "destroy", wtPath, "--yes")
	if code != 0 {
		t.Fatalf("destroy --yes failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "Destroyed 1 worktree") {
		t.Fatalf("expected destroyed summary, got stdout:\n%s", out)
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

func TestDestroySpecificSkipsWhenCallerStillInWorktree(t *testing.T) {
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

	out, errOut, code := runTreehouseFromDir(t, repoDir, wtPath, homeDir, nil, "destroy", wtPath, "--include-in-use", "--yes")
	if code == 0 {
		t.Fatal("expected destroy from inside the target worktree to fail")
	}
	if !strings.Contains(out, "Skipped 1 worktree") || !strings.Contains(out+errOut, "worktree processes still running after termination") {
		t.Fatalf("expected survivor-process skip, got stdout:\n%s\nstderr:\n%s", out, errOut)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected in-use worktree to remain on disk: %v", err)
	}
}

func TestDestroyDirtyRequiresIncludeUnlanded(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(wtPath, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errOut, code := runTreehouse(t, repoDir, homeDir, nil, "destroy", wtPath, "--yes")
	if code == 0 {
		t.Fatalf("expected destroy of a dirty worktree without --include-unlanded to fail")
	}
	if !strings.Contains(out, "[dirty]") {
		t.Fatalf("expected [dirty] tag in preview, got stdout:\n%s", out)
	}
	if !strings.Contains(out+errOut, "--include-unlanded") {
		t.Fatalf("expected --include-unlanded guidance, got:\n%s\n%s", out, errOut)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected dirty worktree to remain on disk: %v", err)
	}

	out, errOut, code = runTreehouse(t, repoDir, homeDir, nil, "destroy", wtPath, "--include-unlanded", "--yes")
	if code != 0 {
		t.Fatalf("destroy --include-unlanded --yes failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "Destroyed 1 worktree") {
		t.Fatalf("expected destroyed summary, got stdout:\n%s", out)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("dirty worktree still exists after --include-unlanded --yes: %s", wtPath)
	}
}

func TestDestroyAllRemovesPoolAndIsScopedToIt(t *testing.T) {
	repoA, homeDir := setupTestRepo(t)
	repoB := setupTestRepoWithHome(t, homeDir, "otherrepo")
	env := []string{"SHELL=" + exitShellBin}

	_, getErrA, code := runTreehouse(t, repoA, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("get in repoA failed (code %d): %s", code, getErrA)
	}
	wtA := extractWorktreePath(getErrA, homeDir)

	_, getErrB, code := runTreehouse(t, repoB, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("get in repoB failed (code %d): %s", code, getErrB)
	}
	wtB := extractWorktreePath(getErrB, homeDir)
	if wtA == "" || wtB == "" {
		t.Fatalf("could not extract worktree paths: A=%q B=%q", wtA, wtB)
	}

	out, errOut, code := runTreehouse(t, repoA, homeDir, nil, "destroy", repoA, "--all", "--yes")
	if code != 0 {
		t.Fatalf("destroy --all --yes failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "Destroyed 1 worktree") {
		t.Fatalf("expected destroyed summary, got stdout:\n%s", out)
	}
	if strings.Contains(out, "All worktrees destroyed") {
		t.Fatalf("destroy must never print a blind 'All worktrees destroyed', got:\n%s", out)
	}
	if _, err := os.Stat(wtA); !os.IsNotExist(err) {
		t.Errorf("repoA worktree still exists after destroy --all: %s", wtA)
	}
	if _, err := os.Stat(wtB); err != nil {
		t.Errorf("repoB worktree must NOT be touched by repoA's destroy --all: %v", err)
	}
}

func TestDestroyAllFromManagedWorktreeSubdirUsesMainRepoPool(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	env := []string{"SHELL=" + exitShellBin}

	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("root = \"../treehouse-pool\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repoDir, "add", "treehouse.toml")
	gitCmd(t, repoDir, "commit", "-m", "configure treehouse root")
	gitCmd(t, repoDir, "push", "origin", "main")

	leaseOut, leaseErr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, leaseErr)
	}
	leasedPath := strings.TrimSpace(leaseOut)
	if leasedPath == "" {
		t.Fatal("could not capture leased worktree path")
	}

	_, getErr, code := runTreehouse(t, repoDir, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("get failed (code %d): %s", code, getErr)
	}
	disposablePath := extractWorktreePath(getErr, homeDir)
	if disposablePath == "" {
		t.Fatal("could not extract disposable worktree path")
	}

	subdir := filepath.Join(leasedPath, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runTreehouseFromDir(t, repoDir, subdir, homeDir, nil, "destroy", ".", "--all", "--yes")
	if code != 0 {
		t.Fatalf("destroy . --all --yes failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "Destroyed 1 worktree") {
		t.Fatalf("expected disposable worktree destroyed from managed subdir, got stdout:\n%s", out)
	}
	if _, err := os.Stat(disposablePath); !os.IsNotExist(err) {
		t.Fatalf("expected disposable worktree removed, got err %v", err)
	}
	if _, err := os.Stat(leasedPath); err != nil {
		t.Fatalf("expected leased worktree preserved: %v", err)
	}
}

func TestDestroyAllRequiresPoolTarget(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	_, errOut, code := runTreehouse(t, repoDir, homeDir, nil, "destroy", "--all", "--yes")
	if code == 0 {
		t.Fatal("expected destroy --all without a pool path to fail")
	}
	if !strings.Contains(errOut, "requires a pool path") {
		t.Fatalf("expected pool-path guidance, got stderr:\n%s", errOut)
	}
}

func TestDestroyAllNeverRemovesLeasedWorktree(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	out, errOut, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, errOut)
	}
	wtPath := strings.TrimSpace(out)
	if wtPath == "" {
		t.Fatal("get --lease printed no path")
	}

	// Even with --yes, a bulk destroy must never remove the leased home.
	out, errOut, code = runTreehouse(t, repoDir, homeDir, nil, "destroy", repoDir, "--all", "--yes")
	if code != 0 {
		t.Fatalf("destroy --all --yes failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "[leased]") || !strings.Contains(out, "Skipped 1 worktree") {
		t.Fatalf("expected leased worktree reported as skipped, got stdout:\n%s", out)
	}
	if strings.Contains(out, "Destroyed 1 worktree") {
		t.Fatalf("leased worktree must not be destroyed by --all, got stdout:\n%s", out)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected leased worktree to remain on disk: %v", err)
	}

	// --include-leased may not be combined with --all.
	_, errOut, code = runTreehouse(t, repoDir, homeDir, nil, "destroy", repoDir, "--all", "--include-leased", "--yes")
	if code == 0 {
		t.Fatal("expected --all --include-leased to be rejected")
	}
	if !strings.Contains(errOut, "cannot be combined with --all") {
		t.Fatalf("expected rejection message, got stderr:\n%s", errOut)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected leased worktree to remain on disk after rejected command: %v", err)
	}
}

func TestDestroyLeasedSinglePathWithIncludeLeased(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	out, errOut, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, errOut)
	}
	wtPath := strings.TrimSpace(out)
	if wtPath == "" {
		t.Fatal("get --lease printed no path")
	}

	out, errOut, code = runTreehouse(t, repoDir, homeDir, nil, "destroy", wtPath, "--include-leased", "--yes")
	if code != 0 {
		t.Fatalf("destroy <leased> --include-leased --yes failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "Destroyed 1 worktree") {
		t.Fatalf("expected destroyed summary, got stdout:\n%s", out)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("leased worktree still exists after --include-leased --yes: %s", wtPath)
	}
}

func TestDestroyNoArgs(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	_, _, code := runTreehouse(t, repoDir, homeDir, nil, "destroy")
	if code == 0 {
		t.Fatal("expected destroy with no args and no --all to fail")
	}
}

func TestPruneDryRunAndYes(t *testing.T) {
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

	pruneOut, pruneErr, code := runTreehouse(t, repoDir, homeDir, nil, "prune")
	if code != 0 {
		t.Fatalf("prune dry run failed (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "Dry run") || !strings.Contains(pruneOut, "would prune 1 stale worktree") {
		t.Fatalf("expected dry-run prune summary, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	prettyWtPath := "~" + wtPath[len(homeDir):]
	if !strings.Contains(pruneOut, prettyWtPath) {
		t.Fatalf("expected dry run to list %s, got:\n%s", prettyWtPath, pruneOut)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("dry run removed worktree %s: %v", wtPath, err)
	}

	pruneOut, pruneErr, code = runTreehouse(t, repoDir, homeDir, nil, "prune", "--yes")
	if code != 0 {
		t.Fatalf("prune --yes failed (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "Pruned 1 stale worktree") || !strings.Contains(pruneOut, "freed") {
		t.Fatalf("expected prune --yes summary, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree to be removed after prune --yes, stat err: %v", err)
	}
}

func TestPruneAllDryRunAndYesAcrossPoolsFromAnywhere(t *testing.T) {
	repoA, homeDir := setupTestRepo(t)
	repoB := setupTestRepoWithHome(t, homeDir, "otherrepo")
	env := []string{"SHELL=" + exitShellBin}

	_, getErrA, code := runTreehouse(t, repoA, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("repo A get failed (code %d): %s", code, getErrA)
	}
	wtPathA := extractWorktreePath(getErrA, homeDir)
	if wtPathA == "" {
		t.Fatal("could not extract repo A worktree path")
	}

	_, getErrB, code := runTreehouse(t, repoB, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("repo B get failed (code %d): %s", code, getErrB)
	}
	wtPathB := extractWorktreePath(getErrB, homeDir)
	if wtPathB == "" {
		t.Fatal("could not extract repo B worktree path")
	}

	outsideDir := t.TempDir()
	pruneOut, pruneErr, code := runTreehouseFromDir(t, repoA, outsideDir, homeDir, nil, "prune", "--all")
	if code != 0 {
		t.Fatalf("prune --all dry run failed from outside a repo (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "would prune 2 stale worktrees across 2 pools") {
		t.Fatalf("expected global dry-run summary, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	for _, wtPath := range []string{wtPathA, wtPathB} {
		prettyWtPath := "~" + wtPath[len(homeDir):]
		if !strings.Contains(pruneOut, prettyWtPath) {
			t.Fatalf("expected dry run to list %s, got:\n%s", prettyWtPath, pruneOut)
		}
		if _, err := os.Stat(wtPath); err != nil {
			t.Fatalf("dry run removed worktree %s: %v", wtPath, err)
		}
	}

	aliasOut, aliasErr, code := runTreehouseFromDir(t, repoA, outsideDir, homeDir, nil, "prune", "--global")
	if code != 0 {
		t.Fatalf("prune --global dry run failed from outside a repo (code %d): %s", code, aliasErr)
	}
	if !strings.Contains(aliasOut, "would prune 2 stale worktrees across 2 pools") {
		t.Fatalf("expected --global alias to match --all, got stdout:\n%s\nstderr:\n%s", aliasOut, aliasErr)
	}

	pruneOut, pruneErr, code = runTreehouseFromDir(t, repoA, outsideDir, homeDir, nil, "prune", "--all", "--yes")
	if code != 0 {
		t.Fatalf("prune --all --yes failed from outside a repo (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "Pruned 2 stale worktrees across 2 pools") || !strings.Contains(pruneOut, "freed") {
		t.Fatalf("expected global prune --yes summary, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	for _, wtPath := range []string{wtPathA, wtPathB} {
		if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
			t.Fatalf("expected worktree to be removed after prune --all --yes, stat err: %v", err)
		}
	}
}

func TestPruneMixedStaleAndSkippedOrphanPrintsOrphanHints(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		fromOutside  bool
		removesStale bool
		wants        []string
	}{
		{
			name: "repo dry run",
			args: []string{"prune"},
			wants: []string{
				"would prune 1 stale worktree",
				"orphaned (backing repository missing)",
				"Re-run with --yes to delete these worktrees.",
				"Re-run with --prune-orphans to include true orphans in the dry run; add --yes to delete them.",
			},
		},
		{
			name:         "repo yes",
			args:         []string{"prune", "--yes"},
			removesStale: true,
			wants: []string{
				"Pruned 1 stale worktree",
				"orphaned (backing repository missing)",
				"Re-run with --prune-orphans --yes to delete true orphans whose backing repository is missing.",
			},
		},
		{
			name:        "global dry run",
			args:        []string{"prune", "--all"},
			fromOutside: true,
			wants: []string{
				"would prune 1 stale worktree across 1 pool",
				"orphaned (backing repository missing)",
				"Re-run with --all --yes to delete these worktrees.",
				"Re-run with --all --prune-orphans to include true orphans in the dry run; add --yes to delete them.",
			},
		},
		{
			name:         "global yes",
			args:         []string{"prune", "--all", "--yes"},
			fromOutside:  true,
			removesStale: true,
			wants: []string{
				"Pruned 1 stale worktree across 1 pool",
				"orphaned (backing repository missing)",
				"Re-run with --all --prune-orphans --yes to delete true orphans whose backing repository is missing.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir, homeDir, stalePath, orphanPath := setupMixedStaleAndOrphanedWorktrees(t)
			workDir := repoDir
			if tt.fromOutside {
				workDir = t.TempDir()
			}

			pruneOut, pruneErr, code := runTreehouseFromDir(t, repoDir, workDir, homeDir, nil, tt.args...)
			if code != 0 {
				t.Fatalf("%s failed (code %d): %s", strings.Join(tt.args, " "), code, pruneErr)
			}
			for _, want := range tt.wants {
				if !strings.Contains(pruneOut, want) {
					t.Fatalf("expected %q in stdout:\n%s\nstderr:\n%s", want, pruneOut, pruneErr)
				}
			}

			if tt.removesStale {
				if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
					t.Fatalf("expected stale worktree to be removed, stat err: %v", err)
				}
			} else if _, err := os.Stat(stalePath); err != nil {
				t.Fatalf("dry run removed worktree %s: %v", stalePath, err)
			}
			if _, err := os.Stat(orphanPath); err != nil {
				t.Fatalf("%s removed orphan %s: %v", strings.Join(tt.args, " "), orphanPath, err)
			}
		})
	}
}

func TestPruneAllReportsOrphanWithoutRawGitErrorsAndPrunesOnlyWithExplicitFlag(t *testing.T) {
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
	if err := os.RemoveAll(repoDir); err != nil {
		t.Fatalf("RemoveAll repo failed: %v", err)
	}

	outsideDir := t.TempDir()
	pruneOut, pruneErr, code := runTreehouseFromDir(t, repoDir, outsideDir, homeDir, nil, "prune", "--all")
	if code != 0 {
		t.Fatalf("prune --all failed on orphan (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "orphaned (backing repository missing)") || !strings.Contains(pruneOut, "content could not be verified") {
		t.Fatalf("expected clean orphan skip, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if containsRawGitFailure(pruneOut) || containsRawGitFailure(pruneErr) {
		t.Fatalf("default orphan output leaked raw git failure, stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("plain prune removed orphan %s: %v", wtPath, err)
	}

	pruneOut, pruneErr, code = runTreehouseFromDir(t, repoDir, outsideDir, homeDir, nil, "prune", "--all", "--prune-orphans")
	if code != 0 {
		t.Fatalf("prune --all --prune-orphans failed on orphan dry run (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "would prune 1 stale/orphaned worktree") || !strings.Contains(pruneOut, "content could not be verified") {
		t.Fatalf("expected orphan dry-run candidate, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("orphan dry run removed %s: %v", wtPath, err)
	}

	pruneOut, pruneErr, code = runTreehouseFromDir(t, repoDir, outsideDir, homeDir, nil, "prune", "--all", "--prune-orphans", "--yes")
	if code != 0 {
		t.Fatalf("prune --all --prune-orphans --yes failed (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "Pruned 1 stale/orphaned worktree") || !strings.Contains(pruneOut, "content could not be verified") {
		t.Fatalf("expected orphan prune summary, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("expected orphan to be removed after explicit prune, stat err: %v", err)
	}
}

func TestPruneAllDoesNotDeleteOriginUnreachableWithPruneOrphans(t *testing.T) {
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
	remoteDir := filepath.Join(filepath.Dir(repoDir), "remote.git")
	if err := os.RemoveAll(remoteDir); err != nil {
		t.Fatalf("RemoveAll remote failed: %v", err)
	}

	outsideDir := t.TempDir()
	pruneOut, pruneErr, code := runTreehouseFromDir(t, repoDir, outsideDir, homeDir, nil, "prune", "--all", "--prune-orphans", "--yes")
	if code != 0 {
		t.Fatalf("prune --all --prune-orphans --yes failed with unreachable origin (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "origin unreachable (cannot verify)") {
		t.Fatalf("expected origin-unreachable skip, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if containsRawGitFailure(pruneOut) || containsRawGitFailure(pruneErr) {
		t.Fatalf("default origin-unreachable output leaked raw git failure, stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("origin-unreachable worktree was removed: %v", err)
	}

	verboseOut, verboseErr, code := runTreehouseFromDir(t, repoDir, outsideDir, homeDir, nil, "prune", "--all", "--verbose")
	if code != 0 {
		t.Fatalf("prune --all --verbose failed with unreachable origin (code %d): %s", code, verboseErr)
	}
	if !strings.Contains(verboseOut, "detail: refresh origin before prune") {
		t.Fatalf("expected verbose origin diagnostic detail, got stdout:\n%s\nstderr:\n%s", verboseOut, verboseErr)
	}
}

// TestPruneAllYesRecoversCorruptPoolWithoutDeletingItsWorktree covers the
// treehouse-state-atomicity-b4 incident: a corrupt/truncated state file in one
// pool (e.g. from a crash mid-write) must not brick `prune --all` for every
// other pool, and the corrupt pool's own on-disk worktree must never be
// silently deleted since its real reservation state is unknown. ReadState
// recovers it as a leased worktree, which prune treats like any other
// leased worktree: skipped, silently, pending human verification via
// `treehouse status`.
func TestPruneAllYesRecoversCorruptPoolWithoutDeletingItsWorktree(t *testing.T) {
	repoA, homeDir := setupTestRepo(t)
	repoB := setupTestRepoWithHome(t, homeDir, "zzrepo")
	env := []string{"SHELL=" + exitShellBin}

	_, getErrA, code := runTreehouse(t, repoA, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("repo A get failed (code %d): %s", code, getErrA)
	}
	wtPathA := extractWorktreePath(getErrA, homeDir)
	if wtPathA == "" {
		t.Fatal("could not extract repo A worktree path")
	}

	_, getErrB, code := runTreehouse(t, repoB, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("repo B get failed (code %d): %s", code, getErrB)
	}
	wtPathB := extractWorktreePath(getErrB, homeDir)
	if wtPathB == "" {
		t.Fatal("could not extract repo B worktree path")
	}

	poolDirB := filepath.Dir(filepath.Dir(wtPathB))
	if err := os.WriteFile(filepath.Join(poolDirB, "treehouse-state.json"), []byte("{"), 0o644); err != nil {
		t.Fatalf("corrupt state failed: %v", err)
	}

	outsideDir := t.TempDir()
	_, pruneErr, code := runTreehouseFromDir(t, repoA, outsideDir, homeDir, nil, "prune", "--all", "--yes")
	if code != 0 {
		t.Fatalf("prune --all --yes should recover from the corrupt pool rather than fail (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneErr, "corrupt or truncated") {
		t.Fatalf("expected a recovery warning for the corrupt pool, got stderr:\n%s", pruneErr)
	}
	if _, err := os.Stat(wtPathA); !os.IsNotExist(err) {
		t.Fatalf("expected repo A's stale worktree to be pruned normally, stat err: %v", err)
	}
	if _, err := os.Stat(wtPathB); err != nil {
		t.Fatalf("expected repo B's worktree to remain (its pool's state was corrupt): %v", err)
	}
}

func TestPruneWithoutAllScopesToCurrentRepo(t *testing.T) {
	repoA, homeDir := setupTestRepo(t)
	repoB := setupTestRepoWithHome(t, homeDir, "otherrepo")
	env := []string{"SHELL=" + exitShellBin}

	_, getErrA, code := runTreehouse(t, repoA, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("repo A get failed (code %d): %s", code, getErrA)
	}
	wtPathA := extractWorktreePath(getErrA, homeDir)
	if wtPathA == "" {
		t.Fatal("could not extract repo A worktree path")
	}

	_, getErrB, code := runTreehouse(t, repoB, homeDir, env, "get")
	if code != 0 {
		t.Fatalf("repo B get failed (code %d): %s", code, getErrB)
	}
	wtPathB := extractWorktreePath(getErrB, homeDir)
	if wtPathB == "" {
		t.Fatal("could not extract repo B worktree path")
	}

	pruneOut, pruneErr, code := runTreehouse(t, repoA, homeDir, nil, "prune", "--yes")
	if code != 0 {
		t.Fatalf("repo-scoped prune --yes failed (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "Pruned 1 stale worktree") {
		t.Fatalf("expected repo-scoped prune summary, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	prettyWtPathB := "~" + wtPathB[len(homeDir):]
	if strings.Contains(pruneOut, prettyWtPathB) {
		t.Fatalf("repo-scoped prune listed other repo worktree %s:\n%s", prettyWtPathB, pruneOut)
	}
	if _, err := os.Stat(wtPathA); !os.IsNotExist(err) {
		t.Fatalf("expected current repo worktree to be removed, stat err: %v", err)
	}
	if _, err := os.Stat(wtPathB); err != nil {
		t.Fatalf("expected other repo worktree to remain: %v", err)
	}
}

func TestPruneRejectsPositionalArgs(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	_, pruneErr, code := runTreehouse(t, repoDir, homeDir, nil, "prune", "/some/path", "--yes")
	if code == 0 {
		t.Fatal("expected prune with positional arg to fail")
	}
	if !strings.Contains(pruneErr, `unknown command "/some/path" for "treehouse prune"`) {
		t.Fatalf("expected positional arg error, got stderr:\n%s", pruneErr)
	}
}

func TestPruneEmptyPoolDoesNotRequireOrigin(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	remoteDir := filepath.Join(filepath.Dir(repoDir), "remote.git")
	if err := os.RemoveAll(remoteDir); err != nil {
		t.Fatal(err)
	}

	pruneOut, pruneErr, code := runTreehouse(t, repoDir, homeDir, nil, "prune")
	if code != 0 {
		t.Fatalf("prune dry run failed on empty pool with offline origin (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "No stale worktrees to prune") {
		t.Fatalf("expected empty prune output, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
}

func TestPruneSkipsUnsafeWorktrees(t *testing.T) {
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

	gitCmd(t, wtPath, "config", "status.showUntrackedFiles", "no")
	if err := os.WriteFile(filepath.Join(wtPath, "uncommitted.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pruneOut, pruneErr, code := runTreehouse(t, repoDir, homeDir, nil, "prune", "--yes")
	if code != 0 {
		t.Fatalf("prune --yes failed on dirty worktree (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "uncommitted changes") {
		t.Fatalf("expected dirty worktree skip, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("dirty worktree was removed: %v", err)
	}

	gitCmd(t, wtPath, "clean", "-fd")
	gitCmd(t, wtPath, "checkout", "-b", "unmerged-work")
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("unmerged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, wtPath, "commit", "-am", "unmerged work")

	pruneOut, pruneErr, code = runTreehouse(t, repoDir, homeDir, nil, "prune", "--yes")
	if code != 0 {
		t.Fatalf("prune --yes failed on unmerged worktree (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "not merged") {
		t.Fatalf("expected unmerged worktree skip, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("unmerged worktree was removed: %v", err)
	}
}

func TestPruneRefreshesOriginBeforeMergeSafety(t *testing.T) {
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

	base := filepath.Dir(repoDir)
	rewriteDir := filepath.Join(base, "rewriter")
	gitCmd(t, "", "clone", filepath.Join(base, "remote.git"), rewriteDir)
	gitCmd(t, rewriteDir, "config", "user.email", "test@test.com")
	gitCmd(t, rewriteDir, "config", "user.name", "Test")
	gitCmd(t, rewriteDir, "checkout", "--orphan", "replacement")
	gitCmd(t, rewriteDir, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(rewriteDir, "README.md"), []byte("replacement\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, rewriteDir, "add", ".")
	gitCmd(t, rewriteDir, "commit", "-m", "replacement")
	gitCmd(t, rewriteDir, "push", "--force", "origin", "replacement:main")

	pruneOut, pruneErr, code := runTreehouse(t, repoDir, homeDir, nil, "prune", "--yes")
	if code != 0 {
		t.Fatalf("prune --yes failed after remote rewrite (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "not merged") {
		t.Fatalf("expected stale local origin to be refreshed, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree with remotely unmerged HEAD was removed: %v", err)
	}
}

func TestPruneUsesCurrentRemoteDefaultBranch(t *testing.T) {
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

	base := filepath.Dir(repoDir)
	remoteDir := filepath.Join(base, "remote.git")
	rewriteDir := filepath.Join(base, "default-renamer")
	gitCmd(t, "", "clone", remoteDir, rewriteDir)
	gitCmd(t, rewriteDir, "config", "user.email", "test@test.com")
	gitCmd(t, rewriteDir, "config", "user.name", "Test")
	gitCmd(t, rewriteDir, "checkout", "--orphan", "trunk")
	gitCmd(t, rewriteDir, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(rewriteDir, "README.md"), []byte("new default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, rewriteDir, "add", ".")
	gitCmd(t, rewriteDir, "commit", "-m", "new default")
	gitCmd(t, rewriteDir, "push", "origin", "trunk")
	gitCmd(t, remoteDir, "symbolic-ref", "HEAD", "refs/heads/trunk")

	pruneOut, pruneErr, code := runTreehouse(t, repoDir, homeDir, nil, "prune", "--yes")
	if code != 0 {
		t.Fatalf("prune --yes failed after remote default rename (code %d): %s", code, pruneErr)
	}
	if !strings.Contains(pruneOut, "not merged") || !strings.Contains(pruneOut, "origin/trunk") {
		t.Fatalf("expected prune to use current remote default, got stdout:\n%s\nstderr:\n%s", pruneOut, pruneErr)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree unmerged into current remote default was removed: %v", err)
	}
}
