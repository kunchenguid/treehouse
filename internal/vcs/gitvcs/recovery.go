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
// It asks for any commit reachable from HEAD but from no remote-tracking ref
// and not from the base branch; HEAD is contained exactly when there is none.
// One walk replaces a merge-base per remote ref, which took minutes under the
// state lock in repositories with thousands of remote refs.
func RecoveryHeadContained(dir, base string) bool {
	args := []string{"rev-list", "-n", "1", "HEAD", "--not", "--remotes"}
	if base != "" {
		// rev-list fails on a missing ref, so only a verified base may join the
		// walk; refs/remotes/origin/<base> is already covered by --remotes.
		ref := "refs/heads/" + base
		if _, err := runGitRaw(dir, "show-ref", "--verify", "--quiet", ref); err == nil {
			args = append(args, ref)
		}
	}
	out, err := runGitRaw(dir, append(args, "--")...)
	return err == nil && len(bytes.TrimSpace(out)) == 0
}
