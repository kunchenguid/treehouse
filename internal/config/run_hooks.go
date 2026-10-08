package config

import (
	"fmt"
	"io"

	"github.com/kunchenguid/treehouse/v3/internal/hooks"
	"github.com/kunchenguid/treehouse/v3/internal/vcs"
)

func (h Hooks) commands(event hooks.Event) []string {
	switch event {
	case hooks.PostCreate:
		return h.PostCreate
	case hooks.PostGet:
		return h.PostGet
	case hooks.PostReturn:
		return h.PostReturn
	case hooks.PreDestroy:
		return h.PreDestroy
	default:
		return nil
	}
}

// RunWorktreeHooks resolves configuration at the lifecycle point, after any
// checkout/reset. User hooks run first. Repository commands require matching
// user-owned approval, rechecked before each command.
func RunWorktreeHooks(event hooks.Event, worktree string, stdout, stderr io.Writer) {
	user, _, err := loadUser()
	if err != nil {
		fmt.Fprintf(stderr, "🌳 Warning: skipping hooks for %s: %v\n", worktree, err)
		return
	}
	hooks.Run(user.Hooks.commands(event), worktree, stdout, stderr)
	if vcs.WorktreeBackendName(worktree) == "" {
		return
	}
	repoRoot, err := vcs.FindMainRepoRootFrom(worktree)
	if err != nil {
		return
	}
	repo, md, tableHash, err := readRepoConfig(repoRoot)
	if err != nil {
		return
	}
	for _, command := range repo.Hooks.commands(event) {
		// A user hook (or an earlier repository command) may have revoked trust.
		user, _, err = loadUser()
		if err != nil {
			fmt.Fprintf(stderr, "🌳 Warning: skipping repository hooks for %s: %v\n", worktree, err)
			return
		}
		if !repoHooksApproved(repoRoot, md, tableHash, user, stderr) {
			return
		}
		hooks.Run([]string{command}, worktree, stdout, stderr)
	}
}
