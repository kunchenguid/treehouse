package vcs

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolateUserConfig points the user home at a fresh temp directory so backend
// selection never sees the developer's real ~/.config/treehouse/config.toml,
// and clears any ambient TREEHOUSE_VCS exported by the developer's shell.
// It returns the new home so tests can plant a user-level config there.
func isolateUserConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	t.Setenv("TREEHOUSE_VCS", "")
	return home
}

// writeVCSKey writes a config file at path containing only a vcs key.
func writeVCSKey(t *testing.T, path, vcs string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("vcs = \""+vcs+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func userConfigPath(home string) string {
	return filepath.Join(home, ".config", "treehouse", "config.toml")
}

// fakeJJOnlyRepo lays down a bare .jj marker directory. Backend selection is
// pure file inspection, so these tests need no jj binary.
func fakeJJOnlyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".jj", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
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

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

func TestBackendForGitRepo(t *testing.T) {
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "git", "init")
	if got := backendFor(dir).Name(); got != "git" {
		t.Fatalf("expected git backend, got %q", got)
	}
}

func TestBackendForJJOnlyRepoDefaultsToGitWithoutOptIn(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	isolateJJConfig(t)
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "jj", "git", "init")
	if got := backendFor(dir).Name(); got != "git" {
		t.Fatalf("expected git default for a .jj-only repo without opt-in, got %q", got)
	}
}

func TestBackendForJJOnlyRepoWithOptIn(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	isolateJJConfig(t)
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "jj", "git", "init")
	writeVCSKey(t, filepath.Join(dir, "treehouse.toml"), "jj")
	if got := backendFor(dir).Name(); got != "jj" {
		t.Fatalf("expected jj after explicit opt-in, got %q", got)
	}
}

func TestBackendForWorkspaceInheritsMainRootOptIn(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	isolateJJConfig(t)
	isolateUserConfig(t)
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wsDir := filepath.Join(base, "ws")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repoDir, "jj", "git", "init")
	mustRun(t, repoDir, "jj", "workspace", "add", "--name", "pool", wsDir)

	// Without the main root's opt-in, the workspace stays on the git default.
	if got := backendFor(wsDir).Name(); got != "git" {
		t.Fatalf("expected git default for a workspace without opt-in, got %q", got)
	}

	// The opt-in lives at the main repository root; the workspace cannot
	// carry an untracked treehouse.toml, so it must inherit through the
	// .jj/repo pointer.
	writeVCSKey(t, filepath.Join(repoDir, "treehouse.toml"), "jj")
	if got := backendFor(wsDir).Name(); got != "jj" {
		t.Fatalf("expected workspace to inherit the main root opt-in, got %q", got)
	}
}

func TestBackendForColocatedRepoDefaultsToGit(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	isolateJJConfig(t)
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "jj", "git", "init", "--colocate")
	if got := backendFor(dir).Name(); got != "git" {
		t.Fatalf("expected git backend for colocated repo without opt-in, got %q", got)
	}
}

func TestBackendForOutsideAnyRepoFallsBackToGit(t *testing.T) {
	isolateUserConfig(t)
	if got := backendFor(t.TempDir()).Name(); got != "git" {
		t.Fatalf("expected git fallback outside repositories, got %q", got)
	}
}

func TestBackendForEnvOverride(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	isolateJJConfig(t)
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "jj", "git", "init", "--colocate")

	t.Setenv("TREEHOUSE_VCS", "jj")
	if got := backendFor(dir).Name(); got != "jj" {
		t.Fatalf("expected TREEHOUSE_VCS=jj to opt the colocated repo in to jj, got %q", got)
	}
}

func TestBackendForConfigOverride(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	isolateJJConfig(t)
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "jj", "git", "init", "--colocate")
	writeVCSKey(t, filepath.Join(dir, "treehouse.toml"), "jj")
	if got := backendFor(dir).Name(); got != "jj" {
		t.Fatalf("expected vcs = jj in treehouse.toml to opt the colocated repo in to jj, got %q", got)
	}
}

func TestBackendForNestedPathFindsRootOptIn(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	isolateJJConfig(t)
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "jj", "git", "init")
	writeVCSKey(t, filepath.Join(dir, "treehouse.toml"), "jj")
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := backendFor(nested).Name(); got != "jj" {
		t.Fatalf("expected nested path to find the marker root's opt-in, got %q", got)
	}
}

func TestBackendForEnvOptInIgnoredInPlainGitRepo(t *testing.T) {
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "git", "init")

	t.Setenv("TREEHOUSE_VCS", "jj")
	if got := backendFor(dir).Name(); got != "git" {
		t.Fatalf("expected TREEHOUSE_VCS=jj to be ignored in a repo without .jj, got %q", got)
	}
}

func TestBackendForConfigOptInIgnoredInPlainGitRepo(t *testing.T) {
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "git", "init")

	writeVCSKey(t, filepath.Join(dir, "treehouse.toml"), "jj")
	if got := backendFor(dir).Name(); got != "git" {
		t.Fatalf("expected vcs = jj to be ignored in a repo without .jj, got %q", got)
	}
}

func TestBackendForUserConfigOptIn(t *testing.T) {
	home := isolateUserConfig(t)
	dir := fakeJJOnlyRepo(t)

	if got := backendFor(dir).Name(); got != "git" {
		t.Fatalf("expected git default before the user-level opt-in, got %q", got)
	}
	writeVCSKey(t, userConfigPath(home), "jj")
	if got := backendFor(dir).Name(); got != "jj" {
		t.Fatalf("expected user-level vcs = jj to opt the jj repo in, got %q", got)
	}
}

func TestBackendForUserConfigOptInIgnoredInPlainGitRepo(t *testing.T) {
	home := isolateUserConfig(t)
	writeVCSKey(t, userConfigPath(home), "jj")

	dir := t.TempDir()
	mustRun(t, dir, "git", "init")
	if got := backendFor(dir).Name(); got != "git" {
		t.Fatalf("expected user-level vcs = jj to be ignored in a repo without .jj, got %q", got)
	}
}

func TestBackendForRepoConfigBeatsUserConfig(t *testing.T) {
	home := isolateUserConfig(t)
	writeVCSKey(t, userConfigPath(home), "jj")

	dir := fakeJJOnlyRepo(t)
	writeVCSKey(t, filepath.Join(dir, "treehouse.toml"), "git")
	if got := backendFor(dir).Name(); got != "git" {
		t.Fatalf("expected repo-level vcs = git to beat the user-level opt-in, got %q", got)
	}
}

func TestBackendForEnvBeatsRepoConfig(t *testing.T) {
	isolateUserConfig(t)
	dir := fakeJJOnlyRepo(t)
	writeVCSKey(t, filepath.Join(dir, "treehouse.toml"), "git")

	t.Setenv("TREEHOUSE_VCS", "jj")
	if got := backendFor(dir).Name(); got != "jj" {
		t.Fatalf("expected TREEHOUSE_VCS=jj to beat the repo-level vcs = git, got %q", got)
	}
}

// TestRemoveWorktreeDispatchesOnSlotFlavorGitUnderJJOptIn covers the
// wrong-backend destroy path: a git worktree created before the repository
// opted in to jj must still be removed by git, deregistering it from
// .git/worktrees rather than deleting the directory out from under git.
func TestRemoveWorktreeDispatchesOnSlotFlavorGitUnderJJOptIn(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	isolateUserConfig(t)
	isolateJJConfig(t)
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	mustRun(t, base, "git", "init", "-b", "main", repoDir)
	mustRun(t, repoDir, "git", "-c", "user.name=t", "-c", "user.email=t@e.com",
		"commit", "--allow-empty", "-m", "init")
	wtPath := filepath.Join(base, "slot")
	mustRun(t, repoDir, "git", "worktree", "add", "--detach", wtPath)
	mustRun(t, repoDir, "jj", "git", "init", "--colocate")

	t.Setenv("TREEHOUSE_VCS", "jj")
	if err := RemoveWorktree(repoDir, wtPath); err != nil {
		t.Fatalf("removing a git slot under jj opt-in: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatal("expected the slot directory to be gone")
	}
	out, err := exec.Command("git", "-C", repoDir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), wtPath) {
		t.Fatalf("git worktree registration left stale after removal:\n%s", out)
	}
}

// TestRemoveWorktreeDispatchesOnSlotFlavorJJWithoutOptIn is the mirror: a jj
// workspace slot must still be removed by jj (forgetting its registration)
// even when the repository is no longer opted in.
func TestRemoveWorktreeDispatchesOnSlotFlavorJJWithoutOptIn(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	isolateUserConfig(t)
	isolateJJConfig(t)
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wsPath := filepath.Join(base, "slot")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repoDir, "jj", "git", "init")
	mustRun(t, repoDir, "jj", "bookmark", "create", "main", "-r", "@")
	// Create the slot the way the pool does: through the jj backend, under
	// an opt-in that is gone again by removal time.
	t.Setenv("TREEHOUSE_VCS", "jj")
	if err := AddWorktree(repoDir, wsPath, "main"); err != nil {
		t.Fatalf("creating the jj slot: %v", err)
	}
	t.Setenv("TREEHOUSE_VCS", "")

	if err := RemoveWorktree(repoDir, wsPath); err != nil {
		t.Fatalf("removing a jj slot without opt-in: %v", err)
	}
	if _, err := os.Stat(wsPath); !os.IsNotExist(err) {
		t.Fatal("expected the workspace directory to be gone")
	}
	out, err := exec.Command("jj", "-R", repoDir, "workspace", "list").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "th-") {
		t.Fatalf("jj workspace registration left stale after removal:\n%s", out)
	}
}

// TestWorktreeFactsUseSlotFlavor pins the deletion-safety contract behind
// destroy/prune: the facts that gate destruction (dirty, merged) must come
// from the slot's own VCS even when the repository's opt-in is gone. Before
// this dispatch existed, a .jj-only slot inspected without the opt-in fell
// back to git, which walked up and read the ENCLOSING repository's facts: in
// an in-project pool a clean enclosing repo made dirty jj work classify as
// clean and merged - i.e. disposable.
func TestWorktreeFactsUseSlotFlavor(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	isolateJJConfig(t)
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "-c", "user.name=t", "-c", "user.email=t@e.com", "commit", "-qm", "init")
	mustRun(t, dir, "jj", "git", "init", "--colocate")

	// The jj slot lives INSIDE the (clean, on-main) repository, as an
	// in-project pool root would place it, and is created under an opt-in
	// that is gone again by inspection time.
	slot := filepath.Join(dir, "pool", "1", "repo")
	if err := os.MkdirAll(filepath.Dir(slot), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TREEHOUSE_VCS", "jj")
	if err := AddWorktree(dir, slot, "main"); err != nil {
		t.Fatalf("creating the jj slot: %v", err)
	}
	t.Setenv("TREEHOUSE_VCS", "")
	if err := os.WriteFile(filepath.Join(slot, "wip.txt"), []byte("unlanded\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirty, err := IsDirty(slot)
	if err != nil {
		t.Fatalf("IsDirty on the jj slot without opt-in: %v", err)
	}
	if !dirty {
		t.Fatal("the slot's own jj facts say dirty; reading the enclosing repository said clean")
	}

	// The acquire-safety pair obeys the same contract: read through the
	// slot's own backend, never the enclosing repository's. Commit the
	// work in the slot: jj's ancestry check reads it as unmerged (unsafe),
	// while the enclosing git repository - clean and on main - would
	// report safe and hand acquire a green light to discard it.
	mustRun(t, slot, "jj", "commit", "-m", "unlanded work")
	safe, _, _, err := IsWorktreeSafeToReset(slot, "main")
	if err == nil && safe {
		t.Fatal("a jj slot with unmerged commits must not be safe to reset; the enclosing repository's git facts leaked through")
	}
	if err := ResetWorktreeToRef(slot, "main", "", true); err == nil {
		t.Fatal("ResetWorktreeToRef through the wrong backend must refuse, not reset the enclosing repository")
	}
	if got, want := readFileOrEmpty(t, filepath.Join(slot, "wip.txt")), "unlanded\n"; got != want {
		t.Fatalf("the slot's unlanded work must survive, got %q", got)
	}
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// TestUnrecognizedVCSValueWarnsOnceAndDefaults pins the misconfiguration
// contract: a value like "Jujutsu" is ignored (git default preserved) but a
// warning names the bad value and its source exactly once, on stderr only.
func TestUnrecognizedVCSValueWarnsOnceAndDefaults(t *testing.T) {
	isolateUserConfig(t)
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "treehouse.toml"), []byte("vcs = \"Jujutsu\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	first := backendFor(dir).Name()
	second := backendFor(dir).Name()
	w.Close()
	os.Stderr = oldStderr
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if first != "git" || second != "git" {
		t.Fatalf("unrecognized value must keep the git default, got %q/%q", first, second)
	}
	warning := string(out)
	if !strings.Contains(warning, "Jujutsu") || !strings.Contains(warning, "treehouse.toml") {
		t.Fatalf("warning must name the value and its source, got %q", warning)
	}
	if strings.Count(warning, "unrecognized vcs value") != 1 {
		t.Fatalf("warning must appear exactly once across repeated selection, got %q", warning)
	}
}

// TestDestructiveWrappersRefuseMarkerlessPath pins the defense-in-depth
// boundary: ResetWorktree, ResetWorktreeToRef, and DetachWorktree refuse a
// path holding no .git or .jj marker instead of dispatching through the
// configured backend, which inside a repository would rewrite the enclosing
// checkout.
func TestDestructiveWrappersRefuseMarkerlessPath(t *testing.T) {
	isolateUserConfig(t)
	repo := t.TempDir()
	mustRun(t, repo, "git", "init", "--initial-branch=main")
	mustRun(t, repo, "git", "config", "user.email", "test@test.com")
	mustRun(t, repo, "git", "config", "user.name", "Test")
	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "git", "add", ".")
	mustRun(t, repo, "git", "commit", "-m", "initial")
	if err := os.WriteFile(tracked, []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	slot := filepath.Join(repo, "pool", "1", "slot")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}

	calls := []struct {
		name string
		call func() error
	}{
		{"ResetWorktree", func() error { return ResetWorktree(slot, "main") }},
		{"ResetWorktreeWithSeededPaths", func() error { return ResetWorktreeWithSeededPaths(slot, "main", nil) }},
		{"ResetWorktreeToRef", func() error { return ResetWorktreeToRef(slot, "main", "", true) }},
		{"ResetWorktreeToRefWithSeededPaths", func() error {
			return ResetWorktreeToRefWithSeededPaths(slot, "main", "", true, nil)
		}},
		{"DetachWorktree", func() error { return DetachWorktree(slot) }},
	}
	for _, c := range calls {
		err := c.call()
		if err == nil {
			t.Fatalf("%s must refuse a markerless path", c.name)
		}
		if !strings.Contains(err.Error(), "no .git or .jj marker") {
			t.Fatalf("%s error should name the missing marker, got %v", c.name, err)
		}
	}

	data, err := os.ReadFile(tracked)
	if err != nil || string(data) != "uncommitted\n" {
		t.Fatalf("enclosing repository's uncommitted change must survive: %q, %v", data, err)
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if ref := strings.TrimSpace(string(out)); ref != "main" {
		t.Fatalf("enclosing repository HEAD moved off main to %q", ref)
	}
}
