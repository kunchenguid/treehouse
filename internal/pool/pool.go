package pool

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/hooks"
	"github.com/kunchenguid/treehouse/internal/process"
)

const (
	StatusAvailable = "available"
	StatusDirty     = "dirty"
	StatusInUse     = "in-use"
	StatusHere      = "you're here"
)

type WorktreeStatus struct {
	Name      string
	Path      string
	Status    string
	Processes []process.ProcessInfo
}

func Acquire(repoRoot, poolDir string, poolSize int, postCreate []string) (string, error) {
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
	var runPostCreate bool

	err = WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		state = healState(state)

		// Try to find an available worktree (clean and not in-use)
		for i, wt := range state.Worktrees {
			if wt.Destroying || ownerAlive(wt) {
				continue
			}
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
			if err := reserveOwner(&state.Worktrees[i]); err != nil {
				return err
			}
			acquired = wt.Path
			if err := WriteState(poolDir, state); err != nil {
				return err
			}
			runPostCreate = true
			return nil
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

		entry := WorktreeEntry{
			Name:      name,
			Path:      wtPath,
			CreatedAt: time.Now(),
		}
		if err := reserveOwner(&entry); err != nil {
			return err
		}
		state.Worktrees = append(state.Worktrees, entry)

		acquired = wtPath
		if err := WriteState(poolDir, state); err != nil {
			return err
		}
		runPostCreate = true
		return nil
	})
	if err != nil {
		return "", err
	}
	if runPostCreate {
		hooks.Run(postCreate, acquired, os.Stdout, os.Stderr)
	}

	return acquired, nil
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
	if err := git.ResetWorktree(worktreePath, branch); err != nil {
		return err
	}
	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		for i := range state.Worktrees {
			if state.Worktrees[i].Path == worktreePath {
				state.Worktrees[i].OwnerPID = 0
				state.Worktrees[i].OwnerStartedAt = 0
				break
			}
		}
		return WriteState(poolDir, state)
	})
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

		cwd, _ := os.Getwd()

		for _, wt := range state.Worktrees {
			if wt.Destroying {
				continue
			}
			ws := WorktreeStatus{
				Name:   wt.Name,
				Path:   wt.Path,
				Status: StatusAvailable,
			}

			procs, _ := process.FindProcessesInWorktree(wt.Path)
			ws.Processes = procs

			if ownerAlive(wt) {
				ws.Status = StatusInUse
			} else if len(procs) > 0 {
				ws.Status = StatusInUse
				if cwdInWorktree(cwd, wt.Path) {
					ws.Status = StatusHere
				}
			} else if dirty, _ := git.IsDirty(wt.Path); dirty {
				ws.Status = StatusDirty
			}

			result = append(result, ws)
		}
		return nil
	})

	return result, err
}

func Destroy(repoRoot, poolDir, worktreePath string, force bool, preDestroy []string) error {
	var reserved WorktreeEntry
	if err := WithStateLock(poolDir, func() error {
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
			inUse, _ := worktreeInUse(state.Worktrees[idx])
			if inUse {
				return fmt.Errorf("worktree %s is in use by an agent. Use --force to override", worktreePath)
			}
		}

		state.Worktrees[idx].Destroying = true
		if err := reserveOwner(&state.Worktrees[idx]); err != nil {
			return err
		}
		reserved = state.Worktrees[idx]
		return WriteState(poolDir, state)
	}); err != nil {
		return err
	}

	hooks.Run(preDestroy, worktreePath, os.Stdout, os.Stderr)

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
			return nil
		}
		if !sameDestroyReservation(state.Worktrees[idx], reserved) {
			return nil
		}

		_ = git.RemoveWorktree(repoRoot, worktreePath)
		// also clean up the parent numbered directory
		os.RemoveAll(filepath.Dir(worktreePath))

		state.Worktrees = append(state.Worktrees[:idx], state.Worktrees[idx+1:]...)
		return WriteState(poolDir, state)
	})
}

func DestroyAll(repoRoot, poolDir string, force bool, preDestroy []string) error {
	var worktrees []WorktreeEntry
	if err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		if !force {
			for _, wt := range state.Worktrees {
				inUse, _ := worktreeInUse(wt)
				if inUse {
					return fmt.Errorf("worktree %s is in use by an agent. Use --force to override", wt.Path)
				}
			}
		}

		for i := range state.Worktrees {
			state.Worktrees[i].Destroying = true
			if err := reserveOwner(&state.Worktrees[i]); err != nil {
				return err
			}
		}
		worktrees = append([]WorktreeEntry(nil), state.Worktrees...)
		return WriteState(poolDir, state)
	}); err != nil {
		return err
	}

	for _, wt := range worktrees {
		hooks.Run(preDestroy, wt.Path, os.Stdout, os.Stderr)
	}

	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		remove := make(map[string]struct{}, len(worktrees))
		for _, wt := range worktrees {
			idx := -1
			for i := range state.Worktrees {
				if state.Worktrees[i].Path == wt.Path {
					idx = i
					break
				}
			}
			if idx == -1 || !sameDestroyReservation(state.Worktrees[idx], wt) {
				continue
			}
			remove[wt.Path] = struct{}{}
			_ = git.RemoveWorktree(repoRoot, wt.Path)
			os.RemoveAll(filepath.Dir(wt.Path))
		}

		kept := state.Worktrees[:0]
		for _, wt := range state.Worktrees {
			if _, ok := remove[wt.Path]; !ok {
				kept = append(kept, wt)
			}
		}
		state.Worktrees = kept
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
			if wt.OwnerPID != 0 && !ownerAlive(wt) {
				wt.OwnerPID = 0
				wt.OwnerStartedAt = 0
				wt.Destroying = false
			}
			healed = append(healed, wt)
		}
	}
	state.Worktrees = healed
	return state
}

func ownerAlive(wt WorktreeEntry) bool {
	if wt.OwnerPID == 0 || wt.OwnerStartedAt == 0 {
		return false
	}
	startedAt, ok := process.StartedAt(wt.OwnerPID)
	return ok && startedAt == wt.OwnerStartedAt
}

func reserveOwner(wt *WorktreeEntry) error {
	pid := int32(os.Getpid())
	startedAt, ok := process.StartedAt(pid)
	if !ok {
		return fmt.Errorf("failed to determine owner process identity")
	}
	wt.OwnerPID = pid
	wt.OwnerStartedAt = startedAt
	return nil
}

func worktreeInUse(wt WorktreeEntry) (bool, error) {
	if ownerAlive(wt) {
		return true, nil
	}
	return process.IsWorktreeInUse(wt.Path)
}

func sameDestroyReservation(current, reserved WorktreeEntry) bool {
	return current.Path == reserved.Path &&
		current.Destroying &&
		current.OwnerPID == reserved.OwnerPID &&
		current.OwnerStartedAt == reserved.OwnerStartedAt
}

func cwdInWorktree(cwd, worktreePath string) bool {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	absWt, err := filepath.Abs(worktreePath)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absWt, absCwd)
	if err != nil {
		return false
	}
	return rel == "." || !filepath.IsAbs(rel) && len(rel) >= 1 && rel[0] != '.'
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
