package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var (
	destroyForce bool
	destroyAll   bool
)

var destroyCmd = &cobra.Command{
	Use:   "destroy [path]",
	Short: "Remove worktrees from the pool",
	RunE: func(cmd *cobra.Command, args []string) error {
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

		if destroyAll {
			if !destroyForce {
				ok, err := ui.Confirm("Destroy all worktrees in the pool?", false)
				if err != nil || !ok {
					fmt.Fprintln(os.Stderr, "🌳 Aborted.")
					return nil
				}
			}
			if err := pool.DestroyAll(repoRoot, poolDir, destroyForce); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "🌳 All worktrees destroyed.")
			return nil
		}

		if len(args) == 0 {
			return fmt.Errorf("specify a worktree path or use --all")
		}

		wtPath, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}

		if !destroyForce {
			ok, err := ui.Confirm(fmt.Sprintf("Destroy worktree %s?", ui.PrettyPath(wtPath)), false)
			if err != nil || !ok {
				fmt.Fprintln(os.Stderr, "🌳 Aborted.")
				return nil
			}
		}

		if err := pool.Destroy(repoRoot, poolDir, wtPath, destroyForce); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "🌳 Worktree destroyed.")
		return nil
	},
}

func init() {
	destroyCmd.Flags().BoolVar(&destroyForce, "force", false, "Force destroy even if in-use")
	destroyCmd.Flags().BoolVar(&destroyAll, "all", false, "Destroy all worktrees in the pool")
	rootCmd.AddCommand(destroyCmd)
}
