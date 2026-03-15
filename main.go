package main

import (
	"os"
	"runtime/debug"

	"github.com/atinylittleshell/treehouse/cmd"
	"github.com/atinylittleshell/treehouse/internal/updater"
)

var version = ""

func init() {
	if version == "" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		} else {
			version = "dev"
		}
	}
}

func main() {
	// Handle --update-check before Cobra: the background child process
	// bypasses the normal command flow.
	if len(os.Args) >= 2 && os.Args[1] == "--update-check" {
		updater.RunBackgroundCheck(os.Args[2:])
		return
	}

	cmd.SetVersion(version)
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
