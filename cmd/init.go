package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/vcs"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a default treehouse.toml config file",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := vcs.FindMainRepoRoot()
		if err != nil {
			return fmt.Errorf("not in a git or jj repository: %w", err)
		}

		dest := filepath.Join(repoRoot, "treehouse.toml")

		if _, err := os.Stat(dest); err == nil {
			return fmt.Errorf("treehouse.toml already exists")
		}

		f, err := os.Create(dest)
		if err != nil {
			return fmt.Errorf("failed to create config file: %w", err)
		}
		defer f.Close()

		if err := toml.NewEncoder(f).Encode(config.DefaultConfig()); err != nil {
			return fmt.Errorf("failed to write config: %w", err)
		}

		// Append comments showing the optional keys.
		if _, err := f.WriteString("\n# Worktree root directory (relative to repo root or absolute path).\n# Worktrees are placed under {root}/.treehouse/. Default: $HOME\n# Override per-command with the --root flag or the TREEHOUSE_ROOT env var.\n# Use \".\" to keep the pool in-project at <repo>/.treehouse/, next to the\n# code and removed with the project. Example: root = \".\"\n"); err != nil {
			return fmt.Errorf("failed to write config: %w", err)
		}

		if _, err := f.WriteString("\n# Branch new and recycled worktrees are cut from.\n# Unset (default) infers it from the repository: origin/HEAD, then the\n# checked-out branch, then init.defaultBranch.\n# When set, the branch must exist locally or on origin; treehouse fails\n# rather than falling back to the inferred default.\n# Override per-command with the --base flag. Example: base_branch = \"develop\"\n"); err != nil {
			return fmt.Errorf("failed to write config: %w", err)
		}

		if _, err := f.WriteString("\n# Where a newly created pool slot is placed.\n# Unset (default) uses {pool}/{slot}/{repo}. Placeholders: {pool}, {slot}\n# (required), {repo}, {repo_parent}; one of {pool}, {repo}, {repo_parent} is\n# required as well, to scope the template to this repository. {repo} alone can\n# still collide between two repositories whose directories share a name.\n# Worktrees already in the pool keep their recorded paths.\n# Override per-command with the --worktree-path flag or the\n# TREEHOUSE_WORKTREE_PATH env var.\n# Example: worktree_path = \"{repo_parent}/{repo}-{slot}\"\n"); err != nil {
			return fmt.Errorf("failed to write config: %w", err)
		}

		fmt.Fprintf(os.Stderr, "Created %s\n", dest)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
