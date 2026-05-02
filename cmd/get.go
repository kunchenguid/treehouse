package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/process"
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
	if err := git.DetachWorktree(wtPath); err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: failed to detach worktree HEAD: %v\n", err)
	}

	dirty, _ := git.IsDirty(wtPath)
	if dirty {
		fmt.Fprintf(os.Stderr, "🌳 Worktree has uncommitted changes.\n")

		ok, promptErr := ui.Confirm("Clean worktree and return to pool?", true)
		if promptErr != nil || !ok {
			fmt.Fprintln(os.Stderr, "🌳 Worktree left dirty. Use 'treehouse return --force' to clean it later.")
			return nil
		}
	}

	killLingeringProcesses(wtPath)

	if err := pool.Release(poolDir, wtPath); err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: failed to clean worktree: %v\n", err)
	} else {
		fmt.Fprintln(os.Stderr, "🌳 Worktree returned to pool.")
	}

	return nil
}

// killLingeringProcesses terminates any process whose cwd is within the given
// worktree. Called before returning a worktree to the pool so detached tools
// (e.g. opencode servers that ignore SIGHUP) don't keep holding the worktree.
func killLingeringProcesses(wtPath string) {
	killed, err := process.TerminateWorktreeProcesses(wtPath, 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: failed to scan for lingering processes: %v\n", err)
		return
	}
	if len(killed) == 0 {
		return
	}
	names := make([]string, len(killed))
	for i, p := range killed {
		names[i] = p.String()
	}
	fmt.Fprintf(os.Stderr, "🌳 Terminated lingering processes: %s\n", strings.Join(names, ", "))
}
