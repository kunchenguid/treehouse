package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/treehouse/internal/git"
)

// EnsureExcluded arranges for treehouseDir to be ignored by the enclosing git
// repository using the repo-local, untracked .git/info/exclude file. This keeps
// an in-project pool out of `git status` and out of any commit without dirtying
// the tracked .gitignore. It is a no-op when the directory is not inside a git
// repo (e.g. the default global root under $HOME), matching the previous
// behavior for the global store.
//
// For backward compatibility with pools created by older versions, a
// pre-existing entry in the tracked .gitignore is left untouched and treated as
// sufficient, so upgrading users are not surprised by a moved ignore rule.
func EnsureExcluded(treehouseDir string) error {
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

	repoRoot, err := git.FindRepoRootFrom(checkDir)
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

	commonDir, err := git.CommonGitDir(repoRoot)
	if err != nil {
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
