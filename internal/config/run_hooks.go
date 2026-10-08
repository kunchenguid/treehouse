package config

import (
	"fmt"
	"github.com/kunchenguid/treehouse/v3/internal/hooks"
	"io"
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

// RunWorktreeHooks resolves user hooks at the lifecycle point, after any reset.
func RunWorktreeHooks(event hooks.Event, worktree string, stdout, stderr io.Writer) {
	user, _, err := loadUser()
	if err != nil {
		fmt.Fprintf(stderr, "🌳 Warning: skipping hooks for %s: %v\n", worktree, err)
		return
	}
	hooks.Run(user.Hooks.commands(event), worktree, stdout, stderr)
}
