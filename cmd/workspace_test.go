package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/kunchenguid/treehouse/v3/internal/config"
	"github.com/kunchenguid/treehouse/v3/internal/pool"
)

// INV-1 (return_cmd.go): workspace return must not discard unsupported flags.
// INV-2 (workspace.go): shell return defers directory removal; lost state fails closed.
// INV-3 (workspace.go): facade cleanup must never remove an unrelated entry.
// INV-4 (workspace_test.go): profile fixtures must preserve paths on every platform.
// INV-5 (workspace.go): child acquisitions must honor repository settings and environment overrides.
// INV-6 (workspace.go): stale membership must not release a later acquisition.
// INV-7 (workspace.go): refused dirty children stay leased and recorded after partial return.
// INV-8 (workspace.go): acquisition rollback must preserve unconfirmed hook-created work.
// INV-9 (workspace.go): child names must not escape the facade or overlap its state files.
// INV-10 (workspace.go): non-forced return must prove dirtiness; damaged slots must not read enclosing repositories.

func writeWorkspaceProfileConfig(t *testing.T, homeDir string, repositories []string) {
	t.Helper()
	configDir := filepath.Join(homeDir, ".config", "treehouse")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Workspace.Profiles = map[string]config.WorkspaceProfile{"default": {Repositories: repositories}}
	var contents bytes.Buffer
	if err := toml.NewEncoder(&contents).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), contents.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceProfileFixturePreservesPaths(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	repositories := []string{`C:\Users\developer\backend`, `C:\tools\new-repo`, `/home/developer/repo"quoted`}
	writeWorkspaceProfileConfig(t, homeDir, repositories)
	cfg, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("load workspace profile fixture: %v", err)
	}
	if got := cfg.Workspace.Profiles["default"].Repositories; !reflect.DeepEqual(got, repositories) {
		t.Fatalf("profile paths = %q, want %q", got, repositories)
	}
}

func TestWorkspaceProfileLeaseAndReturn(t *testing.T) {
	repoOne, homeDir := setupTestRepo(t)
	repoTwo := setupTestRepoWithHome(t, homeDir, "second-repo")
	writeWorkspaceProfileConfig(t, homeDir, []string{repoOne, repoTwo})
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

func TestWorkspaceRepositoryProfileIsIgnored(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	cfg := config.DefaultConfig()
	cfg.Workspace.Profiles = map[string]config.WorkspaceProfile{"default": {Repositories: []string{repo}}}
	var contents bytes.Buffer
	if err := toml.NewEncoder(&contents).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	writeRepoConfig(t, repo, contents.String())
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	if _, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "get", "--lease", "--no-fetch", "--workspace-root", workspaceRoot); code == 0 {
		t.Fatalf("workspace get accepted a repository-controlled profile: %s", stderr)
	}
	if _, err := os.Stat(workspaceRoot); !os.IsNotExist(err) {
		t.Fatalf("refused profile created workspace root: %v", err)
	}
}

func TestWorkspaceAcquisitionHonorsRepositoryOptions(t *testing.T) {
	for _, operation := range []string{"get", "modify"} {
		for _, tc := range []struct {
			name       string
			config     string
			env        []string
			leafSuffix string
		}{
			{name: "base branch", config: "base_branch = \"develop\"\n"},
			{name: "unique leaf config", config: "unique_leaf = true\n", leafSuffix: "-1"},
			{name: "unique leaf env", env: []string{"TREEHOUSE_UNIQUE_LEAF=1"}, leafSuffix: "-1"},
			{name: "worktree path config", config: "worktree_path = \"{repo_parent}/{repo}-custom-{slot}\"\n", leafSuffix: "-custom-1"},
			{name: "worktree path env", env: []string{"TREEHOUSE_WORKTREE_PATH={repo_parent}/{repo}-custom-{slot}"}, leafSuffix: "-custom-1"},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				repo, homeDir := setupTestRepo(t)
				target := setupTestRepoWithHome(t, homeDir, "second-repo")
				addE2EBranch(t, target, "develop", "develop-only.txt")
				if tc.config != "" {
					writeRepoConfig(t, target, tc.config)
				}
				var workspace workspaceState
				if operation == "get" {
					stdout, stderr, code := runTreehouse(t, repo, homeDir, tc.env, "workspace", "get", "--lease", "--json", "--no-fetch", "--workspace-root", filepath.Join(t.TempDir(), "workspaces"), target)
					if code != 0 {
						t.Fatalf("workspace get: %s", stderr)
					}
					if err := json.Unmarshal([]byte(stdout), &workspace); err != nil {
						t.Fatal(err)
					}
				} else {
					workspace = createLeasedWorkspace(t, repo, homeDir)
					_, stderr, code := runTreehouse(t, repo, homeDir, tc.env, "workspace", "modify", "--add", target, "--no-fetch", workspace.Path)
					if code != 0 {
						t.Fatalf("workspace modify: %s", stderr)
					}
					var err error
					workspace, err = readWorkspaceState(workspace.Path)
					if err != nil {
						t.Fatal(err)
					}
				}
				var child workspaceRepository
				for _, entry := range workspace.Repositories {
					if entry.SourceRoot == target {
						child = entry
					}
				}
				if child.Path == "" {
					t.Fatal("target repository is missing from workspace")
				}
				if tc.name == "base branch" {
					if _, err := os.Stat(filepath.Join(child.Path, "develop-only.txt")); err != nil {
						t.Errorf("child did not start on configured base: %v", err)
					}
				} else if got, want := filepath.Base(child.Path), filepath.Base(target)+tc.leafSuffix; got != want {
					t.Errorf("child leaf = %s, want %s", got, want)
				}
				if _, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "return", "--force", workspace.Path); code != 0 {
					t.Fatalf("workspace return: %s", stderr)
				}
			})
		}
	}
}

func TestWorkspaceAcquisitionRollbackPreservesDirtyChild(t *testing.T) {
	for _, operation := range []string{"get", "modify"} {
		t.Run(operation, func(t *testing.T) {
			repo, homeDir := setupTestRepo(t)
			other := setupTestRepoWithHome(t, homeDir, "second-repo")
			workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
			var workspace workspaceState
			if operation == "modify" {
				workspace = createLeasedWorkspace(t, repo, homeDir)
				if err := os.WriteFile(filepath.Join(workspace.Path, filepath.Base(other)), []byte("obstruction"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeRepoConfig(t, other, "base_branch = \"no-such-branch\"\n")
			}
			cfg := config.DefaultConfig()
			cfg.Hooks.PostCreate = []string{"echo keep>uncommitted.txt"}
			var contents bytes.Buffer
			if err := toml.NewEncoder(&contents).Encode(cfg); err != nil {
				t.Fatal(err)
			}
			configDir := filepath.Join(homeDir, ".config", "treehouse")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), contents.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			if operation == "get" {
				if _, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "get", "--lease", "--no-fetch", "--workspace-root", workspaceRoot, repo, other); code == 0 {
					t.Fatalf("acquisition unexpectedly succeeded: %s", stderr)
				}
				entries, err := os.ReadDir(workspaceRoot)
				if err != nil || len(entries) != 1 {
					t.Fatalf("rollback did not keep recoverable workspace state: %v, %v", entries, err)
				}
				workspace.Path = filepath.Join(workspaceRoot, entries[0].Name())
			} else {
				if _, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "modify", "--no-fetch", "--add", other, workspace.Path); code == 0 {
					t.Fatalf("addition unexpectedly succeeded: %s", stderr)
				}
			}
			persisted, err := readWorkspaceState(workspace.Path)
			if err != nil {
				t.Fatalf("read retained workspace: %v", err)
			}
			wantCount, wantRoot := 1, repo
			if operation == "modify" {
				wantCount, wantRoot = 2, other
			}
			if len(persisted.Repositories) != wantCount {
				t.Fatalf("rollback lost membership: %+v", persisted.Repositories)
			}
			child := persisted.Repositories[len(persisted.Repositories)-1]
			if child.SourceRoot != wantRoot {
				t.Fatalf("retained child = %s, want %s", child.SourceRoot, wantRoot)
			}
			state, err := pool.ReadState(child.PoolDir)
			if err != nil || len(state.Worktrees) != 1 || !state.Worktrees[0].Leased || state.Worktrees[0].LeaseID != child.LeaseID {
				t.Fatalf("dirty child was released by rollback: %+v, %v", state.Worktrees, err)
			}
			if data, err := os.ReadFile(filepath.Join(child.Path, "uncommitted.txt")); err != nil || strings.TrimSpace(string(data)) != "keep" {
				t.Fatalf("rollback discarded hook-created work: %q, %v", data, err)
			}
		})
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

func TestWorkspaceReturnRejectsEscapingChildName(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspace := createLeasedWorkspace(t, repo, homeDir)
	child := workspace.Repositories[0]
	outside := filepath.Join(filepath.Dir(workspace.Path), "victim")
	if err := os.Symlink(child.Path, outside); err != nil {
		t.Fatal(err)
	}
	workspace.Repositories[0].Name = filepath.Join("..", "victim")
	if err := writeWorkspaceState(workspace); err != nil {
		t.Fatal(err)
	}
	if err := returnWorkspace(workspace.Path, true); err == nil {
		t.Fatal("return accepted an escaping child name")
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Fatalf("return removed an entry outside the workspace: %v", err)
	}
	state, err := pool.ReadState(child.PoolDir)
	if err != nil || len(state.Worktrees) != 1 || !state.Worktrees[0].Leased || state.Worktrees[0].LeaseID != child.LeaseID {
		t.Fatalf("malformed state released child: %+v, %v", state.Worktrees, err)
	}
}

func TestWorkspaceRejectsReservedRepositoryNames(t *testing.T) {
	_, homeDir := setupTestRepo(t)
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	t.Setenv(config.RootEnvVar, homeDir)
	for _, name := range []string{workspaceStateFile, workspaceStateFile + ".tmp"} {
		t.Run(name, func(t *testing.T) {
			repo := setupTestRepoWithHome(t, homeDir, name)
			if _, err := resolveWorkspaceRepositories([]string{repo}); err == nil {
				t.Fatalf("accepted repository name %q that overlaps workspace state", name)
			}
		})
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

func TestWorkspaceChildReturnFailsClosedOnDirtyCheckError(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspace := createLeasedWorkspace(t, repo, homeDir)
	child := workspace.Repositories[0]
	note := filepath.Join(child.Path, "uncommitted.txt")
	if err := os.WriteFile(note, []byte("keep this"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := workspaceIsDirty
	t.Cleanup(func() { workspaceIsDirty = original })
	checkErr := errors.New("cannot inspect working tree")
	workspaceIsDirty = func(path string) (bool, error) {
		if path != child.Path {
			t.Fatalf("dirty check path = %s, want %s", path, child.Path)
		}
		return false, checkErr
	}
	if err := returnWorkspaceChild(child, false); !errors.Is(err, checkErr) {
		t.Fatalf("return ignored the dirty-check error: %v", err)
	}
	state, err := pool.ReadState(child.PoolDir)
	if err != nil || len(state.Worktrees) != 1 || !state.Worktrees[0].Leased || state.Worktrees[0].LeaseID != child.LeaseID {
		t.Fatalf("unverified child was released: %+v, %v", state.Worktrees, err)
	}
	if data, err := os.ReadFile(note); err != nil || string(data) != "keep this" {
		t.Fatalf("unverified changes were discarded: %q, %v", data, err)
	}
}

func TestWorkspaceChildReturnDoesNotCheckDirtinessWithoutMarker(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspace := createLeasedWorkspace(t, repo, homeDir)
	child := workspace.Repositories[0]
	if err := os.Remove(filepath.Join(child.Path, ".git")); err != nil {
		t.Fatal(err)
	}
	original := workspaceIsDirty
	t.Cleanup(func() { workspaceIsDirty = original })
	workspaceIsDirty = func(string) (bool, error) {
		t.Fatal("markerless child consulted dirtiness through a fallback repository")
		return false, nil
	}
	if err := returnWorkspaceChild(child, false); err != nil {
		t.Fatalf("return damaged child without resetting it: %v", err)
	}
	state, err := pool.ReadState(child.PoolDir)
	if err != nil || len(state.Worktrees) != 1 || state.Worktrees[0].Leased {
		t.Fatalf("damaged child lease was not cleared: %+v, %v", state.Worktrees, err)
	}
}

func TestWorkspaceReturnDoesNotReleaseReacquiredChild(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	workspace := createLeasedWorkspace(t, repo, homeDir)
	child := workspace.Repositories[0]
	if _, stderr, code := runTreehouse(t, repo, homeDir, nil, "return", "--force", child.Path); code != 0 {
		t.Fatalf("return original child: %s", stderr)
	}
	stdout, stderr, code := runTreehouse(t, repo, homeDir, nil, "get", "--lease", "--json", "--no-fetch")
	if code != 0 {
		t.Fatalf("reacquire child: %s", stderr)
	}
	var lease pool.LeaseInfo
	if err := json.Unmarshal([]byte(stdout), &lease); err != nil {
		t.Fatal(err)
	}
	if lease.Path != child.Path || lease.LeaseID == child.LeaseID {
		t.Fatalf("fixture did not reacquire the original slot with a new identity: %+v", lease)
	}
	note := filepath.Join(lease.Path, "new-owner.txt")
	if err := os.WriteFile(note, []byte("keep this"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "return", "--force", workspace.Path); code == 0 {
		t.Fatalf("stale workspace returned a new acquisition: %s", stderr)
	}
	state, err := pool.ReadState(child.PoolDir)
	if err != nil || len(state.Worktrees) != 1 || !state.Worktrees[0].Leased || state.Worktrees[0].LeaseID != lease.LeaseID {
		t.Fatalf("new acquisition changed after stale return: %+v, %v", state.Worktrees, err)
	}
	if data, err := os.ReadFile(note); err != nil || string(data) != "keep this" {
		t.Fatalf("new owner's changes were discarded: %q, %v", data, err)
	}
	persisted, err := readWorkspaceState(workspace.Path)
	if err != nil || len(persisted.Repositories) != 1 || persisted.Repositories[0].LeaseID != child.LeaseID {
		t.Fatalf("refused return lost original membership: %+v, %v", persisted.Repositories, err)
	}
}

func TestWorkspacePartialReturnKeepsUnconfirmedDirtyChild(t *testing.T) {
	repo, homeDir := setupTestRepo(t)
	other := setupTestRepoWithHome(t, homeDir, "second-repo")
	workspace := createLeasedWorkspace(t, repo, homeDir)
	if _, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "modify", "--add", other, "--no-fetch", workspace.Path); code != 0 {
		t.Fatalf("add child: %s", stderr)
	}
	current, err := readWorkspaceState(workspace.Path)
	if err != nil || len(current.Repositories) != 2 {
		t.Fatalf("modified workspace: %+v, %v", current.Repositories, err)
	}
	dirty := current.Repositories[0]
	note := filepath.Join(dirty.Path, "uncommitted.txt")
	if err := os.WriteFile(note, []byte("keep this"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runTreehouse(t, repo, homeDir, nil, "workspace", "return", workspace.Path); code == 0 {
		t.Fatalf("return discarded unconfirmed changes: %s", stderr)
	}
	persisted, err := readWorkspaceState(workspace.Path)
	if err != nil || len(persisted.Repositories) != 1 || persisted.Repositories[0].LeaseID != dirty.LeaseID {
		t.Fatalf("partial return lost dirty child: %+v, %v", persisted.Repositories, err)
	}
	for _, child := range current.Repositories {
		state, err := pool.ReadState(child.PoolDir)
		if err != nil || len(state.Worktrees) != 1 || state.Worktrees[0].Leased != (child.LeaseID == dirty.LeaseID) {
			t.Fatalf("partial return lease state for %s: %+v, %v", child.Name, state.Worktrees, err)
		}
	}
	if data, err := os.ReadFile(note); err != nil || string(data) != "keep this" {
		t.Fatalf("unconfirmed changes were discarded: %q, %v", data, err)
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
