package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/process"
	"github.com/kunchenguid/treehouse/internal/shell"
	"github.com/kunchenguid/treehouse/internal/ui"
	"github.com/kunchenguid/treehouse/internal/vcs"
)

var (
	getLease       bool
	getLeaseHolder string
	getJSON        bool
	getNoFetch     bool
	getBase        string
	getIncludeFile string
)

// Process seams, overridable in tests, matching the pattern in internal/pool.
var (
	terminateWorktreeProcesses     = process.TerminateWorktreeProcesses
	unprotectedProcessesInWorktree = process.UnprotectedProcessesInWorktree
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
it with 'treehouse return <path>'.

Worktrees are cut from the branch treehouse infers from the repository. Pass
--base to cut this one from a different branch, or set base_branch in
treehouse.toml to change it for the whole pool. The worktree is still handed
over in detached HEAD; --base chooses the commit it starts at, it does not
create or check out a branch. A base that cannot be resolved is an error, never
a silent fall back to the inferred default.

Pass --include-file <path> to replace committed .worktreeinclude for this
acquisition. Relative paths use the current directory; patterns inside the file
select ignored, untracked files from the main checkout root. A missing or
unreadable file fails before a worktree is created or reset. An empty file
seeds nothing. Without the flag, only the committed manifest is used.`,
	RunE: getRunE,
}

func init() {
	getCmd.Flags().BoolVar(&getLease, "lease", false, "Durably lease a worktree without opening a subshell; print only its path to stdout")
	getCmd.Flags().StringVar(&getLeaseHolder, "lease-holder", "", "Optional label recorded as the lease holder (defaults to $TREEHOUSE_LEASE_HOLDER)")
	getCmd.Flags().BoolVar(&getJSON, "json", false, "Print lease allocation as JSON (requires --lease)")
	getCmd.Flags().BoolVar(&getNoFetch, "no-fetch", false, "Skip fetching origin before acquiring; use existing local refs")
	// No -b shorthand: git spells branch creation -b, and this creates nothing.
	getCmd.Flags().StringVar(&getBase, "base", "", "Branch to cut this worktree from, overriding base_branch in config (default: inferred from the repository)")
	getCmd.Flags().StringVar(&getIncludeFile, "include-file", "", "Replace committed .worktreeinclude with this file (relative to the current directory)")
	rootCmd.AddCommand(getCmd)
}

func getRunE(cmd *cobra.Command, args []string) error {
	if getJSON && !getLease {
		return fmt.Errorf("--json requires --lease")
	}

	var manifest []byte
	if cmd.Flags().Changed("include-file") {
		// Read once before acquisition can reset the checkout holding this file.
		var err error
		manifest, err = os.ReadFile(getIncludeFile)
		if err != nil {
			return fmt.Errorf("failed to read include file %q: %w", getIncludeFile, err)
		}
	}

	repoRoot, err := vcs.FindRepoRoot()
	if err != nil {
		return fmt.Errorf("not in a git or jj repository: %w", err)
	}
	repoRoot, err = vcs.FindMainRepoRoot()
	if err != nil {
		return fmt.Errorf("not in a git or jj repository: %w", err)
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	poolDir, err := config.ResolvePoolDir(repoRoot, config.ResolveRoot(rootFlag, cfg))
	if err != nil {
		return fmt.Errorf("failed to resolve pool directory: %w", err)
	}

	if err := config.EnsureExcluded(filepath.Dir(poolDir)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to update git exclude: %v\n", err)
	}

	if getLease {
		return getLeaseRunE(repoRoot, poolDir, cfg, manifest)
	}

	wtPath, err := pool.AcquireWithOptions(repoRoot, poolDir, cfg.MaxTrees, cfg.Hooks.PostCreate, pool.AcquireOptions{
		SkipFetch:       getNoFetch,
		BaseBranch:      resolveRequestedBase(cfg),
		IncludeManifest: manifest,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "🌳 Entered worktree at %s. Type 'exit' to return.\n", ui.PrettyPath(wtPath))

	env := []string{
		"TREEHOUSE_DIR=" + wtPath,
	}
	_, err = shell.Spawn(wtPath, env)

	// Subshell exited — handle return, but only while the slot still carries
	// this session's own reservation. A 'treehouse lease' taken over this
	// worktree while the shell was live replaces that reservation with a
	// durable lease, and returning anyway would reset the tree and clear the
	// lease that was protecting it. The detach runs under the same state lock
	// as this check so a takeover cannot land between them and move the HEAD of
	// a home the lease was protecting; the release below re-checks under its
	// own lock, which keeps the dirty prompt and the reset off the slot too.
	ownReservation := pool.ReleasePreconditions{RequireOwnedByCaller: true}
	if err := pool.ValidateReleasePreconditions(poolDir, wtPath, ownReservation, func() error {
		// A markerless slot must never be detached: dispatch on such a path
		// falls back to the configured backend, which in an in-project pool
		// would detach the HEAD of the repository ENCLOSING the pool.
		if vcs.WorktreeBackendName(wtPath) == "" {
			return nil
		}
		if err := vcs.DetachWorktree(wtPath); err != nil {
			return fmt.Errorf("failed to detach worktree HEAD: %w", err)
		}
		return nil
	}); err != nil {
		if errors.Is(err, pool.ErrOwnerPreconditionFailed) {
			fmt.Fprintf(os.Stderr, "🌳 Not returning %s to the pool: %v; leaving it exactly as it is.\n", ui.PrettyPath(wtPath), err)
			return nil
		}
		fmt.Fprintf(os.Stderr, "🌳 Warning: %v\n", err)
	}

	dirty, _ := vcs.IsDirty(wtPath)
	if dirty {
		fmt.Fprintf(os.Stderr, "🌳 Worktree has uncommitted changes.\n")

		ok, promptErr := ui.Confirm("Clean worktree and return to pool?", true)
		if promptErr != nil || !ok {
			// The same starvation an aborted `return` causes: the slot stays
			// dirty, so Acquire skips it and prune will not reclaim it. The
			// two ErrOwnerPreconditionFailed arms either side of this check
			// still exit 0 because a slot another session durably leased was
			// never this session's to return - not returning it is the
			// designed outcome.
			return withExitCode(ExitNotReturned, fmt.Errorf(
				"worktree left dirty and not returned to the pool; prune will not reclaim this slot. Use treehouse return --force %s to clean it later",
				quoteReturnPath(wtPath)))
		}
	}

	if err := returnWorktreeToPool(poolDir, wtPath, releaseBaseBranch(repoRoot, cfg), ownReservation); err != nil {
		if errors.Is(err, pool.ErrOwnerPreconditionFailed) {
			fmt.Fprintf(os.Stderr, "🌳 Not returning %s to the pool: %v; leaving it exactly as it is.\n", ui.PrettyPath(wtPath), err)
			return nil
		}
		fmt.Fprintf(os.Stderr, "🌳 Warning: %v; leaving worktree in place.\n", err)
		return err
	}
	fmt.Fprintln(os.Stderr, "🌳 Worktree returned to pool.")

	return nil
}

// returnWorktreeToPool terminates any lingering writers and resets the worktree
// back into the pool as a single locked transaction: killLingeringProcesses runs
// as the release's beforeReset step, under the same state lock and immediately
// before the reset, so a writer that re-enters the worktree cannot slip between
// the emptiness check and the destructive reset.
func returnWorktreeToPool(poolDir, wtPath, baseBranch string, preconditions pool.ReleasePreconditions) error {
	return pool.ReleaseConditional(poolDir, wtPath, baseBranch, preconditions, func() error {
		return killLingeringProcesses(wtPath)
	})
}

// resolveRequestedBase returns the base this invocation asks for: the --base
// flag, then base_branch from config, then empty to infer it.
func resolveRequestedBase(cfg config.Config) string {
	if getBase != "" {
		return getBase
	}
	return cfg.BaseBranch
}

// releaseBaseBranch returns the branch a returned worktree is parked on: the
// configured base, or "" for the repository default. It reads config rather
// than this invocation's --base because parking decides what the NEXT acquire
// can recycle. An unresolvable base warns and falls back, so a typo cannot
// strand the reservation; get reports it as a real error.
func releaseBaseBranch(repoRoot string, cfg config.Config) string {
	if cfg.BaseBranch == "" {
		return ""
	}
	if err := vcs.VerifyBaseBranch(repoRoot, cfg.BaseBranch); err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: cannot return the worktree to base branch %q (%v); using the repository default instead.\n", cfg.BaseBranch, err)
		return ""
	}
	return cfg.BaseBranch
}

// getLeaseRunE performs a non-interactive, durable acquire. It writes either the
// worktree path or the requested JSON allocation to stdout and routes every
// human-facing message to stderr, keeping both output modes machine-readable.
func getLeaseRunE(repoRoot, poolDir string, cfg config.Config, manifest []byte) error {
	holder := getLeaseHolder
	if holder == "" {
		holder = os.Getenv("TREEHOUSE_LEASE_HOLDER")
	}

	lease, err := pool.AcquireLeaseInfoWithOptions(repoRoot, poolDir, cfg.MaxTrees, cfg.Hooks.PostCreate, holder, pool.AcquireOptions{
		SkipFetch:       getNoFetch,
		BaseBranch:      resolveRequestedBase(cfg),
		IncludeManifest: manifest,
	})
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
// worktree, then verifies the worktree is clear before it is handed back to the
// pool. Detached tools (e.g. opencode servers that ignore SIGHUP) are the usual
// reason a worktree is not quiet on return.
//
// It returns an error when termination cannot be proven complete: a process
// could not be signalled (e.g. an ancestry-lookup failure), or a live writer
// remains after termination. The point-in-time selection inside
// TerminateWorktreeProcesses signals only what a single scan saw, so a process
// that started during the grace period is never targeted; the re-scan below
// catches it. Failing here makes the caller leave the slot in place rather than
// reset a worktree another process is still writing into.
func killLingeringProcesses(wtPath string) error {
	killed, err := terminateWorktreeProcesses(wtPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("could not terminate worktree processes: %w", err)
	}
	if len(killed) > 0 {
		names := make([]string, len(killed))
		for i, p := range killed {
			names[i] = p.String()
		}
		fmt.Fprintf(os.Stderr, "🌳 Terminated lingering processes: %s\n", strings.Join(names, ", "))
	}

	survivors, err := unprotectedProcessesInWorktree(wtPath)
	if err != nil {
		return fmt.Errorf("could not verify worktree processes stopped: %w", err)
	}
	if len(survivors) > 0 {
		names := make([]string, len(survivors))
		for i, p := range survivors {
			names[i] = p.String()
		}
		return fmt.Errorf("worktree still has live processes after termination: %s", strings.Join(names, ", "))
	}
	return nil
}
