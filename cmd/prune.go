package cmd

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var (
	pruneYes     bool
	pruneAll     bool
	pruneGlobal  bool
	pruneOrphans bool
	pruneVerbose bool
)

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove stale worktrees and opted-in orphans from the pool",
	Long: `Remove stale worktrees and opted-in orphans from the pool to reclaim disk space.

A worktree is stale only when treehouse manages it, no owner reservation or
running process is using it, it has no uncommitted changes, and its HEAD is
already merged into the default branch.

Prune is a dry run by default. Pass --yes to delete the listed candidates.
Backing-repository-missing orphans are reported by default. Pass --prune-orphans
to include them as unverified candidates, and combine it with --yes to delete
them.
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

			result, err := pool.PruneAllWithOptions(poolRoot, pool.PruneOptions{
				DryRun:       !pruneYes,
				PruneOrphans: pruneOrphans,
				PreDestroy:   cfg.Hooks.PreDestroy,
			})
			if err != nil {
				return err
			}

			printPruneAllResult(os.Stdout, result, !pruneYes, pruneOrphans, pruneVerbose)
			return nil
		}

		repoRoot, err := git.ResolveWorkDir("")
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

		result, err := pool.PruneWithOptions(repoRoot, poolDir, pool.PruneOptions{
			DryRun:       !pruneYes,
			PruneOrphans: pruneOrphans,
			PreDestroy:   cfg.Hooks.PreDestroy,
		})
		if err != nil {
			return err
		}

		printPruneResult(os.Stdout, result, !pruneYes, pruneOrphans, pruneVerbose)
		return nil
	},
}

func init() {
	pruneCmd.Flags().BoolVar(&pruneYes, "yes", false, "Delete listed prune candidates instead of doing a dry run")
	pruneCmd.Flags().BoolVar(&pruneAll, "all", false, "Sweep every managed pool under the user-level treehouse root")
	pruneCmd.Flags().BoolVar(&pruneGlobal, "global", false, "Alias for --all")
	pruneCmd.Flags().BoolVar(&pruneOrphans, "prune-orphans", false, "Include backing-repository-missing orphaned worktrees in prune candidates")
	pruneCmd.Flags().BoolVarP(&pruneVerbose, "verbose", "v", false, "Show detailed skip diagnostics")
	rootCmd.AddCommand(pruneCmd)
}

func printPruneResult(w io.Writer, result pool.PruneResult, dryRun bool, pruneOrphans bool, verbose bool) {
	if dryRun {
		if len(result.Candidates) == 0 {
			fmt.Fprintln(w, "🌳 No stale worktrees to prune.")
			printPruneSkipped(w, result.Skipped, verbose)
			printPruneOrphanHint(w, result.Skipped, false, pruneOrphans, false)
			return
		}

		fmt.Fprintf(w, "🌳 Dry run: would prune %d %s %s and reclaim %s.\n",
			len(result.Candidates),
			pruneCandidateKind(result.Candidates),
			plural("worktree", len(result.Candidates)),
			formatBytes(result.ReclaimableBytes),
		)
		printPruneWorktrees(w, result.Candidates)
		printPruneSkipped(w, result.Skipped, verbose)
		fmt.Fprintf(w, "🌳 Re-run with %s to delete these worktrees.\n", pruneDeleteFlags(false, pruneOrphans))
		printPruneOrphanHint(w, result.Skipped, false, pruneOrphans, false)
		return
	}

	if len(result.Pruned) == 0 {
		fmt.Fprintln(w, "🌳 No stale worktrees pruned.")
		printPruneSkipped(w, result.Skipped, verbose)
		printPruneOrphanHint(w, result.Skipped, false, pruneOrphans, true)
		return
	}

	fmt.Fprintf(w, "🌳 Pruned %d %s %s and freed %s.\n",
		len(result.Pruned),
		pruneCandidateKind(result.Pruned),
		plural("worktree", len(result.Pruned)),
		formatBytes(result.FreedBytes),
	)
	printPruneWorktrees(w, result.Pruned)
	printPruneSkipped(w, result.Skipped, verbose)
	printPruneOrphanHint(w, result.Skipped, false, pruneOrphans, true)
}

func printPruneAllResult(w io.Writer, result pool.PruneAllResult, dryRun bool, pruneOrphans bool, verbose bool) {
	poolCount := len(result.Pools)
	if dryRun {
		if len(result.Result.Candidates) == 0 {
			fmt.Fprintf(w, "🌳 No stale worktrees to prune across %d %s.\n", poolCount, plural("pool", poolCount))
			printPruneSkipped(w, result.Result.Skipped, verbose)
			printPruneOrphanHint(w, result.Result.Skipped, true, pruneOrphans, false)
			return
		}

		fmt.Fprintf(w, "🌳 Dry run: would prune %d %s %s across %d %s and reclaim %s.\n",
			len(result.Result.Candidates),
			pruneCandidateKind(result.Result.Candidates),
			plural("worktree", len(result.Result.Candidates)),
			poolCount,
			plural("pool", poolCount),
			formatBytes(result.Result.ReclaimableBytes),
		)
		printPruneWorktrees(w, result.Result.Candidates)
		printPruneSkipped(w, result.Result.Skipped, verbose)
		fmt.Fprintf(w, "🌳 Re-run with %s to delete these worktrees.\n", pruneDeleteFlags(true, pruneOrphans))
		printPruneOrphanHint(w, result.Result.Skipped, true, pruneOrphans, false)
		return
	}

	if len(result.Result.Pruned) == 0 {
		fmt.Fprintf(w, "🌳 No stale worktrees pruned across %d %s.\n", poolCount, plural("pool", poolCount))
		printPruneSkipped(w, result.Result.Skipped, verbose)
		printPruneOrphanHint(w, result.Result.Skipped, true, pruneOrphans, true)
		return
	}

	fmt.Fprintf(w, "🌳 Pruned %d %s %s across %d %s and freed %s.\n",
		len(result.Result.Pruned),
		pruneCandidateKind(result.Result.Pruned),
		plural("worktree", len(result.Result.Pruned)),
		poolCount,
		plural("pool", poolCount),
		formatBytes(result.Result.FreedBytes),
	)
	printPruneWorktrees(w, result.Result.Pruned)
	printPruneSkipped(w, result.Result.Skipped, verbose)
	printPruneOrphanHint(w, result.Result.Skipped, true, pruneOrphans, true)
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
		fmt.Fprintf(w, "%-4s  %*s  %s", wt.Name, sizeWidth, sizes[i], ui.PrettyPath(wt.Path))
		if wt.Warning != "" {
			fmt.Fprintf(w, "  %s", wt.Warning)
		}
		fmt.Fprintln(w)
	}
}

func printPruneSkipped(w io.Writer, skipped []pool.PruneSkipped, verbose bool) {
	if len(skipped) == 0 {
		return
	}

	fmt.Fprintf(w, "🌳 Skipped %d unsafe idle %s:\n", len(skipped), plural("worktree", len(skipped)))
	for _, category := range orderedPruneSkipCategories(skipped) {
		group := pruneSkipGroup(skipped, category)
		fmt.Fprintf(w, "  %s:\n", category)
		reasonWidth := 0
		for _, wt := range group {
			if pruneSkipShowsReason(wt) && len(wt.Reason) > reasonWidth {
				reasonWidth = len(wt.Reason)
			}
		}
		for _, wt := range group {
			if pruneSkipShowsReason(wt) {
				fmt.Fprintf(w, "  %-4s  %-*s  %s\n", wt.Name, reasonWidth, wt.Reason, ui.PrettyPath(wt.Path))
			} else {
				fmt.Fprintf(w, "  %-4s  %s\n", wt.Name, ui.PrettyPath(wt.Path))
			}
			if verbose && wt.Detail != "" {
				fmt.Fprintf(w, "        detail: %s\n", wt.Detail)
			}
		}
	}
}

func pruneSkipShowsReason(wt pool.PruneSkipped) bool {
	return wt.Reason != "" && wt.Reason != pruneSkipCategory(wt)
}

func pruneSkipCategory(wt pool.PruneSkipped) string {
	if wt.Category != "" {
		return wt.Category
	}
	if wt.Reason != "" {
		return wt.Reason
	}
	return "cannot verify worktree"
}

func orderedPruneSkipCategories(skipped []pool.PruneSkipped) []string {
	groups := make(map[string]struct{})
	for _, wt := range skipped {
		groups[pruneSkipCategory(wt)] = struct{}{}
	}

	preferred := []string{
		pool.PruneSkipUncommitted,
		pool.PruneSkipUnmerged,
		pool.PruneSkipOrphanedBackingRepo,
		pool.PruneSkipOriginUnreachable,
	}
	var ordered []string
	for _, category := range preferred {
		if _, ok := groups[category]; ok {
			ordered = append(ordered, category)
			delete(groups, category)
		}
	}
	var remaining []string
	for category := range groups {
		remaining = append(remaining, category)
	}
	sort.Strings(remaining)
	return append(ordered, remaining...)
}

func pruneSkipGroup(skipped []pool.PruneSkipped, category string) []pool.PruneSkipped {
	var group []pool.PruneSkipped
	for _, wt := range skipped {
		if pruneSkipCategory(wt) == category {
			group = append(group, wt)
		}
	}
	return group
}

func pruneCandidateKind(worktrees []pool.PruneWorktree) string {
	for _, wt := range worktrees {
		if wt.Orphaned {
			return "stale/orphaned"
		}
	}
	return "stale"
}

func pruneDeleteFlags(all bool, pruneOrphans bool) string {
	var flags []string
	if all {
		flags = append(flags, "--all")
	}
	if pruneOrphans {
		flags = append(flags, "--prune-orphans")
	}
	flags = append(flags, "--yes")
	return strings.Join(flags, " ")
}

func printPruneOrphanHint(w io.Writer, skipped []pool.PruneSkipped, all bool, pruneOrphans bool, yes bool) {
	if pruneOrphans || !hasSkippedCategory(skipped, pool.PruneSkipOrphanedBackingRepo) {
		return
	}
	if yes {
		fmt.Fprintf(w, "🌳 Re-run with %s to delete true orphans whose backing repository is missing.\n", pruneDeleteFlags(all, true))
		return
	}
	flags := "--prune-orphans"
	if all {
		flags = "--all --prune-orphans"
	}
	fmt.Fprintf(w, "🌳 Re-run with %s to include true orphans in the dry run; add --yes to delete them.\n", flags)
}

func hasSkippedCategory(skipped []pool.PruneSkipped, category string) bool {
	for _, wt := range skipped {
		if pruneSkipCategory(wt) == category {
			return true
		}
	}
	return false
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
