package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/shell"
	"github.com/kunchenguid/treehouse/internal/ui"
	"github.com/kunchenguid/treehouse/internal/vcs"
)

const workspaceStateFile = ".treehouse-workspace.json"

var (
	workspaceRoot    string
	workspaceProfile string
	workspaceLease   bool
	workspaceJSON    bool
	workspaceNoFetch bool
	workspaceForce   bool
	workspaceAdd     []string
	workspaceRemove  []string
)

type workspaceState struct {
	Version      int                   `json:"version"`
	ID           string                `json:"id"`
	Path         string                `json:"path"`
	CreatedAt    time.Time             `json:"created_at"`
	Repositories []workspaceRepository `json:"repositories"`
}

type workspaceRepository struct {
	Name       string `json:"name"`
	SourceRoot string `json:"source_root"`
	PoolDir    string `json:"pool_dir"`
	Path       string `json:"path"`
	LeaseID    string `json:"lease_id"`
}

type workspaceRepoConfig struct {
	root, poolDir, name string
	cfg                 config.Config
}

var workspaceCmd = &cobra.Command{Use: "workspace", Short: "Manage a group of leased worktrees as one workspace"}
var workspaceGetCmd = &cobra.Command{Use: "get [repository...]", Short: "Acquire repositories into a linked workspace", Args: cobra.ArbitraryArgs, RunE: workspaceGetRunE}
var workspaceReturnCmd = &cobra.Command{Use: "return <workspace-path>", Short: "Return every leased worktree in a workspace", Args: cobra.ExactArgs(1), RunE: workspaceReturnRunE}
var workspaceModifyCmd = &cobra.Command{Use: "modify [workspace-path]", Short: "Add or remove repositories from a workspace", Args: cobra.MaximumNArgs(1), RunE: workspaceModifyRunE}

func init() {
	workspaceGetCmd.Flags().StringVar(&workspaceRoot, "workspace-root", "", "Directory containing workspace facades (default: ~/.treehouse/workspaces)")
	workspaceGetCmd.Flags().StringVar(&workspaceProfile, "profile", "", "User-configured workspace profile to acquire")
	workspaceGetCmd.Flags().BoolVar(&workspaceLease, "lease", false, "Create a durable workspace without opening a subshell")
	workspaceGetCmd.Flags().BoolVar(&workspaceJSON, "json", false, "Print workspace allocation as JSON (requires --lease)")
	workspaceGetCmd.Flags().BoolVar(&workspaceNoFetch, "no-fetch", false, "Skip fetching origins before acquiring")
	workspaceReturnCmd.Flags().BoolVar(&workspaceForce, "force", false, "Clean, reset, and return without prompting")
	workspaceModifyCmd.Flags().StringSliceVar(&workspaceAdd, "add", nil, "Repository paths to add (comma-separated or repeated)")
	workspaceModifyCmd.Flags().StringSliceVar(&workspaceRemove, "remove", nil, "Repository names or paths to remove (comma-separated or repeated)")
	workspaceModifyCmd.Flags().BoolVar(&workspaceNoFetch, "no-fetch", false, "Skip fetching origins before acquiring added repositories")
	workspaceModifyCmd.Flags().BoolVar(&workspaceForce, "force", false, "Clean, reset, and remove repositories without prompting")
	workspaceCmd.AddCommand(workspaceGetCmd, workspaceModifyCmd, workspaceReturnCmd)
	rootCmd.AddCommand(workspaceCmd)
}

func workspaceGetRunE(cmd *cobra.Command, args []string) error {
	if workspaceJSON && !workspaceLease {
		return fmt.Errorf("--json requires --lease")
	}
	args, err := workspaceRepositoryArgs(args)
	if err != nil {
		return err
	}
	repos, err := resolveWorkspaceRepositories(args)
	if err != nil {
		return err
	}
	root, err := resolveWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}
	id, err := newWorkspaceID()
	if err != nil {
		return err
	}
	path := filepath.Join(root, "ws-"+id)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create workspace root: %w", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	state := workspaceState{Version: 1, ID: id, Path: path, CreatedAt: time.Now(), Repositories: []workspaceRepository{}}
	if err := writeWorkspaceState(state); err != nil {
		return err
	}
	rollback := func(cause error) error {
		if err := returnWorkspaceState(&state, true); err != nil {
			return fmt.Errorf("%w; rollback failed: %v (recover with treehouse workspace return %s --force)", cause, err, path)
		}
		if err := removeWorkspaceDirectory(path); err != nil {
			return fmt.Errorf("%w; remove rolled-back workspace: %v", cause, err)
		}
		return cause
	}
	for _, repo := range repos {
		lease, err := pool.AcquireLeaseInfoWithOptions(repo.root, repo.poolDir, repo.cfg.MaxTrees, repo.cfg.Hooks.PostCreate, "workspace:"+id, pool.AcquireOptions{SkipFetch: workspaceNoFetch})
		if err != nil {
			return rollback(fmt.Errorf("acquire %s: %w", repo.name, err))
		}
		state.Repositories = append(state.Repositories, workspaceRepository{Name: repo.name, SourceRoot: repo.root, PoolDir: repo.poolDir, Path: lease.Path, LeaseID: lease.LeaseID})
		if err := writeWorkspaceState(state); err != nil {
			return rollback(fmt.Errorf("record %s allocation: %w", repo.name, err))
		}
		if err := os.Symlink(lease.Path, filepath.Join(path, repo.name)); err != nil {
			return rollback(fmt.Errorf("link %s into workspace: %w", repo.name, err))
		}
	}
	if workspaceLease {
		fmt.Fprintf(os.Stderr, "🌳 Leased workspace at %s. Run 'treehouse workspace return %s' to release it.\n", ui.PrettyPath(path), ui.PrettyPath(path))
		if workspaceJSON {
			return json.NewEncoder(os.Stdout).Encode(state)
		}
		fmt.Fprintln(os.Stdout, path)
		return nil
	}
	fmt.Fprintf(os.Stderr, "🌳 Entered workspace at %s. Type 'exit' to return.\n", ui.PrettyPath(path))
	_, shellErr := shell.Spawn(path, []string{"TREEHOUSE_WORKSPACE=" + path, "TREEHOUSE_WORKSPACE_ID=" + id})
	returnErr := finishWorkspaceShell(path)
	if shellErr != nil {
		return shellErr
	}
	return returnErr
}

func finishWorkspaceShell(path string) error {
	// Modify can add children while the subshell is open; its initial snapshot is stale.
	state, err := readWorkspaceState(path)
	if err != nil {
		return err
	}
	if err := returnWorkspaceState(&state, false); err != nil {
		return err
	}
	if err := removeWorkspaceDirectory(path); err != nil {
		return fmt.Errorf("remove returned workspace: %w", err)
	}
	fmt.Fprintln(os.Stderr, "🌳 Workspace returned to pool.")
	return nil
}

func removeWorkspaceDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != workspaceStateFile {
			return fmt.Errorf("workspace %s contains %s; leaving it in place", path, entry.Name())
		}
	}
	if err := os.Remove(workspaceStatePath(path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Remove(path)
}

func workspaceReturnRunE(cmd *cobra.Command, args []string) error {
	return returnWorkspace(args[0], workspaceForce)
}

func workspaceModifyRunE(cmd *cobra.Command, args []string) error {
	if len(workspaceAdd) == 0 && len(workspaceRemove) == 0 {
		return fmt.Errorf("workspace modify requires --add, --remove, or both")
	}
	workspacePath := ""
	if len(args) == 1 {
		workspacePath = args[0]
	} else {
		workspacePath = os.Getenv("TREEHOUSE_WORKSPACE")
		if workspacePath == "" {
			return fmt.Errorf("workspace modify requires a workspace path outside a workspace shell")
		}
	}
	path, err := filepath.Abs(workspacePath)
	if err != nil {
		return err
	}
	state, err := readWorkspaceState(path)
	if err != nil {
		return err
	}
	removals, err := resolveWorkspaceRemovals(state, workspaceRemove)
	if err != nil {
		return err
	}
	additions, err := resolveWorkspaceRepositories(workspaceAdd)
	if err != nil {
		return err
	}
	if err := validateWorkspaceAdditions(state, removals, additions); err != nil {
		return err
	}

	for i := 0; i < len(state.Repositories); {
		if !removals[state.Repositories[i].Name] {
			i++
			continue
		}
		name := state.Repositories[i].Name
		if err := removeWorkspaceRepository(&state, i, workspaceForce); err != nil {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	for _, repo := range additions {
		if err := addWorkspaceRepository(&state, repo, workspaceNoFetch); err != nil {
			return fmt.Errorf("add %s: %w", repo.name, err)
		}
	}
	sort.Slice(state.Repositories, func(i, j int) bool { return state.Repositories[i].Name < state.Repositories[j].Name })
	if err := writeWorkspaceState(state); err != nil {
		return fmt.Errorf("record modified workspace: %w", err)
	}
	fmt.Fprintf(os.Stderr, "🌳 Workspace now contains %d repositories.\n", len(state.Repositories))
	return nil
}

func returnWorkspace(path string, force bool) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	state, err := readWorkspaceState(path)
	if err != nil {
		return err
	}
	if err := returnWorkspaceState(&state, force); err != nil {
		return err
	}
	if err := removeWorkspaceDirectory(path); err != nil {
		return fmt.Errorf("remove returned workspace: %w", err)
	}
	fmt.Fprintln(os.Stderr, "🌳 Workspace returned to pool.")
	return nil
}

func workspaceRepositoryArgs(args []string) ([]string, error) {
	if workspaceProfile != "" && len(args) > 0 {
		return nil, fmt.Errorf("--profile cannot be combined with repository arguments")
	}
	if workspaceProfile == "" && len(args) > 0 {
		return args, nil
	}
	name := workspaceProfile
	if name == "" {
		name = "default"
	}
	cfg, err := config.LoadGlobal()
	if err != nil {
		return nil, fmt.Errorf("load user workspace profiles: %w", err)
	}
	profile, ok := cfg.Workspace.Profiles[name]
	if !ok {
		if workspaceProfile == "" {
			return nil, fmt.Errorf("workspace get requires repository arguments or a user-configured workspace profile named %q", name)
		}
		return nil, fmt.Errorf("workspace profile %q is not configured", name)
	}
	if len(profile.Repositories) == 0 {
		return nil, fmt.Errorf("workspace profile %q has no repositories", name)
	}
	return profile.Repositories, nil
}

func resolveWorkspaceRemovals(state workspaceState, selectors []string) (map[string]bool, error) {
	removals := make(map[string]bool, len(selectors))
	for _, selector := range selectors {
		name := ""
		for _, child := range state.Repositories {
			if selector == child.Name || selector == child.SourceRoot || selector == child.Path {
				name = child.Name
				break
			}
		}
		if name == "" {
			path, err := expandWorkspaceRepositoryPath(selector)
			if err == nil {
				path, err = filepath.Abs(path)
			}
			if err == nil {
				if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
					path = resolved
				}
				for _, child := range state.Repositories {
					if path == child.SourceRoot || path == child.Path {
						name = child.Name
						break
					}
				}
			}
		}
		if name == "" {
			return nil, fmt.Errorf("repository %q is not part of workspace %s", selector, state.Path)
		}
		if removals[name] {
			return nil, fmt.Errorf("repository %q was selected for removal more than once", name)
		}
		removals[name] = true
	}
	return removals, nil
}

func validateWorkspaceAdditions(state workspaceState, removals map[string]bool, additions []workspaceRepoConfig) error {
	names, roots := map[string]bool{}, map[string]bool{}
	for _, child := range state.Repositories {
		if !removals[child.Name] {
			names[child.Name] = true
			roots[child.SourceRoot] = true
		}
	}
	for _, addition := range additions {
		if roots[addition.root] {
			return fmt.Errorf("repository %s is already part of workspace %s", addition.root, state.Path)
		}
		if names[addition.name] {
			return fmt.Errorf("workspace repositories must have distinct directory names; duplicate %q", addition.name)
		}
		names[addition.name] = true
		roots[addition.root] = true
	}
	return nil
}

func addWorkspaceRepository(state *workspaceState, repo workspaceRepoConfig, noFetch bool) error {
	lease, err := pool.AcquireLeaseInfoWithOptions(repo.root, repo.poolDir, repo.cfg.MaxTrees, repo.cfg.Hooks.PostCreate, "workspace:"+state.ID, pool.AcquireOptions{SkipFetch: noFetch})
	if err != nil {
		return err
	}
	child := workspaceRepository{Name: repo.name, SourceRoot: repo.root, PoolDir: repo.poolDir, Path: lease.Path, LeaseID: lease.LeaseID}
	state.Repositories = append(state.Repositories, child)
	if err := writeWorkspaceState(*state); err != nil {
		state.Repositories = state.Repositories[:len(state.Repositories)-1]
		if releaseErr := returnWorkspaceChild(child, true); releaseErr != nil {
			return fmt.Errorf("record allocation: %w; return unrecorded lease: %v", err, releaseErr)
		}
		return fmt.Errorf("record allocation: %w", err)
	}
	if err := os.Symlink(child.Path, filepath.Join(state.Path, child.Name)); err != nil {
		if removeErr := removeWorkspaceRepository(state, len(state.Repositories)-1, true); removeErr != nil {
			return fmt.Errorf("link repository: %w; rollback lease: %v", err, removeErr)
		}
		return fmt.Errorf("link repository: %w", err)
	}
	return nil
}

func resolveWorkspaceRepositories(args []string) ([]workspaceRepoConfig, error) {
	repos := make([]workspaceRepoConfig, 0, len(args))
	names, roots := map[string]bool{}, map[string]bool{}
	for _, arg := range args {
		path, err := expandWorkspaceRepositoryPath(arg)
		if err != nil {
			return nil, err
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("resolve repository %s: %w", arg, err)
		}
		repoRoot, err := vcs.FindMainRepoRootFrom(path)
		if err != nil {
			return nil, fmt.Errorf("%s is not in a git or jj repository: %w", arg, err)
		}
		if roots[repoRoot] {
			return nil, fmt.Errorf("repository %s was specified more than once", repoRoot)
		}
		cfg, err := config.Load(repoRoot)
		if err != nil {
			return nil, fmt.Errorf("load config for %s: %w", repoRoot, err)
		}
		poolDir, err := config.ResolvePoolDir(repoRoot, config.ResolveRoot(rootFlag, cfg))
		if err != nil {
			return nil, fmt.Errorf("resolve pool for %s: %w", repoRoot, err)
		}
		if err := config.EnsureExcluded(filepath.Dir(poolDir)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to update git exclude: %v\n", err)
		}
		name := filepath.Base(repoRoot)
		if names[name] {
			return nil, fmt.Errorf("repositories must have distinct directory names; duplicate %q", name)
		}
		names[name], roots[repoRoot] = true, true
		repos = append(repos, workspaceRepoConfig{root: repoRoot, poolDir: poolDir, cfg: cfg, name: name})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].name < repos[j].name })
	return repos, nil
}

func expandWorkspaceRepositoryPath(path string) (string, error) {
	path = os.ExpandEnv(path)
	if path != "~" && !strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
func resolveWorkspaceRoot(flag string) (string, error) {
	if flag == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		flag = filepath.Join(home, ".treehouse", "workspaces")
	}
	return filepath.Abs(os.ExpandEnv(flag))
}
func newWorkspaceID() (string, error) {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
func workspaceStatePath(path string) string { return filepath.Join(path, workspaceStateFile) }
func writeWorkspaceState(state workspaceState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := workspaceStatePath(state.Path)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func readWorkspaceState(path string) (workspaceState, error) {
	data, err := os.ReadFile(workspaceStatePath(path))
	if err != nil {
		return workspaceState{}, fmt.Errorf("read workspace state: %w", err)
	}
	var state workspaceState
	if err := json.Unmarshal(data, &state); err != nil {
		return workspaceState{}, fmt.Errorf("parse workspace state: %w", err)
	}
	if state.Version != 1 || state.Path != path || state.ID == "" {
		return workspaceState{}, fmt.Errorf("invalid workspace state in %s", workspaceStatePath(path))
	}
	return state, nil
}
func returnWorkspaceState(state *workspaceState, force bool) error {
	var failures []string
	for i := 0; i < len(state.Repositories); {
		child := state.Repositories[i]
		if err := removeWorkspaceRepository(state, i, force); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", child.Name, err))
			i++
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("workspace %s was only partially returned: %s", state.Path, strings.Join(failures, "; "))
	}
	return nil
}

func removeWorkspaceRepository(state *workspaceState, index int, force bool) error {
	child := state.Repositories[index]
	if err := returnWorkspaceChild(child, force); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(state.Path, child.Name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove facade link: %w", err)
	}
	repositories := make([]workspaceRepository, 0, len(state.Repositories)-1)
	repositories = append(repositories, state.Repositories[:index]...)
	repositories = append(repositories, state.Repositories[index+1:]...)
	next := *state
	next.Repositories = repositories
	if err := writeWorkspaceState(next); err != nil {
		return fmt.Errorf("record returned repository: %w", err)
	}
	state.Repositories = repositories
	return nil
}

func returnWorkspaceChild(child workspaceRepository, force bool) error {
	leaseID := child.LeaseID
	preconditions := pool.ReleasePreconditions{ExpectedLeaseID: &leaseID}
	if err := pool.ValidateReleasePreconditions(child.PoolDir, child.Path, preconditions, nil); err != nil {
		if errors.Is(err, pool.ErrLeasePreconditionFailed) {
			return confirmWorkspaceChildAlreadyReturned(child, err)
		}
		return err
	}
	if !force {
		if dirty, _ := vcs.IsDirty(child.Path); dirty {
			ok, err := ui.Confirm(fmt.Sprintf("%s has uncommitted changes. Clean and return?", child.Name), true)
			if err != nil || !ok {
				return errReturnAborted
			}
		}
	}
	return pool.ReleaseConditional(child.PoolDir, child.Path, returnBaseBranch(child.Path), preconditions, func() error {
		if !force && vcs.WorktreeBackendName(child.Path) != "" {
			if err := vcs.DetachWorktree(child.Path); err != nil {
				return fmt.Errorf("detach worktree: %w", err)
			}
		}
		return killLingeringProcesses(child.Path)
	})
}
func confirmWorkspaceChildAlreadyReturned(child workspaceRepository, leaseErr error) error {
	statuses, err := pool.List(child.PoolDir)
	if err != nil {
		return fmt.Errorf("%w; check current pool status: %v", leaseErr, err)
	}
	for _, status := range statuses {
		if status.Path != child.Path {
			continue
		}
		if status.Status == pool.StatusAvailable {
			fmt.Fprintf(os.Stderr, "🌳 %s was already returned and is available; removing it from workspace state.\n", child.Name)
			return nil
		}
		return fmt.Errorf("%w; worktree is currently %s, not safely free", leaseErr, status.Status)
	}
	return fmt.Errorf("%w; worktree is no longer managed by its recorded pool", leaseErr)
}
