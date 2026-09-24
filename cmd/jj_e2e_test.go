package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireJJ skips the test when jj is not installed so the jj e2e suite is
// opt-in by environment, exactly like the backend it exercises.
func requireJJ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
}

// isolateJJConfig points JJ_CONFIG at a minimal config so tests are hermetic
// against the developer's own jj configuration (e.g. git.colocate = true
// would silently colocate every repo these tests create).
func isolateJJConfig(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "jjconfig.toml")
	contents := "[user]\nname = \"Treehouse Tests\"\nemail = \"treehouse-tests@example.com\"\n\n[git]\n# jj colocates new repos by default; tests opt out so \"jj-only\" fixtures\n# really are jj-only, and colocated fixtures say --colocate explicitly.\ncolocate = false\n"
	if err := os.WriteFile(cfg, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JJ_CONFIG", cfg)
}

func jjCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("jj", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupJJTestRepo creates a bare git origin with one commit on main and a
// colocated jj clone of it, mirroring setupTestRepo for the jj backend. The
// clone opts in to the jj backend via treehouse.toml, per the scoping ruling
// that git stays the default in colocated repositories: the opt-in path is
// exactly what these tests exercise.
func setupJJTestRepo(t *testing.T) (repoDir, homeDir string) {
	t.Helper()
	isolateJJConfig(t)

	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}

	homeDir = filepath.Join(base, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	bareDir := filepath.Join(base, "remote.git")
	seedDir := filepath.Join(base, "seed")
	repoDir = filepath.Join(base, "myrepo")

	gitCmd(t, "", "init", "--bare", "--initial-branch=main", bareDir)
	gitCmd(t, "", "clone", bareDir, seedDir)
	gitCmd(t, seedDir, "config", "user.email", "test@test.com")
	gitCmd(t, seedDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, seedDir, "add", ".")
	gitCmd(t, seedDir, "commit", "-m", "initial commit")
	gitCmd(t, seedDir, "push", "origin", "HEAD:main")

	jjCmd(t, "", "git", "clone", "--colocate", bareDir, repoDir)
	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("vcs = \"jj\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repoDir, homeDir
}

// setupColocatedRepoWithoutOptIn builds the colocated layout the way existing
// treehouse users reach it: a normal git clone that later ran
// `jj git init --colocate`. Git HEAD stays on main and origin/HEAD is set, so
// the git backend keeps working exactly as it did before jj arrived. (A
// jj-first colocated clone leaves git HEAD detached with no origin/HEAD; the
// git backend cannot derive a default branch there, with or without this
// change.)
func setupColocatedRepoWithoutOptIn(t *testing.T) (repoDir, homeDir string) {
	t.Helper()
	repoDir, homeDir = setupTestRepo(t)
	jjCmd(t, repoDir, "git", "init", "--colocate")
	return repoDir, homeDir
}

func TestJJColocatedWithoutOptInKeepsGitWorktrees(t *testing.T) {
	requireJJ(t)
	repoDir, homeDir := setupColocatedRepoWithoutOptIn(t)

	stdout, stderr, exitCode := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if exitCode != 0 {
		t.Fatalf("get --lease failed (exit %d): %s", exitCode, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	if _, err := os.Stat(filepath.Join(wtPath, ".git")); err != nil {
		t.Fatalf("expected a git worktree by default in a colocated repo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, ".jj")); !os.IsNotExist(err) {
		t.Fatal("expected no .jj in the default git worktree")
	}
}

func TestJJLeaseLifecycleAndReuse(t *testing.T) {
	requireJJ(t)
	repoDir, homeDir := setupJJTestRepo(t)

	// Lease a worktree: must be a jj workspace, not a git worktree.
	stdout, stderr, exitCode := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--json")
	if exitCode != 0 {
		t.Fatalf("get --lease failed (exit %d): %s", exitCode, stderr)
	}
	var lease leaseJSONResult
	if err := json.Unmarshal([]byte(stdout), &lease); err != nil {
		t.Fatalf("failed to parse lease JSON: %v\nstdout: %s", err, stdout)
	}

	if _, err := os.Stat(filepath.Join(lease.Path, ".jj")); err != nil {
		t.Fatalf("expected leased worktree to be a jj workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lease.Path, ".git")); !os.IsNotExist(err) {
		t.Fatal("expected jj workspace without a .git entry once the repo opts in to jj")
	}
	if _, err := os.Stat(filepath.Join(lease.Path, "README.md")); err != nil {
		t.Fatalf("expected checked-out file in workspace: %v", err)
	}
	if !strings.Contains(jjCmd(t, repoDir, "workspace", "list"), "th-") {
		t.Fatal("expected jj workspace list to include the treehouse workspace")
	}

	// The leased workspace must be reported as leased.
	stdout, stderr, exitCode = runTreehouse(t, repoDir, homeDir, nil, "status", "--json")
	if exitCode != 0 {
		t.Fatalf("status --json failed (exit %d): %s", exitCode, stderr)
	}
	var statuses []statusJSONResult
	if err := json.Unmarshal([]byte(stdout), &statuses); err != nil {
		t.Fatalf("failed to parse status JSON: %v\nstdout: %s", err, stdout)
	}
	if len(statuses) != 1 || statuses[0].Path != lease.Path || statuses[0].Status != "leased" {
		t.Fatalf("expected one leased worktree at %s, got %+v", lease.Path, statuses)
	}

	// Dirty the workspace, then force-return it: the reset must discard the
	// local change and hand the worktree back to the pool.
	if err := os.WriteFile(filepath.Join(lease.Path, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, exitCode = runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path, "--force")
	if exitCode != 0 {
		t.Fatalf("return --force failed (exit %d): %s", exitCode, stderr)
	}
	if _, err := os.Stat(filepath.Join(lease.Path, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatal("expected scratch file to be cleaned by the return reset")
	}

	// A second lease must reuse the returned workspace instead of growing the
	// pool: reuse is the point of pooling.
	stdout, stderr, exitCode = runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if exitCode != 0 {
		t.Fatalf("second get --lease failed (exit %d): %s", exitCode, stderr)
	}
	if reused := strings.TrimSpace(stdout); reused != lease.Path {
		t.Fatalf("expected pool reuse of %s, got %s", lease.Path, reused)
	}
}

func TestJJDirtyWorktreeIsNotHandedOut(t *testing.T) {
	requireJJ(t)
	repoDir, homeDir := setupJJTestRepo(t)

	stdout, stderr, exitCode := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if exitCode != 0 {
		t.Fatalf("get --lease failed (exit %d): %s", exitCode, stderr)
	}
	first := strings.TrimSpace(stdout)

	// Commit work in the workspace, then return it while the commit is not
	// merged anywhere. The working copy itself is clean afterwards, so the
	// worktree returns to the pool and the committed work stays in the repo.
	if err := os.WriteFile(filepath.Join(first, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jjCmd(t, first, "commit", "-m", "pooled work")
	_, stderr, exitCode = runTreehouse(t, repoDir, homeDir, nil, "return", first, "--force")
	if exitCode != 0 {
		t.Fatalf("return failed (exit %d): %s", exitCode, stderr)
	}

	// The committed change must still exist in the repository even though the
	// workspace was reset: jj abandons only the working-copy commit.
	if !strings.Contains(jjCmd(t, repoDir, "log", "-r", "all()", "--no-graph", "-T", `description ++ "\n"`), "pooled work") {
		t.Fatal("expected committed pooled work to survive the worktree reset")
	}
}

// TestJJEnvOnlyOptInSlotCanBeReturned pins the release contract: a jj slot
// acquired under an env-only opt-in (TREEHOUSE_VCS=jj, no config key) must
// still be returnable once the variable is gone, because every step of the
// release path - root discovery, default branch, reset - dispatches on the
// slot's own flavor.
func TestJJEnvOnlyOptInSlotCanBeReturned(t *testing.T) {
	requireJJ(t)
	repoDir, homeDir := setupColocatedRepoWithoutOptIn(t)

	stdout, stderr, exitCode := runTreehouse(t, repoDir, homeDir, []string{"TREEHOUSE_VCS=jj"}, "get", "--lease")
	if exitCode != 0 {
		t.Fatalf("get --lease under TREEHOUSE_VCS=jj: exit %d: %s", exitCode, stderr)
	}
	slot := strings.TrimSpace(stdout)
	if _, err := os.Stat(filepath.Join(slot, ".jj")); err != nil {
		t.Fatalf("expected a jj workspace under the env opt-in: %v", err)
	}

	// The env var is gone and no config names jj: the slot's own marker must
	// still route the release through the jj backend.
	_, stderr, exitCode = runTreehouse(t, repoDir, homeDir, nil, "return", slot)
	if exitCode != 0 {
		t.Fatalf("return without the opt-in visible: exit %d: %s", exitCode, stderr)
	}
}

// TestDestroyMigratesOldFlavorSlots pins the migration story: a clean, merged
// git slot left over from before a jj opt-in verifies through its own
// backend's ref vocabulary, so a bare 'destroy --all --yes' removes it -
// landed work never needs --include-unlanded.
func TestDestroyMigratesOldFlavorSlots(t *testing.T) {
	requireJJ(t)
	repoDir, homeDir := setupColocatedRepoWithoutOptIn(t)

	stdout, stderr, exitCode := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if exitCode != 0 {
		t.Fatalf("get --lease (git): exit %d: %s", exitCode, stderr)
	}
	gitSlot := strings.TrimSpace(stdout)
	if _, _, exitCode = runTreehouse(t, repoDir, homeDir, nil, "return", gitSlot); exitCode != 0 {
		t.Fatal("returning the git slot failed")
	}

	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("vcs = \"jj\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exitCode = runTreehouse(t, repoDir, homeDir, nil, "destroy", ".", "--all", "--yes")
	if exitCode != 0 {
		t.Fatalf("destroy --all --yes: exit %d: %s", exitCode, stderr)
	}
	if !strings.Contains(stdout, "Destroyed 1") {
		t.Fatalf("expected the old git slot to be destroyed for migration, got: %s", stdout)
	}
	if _, err := os.Stat(gitSlot); !os.IsNotExist(err) {
		t.Fatalf("expected the git slot gone from disk, got err %v", err)
	}
}

// TestAcquireIsFlavorAware pins the acquire contract after an opt-in change:
// a repository freshly opted in to jj must not be handed its old git slot -
// a worktree where jj commands do not work - and the old slot must survive
// untouched for the documented destroy-and-reacquire migration.
func TestAcquireIsFlavorAware(t *testing.T) {
	requireJJ(t)
	repoDir, homeDir := setupColocatedRepoWithoutOptIn(t)

	// Day 1: no opt-in - a git slot is created, used, and returned.
	stdout, stderr, exitCode := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if exitCode != 0 {
		t.Fatalf("get --lease (git): exit %d: %s", exitCode, stderr)
	}
	gitSlot := strings.TrimSpace(stdout)
	if _, err := os.Stat(filepath.Join(gitSlot, ".git")); err != nil {
		t.Fatalf("expected a git worktree on day 1: %v", err)
	}
	if _, _, exitCode = runTreehouse(t, repoDir, homeDir, nil, "return", gitSlot); exitCode != 0 {
		t.Fatal("returning the git slot failed")
	}

	// Day 2: the repository opts in to jj. The same command must skip the
	// available git slot and hand out a new jj workspace.
	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("vcs = \"jj\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exitCode = runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if exitCode != 0 {
		t.Fatalf("get --lease (jj): exit %d: %s", exitCode, stderr)
	}
	jjSlot := strings.TrimSpace(stdout)
	if jjSlot == gitSlot {
		t.Fatal("acquire handed the git slot to a jj-opted caller")
	}
	if _, err := os.Stat(filepath.Join(jjSlot, ".jj")); err != nil {
		t.Fatalf("expected a jj workspace on day 2: %v", err)
	}

	// The old git slot is intact and visible: status names its flavor.
	if _, err := os.Stat(filepath.Join(gitSlot, ".git")); err != nil {
		t.Fatalf("the git slot must survive untouched for migration: %v", err)
	}
	stdout, _, exitCode = runTreehouse(t, repoDir, homeDir, nil, "status", "--json")
	if exitCode != 0 {
		t.Fatal("status --json failed")
	}
	if !strings.Contains(stdout, `"flavor":"git"`) || !strings.Contains(stdout, `"flavor":"jj"`) {
		t.Fatalf("status --json must name both flavors, got: %s", stdout)
	}
}

func TestJJGetBranchFailsBeforeCreatingWorkspace(t *testing.T) {
	requireJJ(t)
	repoDir, homeDir := setupJJTestRepo(t)

	excludeBefore := readExcludeFile(t, repoDir)
	selfIgnore := filepath.Join(homeDir, ".treehouse", ".gitignore")

	stdout, stderr, exitCode := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--branch", "feature")
	if exitCode == 0 {
		t.Fatalf("jj get --branch unexpectedly succeeded: %s", stdout)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("failed jj acquire wrote machine stdout %q", stdout)
	}
	if !strings.Contains(stderr, "only supported by the git backend") {
		t.Fatalf("expected actionable Git-only error, got %q", stderr)
	}
	if excludeAfter := readExcludeFile(t, repoDir); excludeAfter != excludeBefore {
		t.Fatalf("rejected --branch changed .git/info/exclude: before %q, after %q", excludeBefore, excludeAfter)
	}
	if _, err := os.Stat(selfIgnore); !os.IsNotExist(err) {
		t.Fatalf("rejected --branch created the pool self-ignore file, stat err: %v", err)
	}
	status, statusErr, statusCode := runTreehouse(t, repoDir, homeDir, nil, "status", "--json")
	if statusCode != 0 {
		t.Fatalf("status failed after rejected get (code %d): %s", statusCode, statusErr)
	}
	if strings.TrimSpace(status) != "[]" {
		t.Fatalf("rejected --branch created a pool slot: %s", status)
	}
}
