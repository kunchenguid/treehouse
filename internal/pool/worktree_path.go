package pool

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Placeholders a worktree path template may use. {pool} and {repo_parent}
// expand to absolute directories; {slot} and {repo} expand to a single path
// segment.
const (
	placeholderPool       = "{pool}"
	placeholderSlot       = "{slot}"
	placeholderRepo       = "{repo}"
	placeholderRepoParent = "{repo_parent}"
)

var worktreePathPlaceholders = []string{
	placeholderPool,
	placeholderSlot,
	placeholderRepo,
	placeholderRepoParent,
}

// repositoryScopedPlaceholders are the placeholders that differ between
// repositories. One is required: slot names are allocated per pool, so a
// user-level template carrying none of these sends the first slot of every
// repository to the same directory.
var repositoryScopedPlaceholders = []string{
	placeholderPool,
	placeholderRepo,
	placeholderRepoParent,
}

// placeholderPattern matches every {...} group, so a misspelled placeholder is
// reported instead of becoming a literal directory name.
var placeholderPattern = regexp.MustCompile(`\{[^{}]*\}`)

// expandWorktreePathEnv resolves $VAR and ${VAR} in a template.
//
// Every check below runs on the result, never on the raw template. ${VAR} shares
// its braces with the placeholder syntax, so validating first would reject
// ${HOME}/... as an unknown {HOME} placeholder, and would read ${slot} - an
// environment variable that expands to one fixed value for the whole pool - as
// the per-slot placeholder.
//
// A variable that expands to nothing is refused rather than dropped. An empty
// expansion leaves an empty path segment, filepath.Clean removes it, and the
// same config then places worktrees in two different directories depending on
// whether the invoking environment (an interactive shell, cron, CI) exports the
// variable.
func expandWorktreePathEnv(template string) (string, error) {
	if err := checkEnvReferences(template); err != nil {
		return "", err
	}
	var empty []string
	expanded := os.Expand(template, func(name string) string {
		value := os.Getenv(name)
		if value == "" && !slices.Contains(empty, name) {
			empty = append(empty, name)
		}
		return value
	})
	if len(empty) > 0 {
		return "", fmt.Errorf("worktree path %q references $%s, which the environment leaves empty: an empty expansion drops a directory from the path, so the worktree would not be created where the template names",
			template, strings.Join(empty, ", $"))
	}
	return expanded, nil
}

// checkEnvReferences rejects a ${...} reference os.Expand would eat instead of
// expanding. It never calls the mapping function for "${}" or for a "${" with no
// closing brace, so the empty-expansion check above cannot see either, and a
// leading "${}" resolves the whole template to the filesystem root.
func checkEnvReferences(template string) error {
	for i := 0; i+1 < len(template); i++ {
		if template[i] != '$' || template[i+1] != '{' {
			continue
		}
		// A variable name runs to its closing brace and holds no separator and
		// no second brace, so anything else means the reference was never closed.
		end := strings.IndexAny(template[i+2:], `}/\{`)
		if end == 0 && template[i+2] == '}' {
			return fmt.Errorf("worktree path %q references %q, which names no environment variable and expands to nothing instead of a directory", template, "${}")
		}
		if end < 0 || template[i+2+end] != '}' {
			return fmt.Errorf("worktree path %q has a %q with no closing brace, so the reference expands to nothing instead of a directory", template, "${")
		}
		i += end + 2
	}
	return nil
}

// validateWorktreePathTemplate checks a template without touching the
// filesystem and returns what it expands to. Acquire runs it before anything
// else so a bad template fails on every invocation, including the ones that
// recycle an existing slot and so never expand it - otherwise a typo would
// silently hand back a slot at the built-in layout and never report itself.
func validateWorktreePathTemplate(template string) (string, error) {
	if template == "" {
		return "", nil
	}
	expanded, err := expandWorktreePathEnv(template)
	if err != nil {
		return "", err
	}
	for _, found := range placeholderPattern.FindAllString(expanded, -1) {
		if !slices.Contains(worktreePathPlaceholders, found) {
			return "", fmt.Errorf("worktree path %s uses unknown placeholder %s (supported: %s)",
				describeTemplate(template, expanded), found, strings.Join(worktreePathPlaceholders, " "))
		}
	}
	if !strings.Contains(expanded, placeholderSlot) {
		return "", fmt.Errorf("worktree path %s must contain %s: without it every pool slot resolves to the same directory",
			describeTemplate(template, expanded), placeholderSlot)
	}
	if !slotsResolveApart(expanded) {
		return "", fmt.Errorf("worktree path %s resolves to the same directory for every slot, so two slots would share one worktree; give %s a path segment that survives path cleaning",
			describeTemplate(template, expanded), placeholderSlot)
	}
	scoped := slices.ContainsFunc(repositoryScopedPlaceholders, func(p string) bool {
		return strings.Contains(expanded, p)
	})
	if !scoped {
		return "", fmt.Errorf("worktree path %s must contain one of %s: slot names are allocated per repository, so without one the first slot of every repository resolves to the same directory",
			describeTemplate(template, expanded), strings.Join(repositoryScopedPlaceholders, " "))
	}
	return expanded, nil
}

// slotsResolveApart reports whether the template really gives each slot its own
// directory. Carrying {slot} is not enough: a following ".." cancels the segment
// it produced, so two slots clean down to one path. Only {slot} is substituted -
// the other placeholders stay literal text, which path cleaning treats as
// ordinary segments, so this needs no repository or pool context and stays a
// filesystem-free check that also runs on the recycling path.
func slotsResolveApart(template string) bool {
	resolve := func(slot string) string {
		return filepath.Clean(filepath.FromSlash(strings.ReplaceAll(template, placeholderSlot, slot)))
	}
	return resolve("slot-a") != resolve("slot-b")
}

// describeTemplate names a template in an error, showing what it expanded to
// when the two differ - otherwise a rejected ${slot} or ${HOME} reads as a
// complaint about text the user cannot find in their config.
func describeTemplate(template, expanded string) string {
	if template == expanded {
		return fmt.Sprintf("%q", template)
	}
	return fmt.Sprintf("%q (expanded to %q)", template, expanded)
}

// resolveWorktreePath returns the directory a newly created slot is placed in.
// An empty template keeps the built-in {pool}/{slot}/{repo} layout, byte for
// byte. Recycled slots never reach this function: they are handed back at the
// path recorded in state, so a template never moves a worktree that exists.
func resolveWorktreePath(repoRoot, poolDir, slot, template string) (string, error) {
	repoName := filepath.Base(repoRoot)
	if template == "" {
		return filepath.Join(poolDir, slot, repoName), nil
	}
	expanded, err := validateWorktreePathTemplate(template)
	if err != nil {
		return "", err
	}

	replaced := strings.NewReplacer(
		placeholderPool, poolDir,
		placeholderSlot, slot,
		placeholderRepo, repoName,
		placeholderRepoParent, filepath.Dir(repoRoot),
	).Replace(expanded)

	resolved := filepath.Clean(filepath.FromSlash(replaced))
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("worktree path %q resolves to the relative path %q; anchor it on %s, %s, or an absolute path",
			template, resolved, placeholderPool, placeholderRepoParent)
	}
	if err := checkWorktreePlacement(template, resolved, repoRoot, poolDir, slot); err != nil {
		return "", err
	}
	// The requested spelling is what is returned, not the canonical one: the
	// caller asked for this path because another tool expects a worktree there,
	// and pool state records the path git is given.
	return resolved, nil
}

// checkWorktreePlacement rejects a resolved path that would corrupt the pool or
// the repository instead of holding one worktree. Each rejection happens before
// the directory is created, so a bad template costs nothing but an error.
//
// Every comparison is made on canonicalized paths. filepath.Rel compares
// spellings, but a worktree is created wherever the existing directories in its
// path really point, so a symlink into the repository or the pool passes a
// lexical check and then lands inside it. Canonicalizing both sides also keeps a
// symlinked pool root (on macOS /tmp -> /private/tmp is enough) from reading as
// an escape.
func checkWorktreePlacement(template, resolved, repoRoot, poolDir, slot string) error {
	canonicalResolved, err := canonicalPathPrefix(resolved)
	if err != nil {
		return fmt.Errorf("worktree path %q resolves to %q, which cannot be checked: %w", template, resolved, err)
	}
	canonicalRepoRoot, err := canonicalPathPrefix(repoRoot)
	if err != nil {
		return fmt.Errorf("cannot resolve the repository %q to check the worktree path: %w", repoRoot, err)
	}
	canonicalPoolDir, err := canonicalPathPrefix(poolDir)
	if err != nil {
		return fmt.Errorf("cannot resolve the pool directory %q to check the worktree path: %w", poolDir, err)
	}

	if pathContains(canonicalResolved, canonicalPoolDir) {
		return fmt.Errorf("worktree path %q resolves to %q, which is or contains the pool directory %q holding the pool's state",
			template, resolved, poolDir)
	}
	if pathContains(canonicalResolved, canonicalRepoRoot) {
		return fmt.Errorf("worktree path %q resolves to %q, which is or contains the repository %q",
			template, resolved, repoRoot)
	}
	if pathContains(canonicalPoolDir, canonicalResolved) {
		// State recovery reconstructs a lost state file by scanning the pool
		// two levels deep (<slot>/<worktree>), so an in-pool worktree must keep
		// that shape to stay recoverable.
		rel, err := filepath.Rel(canonicalPoolDir, canonicalResolved)
		if err != nil {
			return err
		}
		segments := strings.Split(rel, string(filepath.Separator))
		if len(segments) != 2 || segments[0] != slot {
			return fmt.Errorf("worktree path %q resolves to %q; inside the pool directory it must be %s/%s/<name> so pool state can be recovered from disk",
				template, resolved, placeholderPool, placeholderSlot)
		}
		// State records the requested spelling while recovery scans the
		// configured pool directory, so an in-pool worktree reached by another
		// spelling (a symlink into the pool) is found twice: once as its recorded
		// entry and once as a recovered one for the same directory.
		if !pathContainsLexically(poolDir, resolved) {
			return fmt.Errorf("worktree path %q resolves to %q, which reaches the pool directory %q by another name; spell an in-pool path through the pool directory itself (use %s) so pool state and recovery agree on one path",
				template, resolved, poolDir, placeholderPool)
		}
		return nil
	}
	// Another repository's pool recovers a lost state file by scanning its own
	// directory, so a worktree nested there is registered as a slot of a pool
	// that never created it: a phantom leased entry that consumes one of its
	// slots for good and leaves this repository unable to return the worktree.
	if foreign := enclosingPoolDir(canonicalResolved); foreign != "" {
		return fmt.Errorf("worktree path %q resolves to %q, inside the treehouse pool directory %q of another repository, which would register the worktree as a slot of its own",
			template, resolved, foreign)
	}
	// An in-project pool is git-ignored and was already accepted above. Any
	// other path inside the working tree is not ignored, so the worktree would
	// show up as untracked content in the repository it was cut from.
	if pathContains(canonicalRepoRoot, canonicalResolved) {
		return fmt.Errorf("worktree path %q resolves to %q, inside the repository working tree %q; place worktrees outside the repository or under %s",
			template, resolved, repoRoot, placeholderPool)
	}
	return nil
}

// enclosingPoolDir returns the nearest ancestor of path that is a managed pool
// directory, or "" when no ancestor is one. The treehouse root holds every pool
// without being one itself, so a worktree placed beside the pools is unaffected.
func enclosingPoolDir(path string) string {
	for current := filepath.Clean(path); ; {
		if IsPoolDir(current) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// canonicalPathPrefix resolves the deepest existing ancestor of path through
// symlinks and re-appends the components that do not exist yet, because a path
// treehouse is about to create can only be judged by where its existing
// ancestors lead. Anything other than a missing component fails closed rather
// than falling back to the lexical path.
func canonicalPathPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	missing := ""
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if missing == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, missing), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing directory found above %s", path)
		}
		missing = filepath.Join(filepath.Base(current), missing)
		current = parent
	}
}

// pathContains reports whether parent is child itself or holds it somewhere
// below.
//
// Spelling decides it only where identity cannot. filepath.Rel compares bytes
// and EvalSymlinks keeps the case it was given, so on a case-insensitive
// filesystem (APFS and NTFS by default) a differently-cased prefix reads as a
// different directory: a literal '<repo_parent>/MyRepo/trees/{slot}' would land
// inside the repository this comparison exists to keep worktrees out of. Every
// existing ancestor of child is therefore also compared with parent by identity.
func pathContains(parent, child string) bool {
	if pathContainsLexically(parent, child) {
		return true
	}
	for current := filepath.Clean(child); ; {
		if samePath(parent, current) {
			return true
		}
		next := filepath.Dir(current)
		if next == current {
			return false
		}
		current = next
	}
}

func pathContainsLexically(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// samePath reports whether two paths name the same directory, by identity when
// both exist, so a case-insensitive filesystem handing one directory two
// spellings is not read as two directories.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	aInfo, err := os.Stat(a)
	if err != nil {
		return false
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(aInfo, bInfo)
}
