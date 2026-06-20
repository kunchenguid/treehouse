package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var (
	pruneYes    bool
	pruneAll    bool
	pruneGlobal bool
)

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove stale idle worktrees from the pool",
	Long: `Remove stale idle worktrees from the pool to reclaim disk space.

A worktree is stale only when treehouse manages it, no owner reservation or
running process is using it, it has no uncommitted changes, and its HEAD is
already merged into the default branch.

Prune is a dry run by default. Pass --yes to delete the listed worktrees.
Pass --all or --global to sweep every managed pool under the user-level
treehouse root from any directory. Global prune derives each worktree's owning
repository from git metadata and requires the configured root to be unset or
absolute.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if pruneAll || pruneGlobal {
			cfg, err := config.LoadGlobal()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			poolRoot, err := config.ResolvePoolRoot("", cfg.Root)
			if err != nil {
				return err
			}

			result, err := pool.PruneAll(poolRoot, !pruneYes, cfg.Hooks.PreDestroy)
			if err != nil {
				return err
			}

			printPruneAllResult(os.Stdout, result, !pruneYes)
			return nil
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
			return err
		}

		result, err := pool.Prune(repoRoot, poolDir, !pruneYes, cfg.Hooks.PreDestroy)
		if err != nil {
			return err
		}

		printPruneResult(os.Stdout, result, !pruneYes)
		return nil
	},
}

func init() {
	pruneCmd.Flags().BoolVar(&pruneYes, "yes", false, "Delete stale worktrees instead of doing a dry run")
	pruneCmd.Flags().BoolVar(&pruneAll, "all", false, "Prune stale worktrees across every managed pool under the user-level treehouse root")
	pruneCmd.Flags().BoolVar(&pruneGlobal, "global", false, "Alias for --all")
	rootCmd.AddCommand(pruneCmd)
}

func printPruneResult(w io.Writer, result pool.PruneResult, dryRun bool) {
	if dryRun {
		if len(result.Candidates) == 0 {
			fmt.Fprintln(w, "🌳 No stale worktrees to prune.")
			printPruneSkipped(w, result.Skipped)
			return
		}

		fmt.Fprintf(w, "🌳 Dry run: would prune %d stale %s and reclaim %s.\n",
			len(result.Candidates),
			plural("worktree", len(result.Candidates)),
			formatBytes(result.ReclaimableBytes),
		)
		printPruneWorktrees(w, result.Candidates)
		printPruneSkipped(w, result.Skipped)
		fmt.Fprintln(w, "🌳 Re-run with --yes to delete these worktrees.")
		return
	}

	if len(result.Pruned) == 0 {
		fmt.Fprintln(w, "🌳 No stale worktrees pruned.")
		printPruneSkipped(w, result.Skipped)
		return
	}

	fmt.Fprintf(w, "🌳 Pruned %d stale %s and freed %s.\n",
		len(result.Pruned),
		plural("worktree", len(result.Pruned)),
		formatBytes(result.FreedBytes),
	)
	printPruneWorktrees(w, result.Pruned)
	printPruneSkipped(w, result.Skipped)
}

func printPruneAllResult(w io.Writer, result pool.PruneAllResult, dryRun bool) {
	poolCount := len(result.Pools)
	if dryRun {
		if len(result.Result.Candidates) == 0 {
			fmt.Fprintf(w, "🌳 No stale worktrees to prune across %d %s.\n", poolCount, plural("pool", poolCount))
			printPruneSkipped(w, result.Result.Skipped)
			return
		}

		fmt.Fprintf(w, "🌳 Dry run: would prune %d stale %s across %d %s and reclaim %s.\n",
			len(result.Result.Candidates),
			plural("worktree", len(result.Result.Candidates)),
			poolCount,
			plural("pool", poolCount),
			formatBytes(result.Result.ReclaimableBytes),
		)
		printPruneWorktrees(w, result.Result.Candidates)
		printPruneSkipped(w, result.Result.Skipped)
		fmt.Fprintln(w, "🌳 Re-run with --all --yes to delete these worktrees.")
		return
	}

	if len(result.Result.Pruned) == 0 {
		fmt.Fprintf(w, "🌳 No stale worktrees pruned across %d %s.\n", poolCount, plural("pool", poolCount))
		printPruneSkipped(w, result.Result.Skipped)
		return
	}

	fmt.Fprintf(w, "🌳 Pruned %d stale %s across %d %s and freed %s.\n",
		len(result.Result.Pruned),
		plural("worktree", len(result.Result.Pruned)),
		poolCount,
		plural("pool", poolCount),
		formatBytes(result.Result.FreedBytes),
	)
	printPruneWorktrees(w, result.Result.Pruned)
	printPruneSkipped(w, result.Result.Skipped)
}

func printPruneWorktrees(w io.Writer, worktrees []pool.PruneWorktree) {
	sizeWidth := 0
	sizes := make([]string, len(worktrees))
	for i, wt := range worktrees {
		sizes[i] = formatBytes(wt.Bytes)
		if len(sizes[i]) > sizeWidth {
			sizeWidth = len(sizes[i])
		}
	}

	for i, wt := range worktrees {
		fmt.Fprintf(w, "%-4s  %*s  %s\n", wt.Name, sizeWidth, sizes[i], ui.PrettyPath(wt.Path))
	}
}

func printPruneSkipped(w io.Writer, skipped []pool.PruneSkipped) {
	if len(skipped) == 0 {
		return
	}

	fmt.Fprintf(w, "🌳 Skipped %d unsafe idle %s:\n", len(skipped), plural("worktree", len(skipped)))
	reasonWidth := 0
	for _, wt := range skipped {
		if len(wt.Reason) > reasonWidth {
			reasonWidth = len(wt.Reason)
		}
	}
	for _, wt := range skipped {
		fmt.Fprintf(w, "%-4s  %-*s  %s\n", wt.Name, reasonWidth, wt.Reason, ui.PrettyPath(wt.Path))
	}
}

func plural(word string, count int) string {
	if count == 1 {
		return word
	}
	return word + "s"
}

func formatBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}

	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(bytes)
	unit := "B"
	for _, next := range units {
		value /= 1024
		unit = next
		if value < 1024 {
			break
		}
	}

	formatted := fmt.Sprintf("%.1f", value)
	formatted = strings.TrimSuffix(strings.TrimSuffix(formatted, "0"), ".")
	return formatted + " " + unit
}
