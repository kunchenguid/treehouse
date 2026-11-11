package vcs

import (
	"fmt"
	"github.com/kunchenguid/treehouse/v3/internal/vcs/gitvcs"
)

// IsPathIgnored checks Git's effective ignore rules without ignoring tracked files.
// Other backends refuse this check until equivalent placement safety is supported.
func IsPathIgnored(repoRoot, path string) (bool, error) {
	if backendFor(repoRoot).Name() != "git" {
		return false, fmt.Errorf("ignored worktree placement is only supported by the git backend")
	}
	return gitvcs.IsPathIgnored(repoRoot, path)
}
