package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/shell"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var goCmd = &cobra.Command{
	Use:   "go [target]",
	Short: "Open a shell in an existing Treehouse worktree",
	Long: `Open a shell in an existing Treehouse-managed worktree.

With no arguments, treehouse go lists every managed worktree under the
user-level treehouse root and prompts you to choose one. With a target, it opens
the unique worktree whose path, basename, name, or path substring matches.`,
	Args: cobra.MaximumNArgs(1),
	RunE: goRunE,
}

func init() {
	rootCmd.AddCommand(goCmd)
}

func goRunE(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	poolRoot, err := config.ResolvePoolRoot("", cfg.Root)
	if err != nil {
		return err
	}

	worktrees, err := pool.ListNavigationWorktrees(poolRoot)
	if err != nil {
		return err
	}
	if len(worktrees) == 0 {
		return fmt.Errorf("no Treehouse worktrees found under %s", ui.PrettyPath(poolRoot))
	}

	var selected pool.NavigationWorktree
	if len(args) > 0 {
		selected, err = pool.ResolveNavigationTarget(worktrees, args[0])
		if err != nil {
			return err
		}
	} else {
		selected, err = promptNavigationWorktree(os.Stdout, os.Stdin, worktrees)
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stderr, "🌳 Entered worktree at %s. Type 'exit' to return.\n", ui.PrettyPath(selected.Path))
	env := []string{
		"TREEHOUSE_DIR=" + selected.Path,
	}
	_, err = shell.Spawn(selected.Path, env)
	return err
}

func promptNavigationWorktree(w io.Writer, r io.Reader, worktrees []pool.NavigationWorktree) (pool.NavigationWorktree, error) {
	fmt.Fprintln(w, "🌳 Treehouse worktrees:")
	for i, wt := range worktrees {
		line := fmt.Sprintf("%d) %-4s  %-11s  %s", i+1, wt.Name, wt.Status, ui.PrettyPath(wt.Path))
		if wt.Status == pool.StatusLeased && wt.LeaseHolder != "" {
			line += fmt.Sprintf("  (held by %s)", wt.LeaseHolder)
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprint(w, "Choose a worktree: ")

	reader := bufio.NewReader(r)
	input, err := reader.ReadString('\n')
	if err != nil && len(input) == 0 {
		return pool.NavigationWorktree{}, err
	}
	trimmed := strings.TrimSpace(input)
	choice, err := strconv.Atoi(trimmed)
	if err != nil || choice < 1 || choice > len(worktrees) {
		return pool.NavigationWorktree{}, fmt.Errorf("invalid worktree selection %q", trimmed)
	}
	return worktrees[choice-1], nil
}
