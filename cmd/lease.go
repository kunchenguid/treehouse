package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/v3/internal/config"
	"github.com/kunchenguid/treehouse/v3/internal/pool"
	"github.com/kunchenguid/treehouse/v3/internal/ui"
	"github.com/kunchenguid/treehouse/v3/internal/vcs"
)

var (
	leaseHolderFlag string
	leaseJSONFlag   bool
)

var leaseCmd = &cobra.Command{
	Use:   "lease <name>",
	Short: "Durably lease an existing pool worktree in place, without touching it",
	Long: `Mark an existing pool worktree, identified by its name (the number shown
by 'treehouse status'), as durably leased using the same persistent lease
state 'treehouse get --lease' writes.

This is a state-only operation: lease never resets, fetches, cleans, or
checks out the worktree, so it is safe on a slot that already holds live
work. Use it to give a worktree acquired with plain 'get' - or created
before leases existed - the same protection a leased acquisition has: a
leased worktree is never handed out by a later get and never removed by
prune or destroy, even with no process running inside it, until you release
it with 'treehouse return <path>'.

By default it prints only the worktree's absolute path to stdout; add --json
for the lease identity and metadata. All banners go to stderr.`,
	Args: cobra.ExactArgs(1),
	RunE: leaseRunE,
}

func init() {
	leaseCmd.Flags().StringVar(&leaseHolderFlag, "lease-holder", "", "Optional label recorded as the lease holder (defaults to $TREEHOUSE_LEASE_HOLDER)")
	leaseCmd.Flags().BoolVar(&leaseJSONFlag, "json", false, "Print the lease as JSON")
	rootCmd.AddCommand(leaseCmd)
}

func leaseRunE(cmd *cobra.Command, args []string) error {
	holder := leaseHolderFlag
	if holder == "" {
		holder = os.Getenv("TREEHOUSE_LEASE_HOLDER")
	}

	repoRoot, err := vcs.FindMainRepoRoot()
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

	lease, err := pool.LeaseExisting(poolDir, args[0], holder)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "🌳 Leased worktree %s at %s. Run 'treehouse return %s' to release it.\n",
		args[0], ui.PrettyPath(lease.Path), ui.PrettyPath(lease.Path))
	if leaseJSONFlag {
		return json.NewEncoder(os.Stdout).Encode(lease)
	}
	// The bare path is the only thing on stdout, so callers can capture it.
	fmt.Fprintln(os.Stdout, lease.Path)
	return nil
}
