package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunchenguid/treehouse/internal/pool"
)

func TestWorkspaceProfileLeaseAndReturn(t *testing.T) {
	repoOne, homeDir := setupTestRepo(t)
	repoTwo := setupTestRepoWithHome(t, homeDir, "second-repo")
	configDir := filepath.Join(homeDir, ".config", "treehouse")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "[workspace.profiles.default]\nrepositories = [\"" + repoOne + "\", \"" + repoTwo + "\"]\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")

	stdout, stderr, code := runTreehouse(t, repoOne, homeDir, nil, "workspace", "get", "--lease", "--json", "--no-fetch", "--workspace-root", workspaceRoot)
	if code != 0 {
		t.Fatalf("workspace get: %s", stderr)
	}
	var state workspaceState
	if err := json.Unmarshal([]byte(stdout), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Repositories) != 2 {
		t.Fatalf("repositories = %d, want 2", len(state.Repositories))
	}
	for _, repo := range state.Repositories {
		info, err := os.Lstat(filepath.Join(state.Path, repo.Name))
		if err != nil {
			t.Fatalf("missing facade link for %s: %v", repo.Name, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("facade %s is not a symlink", repo.Name)
		}
	}

	// Return a child independently. Aggregate cleanup must safely forget this
	// stale lease only after verifying that its slot is available.
	child := state.Repositories[0]
	_, stderr, code = runTreehouse(t, child.SourceRoot, homeDir, nil, "return", "--force", child.Path)
	if code != 0 {
		t.Fatalf("individual return: %s", stderr)
	}
	_, stderr, code = runTreehouse(t, repoOne, homeDir, nil, "workspace", "return", "--force", state.Path)
	if code != 0 {
		t.Fatalf("workspace return: %s", stderr)
	}
	if _, err := os.Stat(state.Path); !os.IsNotExist(err) {
		t.Fatalf("workspace remains: %v", err)
	}
}

func TestWorkspaceShellExitReturnsRepositoriesAddedDuringSession(t *testing.T) {
	repoOne, homeDir := setupTestRepo(t)
	repoTwo := setupTestRepoWithHome(t, homeDir, "second-repo")
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")

	stdout, stderr, code := runTreehouse(t, repoOne, homeDir, nil, "workspace", "get", "--lease", "--json", "--no-fetch", "--workspace-root", workspaceRoot, repoOne)
	if code != 0 {
		t.Fatalf("workspace get: %s", stderr)
	}
	var original workspaceState
	if err := json.Unmarshal([]byte(stdout), &original); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runTreehouse(t, repoOne, homeDir, nil, "workspace", "modify", "--no-fetch", "--add", repoTwo, original.Path)
	if code != 0 {
		t.Fatalf("workspace modify: %s", stderr)
	}
	current, err := readWorkspaceState(original.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Repositories) != 2 || len(original.Repositories) != 1 {
		t.Fatalf("session state was not modified as expected: original=%+v current=%+v", original.Repositories, current.Repositories)
	}

	if err := finishWorkspaceShell(original.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(original.Path); !os.IsNotExist(err) {
		t.Fatalf("workspace remains after shell exit: %v", err)
	}
	for _, child := range current.Repositories {
		state, err := pool.ReadState(child.PoolDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Worktrees) != 1 || state.Worktrees[0].Leased {
			t.Fatalf("repository %s still leased after shell exit: %+v", child.Name, state.Worktrees)
		}
	}
}

func TestWorkspaceReturnPreservesFilesOutsideManagedLinks(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	stdout, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "get", "--lease", "--json", "--no-fetch", "--workspace-root", workspaceRoot, repo)
	if code != 0 {
		t.Fatalf("workspace get: %s", stderr)
	}
	var state workspaceState
	if err := json.Unmarshal([]byte(stdout), &state); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(state.Path, "notes.txt")
	if err := os.WriteFile(note, []byte("keep this"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runTreehouse(t, repo, homeDir, nil, "workspace", "return", "--force", state.Path)
	if code == 0 {
		t.Fatal("return silently removed files created in the workspace")
	}
	if content, err := os.ReadFile(note); err != nil || string(content) != "keep this" {
		t.Fatalf("workspace file was removed: %q, %v; stderr=%s", content, err, stderr)
	}
}

func TestWorkspaceModifyAddsAndRemovesRepositories(t *testing.T) {
	repoOne, homeDir := setupTestRepo(t)
	repoTwo := setupTestRepoWithHome(t, homeDir, "second-repo")
	repoThree := setupTestRepoWithHome(t, homeDir, "third-repo")
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")

	stdout, stderr, code := runTreehouse(t, repoOne, homeDir, nil, "workspace", "get", "--lease", "--json", "--no-fetch", "--workspace-root", workspaceRoot, repoOne, repoTwo)
	if code != 0 {
		t.Fatalf("workspace get: %s", stderr)
	}
	var initial workspaceState
	if err := json.Unmarshal([]byte(stdout), &initial); err != nil {
		t.Fatal(err)
	}

	_, stderr, code = runTreehouse(t, repoOne, homeDir, nil, "workspace", "modify", "--no-fetch", "--add", repoThree, "--remove", filepath.Base(repoOne), initial.Path)
	if code != 0 {
		t.Fatalf("workspace modify: %s", stderr)
	}
	modified, err := readWorkspaceState(initial.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(modified.Repositories) != 2 {
		t.Fatalf("repositories = %d, want 2", len(modified.Repositories))
	}
	got := map[string]bool{}
	for _, repo := range modified.Repositories {
		got[repo.Name] = true
	}
	if got[filepath.Base(repoOne)] || !got[filepath.Base(repoTwo)] || !got[filepath.Base(repoThree)] {
		t.Fatalf("modified repositories = %v", got)
	}
	if _, err := os.Lstat(filepath.Join(initial.Path, filepath.Base(repoOne))); !os.IsNotExist(err) {
		t.Fatalf("removed repository link remains: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(initial.Path, filepath.Base(repoThree))); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("added repository link is not a symlink: info=%v err=%v", info, err)
	}

	_, stderr, code = runTreehouse(t, repoOne, homeDir, nil, "workspace", "return", "--force", initial.Path)
	if code != 0 {
		t.Fatalf("workspace return: %s", stderr)
	}
}

func TestRemoveWorkspaceRepositoryCanRetryAfterStateWriteFailure(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	stdout, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "get", "--lease", "--json", "--no-fetch", "--workspace-root", workspaceRoot, repo)
	if code != 0 {
		t.Fatalf("workspace get: %s", stderr)
	}
	var state workspaceState
	if err := json.Unmarshal([]byte(stdout), &state); err != nil {
		t.Fatal(err)
	}

	tmpStatePath := workspaceStatePath(state.Path) + ".tmp"
	if err := os.Mkdir(tmpStatePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeWorkspaceRepository(&state, 0, true); err == nil {
		t.Fatal("remove unexpectedly succeeded while workspace state was unwritable")
	}
	if len(state.Repositories) != 1 {
		t.Fatalf("in-memory state lost repository after failed write: %+v", state.Repositories)
	}
	persisted, err := readWorkspaceState(state.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Repositories) != 1 {
		t.Fatalf("persisted state lost repository after failed write: %+v", persisted.Repositories)
	}

	if err := os.Remove(tmpStatePath); err != nil {
		t.Fatal(err)
	}
	if err := removeWorkspaceRepository(&state, 0, true); err != nil {
		t.Fatalf("retry remove: %v", err)
	}
	if len(state.Repositories) != 0 {
		t.Fatalf("repository remains after retry: %+v", state.Repositories)
	}
	if err := os.RemoveAll(state.Path); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceModifyRejectsUnknownRemovalWithoutChangingState(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	stdout, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "get", "--lease", "--json", "--no-fetch", "--workspace-root", workspaceRoot, repo)
	if code != 0 {
		t.Fatalf("workspace get: %s", stderr)
	}
	var initial workspaceState
	if err := json.Unmarshal([]byte(stdout), &initial); err != nil {
		t.Fatal(err)
	}

	_, _, code = runTreehouse(t, repo, homeDir, nil, "workspace", "modify", "--remove", "missing", initial.Path)
	if code == 0 {
		t.Fatal("workspace modify unexpectedly accepted an unknown repository")
	}
	state, err := readWorkspaceState(initial.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Repositories) != 1 || state.Repositories[0].LeaseID != initial.Repositories[0].LeaseID {
		t.Fatalf("workspace changed after rejected modify: %+v", state.Repositories)
	}

	_, stderr, code = runTreehouse(t, repo, homeDir, nil, "workspace", "return", "--force", initial.Path)
	if code != 0 {
		t.Fatalf("workspace return: %s", stderr)
	}
}

func TestResolveWorktreePathResolvesFacadeLink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "pool", "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "workspace", "repo")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, err := resolveWorktreePath([]string{link})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved path = %s, want %s", got, want)
	}
}
