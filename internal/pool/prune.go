package pool

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/hooks"
	"github.com/kunchenguid/treehouse/internal/process"
)

// PruneWorktree describes a stale or explicitly selected orphaned worktree that
// prune can remove or did remove.
type PruneWorktree struct {
	Name  string
	Path  string
	Bytes int64
	// Orphaned marks a backing-repository-missing worktree that was explicitly
	// included by PruneOptions.PruneOrphans.
	Orphaned bool
	// Warning describes safety information that should be shown with the entry.
	Warning string
}

// PruneSkipped describes a worktree that prune left in place for safety.
type PruneSkipped struct {
	Name string
	Path string
	// Category is the stable group label for prune skip reporting.
	Category string
	// Reason is the short user-facing explanation for this specific worktree.
	Reason string
	// Detail carries raw diagnostics intended for verbose output.
	Detail string
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

// PruneOptions controls dry-run, orphan, and hook behavior for prune operations.
type PruneOptions struct {
	// DryRun reports candidates and byte counts without deleting worktrees.
	DryRun bool
	// PruneOrphans includes backing-repository-missing linked worktrees as
	// unverified prune candidates.
	PruneOrphans bool
	// PreDestroy is the hook command list to run before deleting each candidate.
	PreDestroy []string
}

const (
	// PruneSkipUncommitted means a worktree has tracked or untracked changes.
	PruneSkipUncommitted = "uncommitted changes"
	// PruneSkipUnmerged means HEAD is not merged into the selected default ref.
	PruneSkipUnmerged = "unmerged"
	// PruneSkipOrphanedBackingRepo means the linked worktree's gitdir is gone.
	PruneSkipOrphanedBackingRepo = "orphaned (backing repository missing)"
	// PruneSkipOriginUnreachable means origin could not be reached for verification.
	PruneSkipOriginUnreachable     = "origin unreachable (cannot verify)"
	pruneSkipCannotVerify          = "cannot verify worktree"
	pruneSkipCannotCheckProcesses  = "cannot check processes"
	pruneSkipCannotMeasureSize     = "cannot measure size"
	pruneSkipCleanupFailed         = "cleanup failed"
	pruneSkipRemoveFailed          = "remove failed"
	pruneSkipInUse                 = "in use"
	pruneOrphanUnverifiedWarning   = "content could not be verified"
	pruneOrphanRecoveredRepository = "backing repository is available again"
)

type plannedPrunePool struct {
	PoolDir string
	Plan    prunePlan
}

// Prune finds stale idle managed worktrees and optionally deletes them.
// A stale worktree is clean, unused, unleased, not reserved by another lifecycle
// operation, and merged into the default branch ref selected by
// git.DefaultBranchMergeRef.
// In dryRun mode Prune reports candidates and reclaimable bytes without deleting.
// Backing-repository-missing orphans are reported as skipped; use
// PruneWithOptions with PruneOptions.PruneOrphans to include them as candidates.
func Prune(repoRoot, poolDir string, dryRun bool, preDestroy []string) (PruneResult, error) {
	return PruneWithOptions(repoRoot, poolDir, PruneOptions{
		DryRun:     dryRun,
		PreDestroy: preDestroy,
	})
}

// PruneWithOptions finds prune candidates in one repository pool and applies
// options for dry-run behavior, orphan handling, and pre-destroy hooks.
func PruneWithOptions(repoRoot, poolDir string, options PruneOptions) (PruneResult, error) {
	return prunePool(poolDir, options, singleRepoPruneContextResolver(repoRoot))
}

// PrunePool prunes one pool by deriving each worktree's repository context from
// git metadata.
// Worktrees whose repository or default branch cannot be resolved are reported
// as skipped.
// Backing-repository-missing orphans are reported as skipped; use
// PrunePoolWithOptions with PruneOptions.PruneOrphans to include them as
// candidates.
func PrunePool(poolDir string, dryRun bool, preDestroy []string) (PruneResult, error) {
	return PrunePoolWithOptions(poolDir, PruneOptions{
		DryRun:     dryRun,
		PreDestroy: preDestroy,
	})
}

// PrunePoolWithOptions prunes one pool by deriving each worktree's repository
// context from git metadata and applying the supplied options.
func PrunePoolWithOptions(poolDir string, options PruneOptions) (PruneResult, error) {
	return prunePool(poolDir, options, worktreePruneContextResolver())
}

// PruneAll prunes every managed pool directly under poolRoot and aggregates the
// results.
// When dryRun is false, all pools are planned before any worktree is deleted.
// Backing-repository-missing orphans are reported as skipped; use
// PruneAllWithOptions with PruneOptions.PruneOrphans to include them as
// candidates.
func PruneAll(poolRoot string, dryRun bool, preDestroy []string) (PruneAllResult, error) {
	return PruneAllWithOptions(poolRoot, PruneOptions{
		DryRun:     dryRun,
		PreDestroy: preDestroy,
	})
}

// PruneAllWithOptions prunes every managed pool directly under poolRoot and
// applies options for dry-run behavior, orphan handling, and pre-destroy hooks.
func PruneAllWithOptions(poolRoot string, options PruneOptions) (PruneAllResult, error) {
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
		plan, err := planPrunePool(poolDir, resolveContext, options)
		if err != nil {
			return PruneAllResult{}, err
		}
		plans = append(plans, plannedPrunePool{
			PoolDir: poolDir,
			Plan:    plan,
		})
		addPrunePoolResult(&result, poolDir, plan.Result)
	}
	if options.DryRun || len(result.Result.Candidates) == 0 {
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
			poolResult, err = executePrune(planned.PoolDir, planned.Plan, options)
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

func prunePool(poolDir string, options PruneOptions, resolveContext pruneContextResolver) (PruneResult, error) {
	plan, err := planPrunePool(poolDir, resolveContext, options)
	if err != nil {
		return PruneResult{}, err
	}
	if options.DryRun || len(plan.Result.Candidates) == 0 {
		return plan.Result, nil
	}

	return executePrune(poolDir, plan, options)
}

func planPrunePool(poolDir string, resolveContext pruneContextResolver, options PruneOptions) (prunePlan, error) {
	entries, err := pruneSnapshot(poolDir)
	if err != nil {
		return prunePlan{}, err
	}
	return planPrune(entries, resolveContext, options)
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
	Reserved map[string]plannedPruneWorktree
}

func planPrune(entries []WorktreeEntry, resolveContext pruneContextResolver, options PruneOptions) (prunePlan, error) {
	plan := prunePlan{
		Planned: make(map[string]plannedPruneWorktree),
	}
	for _, wt := range entries {
		worktree, skipped, stale, context, err := analyzePruneCandidate(resolveContext, wt, options)
		if err != nil {
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
		return "", pruneVerificationError{
			Category: PruneSkipOriginUnreachable,
			Reason:   "cannot fetch origin",
			Detail:   fmt.Sprintf("refresh origin before prune: %v", err),
		}
	}
	defaultRef, err := git.DefaultBranchMergeRef(repoRoot)
	if err != nil {
		category := pruneSkipCannotVerify
		reason := "cannot resolve default branch"
		if git.HasRemote(repoRoot, "origin") && isOriginAccessError(err) {
			category = PruneSkipOriginUnreachable
			reason = "cannot resolve origin default branch"
		} else if git.HasRemote(repoRoot, "origin") {
			reason = "cannot verify origin default branch"
		}
		return "", pruneVerificationError{
			Category: category,
			Reason:   reason,
			Detail:   fmt.Sprintf("resolve default branch before prune: %v", err),
		}
	}
	return defaultRef, nil
}

func singleRepoPruneContextResolver(repoRoot string) pruneContextResolver {
	var defaultRef string
	var defaultErr error
	resolved := false
	return func(WorktreeEntry) (pruneContext, error) {
		if !resolved {
			ref, err := resolvePruneDefaultRef(repoRoot)
			if err != nil {
				defaultErr = err
			} else {
				defaultRef = ref
			}
			resolved = true
		}
		if defaultErr != nil {
			return pruneContext{}, defaultErr
		}
		return pruneContext{RepoRoot: repoRoot, DefaultRef: defaultRef}, nil
	}
}

func worktreePruneContextResolver() pruneContextResolver {
	contexts := make(map[string]pruneContext)
	contextErrors := make(map[string]error)
	return func(wt WorktreeEntry) (pruneContext, error) {
		repoRoot, err := git.FindMainRepoRootFrom(wt.Path)
		if err != nil {
			return pruneContext{}, fmt.Errorf("resolve repository for %s: %w", wt.Path, err)
		}
		if context, ok := contexts[repoRoot]; ok {
			return context, nil
		}
		if err, ok := contextErrors[repoRoot]; ok {
			return pruneContext{}, err
		}

		defaultRef, err := resolvePruneDefaultRef(repoRoot)
		if err != nil {
			contextErrors[repoRoot] = err
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

func executePrune(poolDir string, plan prunePlan, options PruneOptions) (PruneResult, error) {
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

			worktree, skipped, stale, context, err := analyzePruneCandidate(fixedPruneContextResolver(plannedWorktree.Context), state.Worktrees[i], options)
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
				plan.Reserved = make(map[string]plannedPruneWorktree)
			}
			plan.Reserved[state.Worktrees[i].Path] = plannedPruneWorktree{
				Worktree: worktree,
				Context:  context,
			}
			result.Candidates = append(result.Candidates, worktree)
			result.ReclaimableBytes += worktree.Bytes
		}

		return WriteState(poolDir, state)
	}); err != nil {
		return PruneResult{}, err
	}

	for _, wt := range reserved {
		hooks.Run(options.PreDestroy, wt.Path, os.Stdout, os.Stderr)
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

			plannedWorktree := plan.Reserved[reservation.Path]
			context := plannedWorktree.Context
			var worktree PruneWorktree
			var skipped PruneSkipped
			if plannedWorktree.Worktree.Orphaned {
				worktree, skipped = finalOrphanPruneSafetyCheck(state.Worktrees[idx])
			} else {
				worktree, skipped = finalPruneSafetyCheck(context.DefaultRef, state.Worktrees[idx])
			}
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

			if worktree.Orphaned {
				container, err := removableWorktreeContainer(worktree.Path)
				if err != nil {
					clearReservation(&state.Worktrees[idx])
					result.Skipped = append(result.Skipped, newPruneSkipped(worktree.Name, worktree.Path, pruneSkipCleanupFailed, "refusing unsafe cleanup path", err.Error()))
					continue
				}
				if err := os.RemoveAll(container); err != nil {
					clearReservation(&state.Worktrees[idx])
					result.Skipped = append(result.Skipped, newPruneSkipped(worktree.Name, worktree.Path, pruneSkipCleanupFailed, "could not remove worktree directory", err.Error()))
					continue
				}
			} else {
				if err := git.RemoveCleanWorktree(context.RepoRoot, worktree.Path); err != nil {
					clearReservation(&state.Worktrees[idx])
					result.Skipped = append(result.Skipped, newPruneSkipped(worktree.Name, worktree.Path, pruneSkipRemoveFailed, "git refused to remove worktree", err.Error()))
					continue
				}
				container, err := removableWorktreeContainer(worktree.Path)
				if err != nil {
					clearReservation(&state.Worktrees[idx])
					result.Skipped = append(result.Skipped, newPruneSkipped(worktree.Name, worktree.Path, pruneSkipCleanupFailed, "refusing unsafe cleanup path", err.Error()))
					continue
				}
				if err := os.RemoveAll(container); err != nil {
					clearReservation(&state.Worktrees[idx])
					result.Skipped = append(result.Skipped, newPruneSkipped(worktree.Name, worktree.Path, pruneSkipCleanupFailed, "could not remove worktree directory", err.Error()))
					continue
				}
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

func analyzePruneCandidate(resolveContext pruneContextResolver, wt WorktreeEntry, options PruneOptions) (PruneWorktree, PruneSkipped, bool, pruneContext, error) {
	worktree := PruneWorktree{Name: wt.Name, Path: wt.Path}
	skipped := PruneSkipped{Name: wt.Name, Path: wt.Path}

	if wt.Destroying || wt.Leased || ownerAlive(wt) {
		return worktree, skipped, false, pruneContext{}, nil
	}
	inUse, err := process.IsWorktreeInUse(wt.Path)
	if err != nil {
		skipped = newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotCheckProcesses, "cannot check processes", err.Error())
		return worktree, skipped, true, pruneContext{}, nil
	}
	if inUse {
		return worktree, skipped, false, pruneContext{}, nil
	}
	return analyzeIdleWorktree(resolveContext, wt, worktree, skipped, options)
}

func finalPruneSafetyCheck(defaultRef string, wt WorktreeEntry) (PruneWorktree, PruneSkipped) {
	worktree := PruneWorktree{Name: wt.Name, Path: wt.Path}
	skipped := PruneSkipped{Name: wt.Name, Path: wt.Path}

	inUse, err := process.IsWorktreeInUse(wt.Path)
	if err != nil {
		skipped = newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotCheckProcesses, "cannot check processes", err.Error())
		return worktree, skipped
	}
	if inUse {
		skipped = newPruneSkipped(wt.Name, wt.Path, pruneSkipInUse, pruneSkipInUse, "")
		return worktree, skipped
	}
	context := pruneContext{DefaultRef: defaultRef}
	worktree, skipped, _, _, err = analyzeIdleWorktree(fixedPruneContextResolver(context), wt, worktree, skipped, PruneOptions{})
	if err != nil {
		skipped = newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotVerify, "cannot prove HEAD is merged into default branch", err.Error())
	}
	return worktree, skipped
}

func finalOrphanPruneSafetyCheck(wt WorktreeEntry) (PruneWorktree, PruneSkipped) {
	worktree := PruneWorktree{
		Name:     wt.Name,
		Path:     wt.Path,
		Orphaned: true,
		Warning:  pruneOrphanUnverifiedWarning,
	}
	skipped := PruneSkipped{Name: wt.Name, Path: wt.Path}

	inUse, err := process.IsWorktreeInUse(wt.Path)
	if err != nil {
		return worktree, newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotCheckProcesses, "cannot check processes", err.Error())
	}
	if inUse {
		return worktree, newPruneSkipped(wt.Name, wt.Path, pruneSkipInUse, pruneSkipInUse, "")
	}

	orphaned, detail := backingRepositoryMissing(wt.Path)
	if !orphaned {
		return worktree, newPruneSkipped(wt.Name, wt.Path, PruneSkipOrphanedBackingRepo, pruneOrphanRecoveredRepository, detail)
	}

	container, err := removableWorktreeContainer(worktree.Path)
	if err != nil {
		return worktree, newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotMeasureSize, "refusing unsafe cleanup path", err.Error())
	}
	bytes, err := dirSize(container)
	if err != nil {
		return worktree, newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotMeasureSize, "cannot measure size", err.Error())
	}
	worktree.Bytes = bytes
	return worktree, skipped
}

func analyzeIdleWorktree(resolveContext pruneContextResolver, wt WorktreeEntry, worktree PruneWorktree, skipped PruneSkipped, options PruneOptions) (PruneWorktree, PruneSkipped, bool, pruneContext, error) {
	if orphaned, detail := backingRepositoryMissing(worktree.Path); orphaned {
		if !options.PruneOrphans {
			skipped = newPruneSkipped(wt.Name, wt.Path, PruneSkipOrphanedBackingRepo, pruneOrphanUnverifiedWarning, detail)
			return worktree, skipped, true, pruneContext{}, nil
		}

		container, err := removableWorktreeContainer(worktree.Path)
		if err != nil {
			skipped = newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotMeasureSize, "refusing unsafe cleanup path", err.Error())
			return worktree, skipped, true, pruneContext{}, nil
		}
		bytes, err := dirSize(container)
		if err != nil {
			skipped = newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotMeasureSize, "cannot measure size", err.Error())
			return worktree, skipped, true, pruneContext{}, nil
		}
		worktree.Bytes = bytes
		worktree.Orphaned = true
		worktree.Warning = pruneOrphanUnverifiedWarning
		return worktree, skipped, true, pruneContext{}, nil
	}

	dirty, err := git.IsDirty(worktree.Path)
	if err != nil {
		if orphaned, detail := backingRepositoryMissing(worktree.Path); orphaned {
			skipped = newPruneSkipped(wt.Name, wt.Path, PruneSkipOrphanedBackingRepo, pruneOrphanUnverifiedWarning, detail)
		} else {
			skipped = newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotVerify, "cannot check status", err.Error())
		}
		return worktree, skipped, true, pruneContext{}, nil
	}
	if dirty {
		skipped = newPruneSkipped(wt.Name, wt.Path, PruneSkipUncommitted, PruneSkipUncommitted, "")
		return worktree, skipped, true, pruneContext{}, nil
	}

	context, err := resolveContext(wt)
	if err != nil {
		skipped = skippedFromVerificationError(wt.Name, wt.Path, err)
		return worktree, skipped, true, pruneContext{}, nil
	}

	merged, err := git.IsHeadMergedIntoRef(worktree.Path, context.DefaultRef)
	if err != nil {
		if orphaned, detail := backingRepositoryMissing(worktree.Path); orphaned {
			skipped = newPruneSkipped(wt.Name, wt.Path, PruneSkipOrphanedBackingRepo, pruneOrphanUnverifiedWarning, detail)
		} else {
			skipped = newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotVerify, "cannot prove HEAD is merged into default branch", err.Error())
		}
		return worktree, skipped, true, context, nil
	}
	if !merged {
		skipped = newPruneSkipped(wt.Name, wt.Path, PruneSkipUnmerged, fmt.Sprintf("HEAD not merged into %s", context.DefaultRef), "")
		return worktree, skipped, true, context, nil
	}

	container, err := removableWorktreeContainer(worktree.Path)
	if err != nil {
		skipped = newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotMeasureSize, "refusing unsafe cleanup path", err.Error())
		return worktree, skipped, true, context, nil
	}
	bytes, err := dirSize(container)
	if err != nil {
		skipped = newPruneSkipped(wt.Name, wt.Path, pruneSkipCannotMeasureSize, "cannot measure size", err.Error())
		return worktree, skipped, true, context, nil
	}
	worktree.Bytes = bytes
	return worktree, skipped, true, context, nil
}

type pruneVerificationError struct {
	Category string
	Reason   string
	Detail   string
}

func (e pruneVerificationError) Error() string {
	return e.Detail
}

func skippedFromVerificationError(name, path string, err error) PruneSkipped {
	var pruneErr pruneVerificationError
	if errors.As(err, &pruneErr) {
		return newPruneSkipped(name, path, pruneErr.Category, pruneErr.Reason, pruneErr.Detail)
	}
	return newPruneSkipped(name, path, pruneSkipCannotVerify, "cannot verify worktree", err.Error())
}

func isOriginAccessError(err error) bool {
	detail := err.Error()
	return strings.Contains(detail, "git ls-remote") ||
		strings.Contains(detail, "Could not read from remote repository") ||
		strings.Contains(detail, "does not appear to be a git repository") ||
		strings.Contains(detail, "repository") && strings.Contains(detail, "not found")
}

func newPruneSkipped(name, path, category, reason, detail string) PruneSkipped {
	if category == "" {
		category = pruneSkipCannotVerify
	}
	if reason == "" {
		reason = category
	}
	return PruneSkipped{
		Name:     name,
		Path:     path,
		Category: category,
		Reason:   reason,
		Detail:   detail,
	}
}

func backingRepositoryMissing(worktreePath string) (bool, string) {
	gitDir, ok, detail := linkedWorktreeGitDir(worktreePath)
	if !ok {
		return false, detail
	}
	if _, err := os.Stat(gitDir); err == nil {
		return false, ""
	} else if os.IsNotExist(err) {
		return true, fmt.Sprintf("gitdir %s does not exist", gitDir)
	} else {
		return false, fmt.Sprintf("cannot inspect gitdir %s: %v", gitDir, err)
	}
}

func linkedWorktreeGitDir(worktreePath string) (string, bool, string) {
	gitFile := filepath.Join(worktreePath, ".git")
	info, err := os.Stat(gitFile)
	if err != nil {
		return "", false, err.Error()
	}
	if info.IsDir() {
		return "", false, ""
	}

	data, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false, err.Error()
	}
	line, _, _ := strings.Cut(string(data), "\n")
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return "", false, fmt.Sprintf("%s does not contain a gitdir pointer", gitFile)
	}
	gitDir = strings.TrimSpace(gitDir)
	if gitDir == "" {
		return "", false, fmt.Sprintf("%s has an empty gitdir pointer", gitFile)
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}
	return filepath.Clean(gitDir), true, ""
}

func removableWorktreeContainer(worktreePath string) (string, error) {
	container := filepath.Clean(filepath.Dir(worktreePath))
	if container == "." || filepath.Dir(container) == container {
		return "", fmt.Errorf("refusing to remove %s", container)
	}
	return container, nil
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
