package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/shell"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var enterCmd = &cobra.Command{
	Use:   "enter <name>",
	Short: "Open a subshell in an existing worktree by name, even if in use",
	Long: `Open a subshell in an existing pool worktree identified by its name
(the number shown by 'treehouse status'), including worktrees that are
already in use.

Unlike 'get', enter does not acquire, reset, or return the worktree: it
drops you into the directory and leaves all pool state untouched when you
exit. Use it to attach to a worktree another agent is already using.`,
	Args: cobra.ExactArgs(1),
	RunE: enterRunE,
}

func init() {
	rootCmd.AddCommand(enterCmd)
}

func enterRunE(cmd *cobra.Command, args []string) error {
	name := args[0]

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

	worktrees, err := pool.List(poolDir)
	if err != nil {
		return err
	}

	var target *pool.WorktreeStatus
	for i := range worktrees {
		if worktrees[i].Name == name {
			target = &worktrees[i]
			break
		}
	}
	if target == nil {
		names := make([]string, len(worktrees))
		for i, wt := range worktrees {
			names[i] = wt.Name
		}
		if len(names) == 0 {
			return fmt.Errorf("no worktree named %q: the pool is empty. Run 'treehouse get' to create one", name)
		}
		return fmt.Errorf("no worktree named %q in pool (available: %s). Run 'treehouse status' for details", name, strings.Join(names, ", "))
	}

	fmt.Fprintf(os.Stderr, "🌳 Entered worktree %s at %s. Type 'exit' to leave.\n", target.Name, ui.PrettyPath(target.Path))

	env := []string{
		"TREEHOUSE_DIR=" + target.Path,
	}
	if _, err := shell.Spawn(target.Path, env); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "🌳 Left worktree. Pool state unchanged.")
	return nil
}
