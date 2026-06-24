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
// unleased, idle, clean, and merged into the default branch. Anything else is
// classified by its first failing safety check so the caller can gate removal
// behind the matching opt-in flag.
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

// DestroyTarget describes one worktree considered for destruction.
type DestroyTarget struct {
	Name      string
	Path      string
	Bytes     int64
	Class     DestroyClass
	Processes []process.ProcessInfo
	// Detail is an honest, user-facing diagnostic for non-disposable targets
	// (e.g. "HEAD not merged into origin/main" or "held by secondmate").
	Detail string
}

// DestroySkip records a worktree that was left in place and the opt-in flag that
// would have authorized its removal.
type DestroySkip struct {
	Target DestroyTarget
	// NeededFlag is the --include-* flag that would authorize removal, or empty
	// when no flag can (e.g. a worktree re-acquired during the pre-destroy hook).
	NeededFlag string
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
// set. It returns an error only when the path is not managed by treehouse.
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

	destroyed, execSkips, err := executeDestroy(poolDir, removable, repoRoot, opts)
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
	switch target.Class {
	case DestroyDisposable:
		return true, DestroySkip{}
	case DestroyLeased:
		if allowLeased && opts.IncludeLeased {
			return true, DestroySkip{}
		}
		return false, DestroySkip{Target: target, NeededFlag: IncludeLeasedFlag, LeasedBulk: !allowLeased}
	case DestroyInUse:
		if opts.IncludeInUse {
			return true, DestroySkip{}
		}
		return false, DestroySkip{Target: target, NeededFlag: IncludeInUseFlag}
	default: // DestroyDirty, DestroyUnmerged, DestroyUnverified
		if opts.IncludeUnlanded {
			return true, DestroySkip{}
		}
		return false, DestroySkip{Target: target, NeededFlag: IncludeUnlandedFlag}
	}
}

// classifyForDestroy determines a managed worktree's destroy class using the
// same safety primitives prune relies on (ownerAlive, process.IsWorktreeInUse,
// backingRepositoryMissing, git.IsDirty, git.IsHeadMergedIntoRef against the ref
// from resolvePruneDefaultRef). Checks run in precedence order so the loudest
// risk wins the tag.
func classifyForDestroy(wt WorktreeEntry, defaultRef string) DestroyTarget {
	target := DestroyTarget{Name: wt.Name, Path: wt.Path}

	// A lease is a deliberate, process-independent reservation: it must be the
	// loudest tag and never be hidden behind in-use or merge state.
	if wt.Leased {
		target.Class = DestroyLeased
		if wt.LeaseHolder != "" {
			target.Detail = "held by " + wt.LeaseHolder
		}
		return target
	}

	procs, procErr := process.FindProcessesInWorktree(wt.Path)
	if ownerAlive(wt) || len(procs) > 0 {
		target.Class = DestroyInUse
		target.Processes = procs
		return target
	}
	if procErr != nil {
		target.Class = DestroyUnverified
		target.Detail = "cannot check processes: " + procErr.Error()
		return target
	}

	if orphaned, detail := backingRepositoryMissing(wt.Path); orphaned {
		target.Class = DestroyUnverified
		target.Detail = "backing repository missing: " + detail
		return target
	}

	dirty, err := git.IsDirty(wt.Path)
	if err != nil {
		target.Class = DestroyUnverified
		target.Detail = "cannot check status: " + err.Error()
		return target
	}
	if dirty {
		target.Class = DestroyDirty
		target.Detail = "uncommitted changes"
		return target
	}

	if defaultRef == "" {
		target.Class = DestroyUnverified
		target.Detail = "cannot verify HEAD is merged into the default branch"
		return target
	}
	merged, err := git.IsHeadMergedIntoRef(wt.Path, defaultRef)
	if err != nil {
		target.Class = DestroyUnverified
		target.Detail = "cannot verify merge into " + defaultRef + ": " + err.Error()
		return target
	}
	if !merged {
		target.Class = DestroyUnmerged
		target.Detail = "HEAD not merged into " + defaultRef
		return target
	}

	target.Class = DestroyDisposable
	return target
}

// executeDestroy removes the planned worktrees with the same two-phase
// reservation prune and the legacy destroy used: it stamps a destroy reservation
// under the state lock, runs pre-destroy hooks with the lock released, then
// removes only the worktrees whose reservation is still intact. A worktree
// re-acquired during its hook (its reservation superseded) is left in place.
func executeDestroy(poolDir string, removable []DestroyTarget, repoRoot string, opts DestroyOptions) ([]DestroyTarget, []DestroySkip, error) {
	if len(removable) == 0 {
		return nil, nil, nil
	}

	plannedByPath := make(map[string]DestroyTarget, len(removable))
	for _, t := range removable {
		plannedByPath[t.Path] = t
	}

	var reserved []WorktreeEntry
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
			state.Worktrees[i].Destroying = true
			if err := reserveOwner(&state.Worktrees[i]); err != nil {
				return err
			}
			reserved = append(reserved, state.Worktrees[i])
		}
		return WriteState(poolDir, state)
	}); err != nil {
		return nil, nil, err
	}

	for _, wt := range reserved {
		hooks.Run(opts.PreDestroy, wt.Path, os.Stdout, os.Stderr)
	}

	var destroyed []DestroyTarget
	var skips []DestroySkip
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
			if idx == -1 {
				continue
			}
			if !sameDestroyReservation(state.Worktrees[idx], reservation) {
				// Re-acquired during the pre-destroy hook; never remove it.
				superseded := plannedByPath[reservation.Path]
				superseded.Detail = "re-acquired during pre-destroy hook"
				skips = append(skips, DestroySkip{Target: superseded})
				continue
			}

			path := state.Worktrees[idx].Path
			if planned, ok := plannedByPath[path]; ok && planned.Class == DestroyInUse {
				// Terminate lingering processes so removal proceeds cleanly.
				_, _ = process.TerminateWorktreeProcesses(path, destroyGracePeriod)
			}

			removeManagedWorktree(repoRoot, path)
			removed[path] = struct{}{}
			destroyed = append(destroyed, plannedByPath[path])
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
	return destroyed, skips, nil
}

// removeManagedWorktree deletes a worktree's git registration (when its backing
// repository is still present) and its numbered container directory. git removal
// uses --force because destroy deliberately removes dirty or unmerged worktrees
// once the caller has opted in.
func removeManagedWorktree(repoRoot, path string) {
	orphaned, _ := backingRepositoryMissing(path)
	if !orphaned && repoRoot != "" {
		_ = git.RemoveWorktree(repoRoot, path)
	}
	if container, err := removableWorktreeContainer(path); err == nil {
		_ = os.RemoveAll(container)
	}
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
