package cmd

import (
	"fmt"
	"os"

	"github.com/kunchenguid/treehouse/v3/internal/config"
	"github.com/kunchenguid/treehouse/v3/internal/vcs"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use: "trust", Short: "Approve the current repository lifecycle hooks",
		Long: `Approve the [hooks] table in the main repository's treehouse.toml.
Review those commands before running this command. Approval is stored in the
user config and applies only while the repository identity and hooks hash match.
Changing hook contents requires running trust again. This command runs no hooks.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := vcs.FindMainRepoRoot()
			if err != nil {
				return fmt.Errorf("not in a git or jj repository: %w", err)
			}
			identity, hash, err := config.Trust(root)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "🌳 Trusted hooks for %s (%s).\n", identity, hash)
			return nil
		},
	}, &cobra.Command{
		Use: "untrust", Short: "Revoke approval for this repository's lifecycle hooks",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := vcs.FindMainRepoRoot()
			if err != nil {
				return fmt.Errorf("not in a git or jj repository: %w", err)
			}
			identity, err := config.Untrust(root)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "🌳 Revoked hook trust for %s.\n", identity)
			return nil
		},
	})
}
