package pool

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/atinylittleshell/treehouse/internal/git"
	"github.com/atinylittleshell/treehouse/internal/process"
)

const (
	StatusAvailable = "available"
	StatusDirty     = "dirty"
	StatusInUse     = "in-use"
)

type WorktreeStatus struct {
	Name      string
	Path      string
	Status    string
	Processes []process.ProcessInfo
}

func Acquire(repoRoot, poolDir string, poolSize int) (string, error) {
	branch, err := git.GetDefaultBranch(repoRoot)
	if err != nil {
		return "", err
	}

	fmt.Fprintf(os.Stderr, "🌳 Setting up worktree...\n")
	if git.HasRemote(repoRoot, "origin") {
		if err := git.Fetch(repoRoot); err != nil {
			return "", fmt.Errorf("fetch failed: %w", err)
		}
	}

	var acquired string

	err = WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		state = healState(state)

		// Try to find an available worktree (clean and not in-use)
		for _, wt := range state.Worktrees {
			inUse, _ := process.IsWorktreeInUse(wt.Path)
			if inUse {
				continue
			}
			dirty, _ := git.IsDirty(wt.Path)
			if dirty {
				continue
			}
			// Found an available one — reset it
			if err := git.ResetWorktree(wt.Path, branch); err != nil {
				continue
			}
			acquired = wt.Path
			return WriteState(poolDir, state)
		}

		// No available worktree — create new if pool allows
		if len(state.Worktrees) >= poolSize {
			return fmt.Errorf("all %d worktrees are in use or dirty (max_trees = %d). Run 'treehouse status' to see details, or increase max_trees in treehouse.toml", len(state.Worktrees), poolSize)
		}

		name := nextName(state)
		repoName := filepath.Base(repoRoot)
		wtPath := filepath.Join(poolDir, name, repoName)

		if err := os.MkdirAll(filepath.Dir(wtPath), 0755); err != nil {
			return err
		}

		if err := git.AddWorktree(repoRoot, wtPath, branch); err != nil {
			return fmt.Errorf("failed to create worktree: %w", err)
		}

		state.Worktrees = append(state.Worktrees, WorktreeEntry{
			Name:      name,
			Path:      wtPath,
			CreatedAt: time.Now(),
		})

		acquired = wtPath
		return WriteState(poolDir, state)
	})

	return acquired, err
}

func Release(poolDir, worktreePath string) error {
	repoRoot, err := git.FindRepoRoot()
	if err != nil {
		return err
	}
	branch, err := git.GetDefaultBranch(repoRoot)
	if err != nil {
		return err
	}
	return git.ResetWorktree(worktreePath, branch)
}

func List(poolDir string) ([]WorktreeStatus, error) {
	var result []WorktreeStatus

	err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		state = healState(state)
		if err := WriteState(poolDir, state); err != nil {
			return err
		}

		for _, wt := range state.Worktrees {
			ws := WorktreeStatus{
				Name:   wt.Name,
				Path:   wt.Path,
				Status: StatusAvailable,
			}

			procs, _ := process.FindProcessesInWorktree(wt.Path)
			ws.Processes = procs

			if len(procs) > 0 {
				ws.Status = StatusInUse
			} else if dirty, _ := git.IsDirty(wt.Path); dirty {
				ws.Status = StatusDirty
			}

			result = append(result, ws)
		}
		return nil
	})

	return result, err
}

func Destroy(repoRoot, poolDir, worktreePath string, force bool) error {
	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		idx := -1
		for i, wt := range state.Worktrees {
			if wt.Path == worktreePath {
				idx = i
				break
			}
		}
		if idx == -1 {
			return fmt.Errorf("worktree %s is not managed by treehouse", worktreePath)
		}

		if !force {
			inUse, _ := process.IsWorktreeInUse(worktreePath)
			if inUse {
				return fmt.Errorf("worktree %s is in use by an agent. Use --force to override", worktreePath)
			}
		}

		_ = git.RemoveWorktree(repoRoot, worktreePath)
		// also clean up the parent numbered directory
		os.RemoveAll(filepath.Dir(worktreePath))

		state.Worktrees = append(state.Worktrees[:idx], state.Worktrees[idx+1:]...)
		return WriteState(poolDir, state)
	})
}

func DestroyAll(repoRoot, poolDir string, force bool) error {
	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		if !force {
			for _, wt := range state.Worktrees {
				inUse, _ := process.IsWorktreeInUse(wt.Path)
				if inUse {
					return fmt.Errorf("worktree %s is in use by an agent. Use --force to override", wt.Path)
				}
			}
		}

		for _, wt := range state.Worktrees {
			_ = git.RemoveWorktree(repoRoot, wt.Path)
			os.RemoveAll(filepath.Dir(wt.Path))
		}

		state.Worktrees = nil
		return WriteState(poolDir, state)
	})
}

func FindByPath(poolDir, path string) (*WorktreeEntry, error) {
	state, err := ReadState(poolDir)
	if err != nil {
		return nil, err
	}
	for _, wt := range state.Worktrees {
		if wt.Path == path {
			return &wt, nil
		}
	}
	return nil, nil
}

func healState(state State) State {
	var healed []WorktreeEntry
	for _, wt := range state.Worktrees {
		if _, err := os.Stat(wt.Path); err == nil {
			healed = append(healed, wt)
		}
	}
	state.Worktrees = healed
	return state
}

func nextName(state State) string {
	max := 0
	for _, wt := range state.Worktrees {
		if n, err := strconv.Atoi(wt.Name); err == nil && n > max {
			max = n
		}
	}
	return strconv.Itoa(max + 1)
}
