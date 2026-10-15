package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/kunchenguid/treehouse/v3/internal/ui"
)

// hookKeys are the lifecycle hook keys a repo-level treehouse.toml can declare
// and that Load gates on explicit trust. Order fixes their warning order.
var hookKeys = []string{"post_create", "pre_destroy", "post_get", "post_return"}

// warnedRepoHooks dedupes the ignored-hooks warning per config file: a single
// command loads config repeatedly, and the warning is for a human, once. It
// mirrors warnedVCSValues in internal/vcs.
var warnedRepoHooks sync.Map

// WarnIfRepoHooksIgnored warns once per file when declared hooks lack a matching
// user approval. Cleanup commands use this best-effort helper while inspecting
// their targets, including dry runs. Missing or malformed repo config stays
// silent so damaged repositories can still be removed.
func WarnIfRepoHooksIgnored(repoRoot string) {
	warnIfRepoHooksIgnored(os.Stderr, repoRoot)
}

func warnIfRepoHooksIgnored(w io.Writer, repoRoot string) {
	repoCfg, md, hash, err := readRepoConfig(repoRoot)
	if err != nil {
		return
	}
	userCfg, _, err := loadUser()
	if err != nil {
		return
	}
	_ = effectiveHooks(repoRoot, repoCfg.Hooks, md, hash, userCfg, w)
}

// warnRepoHooks emits the warning from metadata the caller already decoded, so
// Load does not have to read the repo config a second time.
func warnRepoHooks(w io.Writer, path string, md toml.MetaData) {
	ignored := declaredHookKeys(md)
	if len(ignored) == 0 {
		return
	}
	if _, seen := warnedRepoHooks.LoadOrStore(path, true); seen {
		return
	}
	fmt.Fprintf(w, "🌳 Warning: ignoring [hooks] in %s: %s\n", ui.PrettyPath(path), strings.Join(ignored, ", "))
	fmt.Fprintf(w, "   Review the hooks and run treehouse trust to approve them, or put user hooks in %s.\n", userConfigPathForDisplay())
}

// userConfigPathForDisplay names the user-level config file for humans, falling
// back to the conventional ~-rooted spelling when the home directory cannot be
// resolved: the warning must still point somewhere useful.
func userConfigPathForDisplay() string {
	path := userConfigPath()
	if path == "" {
		return filepath.Join("~", ".config", "treehouse", "config.toml")
	}
	return ui.PrettyPath(path)
}

// declaredHookKeys returns the hook keys the file actually declares. An empty
// [hooks] table declares no hook and so silently discards nothing worth
// warning about.
func declaredHookKeys(md toml.MetaData) []string {
	var declared []string
	for _, key := range hookKeys {
		if md.IsDefined("hooks", key) {
			declared = append(declared, key)
		}
	}
	return declared
}
