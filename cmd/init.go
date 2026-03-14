package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/atinylittleshell/treehouse/internal/config"
	"github.com/atinylittleshell/treehouse/internal/git"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a default treehouse.toml config file",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := git.FindRepoRoot()
		if err != nil {
			return fmt.Errorf("not in a git repository: %w", err)
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

		fmt.Fprintf(os.Stderr, "Created %s\n", dest)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
