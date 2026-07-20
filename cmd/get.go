package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/process"
	"github.com/kunchenguid/treehouse/internal/shell"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var (
	getLease       bool
	getLeaseHolder string
	getJSON        bool
)

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Acquire a worktree from the pool and open a subshell",
	Long: `Acquire a worktree from the pool and open a subshell in it.

Pass --lease for a non-interactive, durable acquire: treehouse reserves the
worktree and marks it leased in persistent state. By default it prints only the
absolute path to stdout; add --json for the lease identity and metadata. All
banners go to stderr. A leased worktree is never handed out by a later get and
never removed by prune, even with no process running inside it, until you release
it with 'treehouse return <path>'.`,
	RunE: getRunE,
}

func init() {
	getCmd.Flags().BoolVar(&getLease, "lease", false, "Durably lease a worktree without opening a subshell; print only its path to stdout")
	getCmd.Flags().StringVar(&getLeaseHolder, "lease-holder", "", "Optional label recorded as the lease holder (defaults to $TREEHOUSE_LEASE_HOLDER)")
	getCmd.Flags().BoolVar(&getJSON, "json", false, "Print lease allocation as JSON (requires --lease)")
	rootCmd.AddCommand(getCmd)
}

func getRunE(cmd *cobra.Command, args []string) error {
	if getJSON && !getLease {
		return fmt.Errorf("--json requires --lease")
	}

	repoRoot, err := git.FindRepoRoot()
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	poolDir, err := config.ResolvePoolDir(repoRoot, cfg.Root)
	if err != nil {
		return fmt.Errorf("failed to resolve pool directory: %w", err)
	}

	if err := config.EnsureGitignore(filepath.Dir(poolDir)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to update .gitignore: %v\n", err)
	}

	if getLease {
		return getLeaseRunE(repoRoot, poolDir, cfg)
	}

	wtPath, err := pool.Acquire(repoRoot, poolDir, cfg.MaxTrees, cfg.Hooks.PostCreate)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "🌳 Entered worktree at %s. Type 'exit' to return.\n", ui.PrettyPath(wtPath))

	env := []string{
		"TREEHOUSE_DIR=" + wtPath,
	}
	_, err = shell.Spawn(wtPath, env)

	// Subshell exited — handle return
	if err := git.DetachWorktree(wtPath); err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: failed to detach worktree HEAD: %v\n", err)
	}

	dirty, _ := git.IsDirty(wtPath)
	if dirty {
		fmt.Fprintf(os.Stderr, "🌳 Worktree has uncommitted changes.\n")

		ok, promptErr := ui.Confirm("Clean worktree and return to pool?", true)
		if promptErr != nil || !ok {
			fmt.Fprintln(os.Stderr, "🌳 Worktree left dirty. Use 'treehouse return --force' to clean it later.")
			return nil
		}
	}

	killLingeringProcesses(wtPath)

	if err := pool.Release(poolDir, wtPath); err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: failed to clean worktree: %v\n", err)
	} else {
		fmt.Fprintln(os.Stderr, "🌳 Worktree returned to pool.")
	}

	return nil
}

// getLeaseRunE performs a non-interactive, durable acquire. It writes either the
// worktree path or the requested JSON allocation to stdout and routes every
// human-facing message to stderr, keeping both output modes machine-readable.
func getLeaseRunE(repoRoot, poolDir string, cfg config.Config) error {
	holder := getLeaseHolder
	if holder == "" {
		holder = os.Getenv("TREEHOUSE_LEASE_HOLDER")
	}

	lease, err := pool.AcquireLeaseInfo(repoRoot, poolDir, cfg.MaxTrees, cfg.Hooks.PostCreate, holder)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "🌳 Leased worktree at %s. Run 'treehouse return %s' to release it.\n",
		ui.PrettyPath(lease.Path), ui.PrettyPath(lease.Path))
	if getJSON {
		return json.NewEncoder(os.Stdout).Encode(lease)
	}
	// The bare path is the only thing on stdout, so callers can capture it.
	fmt.Fprintln(os.Stdout, lease.Path)
	return nil
}

// killLingeringProcesses terminates any process whose cwd is within the given
// worktree. Called before returning a worktree to the pool so detached tools
// (e.g. opencode servers that ignore SIGHUP) don't keep holding the worktree.
func killLingeringProcesses(wtPath string) {
	killed, err := process.TerminateWorktreeProcesses(wtPath, 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: failed to scan for lingering processes: %v\n", err)
		return
	}
	if len(killed) == 0 {
		return
	}
	names := make([]string, len(killed))
	for i, p := range killed {
		names[i] = p.String()
	}
	fmt.Fprintf(os.Stderr, "🌳 Terminated lingering processes: %s\n", strings.Join(names, ", "))
}
