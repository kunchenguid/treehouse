package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var (
	destroyAll             bool
	destroyYes             bool
	destroyIncludeUnlanded bool
	destroyIncludeInUse    bool
	destroyIncludeLeased   bool
)

var destroyCmd = &cobra.Command{
	Use:   "destroy <path> [--all]",
	Short: "Remove worktrees from the pool, safely by default",
	Long: `Remove worktrees from the pool. Destroy is the deliberate tool for removing a
worktree even though it still has unlanded work, but it is safe by default.

Targets are narrow and explicit:

  treehouse destroy <worktree-path>        Target exactly one worktree.
  treehouse destroy <pool-path> --all      Target all worktrees in THAT pool.

There is no cross-pool or global destroy; --all without a pool path is an error.

Destroy is a dry run by default: it prints a risk-revealing preview and removes
nothing. Pass --yes to execute.

A bare destroy removes only the genuinely disposable set (merged, clean, idle,
unleased) and SKIPS everything else, telling you which flag would include it.
Each risky class is opt-in:

  --include-unlanded   Also remove dirty, unmerged, or unverified worktrees
                       (DATA LOSS).
  --include-in-use     Also remove worktrees with a running process or owner
                       reservation (processes terminated cleanly first).
  --include-leased     Also remove a leased worktree. Honored only when the exact
                       worktree path is named; leased worktrees are NEVER removed
                       by --all.

Single-target skips for missing flags exit non-zero so scripts notice. Bulk
--all skips are normal and exit zero.

Migrating from the removed --force flag: replace it with the specific
--include-* flag(s) for the risk you intend to override, plus --yes.`,
	RunE: destroyRunE,
}

func init() {
	destroyCmd.Flags().BoolVar(&destroyAll, "all", false, "Remove all worktrees in the named pool (requires a pool path)")
	destroyCmd.Flags().BoolVar(&destroyYes, "yes", false, "Execute the removal instead of doing a dry run")
	destroyCmd.Flags().BoolVar(&destroyIncludeUnlanded, "include-unlanded", false, "Also remove dirty, unmerged, or unverified worktrees (irreversible data loss)")
	destroyCmd.Flags().BoolVar(&destroyIncludeInUse, "include-in-use", false, "Also remove worktrees with a running process or owner reservation (processes terminated cleanly first)")
	destroyCmd.Flags().BoolVar(&destroyIncludeLeased, "include-leased", false, "Also remove a leased worktree; only when the exact path is named, never via --all")
	rootCmd.AddCommand(destroyCmd)
}

func destroyRunE(cmd *cobra.Command, args []string) error {
	if destroyIncludeLeased && destroyAll {
		return errors.New("--include-leased cannot be combined with --all; name the exact worktree path instead (leased worktrees are never removed in bulk)")
	}

	preDestroy, err := destroyPreDestroyHooks()
	if err != nil {
		return err
	}

	opts := pool.DestroyOptions{
		DryRun:          !destroyYes,
		IncludeUnlanded: destroyIncludeUnlanded,
		IncludeInUse:    destroyIncludeInUse,
		IncludeLeased:   destroyIncludeLeased,
		PreDestroy:      preDestroy,
	}

	if destroyAll {
		if len(args) == 0 {
			return errors.New("--all requires a pool path; name the pool to clear, e.g. 'treehouse destroy . --all'")
		}
		poolDir, err := resolveDestroyPoolFromTarget(args[0])
		if err != nil {
			return err
		}
		result, err := pool.DestroyPool(poolDir, opts)
		if err != nil {
			return err
		}
		printDestroyResult(os.Stdout, result, opts, ui.PrettyPath(poolDir), true)
		return nil
	}

	if len(args) == 0 {
		return errors.New("specify a worktree path to destroy, or a pool path with --all")
	}

	wtPath, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	poolDir, err := resolveDestroyPoolFromWorktree(wtPath)
	if err != nil {
		return err
	}
	result, err := pool.DestroyWorktree(poolDir, wtPath, opts)
	if err != nil {
		return err
	}
	printDestroyResult(os.Stdout, result, opts, ui.PrettyPath(wtPath), false)
	return destroySingleExit(result, opts)
}

// destroyPreDestroyHooks returns the user-level pre_destroy hooks. Destroy can
// target a pool in another repository, and pre_destroy hooks are user-level
// only, so it always loads them globally.
func destroyPreDestroyHooks() ([]string, error) {
	cfg, err := config.LoadGlobal()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return cfg.Hooks.PreDestroy, nil
}

// resolveDestroyPoolFromWorktree resolves the managed pool that owns a single
// named worktree, ascending to the pool directory or falling back to the
// repository-configured pool.
func resolveDestroyPoolFromWorktree(wtPath string) (string, error) {
	poolDir, err := resolveReturnPoolDir(wtPath, true)
	if err != nil {
		if errors.Is(err, errReturnWorktreeUnmanaged) {
			return "", fmt.Errorf("worktree %s is not managed by treehouse", wtPath)
		}
		return "", err
	}
	return poolDir, nil
}

// resolveDestroyPoolFromTarget resolves the pool named by a --all target. The
// target may be the pool directory itself, a worktree inside a pool, or a
// repository (resolved via its treehouse config).
func resolveDestroyPoolFromTarget(targetPath string) (string, error) {
	abs, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	if pool.IsPoolDir(abs) {
		return abs, nil
	}
	if candidate := filepath.Dir(filepath.Dir(abs)); pool.IsPoolDir(candidate) {
		return candidate, nil
	}

	repoRoot, err := git.FindMainRepoRootFrom(abs)
	if err != nil {
		return "", fmt.Errorf("cannot resolve a treehouse pool from %s: not a pool directory or git repository", targetPath)
	}
	cfg, err := config.Load(repoRoot)
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}
	return config.ResolvePoolDir(repoRoot, cfg.Root)
}

// destroySingleExit makes a single named destruction fail loudly when the
// requested worktree was not removed, so scripts notice that a flag was missing.
func destroySingleExit(result pool.DestroyResult, opts pool.DestroyOptions) error {
	if opts.DryRun || len(result.Destroyed) > 0 {
		return nil
	}
	for _, skip := range result.Skipped {
		if skip.LeasedBulk {
			continue
		}
		if len(skip.NeededFlags) > 0 {
			return fmt.Errorf("did not destroy %s (%s); re-run with %s",
				skip.Target.Name, skip.Target.Class, strings.Join(skip.NeededFlags, " "))
		}
		if skip.NeededFlag != "" {
			return fmt.Errorf("did not destroy %s (%s); re-run with %s",
				skip.Target.Name, skip.Target.Class, skip.NeededFlag)
		}
		return fmt.Errorf("did not destroy %s: %s", skip.Target.Name, skip.Target.Detail)
	}
	return nil
}

func printDestroyResult(w io.Writer, result pool.DestroyResult, opts pool.DestroyOptions, scope string, all bool) {
	if opts.DryRun {
		if len(result.Planned) == 0 {
			if len(result.Skipped) == 0 {
				fmt.Fprintf(w, "🌳 Nothing to destroy in %s.\n", scope)
				return
			}
			fmt.Fprintf(w, "🌳 Dry run: nothing disposable to destroy in %s.\n", scope)
			printDestroySkipped(w, result.Skipped)
			return
		}

		fmt.Fprintf(w, "🌳 Dry run: would destroy %d %s in %s and reclaim %s.\n",
			len(result.Planned), plural("worktree", len(result.Planned)), scope, formatBytes(result.PlannedBytes))
		printDestroyTargets(w, result.Planned)
		printDestroySkipped(w, result.Skipped)
		fmt.Fprintf(w, "🌳 Re-run with %s to destroy %s.\n",
			destroyExecuteFlags(opts), plural("this worktree", len(result.Planned)))
		return
	}

	if len(result.Destroyed) == 0 {
		if len(result.Skipped) == 0 {
			fmt.Fprintf(w, "🌳 Nothing to destroy in %s.\n", scope)
			return
		}
		fmt.Fprintf(w, "🌳 Destroyed 0 worktrees in %s.\n", scope)
		printDestroySkipped(w, result.Skipped)
		return
	}

	fmt.Fprintf(w, "🌳 Destroyed %d %s in %s and freed %s.\n",
		len(result.Destroyed), plural("worktree", len(result.Destroyed)), scope, formatBytes(result.FreedBytes))
	printDestroyTargets(w, result.Destroyed)
	printDestroySkipped(w, result.Skipped)
}

func printDestroyTargets(w io.Writer, targets []pool.DestroyTarget) {
	tagWidth, sizeWidth := 0, 0
	tags := make([]string, len(targets))
	sizes := make([]string, len(targets))
	for i, t := range targets {
		tags[i] = destroyTag(t)
		sizes[i] = formatBytes(t.Bytes)
		if len(tags[i]) > tagWidth {
			tagWidth = len(tags[i])
		}
		if len(sizes[i]) > sizeWidth {
			sizeWidth = len(sizes[i])
		}
	}

	for i, t := range targets {
		fmt.Fprintf(w, "  %-4s  %-*s  %*s  %s", t.Name, tagWidth, tags[i], sizeWidth, sizes[i], ui.PrettyPath(t.Path))
		if t.Detail != "" {
			fmt.Fprintf(w, "  (%s)", t.Detail)
		}
		fmt.Fprintln(w)
	}
}

func printDestroySkipped(w io.Writer, skipped []pool.DestroySkip) {
	if len(skipped) == 0 {
		return
	}

	fmt.Fprintf(w, "🌳 Skipped %d %s:\n", len(skipped), plural("worktree", len(skipped)))
	tagWidth := 0
	tags := make([]string, len(skipped))
	for i, s := range skipped {
		tags[i] = destroyTag(s.Target)
		if len(tags[i]) > tagWidth {
			tagWidth = len(tags[i])
		}
	}
	for i, s := range skipped {
		fmt.Fprintf(w, "  %-4s  %-*s  %s  %s\n",
			s.Target.Name, tagWidth, tags[i], ui.PrettyPath(s.Target.Path), destroySkipHint(s))
	}
}

func destroySkipHint(s pool.DestroySkip) string {
	if s.LeasedBulk {
		return "leased: name the exact path with " + pool.IncludeLeasedFlag + " (never removed by --all)"
	}
	if len(s.NeededFlags) > 0 {
		return "re-run with " + strings.Join(s.NeededFlags, " ") + " to include"
	}
	if s.NeededFlag != "" {
		return "re-run with " + s.NeededFlag + " to include"
	}
	if s.Target.Detail != "" {
		return s.Target.Detail
	}
	return "left in place"
}

func destroyTag(t pool.DestroyTarget) string {
	classes := t.Classes
	if len(classes) == 0 {
		classes = []pool.DestroyClass{t.Class}
	}
	parts := make([]string, 0, len(classes))
	for _, class := range classes {
		if class == pool.DestroyInUse && len(t.Processes) > 0 {
			pids := make([]string, len(t.Processes))
			for i, p := range t.Processes {
				pids[i] = strconv.Itoa(int(p.PID))
			}
			parts = append(parts, "in-use:"+strings.Join(pids, ","))
			continue
		}
		parts = append(parts, string(class))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func destroyExecuteFlags(opts pool.DestroyOptions) string {
	flags := []string{"--yes"}
	if opts.IncludeUnlanded {
		flags = append(flags, pool.IncludeUnlandedFlag)
	}
	if opts.IncludeInUse {
		flags = append(flags, pool.IncludeInUseFlag)
	}
	if opts.IncludeLeased {
		flags = append(flags, pool.IncludeLeasedFlag)
	}
	return strings.Join(flags, " ")
}
