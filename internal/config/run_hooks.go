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
// checkout/reset. User hooks run first. Repository dependencies are checked
// after user hooks and before each repository command, in both the main checkout
// and the actual execution directory. These checks do not lock file writers.
func RunWorktreeHooks(event hooks.Event, worktree string, stdout, stderr io.Writer) {
	user, _, err := loadUser()
	if err != nil {
		fmt.Fprintf(stderr, "🌳 Warning: skipping hooks for %s: %v\n", worktree, err)
		return
	}
	hooks.Run(user.Hooks.commands(event), worktree, stdout, stderr)
	// A user hook may have granted or revoked approval, so everything below
	// decides from a freshly read user config, never from the pre-hook snapshot.
	user, _, err = loadUser()
	if err != nil {
		fmt.Fprintf(stderr, "🌳 Warning: skipping repository hooks for %s: %v\n", worktree, err)
		return
	}
	// A repository command runs only against a user-owned approval, so with no
	// approval recorded at all there is nothing a repository config can add.
	// Resolving the owning repository costs git subprocesses at every lifecycle
	// event; skipping can only withhold repository hooks, never run one.
	if len(user.TrustedRepos) == 0 {
		return
	}
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
		// An earlier repository command may have revoked trust.
		user, _, err = loadUser()
		if err != nil {
			fmt.Fprintf(stderr, "🌳 Warning: skipping repository hooks for %s: %v\n", worktree, err)
			return
		}
		if !repoHooksApproved(repoRoot, worktree, repo.Hooks, md, tableHash, user, stderr) {
			return
		}
		hooks.Run([]string{command}, worktree, stdout, stderr)
	}
}
