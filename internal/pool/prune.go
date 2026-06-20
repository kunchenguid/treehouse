package pool

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

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

// PrunePoolResult records the prune result for one managed pool directory.
type PrunePoolResult struct {
	PoolDir string
	Result  PruneResult
}

// PruneAllResult records per-pool and aggregate prune results under a pool root.
type PruneAllResult struct {
	PoolRoot string
	Pools    []PrunePoolResult
	Result   PruneResult
}

type plannedPrunePool struct {
	PoolDir string
	Plan    prunePlan
}

// Prune finds stale idle managed worktrees and optionally deletes them.
// A stale worktree is clean, unused, unreserved, and merged into the default
// branch ref selected by git.DefaultBranchMergeRef.
// In dryRun mode Prune reports candidates and reclaimable bytes without deleting.
func Prune(repoRoot, poolDir string, dryRun bool, preDestroy []string) (PruneResult, error) {
	return prunePool(poolDir, dryRun, preDestroy, singleRepoPruneContextResolver(repoRoot), false)
}

// PrunePool prunes one pool by deriving each worktree's repository context from
// git metadata.
// Worktrees whose repository or default branch cannot be resolved are reported
// as skipped.
func PrunePool(poolDir string, dryRun bool, preDestroy []string) (PruneResult, error) {
	return prunePool(poolDir, dryRun, preDestroy, worktreePruneContextResolver(), true)
}

// PruneAll prunes every managed pool directly under poolRoot and aggregates the
// results.
// When dryRun is false, all pools are planned before any worktree is deleted.
func PruneAll(poolRoot string, dryRun bool, preDestroy []string) (PruneAllResult, error) {
	poolDirs, err := prunePoolDirs(poolRoot)
	if err != nil {
		return PruneAllResult{}, err
	}

	result := PruneAllResult{
		PoolRoot: poolRoot,
		Pools:    make([]PrunePoolResult, 0, len(poolDirs)),
	}
	plans := make([]plannedPrunePool, 0, len(poolDirs))
	resolveContext := worktreePruneContextResolver()
	for _, poolDir := range poolDirs {
		plan, err := planPrunePool(poolDir, resolveContext, true)
		if err != nil {
			return PruneAllResult{}, err
		}
		plans = append(plans, plannedPrunePool{
			PoolDir: poolDir,
			Plan:    plan,
		})
		addPrunePoolResult(&result, poolDir, plan.Result)
	}
	if dryRun || len(result.Result.Candidates) == 0 {
		return result, nil
	}

	executed := PruneAllResult{
		PoolRoot: poolRoot,
		Pools:    make([]PrunePoolResult, 0, len(plans)),
	}
	for _, planned := range plans {
		poolResult := planned.Plan.Result
		if len(planned.Plan.Result.Candidates) > 0 {
			var err error
			poolResult, err = executePrune(planned.PoolDir, planned.Plan, preDestroy)
			if err != nil {
				return PruneAllResult{}, err
			}
		}
		addPrunePoolResult(&executed, planned.PoolDir, poolResult)
	}
	return executed, nil
}

func addPrunePoolResult(all *PruneAllResult, poolDir string, poolResult PruneResult) {
	all.Pools = append(all.Pools, PrunePoolResult{
		PoolDir: poolDir,
		Result:  poolResult,
	})
	all.Result.Candidates = append(all.Result.Candidates, poolResult.Candidates...)
	all.Result.Pruned = append(all.Result.Pruned, poolResult.Pruned...)
	all.Result.Skipped = append(all.Result.Skipped, poolResult.Skipped...)
	all.Result.ReclaimableBytes += poolResult.ReclaimableBytes
	all.Result.FreedBytes += poolResult.FreedBytes
}

func prunePool(poolDir string, dryRun bool, preDestroy []string, resolveContext pruneContextResolver, skipContextErrors bool) (PruneResult, error) {
	plan, err := planPrunePool(poolDir, resolveContext, skipContextErrors)
	if err != nil {
		return PruneResult{}, err
	}
	if dryRun || len(plan.Result.Candidates) == 0 {
		return plan.Result, nil
	}

	return executePrune(poolDir, plan, preDestroy)
}

func planPrunePool(poolDir string, resolveContext pruneContextResolver, skipContextErrors bool) (prunePlan, error) {
	entries, err := pruneSnapshot(poolDir)
	if err != nil {
		return prunePlan{}, err
	}
	return planPrune(entries, resolveContext, skipContextErrors)
}

func prunePoolDirs(poolRoot string) ([]string, error) {
	entries, err := os.ReadDir(poolRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var poolDirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		poolDir := filepath.Join(poolRoot, entry.Name())
		if _, err := os.Stat(stateFilePath(poolDir)); err == nil {
			poolDirs = append(poolDirs, poolDir)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Strings(poolDirs)
	return poolDirs, nil
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

type pruneContext struct {
	RepoRoot   string
	DefaultRef string
}

type pruneContextResolver func(WorktreeEntry) (pruneContext, error)

type plannedPruneWorktree struct {
	Worktree PruneWorktree
	Context  pruneContext
}

type prunePlan struct {
	Result   PruneResult
	Planned  map[string]plannedPruneWorktree
	Reserved map[string]pruneContext
}

func planPrune(entries []WorktreeEntry, resolveContext pruneContextResolver, skipContextErrors bool) (prunePlan, error) {
	plan := prunePlan{
		Planned: make(map[string]plannedPruneWorktree),
	}
	for _, wt := range entries {
		worktree, skipped, stale, context, err := analyzePruneCandidate(resolveContext, wt)
		if err != nil {
			if skipContextErrors {
				skipped.Reason = fmt.Sprintf("cannot resolve default branch: %v", err)
				plan.Result.Skipped = append(plan.Result.Skipped, skipped)
				continue
			}
			return prunePlan{}, err
		}
		if !stale {
			continue
		}
		if skipped.Reason != "" {
			plan.Result.Skipped = append(plan.Result.Skipped, skipped)
			continue
		}
		plan.Result.Candidates = append(plan.Result.Candidates, worktree)
		plan.Result.ReclaimableBytes += worktree.Bytes
		plan.Planned[worktree.Path] = plannedPruneWorktree{
			Worktree: worktree,
			Context:  context,
		}
	}
	return plan, nil
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

func singleRepoPruneContextResolver(repoRoot string) pruneContextResolver {
	var defaultRef string
	return func(WorktreeEntry) (pruneContext, error) {
		if defaultRef == "" {
			ref, err := resolvePruneDefaultRef(repoRoot)
			if err != nil {
				return pruneContext{}, err
			}
			defaultRef = ref
		}
		return pruneContext{RepoRoot: repoRoot, DefaultRef: defaultRef}, nil
	}
}

func worktreePruneContextResolver() pruneContextResolver {
	contexts := make(map[string]pruneContext)
	return func(wt WorktreeEntry) (pruneContext, error) {
		repoRoot, err := git.FindMainRepoRootFrom(wt.Path)
		if err != nil {
			return pruneContext{}, fmt.Errorf("resolve repository for %s: %w", wt.Path, err)
		}
		if context, ok := contexts[repoRoot]; ok {
			return context, nil
		}

		defaultRef, err := resolvePruneDefaultRef(repoRoot)
		if err != nil {
			return pruneContext{}, err
		}
		context := pruneContext{RepoRoot: repoRoot, DefaultRef: defaultRef}
		contexts[repoRoot] = context
		return context, nil
	}
}

func fixedPruneContextResolver(context pruneContext) pruneContextResolver {
	return func(WorktreeEntry) (pruneContext, error) {
		return context, nil
	}
}

func executePrune(poolDir string, plan prunePlan, preDestroy []string) (PruneResult, error) {
	result := PruneResult{
		Skipped: append([]PruneSkipped(nil), plan.Result.Skipped...),
	}

	var reserved []WorktreeEntry
	if err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		state = healState(state)

		for i := range state.Worktrees {
			plannedWorktree, ok := plan.Planned[state.Worktrees[i].Path]
			if !ok {
				continue
			}

			worktree, skipped, stale, context, err := analyzePruneCandidate(fixedPruneContextResolver(plannedWorktree.Context), state.Worktrees[i])
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
			worktree.Bytes = plannedWorktree.Worktree.Bytes
			state.Worktrees[i].Destroying = true
			if err := reserveOwner(&state.Worktrees[i]); err != nil {
				return err
			}
			reserved = append(reserved, state.Worktrees[i])
			if plan.Reserved == nil {
				plan.Reserved = make(map[string]pruneContext)
			}
			plan.Reserved[state.Worktrees[i].Path] = context
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

			context := plan.Reserved[reservation.Path]
			worktree, skipped := finalPruneSafetyCheck(context.DefaultRef, state.Worktrees[idx])
			if skipped.Reason != "" {
				clearReservation(&state.Worktrees[idx])
				result.Skipped = append(result.Skipped, skipped)
				continue
			}

			if worktree.Bytes == 0 {
				if plannedWorktree, ok := plan.Planned[worktree.Path]; ok {
					worktree.Bytes = plannedWorktree.Worktree.Bytes
				}
			}

			if err := git.RemoveCleanWorktree(context.RepoRoot, worktree.Path); err != nil {
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

func analyzePruneCandidate(resolveContext pruneContextResolver, wt WorktreeEntry) (PruneWorktree, PruneSkipped, bool, pruneContext, error) {
	worktree := PruneWorktree{Name: wt.Name, Path: wt.Path}
	skipped := PruneSkipped{Name: wt.Name, Path: wt.Path}

	if wt.Destroying || ownerAlive(wt) {
		return worktree, skipped, false, pruneContext{}, nil
	}
	inUse, err := process.IsWorktreeInUse(wt.Path)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot check processes: %v", err)
		return worktree, skipped, true, pruneContext{}, nil
	}
	if inUse {
		return worktree, skipped, false, pruneContext{}, nil
	}
	return analyzeIdleWorktree(resolveContext, wt, worktree, skipped)
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
	context := pruneContext{DefaultRef: defaultRef}
	worktree, skipped, _, _, err = analyzeIdleWorktree(fixedPruneContextResolver(context), wt, worktree, skipped)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot prove HEAD is merged into default branch: %v", err)
	}
	return worktree, skipped
}

func analyzeIdleWorktree(resolveContext pruneContextResolver, wt WorktreeEntry, worktree PruneWorktree, skipped PruneSkipped) (PruneWorktree, PruneSkipped, bool, pruneContext, error) {
	dirty, err := git.IsDirty(worktree.Path)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot check status: %v", err)
		return worktree, skipped, true, pruneContext{}, nil
	}
	if dirty {
		skipped.Reason = "uncommitted changes"
		return worktree, skipped, true, pruneContext{}, nil
	}

	context, err := resolveContext(wt)
	if err != nil {
		return worktree, skipped, true, pruneContext{}, err
	}

	merged, err := git.IsHeadMergedIntoRef(worktree.Path, context.DefaultRef)
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot prove HEAD is merged into default branch: %v", err)
		return worktree, skipped, true, context, nil
	}
	if !merged {
		skipped.Reason = fmt.Sprintf("HEAD is not merged into %s", context.DefaultRef)
		return worktree, skipped, true, context, nil
	}

	bytes, err := dirSize(filepath.Dir(worktree.Path))
	if err != nil {
		skipped.Reason = fmt.Sprintf("cannot measure size: %v", err)
		return worktree, skipped, true, context, nil
	}
	worktree.Bytes = bytes
	return worktree, skipped, true, context, nil
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
