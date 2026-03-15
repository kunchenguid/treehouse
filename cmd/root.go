package cmd

import (
	"fmt"
	"os"

	"github.com/atinylittleshell/treehouse/internal/updater"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var version = "dev"

func SetVersion(v string) {
	version = v
	rootCmd.Version = v
}

var rootCmd = &cobra.Command{
	Use:   "treehouse",
	Short: "Manage a pool of git worktrees for parallel AI agent workflows",
	Long: `Treehouse maintains a pool of reusable, pre-warmed git worktrees
so that multiple AI coding agents can work on the same repo in parallel.`,
	Version:       version,
	SilenceUsage: true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return getRunE(cmd, args)
	},
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Skip update check for dev builds, the update command itself,
		// or when explicitly suppressed via env var.
		if version == "dev" || os.Getenv("TREEHOUSE_NO_UPDATE_CHECK") == "1" {
			return
		}
		if cmd.Name() == "update" {
			return
		}

		// Show cached update notice from a previous check
		if result := updater.ReadCache(version); result != nil && result.UpdateAvailable {
			yellow := color.New(color.FgYellow)
			yellow.Fprintf(os.Stderr, "A new version of treehouse is available: %s → %s\n", version, result.LatestVersion)
			yellow.Fprintln(os.Stderr, "Run \"treehouse update\" to update")
			fmt.Fprintln(os.Stderr)
		}

		// Spawn background check if cache is stale
		if updater.IsCacheStale(version) {
			_ = updater.SpawnBackgroundCheck(version)
		}
	},
}

func init() {
	rootCmd.SetVersionTemplate(`{{.Version}}` + "\n")
}

func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}
