package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/atinylittleshell/treehouse/internal/config"
	"github.com/atinylittleshell/treehouse/internal/git"
	"github.com/atinylittleshell/treehouse/internal/pool"
	"github.com/atinylittleshell/treehouse/internal/ui"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the status of all worktrees in the pool",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := git.FindRepoRoot()
		if err != nil {
			return fmt.Errorf("not in a git repository: %w", err)
		}

		poolDir, err := config.ResolvePoolDir(repoRoot)
		if err != nil {
			return err
		}

		worktrees, err := pool.List(poolDir)
		if err != nil {
			return err
		}

		if len(worktrees) == 0 {
			fmt.Fprintln(os.Stderr, "🌳 No worktrees in pool.")
			return nil
		}

		green := color.New(color.FgGreen).SprintFunc()
		red := color.New(color.FgRed).SprintFunc()
		yellow := color.New(color.FgYellow).SprintFunc()

		for _, wt := range worktrees {
			var status string
			switch wt.Status {
			case pool.StatusAvailable:
				status = green(wt.Status)
			case pool.StatusInUse:
				status = red(wt.Status)
			case pool.StatusDirty:
				status = yellow(wt.Status)
			}

			// "%-4s  %-9s  " = 4 + 2 + 9 + 2 = 17 chars before path
			statusPad := strings.Repeat(" ", 9-len(wt.Status))
			fmt.Fprintf(os.Stdout, "%-4s  %s%s  %s\n", wt.Name, status, statusPad, ui.PrettyPath(wt.Path))

			if len(wt.Processes) > 0 {
				var procStrs []string
				for _, p := range wt.Processes {
					procStrs = append(procStrs, p.String())
				}
				fmt.Fprintf(os.Stdout, "%s%s\n", strings.Repeat(" ", 17), strings.Join(procStrs, ", "))
			}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
