package pool

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/hooks"
	"github.com/kunchenguid/treehouse/internal/process"
)

type PruneWorktree struct {
	Name  string
	Path  string
	Bytes int64
}

type PruneSkipped struct {
	Name   string
	Path   string
	Reason string
}

type PruneResult struct {
	Candidates       []PruneWorktree
	Pruned           []PruneWorktree
	Skipped          []PruneSkipped
	ReclaimableBytes int64
	FreedBytes       int64
}

func Prune(repoRoot, poolDir string, dryRun bool, preDestroy []string) (PruneResult, error) {
	if err := git.Fetch(repoRoot); err != nil {
		return PruneResult{}, fmt.Errorf("refresh origin before prune: %w", err)
	}
	defaultRef, err := git.DefaultBranchMergeRef(repoRoot)
	if err != nil {
		return PruneResult{}, fmt.Errorf("resolve default branch before prune: %w", err)
	}

	entries, err := pruneSnapshot(poolDir)
	if err != nil {
		return PruneResult{}, err
	}

	plan := planPrune(defaultRef, entries)
	if dryRun || len(plan.Candidates) == 0 {
		return plan, nil
	}

	return executePrune(repoRoot, poolDir, defaultRef, plan, preDestroy)
}

func pruneSnapshot(poolDir string) ([]WorktreeEntry, error) {
	var entries []WorktreeEntry
	err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		state = healState(state)
		if err := WriteState(poolDir, state); err != nil {
			return err
		}

		entries = append([]WorktreeEntry(nil), state.Worktrees...)
		return nil
	})
	return entries, err
}

func planPrune(defaultRef string, entries []WorktreeEntry) PruneResult {
	var result PruneResult
	for _, wt := range entries {
		worktree, skipped, stale := analyzePruneCandidate(defaultRef, wt)
		if !stale {
			continue
		}
		if skipped.Reason != "" {
			result.Skipped = append(result.Skipped, skipped)
			continue
		}
		result.Candidates = append(result.Candidates, worktree)
		result.ReclaimableBytes += worktree.Bytes
	}
	return result
}

func executePrune(repoRoot, poolDir, defaultRef string, plan PruneResult, preDestroy []string) (PruneResult, error) {
	result := PruneResult{
		Skipped: append([]PruneSkipped(nil), plan.Skipped...),
	}

	planned := make(map[string]PruneWorktree, len(plan.Candidates))
	for _, wt := range plan.Candidates {
		planned[wt.Path] = wt
	}

	var reserved []WorktreeEntry
	if err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		state = healState(state)

		for i := range state.Worktrees {
			plannedWorktree, ok := planned[state.Worktrees[i].Path]
			if !ok {
				continue
			}

			worktree, skipped, stale := analyzePruneCandidate(defaultRef, state.Worktrees[i])
			if !stale {
				continue
			}
			if skipped.Reason != "" {
				result.Skipped = append(result.Skipped, skipped)
				continue
			}
			worktree.Bytes = plannedWorktree.Bytes
			state.Worktrees[i].Destroying = true
			if err := reserveOwner(&state.Worktrees[i]); err != nil {
				return err
			}
			reserved = append(reserved, state.Worktrees[i])
			result.Candidates = append(result.Candidates, worktree)
			result.ReclaimableBytes += worktree.Bytes
		}

		return WriteState(poolDir, state)
	}); err != nil {
		return PruneResult{}, err
	}

	for _, wt := range reserved {
		hooks.Run(preDestroy, wt.Path, os.Stdout, os.Stderr)
	}

	if err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		removed := make(map[string]struct{}, len(reserved))
		for _, reservation := range reserved {
			idx := -1
			for i := range state.Worktrees {
				if state.Worktrees[i].Path == reservation.Path {
					idx = i
					break
				}
			}
			if idx == -1 || !sameDestroyReservation(state.Worktrees[idx], reservation) {
				continue
			}

			worktree, skipped := finalPruneSafetyCheck(defaultRef, state.Worktrees[idx])
			if skipped.Reason != "" {
				clearReservation(&state.Worktrees[idx])
				result.Skipped = append(result.Skipped, skipped)
				continue
			}

			if worktree.Bytes == 0 {
				if plannedWorktree, ok := planned[worktree.Path]; ok {
					worktree.Bytes = plannedWorktree.Bytes
				}
			}

			if err := git.RemoveCleanWorktree(repoRoot, worktree.Path); err != nil {
				clearReservation(&state.Worktrees[idx])
				result.Skipped = append(result.Skipped, PruneSkipped{
					Name:   worktree.Name,
					Path:   worktree.Path,
					Reason: fmt.Sprintf("remove failed: %v", err),
				})
				continue
			}
			if err := os.RemoveAll(filepath.Dir(worktree.Path)); err != nil {
				clearReservation(&state.Worktrees[idx])
				result.Skipped = append(result.Skipped, PruneSkipped{
					Name:   worktree.Name,
					Path:   worktree.Path,
					Reason: fmt.Sprintf("cleanup failed: %v", err),
				})
				continue
			}

			removed[worktree.Path] = struct{}{}
			result.Pruned = append(result.Pruned, worktree)
			result.FreedBytes += worktree.Bytes
		}

		kept := state.Worktrees[:0]
		for _, wt := range state.Worktrees {
			if _, ok := removed[wt.Path]; !ok {
				kept = append(kept, wt)
			}
		}
		state.Worktrees = kept
		return WriteState(poolDir, state)
	}); err != nil {
		return PruneResult{}, err
	}

	return result, nil
}

func analyzePruneCandidate(defaultRef string, wt WorktreeEntry) (PruneWorktree, PruneSkipped, bool) {
	worktree := PruneWorktree{Name: wt.Name, Path: wt.Path}
	skipped := PruneSkipped{Name: wt.Name, Path: wt.Path}

	if wt.Destroying || ownerAlive(wt) {
		return worktree, skipped, false
	}
	inUse, err := process.IsWorktreeInUse(wt.Path)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot check processes: %v", err)
		return worktree, skipped, true
	}
	if inUse {
		return worktree, skipped, false
	}
	return analyzeIdleWorktree(defaultRef, worktree, skipped)
}

func finalPruneSafetyCheck(defaultRef string, wt WorktreeEntry) (PruneWorktree, PruneSkipped) {
	worktree := PruneWorktree{Name: wt.Name, Path: wt.Path}
	skipped := PruneSkipped{Name: wt.Name, Path: wt.Path}

	inUse, err := process.IsWorktreeInUse(wt.Path)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot check processes: %v", err)
		return worktree, skipped
	}
	if inUse {
		skipped.Reason = "in use"
		return worktree, skipped
	}
	worktree, skipped, _ = analyzeIdleWorktree(defaultRef, worktree, skipped)
	return worktree, skipped
}

func analyzeIdleWorktree(defaultRef string, worktree PruneWorktree, skipped PruneSkipped) (PruneWorktree, PruneSkipped, bool) {
	dirty, err := git.IsDirty(worktree.Path)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot check status: %v", err)
		return worktree, skipped, true
	}
	if dirty {
		skipped.Reason = "uncommitted changes"
		return worktree, skipped, true
	}

	merged, err := git.IsHeadMergedIntoRef(worktree.Path, defaultRef)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot prove HEAD is merged into default branch: %v", err)
		return worktree, skipped, true
	}
	if !merged {
		skipped.Reason = fmt.Sprintf("HEAD is not merged into %s", defaultRef)
		return worktree, skipped, true
	}

	bytes, err := dirSize(filepath.Dir(worktree.Path))
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot measure size: %v", err)
		return worktree, skipped, true
	}
	worktree.Bytes = bytes
	return worktree, skipped, true
}

func clearReservation(wt *WorktreeEntry) {
	wt.Destroying = false
	wt.OwnerPID = 0
	wt.OwnerStartedAt = 0
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
