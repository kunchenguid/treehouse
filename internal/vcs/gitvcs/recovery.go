package gitvcs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RecoveryWorktree checks tracked edits and untracked files without trusting
// Git's configurable submodule ignore setting or index-hidden worktree files.
// Only root untracked paths may be moved to the recovery backup.
func RecoveryWorktree(dir string) ([]string, string) {
	flags, err := runGitRaw(dir, "ls-files", "-v", "-z")
	if err != nil {
		return nil, "cannot verify tracked changes"
	}
	for _, entry := range recoveryNUL(flags) {
		if tag := entry[0]; tag == 'S' || (tag >= 'a' && tag <= 'z') {
			return nil, "tracked files are marked skip-worktree or assume-unchanged, which hides their edits; clear those flags and check them"
		}
	}
	stages, err := runGitRaw(dir, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, "cannot verify submodules"
	}
	for _, entry := range recoveryNUL(stages) {
		if !strings.HasPrefix(entry, "160000 ") {
			continue
		}
		_, name, ok := strings.Cut(entry, "\t")
		if !ok {
			return nil, "cannot verify submodules"
		}
		sub := filepath.Join(dir, filepath.FromSlash(name))
		if _, err := os.Stat(filepath.Join(sub, ".git")); os.IsNotExist(err) {
			continue // An uninitialized gitlink has no checkout to inspect.
		} else if err != nil {
			return nil, fmt.Sprintf("cannot verify submodule %s: %v", name, err)
		}
		files, reason := RecoveryWorktree(sub)
		if reason != "" {
			return nil, fmt.Sprintf("submodule %s: %s", name, reason)
		}
		if len(files) != 0 {
			return nil, fmt.Sprintf("submodule %s has untracked files; inspect and return it by name", name)
		}
	}
	tracked, err := runGitRaw(dir, "diff", "--name-only", "--ignore-submodules=none", "HEAD", "--")
	if err != nil {
		return nil, "cannot verify tracked changes"
	}
	if len(bytes.TrimSpace(tracked)) != 0 {
		return nil, "tracked changes (including submodule contents) are present; commit or preserve them"
	}
	untracked, err := runGitRaw(dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, "cannot verify untracked files"
	}
	return recoveryNUL(untracked), ""
}

func recoveryNUL(data []byte) []string {
	var paths []string
	for _, p := range bytes.Split(data, []byte{0}) {
		if len(p) != 0 {
			paths = append(paths, string(p))
		}
	}
	return paths
}

// RecoveryHeadContained requires commit ancestry, not squash equivalence.
//
// It answers the same question the per-ref merge-base loop used to answer —
// whether HEAD is reachable from a remote-tracking ref or the base branch —
// with one commit walk instead of one process per ref. rev-list prints the
// commits reachable from HEAD but not from any exclusion; when a ref contains
// HEAD it contains all of HEAD's ancestors too, so empty output means HEAD is
// contained. Any Git failure (no HEAD, unreadable repo, unresolvable
// exclusion) fails closed to false, as before.
func RecoveryHeadContained(dir, base string) bool {
	exclusions := []string{"--remotes"}
	if base != "" {
		for _, ref := range []string{"refs/heads/" + base, "refs/remotes/origin/" + base} {
			if _, e := runGitRaw(dir, "show-ref", "--verify", "--quiet", ref); e == nil {
				exclusions = append(exclusions, ref)
			}
		}
	}
	args := append([]string{"rev-list", "-n", "1", "HEAD", "--not"}, exclusions...)
	out, err := runGitRaw(dir, args...)
	if err != nil {
		return false
	}
	return len(bytes.TrimSpace(out)) == 0
}
