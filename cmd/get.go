package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/shell"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Acquire a worktree from the pool and open a subshell",
	RunE:  getRunE,
}

func init() {
	rootCmd.AddCommand(getCmd)
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

	wtPath, err := pool.Acquire(repoRoot, poolDir, cfg.MaxTrees)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "🌳 Entered worktree at %s. Type 'exit' to return.\n", ui.PrettyPath(wtPath))

	env := []string{
		"TREEHOUSE_DIR=" + wtPath,
	}
	_, err = shell.Spawn(wtPath, env)

	// Subshell exited — handle return
	dirty, _ := git.IsDirty(wtPath)
	if dirty {
		fmt.Fprintf(os.Stderr, "🌳 Worktree has uncommitted changes.\n")

		ok, promptErr := ui.Confirm("Clean worktree and return to pool?", true)
		if promptErr != nil || !ok {
			fmt.Fprintln(os.Stderr, "🌳 Worktree left dirty. Use 'treehouse return --force' to clean it later.")
			return nil
		}
	}

	if err := pool.Release(poolDir, wtPath); err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: failed to clean worktree: %v\n", err)
	} else {
		fmt.Fprintln(os.Stderr, "🌳 Worktree returned to pool.")
	}

	return nil
}
