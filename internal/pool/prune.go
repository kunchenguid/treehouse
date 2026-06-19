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

// PruneWorktree describes a stale worktree that prune can remove or did remove.
type PruneWorktree struct {
	Name  string
	Path  string
	Bytes int64
}

// PruneSkipped describes a worktree that prune left in place for safety.
type PruneSkipped struct {
	Name   string
	Path   string
	Reason string
}

// PruneResult describes dry-run candidates, removed worktrees, skipped worktrees,
// and the corresponding byte counts.
type PruneResult struct {
	Candidates       []PruneWorktree
	Pruned           []PruneWorktree
	Skipped          []PruneSkipped
	ReclaimableBytes int64
	FreedBytes       int64
}

// Prune finds stale idle managed worktrees and optionally deletes them.
// A stale worktree is clean, unused, unreserved, and merged into the default
// branch ref selected by git.DefaultBranchMergeRef.
// In dryRun mode Prune reports candidates and reclaimable bytes without deleting.
func Prune(repoRoot, poolDir string, dryRun bool, preDestroy []string) (PruneResult, error) {
	entries, err := pruneSnapshot(poolDir)
	if err != nil {
		return PruneResult{}, err
	}

	plan, defaultRef, err := planPrune(repoRoot, entries)
	if err != nil {
		return PruneResult{}, err
	}
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

func planPrune(repoRoot string, entries []WorktreeEntry) (PruneResult, string, error) {
	var result PruneResult
	var defaultRef string
	resolveDefaultRef := func() (string, error) {
		if defaultRef != "" {
			return defaultRef, nil
		}
		ref, err := resolvePruneDefaultRef(repoRoot)
		if err != nil {
			return "", err
		}
		defaultRef = ref
		return defaultRef, nil
	}

	for _, wt := range entries {
		worktree, skipped, stale, err := analyzePruneCandidate(resolveDefaultRef, wt)
		if err != nil {
			return PruneResult{}, "", err
		}
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
	return result, defaultRef, nil
}

func resolvePruneDefaultRef(repoRoot string) (string, error) {
	if err := git.Fetch(repoRoot); err != nil {
		return "", fmt.Errorf("refresh origin before prune: %w", err)
	}
	defaultRef, err := git.DefaultBranchMergeRef(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve default branch before prune: %w", err)
	}
	return defaultRef, nil
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

			worktree, skipped, stale, err := analyzePruneCandidate(fixedPruneDefaultRef(defaultRef), state.Worktrees[i])
			if err != nil {
				return err
			}
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

type pruneRefResolver func() (string, error)

func fixedPruneDefaultRef(defaultRef string) pruneRefResolver {
	return func() (string, error) {
		return defaultRef, nil
	}
}

func analyzePruneCandidate(resolveDefaultRef pruneRefResolver, wt WorktreeEntry) (PruneWorktree, PruneSkipped, bool, error) {
	worktree := PruneWorktree{Name: wt.Name, Path: wt.Path}
	skipped := PruneSkipped{Name: wt.Name, Path: wt.Path}

	if wt.Destroying || ownerAlive(wt) {
		return worktree, skipped, false, nil
	}
	inUse, err := process.IsWorktreeInUse(wt.Path)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot check processes: %v", err)
		return worktree, skipped, true, nil
	}
	if inUse {
		return worktree, skipped, false, nil
	}
	return analyzeIdleWorktree(resolveDefaultRef, worktree, skipped)
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
	worktree, skipped, _, err = analyzeIdleWorktree(fixedPruneDefaultRef(defaultRef), worktree, skipped)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot prove HEAD is merged into default branch: %v", err)
	}
	return worktree, skipped
}

func analyzeIdleWorktree(resolveDefaultRef pruneRefResolver, worktree PruneWorktree, skipped PruneSkipped) (PruneWorktree, PruneSkipped, bool, error) {
	dirty, err := git.IsDirty(worktree.Path)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot check status: %v", err)
		return worktree, skipped, true, nil
	}
	if dirty {
		skipped.Reason = "uncommitted changes"
		return worktree, skipped, true, nil
	}

	defaultRef, err := resolveDefaultRef()
	if err != nil {
		return worktree, skipped, true, err
	}

	merged, err := git.IsHeadMergedIntoRef(worktree.Path, defaultRef)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot prove HEAD is merged into default branch: %v", err)
		return worktree, skipped, true, nil
	}
	if !merged {
		skipped.Reason = fmt.Sprintf("HEAD is not merged into %s", defaultRef)
		return worktree, skipped, true, nil
	}

	bytes, err := dirSize(filepath.Dir(worktree.Path))
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot measure size: %v", err)
		return worktree, skipped, true, nil
	}
	worktree.Bytes = bytes
	return worktree, skipped, true, nil
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
