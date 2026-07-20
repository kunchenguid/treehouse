package cmd

import (
	"errors"
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
	returnForce         bool
	returnIfLeaseID     string
	returnIfLeaseHolder string
)

var (
	errReturnWorktreeUnmanaged = errors.New("return worktree unmanaged")
	errReturnAborted           = errors.New("return aborted")
)

var returnCmd = &cobra.Command{
	Use:   "return [path]",
	Short: "Terminate lingering processes and return a worktree",
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("if-lease-id") && returnIfLeaseID == "" {
			return fmt.Errorf("--if-lease-id cannot be empty")
		}

		wtPath, err := resolveWorktreePath(args)
		if err != nil {
			return err
		}

		poolDir, err := resolveReturnPoolDir(wtPath, len(args) > 0)
		if err != nil {
			if errors.Is(err, errReturnWorktreeUnmanaged) {
				return fmt.Errorf("worktree %s is not managed by treehouse", wtPath)
			}
			return err
		}

		conditional := cmd.Flags().Changed("if-lease-id") || cmd.Flags().Changed("if-lease-holder")
		if conditional {
			preconditions := pool.ReleasePreconditions{}
			if cmd.Flags().Changed("if-lease-id") {
				preconditions.ExpectedLeaseID = &returnIfLeaseID
			}
			if cmd.Flags().Changed("if-lease-holder") {
				preconditions.ExpectedLeaseHolder = &returnIfLeaseHolder
			}
			err = pool.ValidateReleasePreconditions(poolDir, wtPath, preconditions)
			if err == nil {
				err = confirmWorktreeReturn(wtPath)
			}
			if err == nil {
				err = pool.ReleaseConditional(poolDir, wtPath, preconditions, func() error {
					return finalizeWorktreeReturn(wtPath)
				})
			}
		} else {
			err = prepareWorktreeReturn(wtPath)
			if err == nil {
				err = pool.Release(poolDir, wtPath)
			}
		}
		if errors.Is(err, errReturnAborted) {
			fmt.Fprintln(os.Stderr, "🌳 Aborted.")
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to return worktree: %w", err)
		}

		fmt.Fprintln(os.Stderr, "🌳 Worktree returned to pool.")
		return nil
	},
}

func init() {
	returnCmd.Flags().BoolVar(&returnForce, "force", false, "Clean, reset, and return without prompting")
	returnCmd.Flags().StringVar(&returnIfLeaseID, "if-lease-id", "", "Return only if the current lease has this identity")
	returnCmd.Flags().StringVar(&returnIfLeaseHolder, "if-lease-holder", "", "Return only if the current lease has this holder")
	rootCmd.AddCommand(returnCmd)
}

func prepareWorktreeReturn(wtPath string) error {
	if err := confirmWorktreeReturn(wtPath); err != nil {
		return err
	}
	return finalizeWorktreeReturn(wtPath)
}

func confirmWorktreeReturn(wtPath string) error {
	if !returnForce {
		dirty, _ := git.IsDirty(wtPath)
		if dirty {
			ok, err := ui.Confirm("Worktree has uncommitted changes. Clean and return?", true)
			if err != nil || !ok {
				return errReturnAborted
			}
		}
	}
	return nil
}

func finalizeWorktreeReturn(wtPath string) error {
	if !returnForce {
		if err := git.DetachWorktree(wtPath); err != nil {
			return fmt.Errorf("failed to detach worktree HEAD: %w", err)
		}
	}

	killLingeringProcesses(wtPath)
	return nil
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

func resolveReturnPoolDir(wtPath string, explicitPath bool) (string, error) {
	pathPoolDir := filepath.Dir(filepath.Dir(wtPath))
	entry, err := pool.FindByPath(pathPoolDir, wtPath)
	if err != nil {
		return "", err
	}
	if entry != nil {
		return pathPoolDir, nil
	}

	var repoRoot string
	if explicitPath {
		repoRoot, err = git.FindMainRepoRootFrom(wtPath)
	} else {
		repoRoot, err = git.FindRepoRoot()
	}
	if err != nil {
		if explicitPath {
			return "", errReturnWorktreeUnmanaged
		}
		return "", fmt.Errorf("not in a git repository: %w", err)
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}

	fallbackPoolDir, err := config.ResolvePoolDir(repoRoot, cfg.Root)
	if err != nil {
		return "", err
	}

	entry, err = pool.FindByPath(fallbackPoolDir, wtPath)
	if err != nil {
		return "", err
	}
	if entry == nil {
		return "", errReturnWorktreeUnmanaged
	}
	return fallbackPoolDir, nil
}
