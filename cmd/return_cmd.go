package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/atinylittleshell/treehouse/internal/config"
	"github.com/atinylittleshell/treehouse/internal/git"
	"github.com/atinylittleshell/treehouse/internal/pool"
	"github.com/atinylittleshell/treehouse/internal/ui"
)

var returnForce bool

var returnCmd = &cobra.Command{
	Use:   "return [path]",
	Short: "Return a worktree to the pool",
	RunE: func(cmd *cobra.Command, args []string) error {
		wtPath, err := resolveWorktreePath(args)
		if err != nil {
			return err
		}

		repoRoot, err := git.FindRepoRoot()
		if err != nil {
			return fmt.Errorf("not in a git repository: %w", err)
		}

		poolDir, err := config.ResolvePoolDir(repoRoot)
		if err != nil {
			return err
		}

		entry, err := pool.FindByPath(poolDir, wtPath)
		if err != nil || entry == nil {
			return fmt.Errorf("worktree %s is not managed by treehouse", wtPath)
		}

		if !returnForce {
			dirty, _ := git.IsDirty(wtPath)
			if dirty {
				ok, err := ui.Confirm("Worktree has uncommitted changes. Clean and return?", true)
				if err != nil || !ok {
					fmt.Fprintln(os.Stderr, "🌳 Aborted.")
					return nil
				}
			}
		}

		if err := pool.Release(poolDir, wtPath); err != nil {
			return fmt.Errorf("failed to return worktree: %w", err)
		}

		fmt.Fprintln(os.Stderr, "🌳 Worktree returned to pool.")
		return nil
	},
}

func init() {
	returnCmd.Flags().BoolVar(&returnForce, "force", false, "Skip dirty check prompt")
	rootCmd.AddCommand(returnCmd)
}

func resolveWorktreePath(args []string) (string, error) {
	if len(args) > 0 {
		return filepath.Abs(args[0])
	}
	if env := os.Getenv("TREEHOUSE_DIR"); env != "" {
		return filepath.Abs(env)
	}
	return os.Getwd()
}
