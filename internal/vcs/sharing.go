package vcs

import (
	"github.com/kunchenguid/treehouse/v3/internal/fileclone"
	"github.com/kunchenguid/treehouse/v3/internal/vcs/gitvcs"
)

// ShareWorktreeFiles is an opt-in fresh-slot operation, not a way to mutate an
// existing slot. Dispatch uses the artifact's own marker, never configuration.
func ShareWorktreeFiles(repoRoot, worktreePath string) (fileclone.Report, error) {
	if !fileclone.Supported {
		return fileclone.Report{Reason: "requires macOS APFS"}, nil
	}
	if WorktreeBackendName(worktreePath) != "git" {
		return fileclone.Report{Reason: "only fresh Git slots are supported"}, nil
	}
	return gitvcs.ShareWorktreeFiles(repoRoot, worktreePath)
}
