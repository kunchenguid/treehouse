package pool

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// NavigationWorktree describes a managed worktree that can be opened by
// treehouse go.
type NavigationWorktree struct {
	PoolDir     string
	Project     string
	Name        string
	Path        string
	Status      string
	LeaseHolder string
}

// ListNavigationWorktrees returns every non-destroying managed worktree under
// the given user-level treehouse root.
func ListNavigationWorktrees(poolRoot string) ([]NavigationWorktree, error) {
	poolDirs, err := prunePoolDirs(poolRoot)
	if err != nil {
		return nil, err
	}

	var result []NavigationWorktree
	for _, poolDir := range poolDirs {
		worktrees, err := List(poolDir)
		if err != nil {
			return nil, err
		}
		project := navigationProjectName(poolDir)
		for _, wt := range worktrees {
			result = append(result, NavigationWorktree{
				PoolDir:     poolDir,
				Project:     project,
				Name:        wt.Name,
				Path:        wt.Path,
				Status:      wt.Status,
				LeaseHolder: wt.LeaseHolder,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Path == result[j].Path {
			return result[i].Name < result[j].Name
		}
		return result[i].Path < result[j].Path
	})
	return result, nil
}

// ResolveNavigationTarget resolves target by exact path, exact basename or
// worktree name, then unique substring of the path, basename, or name.
func ResolveNavigationTarget(worktrees []NavigationWorktree, target string) (NavigationWorktree, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return NavigationWorktree{}, fmt.Errorf("empty target")
	}

	if wt, ok := uniqueNavigationMatch(worktrees, func(wt NavigationWorktree) bool {
		return cleanPathEqual(wt.Path, target)
	}); ok {
		return wt, nil
	}

	if wt, ok := uniqueNavigationMatch(worktrees, func(wt NavigationWorktree) bool {
		return wt.Name == target || filepath.Base(wt.Path) == target
	}); ok {
		return wt, nil
	}

	needle := strings.ToLower(target)
	matches := navigationMatches(worktrees, func(wt NavigationWorktree) bool {
		return strings.Contains(strings.ToLower(wt.Path), needle) ||
			strings.Contains(strings.ToLower(filepath.Base(wt.Path)), needle) ||
			strings.Contains(strings.ToLower(wt.Name), needle)
	})
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return NavigationWorktree{}, fmt.Errorf("no worktree matches %q", target)
	}
	return NavigationWorktree{}, fmt.Errorf("target %q is ambiguous; matches: %s", target, formatNavigationMatches(matches))
}

func uniqueNavigationMatch(worktrees []NavigationWorktree, match func(NavigationWorktree) bool) (NavigationWorktree, bool) {
	matches := navigationMatches(worktrees, match)
	if len(matches) != 1 {
		return NavigationWorktree{}, false
	}
	return matches[0], true
}

func navigationMatches(worktrees []NavigationWorktree, match func(NavigationWorktree) bool) []NavigationWorktree {
	var matches []NavigationWorktree
	for _, wt := range worktrees {
		if match(wt) {
			matches = append(matches, wt)
		}
	}
	return matches
}

func navigationProjectName(poolDir string) string {
	name := filepath.Base(poolDir)
	if len(name) > 7 && name[len(name)-7] == '-' && isLowerHex(name[len(name)-6:]) {
		return name[:len(name)-7]
	}
	return name
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func cleanPathEqual(path, target string) bool {
	if filepath.Clean(path) == filepath.Clean(target) {
		return true
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	return filepath.Clean(path) == filepath.Clean(absTarget)
}

func formatNavigationMatches(worktrees []NavigationWorktree) string {
	parts := make([]string, len(worktrees))
	for i, wt := range worktrees {
		parts[i] = fmt.Sprintf("%s (%s)", wt.Name, wt.Path)
	}
	return strings.Join(parts, ", ")
}
