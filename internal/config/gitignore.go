package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/treehouse/v3/internal/vcs"
)

// EnsureExcluded arranges for treehouseDir to be ignored by the enclosing
// repository. It first makes the pool root self-ignoring by writing a
// .gitignore containing "*" inside it; both git and jj honor nested
// .gitignore files, so this keeps an in-project pool out of snapshots even
// in non-colocated jj repositories, which never read .git/info/exclude.
// When the directory sits inside a git repository, the pool root is
// additionally recorded in the repo-local, untracked .git/info/exclude file,
// keeping an in-project pool out of `git status` and out of any commit
// without dirtying the tracked .gitignore. The exclude step is skipped when
// the directory is not inside a git repo (e.g. the default global root under
// $HOME), matching the previous behavior for the global store, and degrades
// to the self-ignoring .gitignore alone when no usable git dir exists.
//
// For backward compatibility with pools created by older versions, a
// pre-existing entry in the tracked .gitignore is left untouched and treated as
// sufficient, so upgrading users are not surprised by a moved ignore rule.
func EnsureExcluded(treehouseDir string) error {
	selfIgnored := writeSelfIgnore(treehouseDir) == nil

	// Walk up from treehouseDir to find an existing ancestor for the git check,
	// since the directory itself may not exist yet.
	checkDir := treehouseDir
	for {
		if info, err := os.Stat(checkDir); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(checkDir)
		if parent == checkDir {
			return nil
		}
		checkDir = parent
	}

	repoRoot, err := vcs.FindRepoRootFrom(checkDir)
	if err != nil {
		// Not inside a git repo — nothing to do (e.g. the global ~/.treehouse root).
		return nil
	}

	rel, err := filepath.Rel(repoRoot, treehouseDir)
	if err != nil {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// Directory is outside the repository working tree — nothing to do.
		return nil
	}

	// Use forward slashes and a leading slash to anchor the entry at the repo root.
	entry := "/" + filepath.ToSlash(rel)

	// Backward compatibility: if a previous version already recorded the entry in
	// the tracked .gitignore, leave it in place rather than writing a duplicate
	// to .git/info/exclude.
	if hasIgnoreEntry(filepath.Join(repoRoot, ".gitignore"), entry) {
		return nil
	}

	commonDir, err := vcs.CommonGitDir(repoRoot)
	if err != nil {
		if selfIgnored {
			return nil
		}
		return err
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")

	if hasIgnoreEntry(excludePath, entry) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return err
	}

	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	f, err := os.OpenFile(excludePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	_, err = f.WriteString(prefix + entry + "\n")
	return err
}

// writeSelfIgnore creates treehouseDir and writes a .gitignore containing "*"
// into it unless one already exists. The pool root then ignores its entire
// contents (including the .gitignore itself) under both git and jj, which
// makes in-project roots safe even for backends that do not read
// .git/info/exclude. Ignore rules never cross a worktree or workspace root,
// so the file has no effect inside the pooled worktrees below it.
func writeSelfIgnore(treehouseDir string) error {
	if err := os.MkdirAll(treehouseDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(treehouseDir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte("*\n"), 0o644)
}

// hasIgnoreEntry reports whether the ignore file at path already contains entry
// as a standalone line. A missing file reads as absent.
func hasIgnoreEntry(path, entry string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return true
		}
	}
	return false
}
