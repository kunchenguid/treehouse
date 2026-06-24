package pool

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/hooks"
	"github.com/kunchenguid/treehouse/internal/process"
)

// DestroyClass is the safety classification of a worktree considered for
// destruction. It mirrors the notions prune uses (see analyzeIdleWorktree and
// analyzePruneCandidate in prune.go): a worktree is disposable only when it is
// unleased, idle, clean, and merged into the default branch.
type DestroyClass string

const (
	// DestroyDisposable means merged, clean, idle, and unleased: the genuinely
	// safe set that a bare destroy removes.
	DestroyDisposable DestroyClass = "disposable"
	// DestroyLeased means the worktree carries a durable lease.
	DestroyLeased DestroyClass = "leased"
	// DestroyInUse means an owner reservation or a live process is using it.
	DestroyInUse DestroyClass = "in-use"
	// DestroyDirty means the working tree has uncommitted (tracked or untracked)
	// changes.
	DestroyDirty DestroyClass = "dirty"
	// DestroyUnmerged means HEAD is not merged into the default branch ref.
	DestroyUnmerged DestroyClass = "unmerged"
	// DestroyUnverified means treehouse could not prove the work landed (backing
	// repository missing, or status/merge could not be checked). It is gated like
	// unlanded work because removing it may lose data.
	DestroyUnverified DestroyClass = "unverified"
)

// Opt-in flag names recorded on skipped targets so the command layer can tell
// the user exactly which flag would authorize a risky removal.
const (
	IncludeUnlandedFlag = "--include-unlanded"
	IncludeInUseFlag    = "--include-in-use"
	IncludeLeasedFlag   = "--include-leased"
)

// destroyGracePeriod bounds how long destruction waits for lingering worktree
// processes to exit after SIGTERM before escalating, matching `get`/`return`.
const destroyGracePeriod = 2 * time.Second

var findProcessesInWorktree = process.FindProcessesInWorktree
var terminateWorktreeProcesses = process.TerminateWorktreeProcesses

type destroyReservation struct {
	worktree               WorktreeEntry
	originalOwnerPID       int32
	originalOwnerStartedAt int64
}

// DestroyTarget describes one worktree considered for destruction.
type DestroyTarget struct {
	Name  string
	Path  string
	Bytes int64
	// Class is the first safety class assigned to the target, kept for simple
	// callers and stable grouping.
	Class DestroyClass
	// Classes lists every safety class assigned to the target. A worktree can be
	// leased and dirty, for example, and then requires every corresponding flag.
	Classes   []DestroyClass
	Processes []process.ProcessInfo
	// Detail is an honest, user-facing diagnostic for non-disposable targets
	// (e.g. "HEAD not merged into origin/main" or "held by secondmate").
	Detail string
}

// DestroySkip records a worktree left in place and the opt-in flag or flags
// that would have authorized its removal when a flag can authorize it.
type DestroySkip struct {
	Target DestroyTarget
	// NeededFlag is the --include-* flag that would authorize removal, or empty
	// when no flag can (e.g. a worktree re-acquired during the pre-destroy hook).
	NeededFlag string
	// NeededFlags lists every missing --include-* flag for combined-risk targets.
	NeededFlags []string
	// LeasedBulk marks a leased worktree skipped by a bulk pool destroy. Such a
	// worktree can NEVER be removed by --all; it can only be removed by naming its
	// exact path with --include-leased.
	LeasedBulk bool
}

// DestroyResult reports what a plan would remove (dry run) or did remove.
type DestroyResult struct {
	// Planned lists the removable targets: previewed in a dry run, attempted
	// otherwise.
	Planned []DestroyTarget
	// Destroyed lists targets actually removed (empty on a dry run).
	Destroyed []DestroyTarget
	// Skipped lists targets left in place and why.
	Skipped []DestroySkip
	// PlannedBytes is the reclaimable size of Planned.
	PlannedBytes int64
	// FreedBytes is the size freed by Destroyed.
	FreedBytes int64
}

// DestroyOptions gates which risky classes a destroy may remove. Each risky
// class is opt-in so a bare destroy only removes the disposable set.
type DestroyOptions struct {
	// DryRun classifies and previews without removing anything.
	DryRun bool
	// IncludeUnlanded allows removing dirty, unmerged, or unverified worktrees
	// (irreversible data loss).
	IncludeUnlanded bool
	// IncludeInUse allows removing worktrees with a live process or owner
	// reservation; their processes are terminated first.
	IncludeInUse bool
	// IncludeLeased allows removing leased worktrees. It is honored only when the
	// exact worktree path is named (DestroyWorktree); a bulk pool destroy never
	// removes leased worktrees regardless of this flag.
	IncludeLeased bool
	// PreDestroy is the hook command list to run before deleting each worktree.
	PreDestroy []string
}

// DestroyWorktree plans or removes a single named managed worktree. Because the
// exact path is named, a leased worktree may be removed when IncludeLeased is
// set. Missing opt-in flags are reported as skips, not errors.
func DestroyWorktree(poolDir, worktreePath string, opts DestroyOptions) (DestroyResult, error) {
	var target *WorktreeEntry
	if err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		state = healState(state)
		if err := WriteState(poolDir, state); err != nil {
			return err
		}
		for i := range state.Worktrees {
			if state.Worktrees[i].Path == worktreePath {
				entry := state.Worktrees[i]
				target = &entry
				break
			}
		}
		return nil
	}); err != nil {
		return DestroyResult{}, err
	}
	if target == nil {
		return DestroyResult{}, fmt.Errorf("worktree %s is not managed by treehouse", worktreePath)
	}

	// allowLeased is true: a named path is an explicit, single-target choice.
	return planAndDestroy(poolDir, []WorktreeEntry{*target}, true, opts)
}

// DestroyPool plans or removes every managed worktree in poolDir (the bulk
// `--all` path). Leased worktrees are NEVER removable here, regardless of
// IncludeLeased: a lease can only be cleared by naming its exact path.
func DestroyPool(poolDir string, opts DestroyOptions) (DestroyResult, error) {
	var targets []WorktreeEntry
	if err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		state = healState(state)
		if err := WriteState(poolDir, state); err != nil {
			return err
		}
		targets = append([]WorktreeEntry(nil), state.Worktrees...)
		return nil
	}); err != nil {
		return DestroyResult{}, err
	}

	return planAndDestroy(poolDir, targets, false, opts)
}

func planAndDestroy(poolDir string, targets []WorktreeEntry, allowLeased bool, opts DestroyOptions) (DestroyResult, error) {
	repoRoot := resolvePoolRepoRoot(targets)
	defaultRef := ""
	if repoRoot != "" {
		// Resolve the merge target the same way prune does, so destroy and prune
		// agree on what "unmerged" means. A failure leaves defaultRef empty, which
		// classifyForDestroy reports as unverified rather than disposable.
		if ref, err := resolvePruneDefaultRef(repoRoot); err == nil {
			defaultRef = ref
		}
	}

	var result DestroyResult
	var removable []DestroyTarget
	for _, wt := range targets {
		target := classifyForDestroy(wt, defaultRef)
		measureDestroySize(&target)
		ok, skip := opts.allows(target, allowLeased)
		if ok {
			removable = append(removable, target)
		} else {
			result.Skipped = append(result.Skipped, skip)
		}
	}
	sortDestroyTargets(removable)
	sortDestroySkips(result.Skipped)
	result.Planned = removable
	for _, t := range removable {
		result.PlannedBytes += t.Bytes
	}

	if opts.DryRun {
		return result, nil
	}

	destroyed, execSkips, err := executeDestroy(poolDir, removable, repoRoot, defaultRef, allowLeased, opts)
	if err != nil {
		return DestroyResult{}, err
	}
	result.Destroyed = destroyed
	for _, t := range destroyed {
		result.FreedBytes += t.Bytes
	}
	result.Skipped = append(result.Skipped, execSkips...)
	return result, nil
}

// allows reports whether opts authorize removing target, returning a populated
// DestroySkip otherwise.
func (opts DestroyOptions) allows(target DestroyTarget, allowLeased bool) (bool, DestroySkip) {
	missing := opts.missingFlags(target, allowLeased)
	if len(missing) == 0 {
		return true, DestroySkip{}
	}
	return false, DestroySkip{
		Target:      target,
		NeededFlag:  missing[0],
		NeededFlags: missing,
		LeasedBulk:  target.hasClass(DestroyLeased) && !allowLeased,
	}
}

func (opts DestroyOptions) missingFlags(target DestroyTarget, allowLeased bool) []string {
	var missing []string
	if target.hasClass(DestroyLeased) && (!allowLeased || !opts.IncludeLeased) {
		missing = append(missing, IncludeLeasedFlag)
	}
	if target.hasClass(DestroyInUse) && !opts.IncludeInUse {
		missing = append(missing, IncludeInUseFlag)
	}
	if target.hasUnlandedClass() && !opts.IncludeUnlanded {
		missing = append(missing, IncludeUnlandedFlag)
	}
	return missing
}

// classifyForDestroy determines a managed worktree's destroy class using the
// same safety primitives prune relies on (ownerAlive,
// process.FindProcessesInWorktree, backingRepositoryMissing, git.IsDirty,
// git.IsHeadMergedIntoRef against the ref from resolvePruneDefaultRef).
func classifyForDestroy(wt WorktreeEntry, defaultRef string) DestroyTarget {
	target := DestroyTarget{Name: wt.Name, Path: wt.Path}

	if wt.Leased {
		detail := ""
		if wt.LeaseHolder != "" {
			detail = "held by " + wt.LeaseHolder
		}
		target.addClass(DestroyLeased, detail)
	}

	procs, procErr := findProcessesInWorktree(wt.Path)
	if ownerAlive(wt) || len(procs) > 0 {
		target.Processes = procs
		target.addClass(DestroyInUse, "")
	}
	if procErr != nil {
		target.addClass(DestroyInUse, "cannot check processes: "+procErr.Error())
	}

	if orphaned, detail := backingRepositoryMissing(wt.Path); orphaned {
		target.addClass(DestroyUnverified, "backing repository missing: "+detail)
		return finalizeDestroyTarget(target)
	}

	dirty, err := git.IsDirty(wt.Path)
	if err != nil {
		target.addClass(DestroyUnverified, "cannot check status: "+err.Error())
		return finalizeDestroyTarget(target)
	} else if dirty {
		target.addClass(DestroyDirty, "uncommitted changes")
	}

	if defaultRef == "" {
		target.addClass(DestroyUnverified, "cannot verify HEAD is merged into the default branch")
		return finalizeDestroyTarget(target)
	}
	merged, err := git.IsHeadMergedIntoRef(wt.Path, defaultRef)
	if err != nil {
		target.addClass(DestroyUnverified, "cannot verify merge into "+defaultRef+": "+err.Error())
		return finalizeDestroyTarget(target)
	}
	if !merged {
		target.addClass(DestroyUnmerged, "HEAD not merged into "+defaultRef)
	}

	return finalizeDestroyTarget(target)
}

func (target *DestroyTarget) addClass(class DestroyClass, detail string) {
	if target.hasClass(class) {
		return
	}
	target.Classes = append(target.Classes, class)
	if target.Class == "" {
		target.Class = class
		target.Detail = detail
	}
}

func finalizeDestroyTarget(target DestroyTarget) DestroyTarget {
	if len(target.Classes) == 0 {
		target.addClass(DestroyDisposable, "")
	}
	return target
}

func (target DestroyTarget) hasClass(class DestroyClass) bool {
	for _, existing := range target.Classes {
		if existing == class {
			return true
		}
	}
	return target.Class == class
}

func (target DestroyTarget) hasUnlandedClass() bool {
	return target.hasClass(DestroyDirty) ||
		target.hasClass(DestroyUnmerged) ||
		target.hasClass(DestroyUnverified)
}

// executeDestroy removes the planned worktrees with the same two-phase
// reservation prune and the legacy destroy used: it stamps a destroy reservation
// under the state lock, runs pre-destroy hooks with the lock released, then
// removes only the worktrees whose reservation is still intact. A worktree
// re-acquired during its hook (its reservation superseded) is left in place.
func executeDestroy(poolDir string, removable []DestroyTarget, repoRoot, defaultRef string, allowLeased bool, opts DestroyOptions) ([]DestroyTarget, []DestroySkip, error) {
	if len(removable) == 0 {
		return nil, nil, nil
	}

	plannedByPath := make(map[string]DestroyTarget, len(removable))
	for _, t := range removable {
		plannedByPath[t.Path] = t
	}

	var reserved []destroyReservation
	var skips []DestroySkip
	if err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		state = healState(state)
		for i := range state.Worktrees {
			if _, ok := plannedByPath[state.Worktrees[i].Path]; !ok {
				continue
			}
			current := classifyForDestroy(state.Worktrees[i], defaultRef)
			if planned, ok := plannedByPath[current.Path]; ok && current.Bytes == 0 {
				current.Bytes = planned.Bytes
			}
			if state.Worktrees[i].Destroying && ownerAlive(state.Worktrees[i]) {
				current.Detail = "reserved by another destroy"
				skips = append(skips, DestroySkip{Target: current})
				continue
			}
			ok, skip := opts.allows(current, allowLeased)
			if !ok {
				skips = append(skips, skip)
				continue
			}
			reservation, err := reserveDestroyReservation(&state.Worktrees[i])
			if err != nil {
				return err
			}
			reserved = append(reserved, reservation)
			plannedByPath[current.Path] = current
		}
		return WriteState(poolDir, state)
	}); err != nil {
		return nil, nil, err
	}

	for _, wt := range reserved {
		hooks.Run(opts.PreDestroy, wt.worktree.Path, os.Stdout, os.Stderr)
	}

	var destroyed []DestroyTarget
	if err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		removed := make(map[string]struct{}, len(reserved))
		for _, reservation := range reserved {
			idx := -1
			for i := range state.Worktrees {
				if state.Worktrees[i].Path == reservation.worktree.Path {
					idx = i
					break
				}
			}
			if idx == -1 {
				continue
			}
			if !sameDestroyReservation(state.Worktrees[idx], reservation.worktree) {
				// Re-acquired during the pre-destroy hook; never remove it.
				superseded := plannedByPath[reservation.worktree.Path]
				superseded.Detail = "re-acquired during pre-destroy hook"
				skips = append(skips, DestroySkip{Target: superseded})
				continue
			}

			path := state.Worktrees[idx].Path
			currentEntry := state.Worktrees[idx]
			restoreOriginalOwnerReservation(&currentEntry, reservation)
			current := classifyForDestroy(currentEntry, defaultRef)
			measureDestroySize(&current)
			if planned, ok := plannedByPath[path]; ok && current.Bytes == 0 {
				current.Bytes = planned.Bytes
			}
			ok, skip := opts.allows(current, allowLeased)
			if !ok {
				restoreOriginalOwnerReservation(&state.Worktrees[idx], reservation)
				skips = append(skips, skip)
				continue
			}

			if current.hasClass(DestroyInUse) {
				if _, err := terminateWorktreeProcesses(path, destroyGracePeriod); err != nil {
					restoreOriginalOwnerReservation(&state.Worktrees[idx], reservation)
					current.Detail = "could not terminate worktree processes: " + err.Error()
					skips = append(skips, DestroySkip{Target: current})
					continue
				}
				survivors, err := findProcessesInWorktree(path)
				if err != nil {
					restoreOriginalOwnerReservation(&state.Worktrees[idx], reservation)
					current.Detail = "could not verify worktree processes stopped: " + err.Error()
					skips = append(skips, DestroySkip{Target: current})
					continue
				}
				if len(survivors) > 0 {
					restoreOriginalOwnerReservation(&state.Worktrees[idx], reservation)
					current.Processes = survivors
					current.Detail = "worktree processes still running after termination"
					skips = append(skips, DestroySkip{Target: current})
					continue
				}
			}

			if err := removeManagedWorktree(repoRoot, path); err != nil {
				restoreOriginalOwnerReservation(&state.Worktrees[idx], reservation)
				current.Detail = err.Error()
				skips = append(skips, DestroySkip{Target: current})
				continue
			}
			removed[path] = struct{}{}
			destroyed = append(destroyed, current)
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
		return nil, nil, err
	}

	sortDestroyTargets(destroyed)
	sortDestroySkips(skips)
	return destroyed, skips, nil
}

func reserveDestroyReservation(wt *WorktreeEntry) (destroyReservation, error) {
	reservation := destroyReservation{
		originalOwnerPID:       wt.OwnerPID,
		originalOwnerStartedAt: wt.OwnerStartedAt,
	}
	wt.Destroying = true
	if err := reserveOwner(wt); err != nil {
		return destroyReservation{}, err
	}
	reservation.worktree = *wt
	return reservation, nil
}

func restoreOriginalOwnerReservation(wt *WorktreeEntry, reservation destroyReservation) {
	wt.Destroying = false
	wt.OwnerPID = reservation.originalOwnerPID
	wt.OwnerStartedAt = reservation.originalOwnerStartedAt
}

// removeManagedWorktree deletes a worktree's git registration (when its backing
// repository is still present) and its numbered container directory. git removal
// uses --force because destroy deliberately removes dirty, unmerged, or
// unverified worktrees once the caller has opted in.
func removeManagedWorktree(repoRoot, path string) error {
	orphaned, _ := backingRepositoryMissing(path)
	if !orphaned {
		removeRepoRoot := repoRoot
		if removeRepoRoot == "" {
			resolvedRoot, err := git.FindMainRepoRootFrom(path)
			if err != nil {
				return fmt.Errorf("cannot resolve repository for worktree removal: %w", err)
			}
			removeRepoRoot = resolvedRoot
		}
		if err := git.RemoveWorktree(removeRepoRoot, path); err != nil {
			return fmt.Errorf("git refused to remove worktree: %w", err)
		}
	}
	container, err := removableWorktreeContainer(path)
	if err != nil {
		return fmt.Errorf("refusing unsafe cleanup path: %w", err)
	}
	if err := os.RemoveAll(container); err != nil {
		return fmt.Errorf("could not remove worktree directory: %w", err)
	}
	return nil
}

// resolvePoolRepoRoot derives the owning repository from the first target whose
// backing repository is still present. A pool is per-repository, so one root
// applies to every worktree in it.
func resolvePoolRepoRoot(targets []WorktreeEntry) string {
	for _, wt := range targets {
		if orphaned, _ := backingRepositoryMissing(wt.Path); orphaned {
			continue
		}
		if root, err := git.FindMainRepoRootFrom(wt.Path); err == nil {
			return root
		}
	}
	return ""
}

func measureDestroySize(target *DestroyTarget) {
	container, err := removableWorktreeContainer(target.Path)
	if err != nil {
		return
	}
	if bytes, err := dirSize(container); err == nil {
		target.Bytes = bytes
	}
}

func sortDestroyTargets(targets []DestroyTarget) {
	sort.SliceStable(targets, func(i, j int) bool {
		return lessByWorktreeName(targets[i].Name, targets[j].Name)
	})
}

func sortDestroySkips(skips []DestroySkip) {
	sort.SliceStable(skips, func(i, j int) bool {
		if skips[i].Target.Class != skips[j].Target.Class {
			return skips[i].Target.Class < skips[j].Target.Class
		}
		return lessByWorktreeName(skips[i].Target.Name, skips[j].Target.Name)
	})
}

func lessByWorktreeName(a, b string) bool {
	na, ea := strconv.Atoi(a)
	nb, eb := strconv.Atoi(b)
	if ea == nil && eb == nil {
		return na < nb
	}
	return a < b
}
