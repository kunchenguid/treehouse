package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/pool"
)

// INV-1 (return_cmd.go): workspace return must not discard unsupported flags.
// INV-2 (workspace.go): shell return defers directory removal; lost state fails closed.
// INV-3 (workspace.go): facade cleanup must never remove an unrelated entry.

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

func TestWorkspaceBareReturnRejectsIncompatibleFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag []string
	}{
		{name: "all", flag: []string{"--all"}},
		{name: "lease ID", flag: []string{"--if-lease-id", "wrong-lease"}},
		{name: "lease holder", flag: []string{"--if-lease-holder", "other-holder"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, homeDir := setupTestRepo(t)
			workspace := createLeasedWorkspace(t, repo, homeDir)
			args := append([]string{"return"}, tc.flag...)
			_, stderr, code := runTreehouse(t, repo, homeDir, []string{"TREEHOUSE_WORKSPACE=" + workspace.Path}, args...)
			if code == 0 {
				t.Fatalf("return silently ignored %v: %s", tc.flag, stderr)
			}
			if _, err := readWorkspaceState(workspace.Path); err != nil {
				t.Fatalf("refused return destroyed workspace: %v", err)
			}
			state, err := pool.ReadState(workspace.Repositories[0].PoolDir)
			if err != nil || len(state.Worktrees) != 1 || !state.Worktrees[0].Leased {
				t.Fatalf("refused return released child: %+v, %v", state.Worktrees, err)
			}
		})
	}
}

func TestWorkspaceShellExitAfterExplicitReturn(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspace := createLeasedWorkspace(t, repo, homeDir)
	t.Setenv("TREEHOUSE_WORKSPACE", workspace.Path)
	t.Setenv("TREEHOUSE_WORKSPACE_ID", workspace.ID)
	if err := returnWorkspace(workspace.Path, true); err != nil {
		t.Fatal(err)
	}
	returned, err := readWorkspaceState(workspace.Path)
	if err != nil || len(returned.Repositories) != 0 {
		t.Fatalf("shell return removed its state before exit: %+v, %v", returned.Repositories, err)
	}
	if err := finishWorkspaceShell(workspace.Path); err != nil {
		t.Fatalf("shell exit failed after an explicit return: %v", err)
	}
	if _, err := os.Lstat(workspace.Path); !os.IsNotExist(err) {
		t.Fatalf("workspace remains after shell exit: %v", err)
	}
}

func TestWorkspaceShellExitAfterExplicitReturnWithAddedChild(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	other := setupTestRepoWithHome(t, homeDir, "second-repo")
	workspace := createLeasedWorkspace(t, repo, homeDir)
	_, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "modify", "--add", other, "--no-fetch", workspace.Path)
	if code != 0 {
		t.Fatalf("workspace modify: %s", stderr)
	}
	current, err := readWorkspaceState(workspace.Path)
	if err != nil || len(current.Repositories) != 2 {
		t.Fatalf("modified workspace: %+v, %v", current.Repositories, err)
	}
	t.Setenv("TREEHOUSE_WORKSPACE", workspace.Path)
	t.Setenv("TREEHOUSE_WORKSPACE_ID", workspace.ID)
	if err := returnWorkspace(workspace.Path, true); err != nil {
		t.Fatal(err)
	}
	if err := finishWorkspaceShell(workspace.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(workspace.Path); !os.IsNotExist(err) {
		t.Fatalf("workspace remains after shell exit: %v", err)
	}
	for _, child := range current.Repositories {
		state, err := pool.ReadState(child.PoolDir)
		if err != nil || len(state.Worktrees) != 1 || state.Worktrees[0].Leased {
			t.Fatalf("child %s still leased: %+v, %v", child.Name, state.Worktrees, err)
		}
	}
}

func TestWorkspaceShellExitRejectsMissingStateInExistingDirectory(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspace := createLeasedWorkspace(t, repo, homeDir)
	if err := os.Remove(workspaceStatePath(workspace.Path)); err != nil {
		t.Fatal(err)
	}
	if err := finishWorkspaceShell(workspace.Path); err == nil {
		t.Fatal("shell exit silently accepted missing state while children are leased")
	}
}

func TestWorkspaceShellExitRejectsMissingDirectoryWithLiveLease(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspace := createLeasedWorkspace(t, repo, homeDir)
	if err := os.Remove(filepath.Join(workspace.Path, workspace.Repositories[0].Name)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(workspaceStatePath(workspace.Path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(workspace.Path); err != nil {
		t.Fatal(err)
	}
	if err := finishWorkspaceShell(workspace.Path); err == nil {
		t.Fatal("shell exit silently accepted a lost workspace with a live lease")
	}
}

func TestWorkspaceShellExitRejectsLostAddedLease(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	other := setupTestRepoWithHome(t, homeDir, "second-repo")
	workspace := createLeasedWorkspace(t, repo, homeDir)
	_, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "modify", "--add", other, "--no-fetch", workspace.Path)
	if code != 0 {
		t.Fatalf("workspace modify: %s", stderr)
	}
	current, err := readWorkspaceState(workspace.Path)
	if err != nil || len(current.Repositories) != 2 {
		t.Fatalf("modified workspace: %+v, %v", current.Repositories, err)
	}
	initial := workspace.Repositories[0]
	_, stderr, code = runTreehouse(t, repo, homeDir, nil, "return", "--force", initial.Path)
	if code != 0 {
		t.Fatalf("return original child: %s", stderr)
	}
	for _, child := range current.Repositories {
		if err := os.Remove(filepath.Join(workspace.Path, child.Name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(workspaceStatePath(workspace.Path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(workspace.Path); err != nil {
		t.Fatal(err)
	}
	if err := finishWorkspaceShell(workspace.Path); err == nil {
		t.Fatal("shell exit accepted a lost added lease")
	}
	state, err := pool.ReadState(current.Repositories[1].PoolDir)
	if err != nil || len(state.Worktrees) != 1 || !state.Worktrees[0].Leased {
		t.Fatalf("added child unexpectedly released: %+v, %v", state.Worktrees, err)
	}
}

func TestWorkspaceModifyDoesNotRemoveExistingFileOnAddFailure(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	other := setupTestRepoWithHome(t, homeDir, "second-repo")
	workspace := createLeasedWorkspace(t, repo, homeDir)
	obstruction := filepath.Join(workspace.Path, filepath.Base(other))
	if err := os.WriteFile(obstruction, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, code := runTreehouse(t, repo, homeDir, nil, "workspace", "modify", "--add", other, "--no-fetch", workspace.Path)
	if code == 0 {
		t.Fatal("modify adopted an unrelated file")
	}
	if data, err := os.ReadFile(obstruction); err != nil || string(data) != "unrelated" {
		t.Fatalf("modify deleted unrelated file: %q, %v", data, err)
	}
	persisted, err := readWorkspaceState(workspace.Path)
	if err != nil || len(persisted.Repositories) != 1 {
		t.Fatalf("failed addition remained in workspace state: %+v, %v", persisted.Repositories, err)
	}
	poolDir, err := config.ResolvePoolDir(other, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := pool.ReadState(poolDir)
	if err != nil || len(state.Worktrees) != 1 || state.Worktrees[0].Leased {
		t.Fatalf("failed addition leaked child lease: %+v, %v", state.Worktrees, err)
	}
}

func TestWorkspaceReturnDoesNotRemoveReplacedFacade(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspace := createLeasedWorkspace(t, repo, homeDir)
	facade := filepath.Join(workspace.Path, workspace.Repositories[0].Name)
	if err := os.Remove(facade); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(facade, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := returnWorkspace(workspace.Path, true); err == nil {
		t.Fatal("return accepted a replaced facade")
	}
	if data, err := os.ReadFile(facade); err != nil || string(data) != "unrelated" {
		t.Fatalf("return deleted unrelated file: %q, %v", data, err)
	}
	state, err := pool.ReadState(workspace.Repositories[0].PoolDir)
	if err != nil || len(state.Worktrees) != 1 || !state.Worktrees[0].Leased {
		t.Fatalf("refused return released child: %+v, %v", state.Worktrees, err)
	}
}

func createLeasedWorkspace(t *testing.T, repo, homeDir string) workspaceState {
	t.Helper()
	stdout, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "get", "--lease", "--json", "--no-fetch", "--workspace-root", filepath.Join(t.TempDir(), "workspaces"), repo)
	if code != 0 {
		t.Fatalf("workspace get: %s", stderr)
	}
	var workspace workspaceState
	if err := json.Unmarshal([]byte(stdout), &workspace); err != nil {
		t.Fatal(err)
	}
	return workspace
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
