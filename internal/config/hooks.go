package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/kunchenguid/treehouse/internal/ui"
)

// hookKeys are the lifecycle hook keys a repo-level treehouse.toml can declare
// and that Load discards. Order fixes the order they are named in the warning.
var hookKeys = []string{"post_create", "pre_destroy"}

// warnedRepoHooks dedupes the ignored-hooks warning per config file: a single
// command loads config repeatedly, and the warning is for a human, once. It
// mirrors warnedVCSValues in internal/vcs.
var warnedRepoHooks sync.Map

// WarnIfRepoHooksIgnored warns on stderr, once per repo config file, when the
// repo-level treehouse.toml declares lifecycle hooks. Load deliberately
// discards those hooks so that `treehouse get` on an untrusted clone cannot
// execute checked-in shell; this only makes the discard audible, and changes no
// behavior.
//
// It is a standalone helper rather than a step inside Load because
// `treehouse destroy <path>` reads hooks through LoadGlobal and never opens the
// repo config at all, so a warning wired only into Load would stay silent for
// exactly the command that surfaced the defect.
//
// It is best-effort and never fails: a missing, unreadable, or malformed repo
// config produces no warning and no error, because destroy has to keep working
// against a pool whose repository config is broken or whose repository is gone.
func WarnIfRepoHooksIgnored(repoRoot string) {
	warnIfRepoHooksIgnored(os.Stderr, repoRoot)
}

func warnIfRepoHooksIgnored(w io.Writer, repoRoot string) {
	path := filepath.Join(repoRoot, "treehouse.toml")
	var cfg Config
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return
	}
	warnRepoHooks(w, path, md)
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
	fmt.Fprintf(w, "   Lifecycle hooks run only from %s; hooks in repo-level config are ignored for safety.\n", userConfigPathForDisplay())
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
