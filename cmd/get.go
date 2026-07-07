package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/herdr"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/process"
	"github.com/kunchenguid/treehouse/internal/shell"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var (
	getLease       bool
	getLeaseHolder string
	getNoHerdr     bool
	getNoFocus     bool
)

// errHerdrFallback signals that herdr-native spawning could not open a pane and
// that get should fall back to the classic in-place subshell.
var errHerdrFallback = errors.New("herdr pane unavailable; falling back to subshell")

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Acquire a worktree from the pool and open a subshell",
	Long: `Acquire a worktree from the pool and open a subshell in it.

Pass --lease for a non-interactive, durable acquire: treehouse reserves the
worktree, marks it leased in its persistent state, and prints only the worktree's
absolute path to stdout (all banners go to stderr). A leased worktree is never
handed out by a later get and never removed by prune, even with no process
running inside it, until you release it with 'treehouse return <path>'.

When treehouse runs inside the herdr terminal multiplexer (HERDR_ENV=1) and the
herdr CLI is available, get opens the worktree in its own herdr pane and routes
the agent there to the /herdr skill. The worktree still returns to the pool when
you exit that pane. Pass --no-herdr (or set TREEHOUSE_NO_HERDR=1) to force the
classic in-place subshell.`,
	RunE: getRunE,
}

// holdCmd is the hidden holder that runs inside a herdr-opened pane. It holds
// the worktree for the lifetime of its shell and returns it to the pool on exit,
// preserving treehouse's exit-to-return UX even though the outer `get` process
// has already exited.
var holdCmd = &cobra.Command{
	Use:    herdr.HoldSubcommand + " <worktree> <pool>",
	Hidden: true,
	Args:   cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		wtPath, poolDir := args[0], args[1]
		if _, err := os.Stat(wtPath); err != nil {
			return fmt.Errorf("worktree %s is not accessible: %w", wtPath, err)
		}
		return holdAndReturn(wtPath, poolDir)
	},
}

func init() {
	getCmd.Flags().BoolVar(&getLease, "lease", false, "Durably lease a worktree without opening a subshell; print only its path to stdout")
	getCmd.Flags().StringVar(&getLeaseHolder, "lease-holder", "", "Optional label recorded as the lease holder (defaults to $TREEHOUSE_LEASE_HOLDER)")
	getCmd.Flags().BoolVar(&getNoHerdr, "no-herdr", false, "Force a classic in-place subshell even when running inside herdr")
	getCmd.Flags().BoolVar(&getNoFocus, "no-focus", false, "When opening a worktree in a herdr pane, do not move focus to the new pane")
	rootCmd.AddCommand(getCmd)
	rootCmd.AddCommand(holdCmd)
}

func getRunE(cmd *cobra.Command, args []string) error {
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

	// Inside herdr, open the worktree in its own pane. On any failure to create
	// the pane we fall through to the classic in-place subshell below.
	if useHerdrPane(cfg) {
		if err := getHerdrRunE(repoRoot, poolDir, cfg); err == nil {
			return nil
		} else if !errors.Is(err, errHerdrFallback) {
			return err
		}
	}

	wtPath, err := pool.Acquire(repoRoot, poolDir, cfg.MaxTrees, cfg.Hooks.PostCreate)
	if err != nil {
		return err
	}
	return holdAndReturn(wtPath, poolDir)
}

// useHerdrPane reports whether get should open the worktree in a dedicated herdr
// pane rather than a classic in-place subshell.
func useHerdrPane(cfg config.Config) bool {
	if getNoHerdr || herdr.Disabled() || !cfg.Herdr.IsEnabled() {
		return false
	}
	return herdr.IsRuntime() && herdr.Available()
}

// getHerdrRunE acquires a worktree, leases it so it survives the immediate exit
// of this process, and opens it in a new herdr pane running the treehouse
// holder. It returns errHerdrFallback (after releasing the lease) when no pane
// could be created, so the caller can fall back to a classic subshell.
func getHerdrRunE(repoRoot, poolDir string, cfg config.Config) error {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: cannot locate treehouse binary for a herdr pane (%v); using subshell.\n", err)
		return errHerdrFallback
	}

	wtPath, err := pool.AcquireLease(repoRoot, poolDir, cfg.MaxTrees, cfg.Hooks.PostCreate, herdr.LeaseHolder)
	if err != nil {
		return err
	}

	pane, err := herdr.SpawnHold(herdr.SpawnOptions{
		Exe:          exe,
		WorktreePath: wtPath,
		PoolDir:      poolDir,
		Label:        herdrPaneLabel(repoRoot, wtPath),
		Split:        cfg.Herdr.SplitDirection(),
		Focus:        cfg.Herdr.FocusNewPane() && !getNoFocus,
	})
	if err != nil {
		// No pane was created, so release the lease and let get fall back.
		if relErr := pool.Release(poolDir, wtPath); relErr != nil {
			fmt.Fprintf(os.Stderr, "🌳 Warning: failed to release worktree after herdr error: %v\n", relErr)
		}
		fmt.Fprintf(os.Stderr, "🌳 Warning: could not open a herdr pane (%v); using subshell.\n", err)
		return errHerdrFallback
	}

	where := "a new herdr pane"
	if pane.ID != "" {
		where = fmt.Sprintf("herdr pane %s", pane.ID)
	}
	fmt.Fprintf(os.Stderr, "🌳 Opened worktree at %s in %s (leased, held by %s).\n",
		ui.PrettyPath(wtPath), where, herdr.LeaseHolder)
	fmt.Fprintf(os.Stderr, "🌳 It returns to the pool when you exit that pane, or run 'treehouse return %s'.\n",
		ui.PrettyPath(wtPath))
	return nil
}

// herdrPaneLabel builds a short, identifiable label for the herdr sidebar from
// the repo name and the worktree's pool slot, e.g. "myproject:1".
func herdrPaneLabel(repoRoot, wtPath string) string {
	slot := filepath.Base(filepath.Dir(wtPath))
	return filepath.Base(repoRoot) + ":" + slot
}

// holdAndReturn runs the interactive worktree session and returns the worktree
// to the pool when the shell exits. It backs both the classic in-place `get`
// subshell and the hidden __hold holder that runs inside a herdr pane.
func holdAndReturn(wtPath, poolDir string) error {
	fmt.Fprintf(os.Stderr, "🌳 Entered worktree at %s. Type 'exit' to return.\n", ui.PrettyPath(wtPath))

	env := []string{"TREEHOUSE_DIR=" + wtPath}
	if herdr.IsRuntime() {
		env = append(env, herdr.Marker)
		fmt.Fprintln(os.Stderr, herdr.RoutingMessage())
	}

	_, _ = shell.Spawn(wtPath, env)

	// Subshell exited - handle return.
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

// getLeaseRunE performs a non-interactive, durable acquire. It reserves a
// worktree, marks it leased in persistent state, prints only the worktree path
// to stdout, and routes every human-facing message to stderr so that
// `path=$(treehouse get --lease)` works cleanly in scripts.
func getLeaseRunE(repoRoot, poolDir string, cfg config.Config) error {
	holder := getLeaseHolder
	if holder == "" {
		holder = os.Getenv("TREEHOUSE_LEASE_HOLDER")
	}

	wtPath, err := pool.AcquireLease(repoRoot, poolDir, cfg.MaxTrees, cfg.Hooks.PostCreate, holder)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "🌳 Leased worktree at %s. Run 'treehouse return %s' to release it.\n",
		ui.PrettyPath(wtPath), ui.PrettyPath(wtPath))
	if herdr.IsRuntime() {
		fmt.Fprintln(os.Stderr, herdr.RoutingMessage())
	}
	// The bare path is the only thing on stdout, so callers can capture it.
	fmt.Fprintln(os.Stdout, wtPath)
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
