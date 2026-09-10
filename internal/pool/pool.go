package pool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kunchenguid/treehouse/internal/hooks"
	"github.com/kunchenguid/treehouse/internal/process"
	"github.com/kunchenguid/treehouse/internal/vcs"
)

const (
	StatusAvailable = "available"
	StatusDirty     = "dirty"
	StatusInUse     = "in-use"
	StatusLeased    = "leased"
	StatusHere      = "you're here"
	StatusDamaged   = "damaged"
	// StatusUnverified is a slot whose process table could not be read: the
	// question "is anything running here?" has no answer, so nothing decided
	// from the process list (in-use) or from its absence (damaged, dirty,
	// available) is reported. Leased, a live owner reservation, and "you're
	// here" are facts known without a scan and still outrank it.
	StatusUnverified = "unverified"
)

// WorktreeStatus describes one managed worktree as reported by List.
type WorktreeStatus struct {
	Name   string
	Path   string
	Status string
	// Flavor is the backend the worktree's own marker identifies ("git" or
	// "jj"), independent of what the repository currently selects.
	Flavor string
	// Processes is the set `return` would terminate in this worktree: the
	// scan minus the caller and its ancestors. See List for why that is not
	// the raw scan.
	Processes []process.ProcessInfo
	// LeaseID identifies the current acquisition of a leased worktree.
	LeaseID string
	// LeaseHolder is the recorded holder for a leased worktree, if any.
	LeaseHolder string
	// LeasedAt records when the current lease was acquired.
	LeasedAt time.Time
	// Branch is the branch this slot currently has checked out. It is empty
	// for a detached HEAD, which is what `treehouse get` leaves by default,
	// and empty for backends that do not report one.
	Branch string
}

// LeaseInfo is the stable machine-readable identity of one lease acquisition.
type LeaseInfo struct {
	Path        string    `json:"path"`
	LeaseID     string    `json:"lease_id"`
	LeaseHolder string    `json:"lease_holder"`
	LeasedAt    time.Time `json:"leased_at"`
	// BaseBranch is the branch this acquisition was cut from, explicit or
	// inferred, and is never persisted: it describes one acquisition, and the
	// next reset resolves the branch again. Acquisition always populates it,
	// because acquire cannot proceed without a resolved base. LeaseExisting
	// resolves it best-effort and reports it empty when the slot records no
	// explicit base and its own backend cannot answer, because that verb
	// needs no branch and must never refuse to protect a home over a
	// reporting field.
	BaseBranch string `json:"base_branch"`
}

// AcquireOptions controls optional acquisition behavior.
type AcquireOptions struct {
	// SkipFetch uses the repository's existing local refs instead of fetching
	// origin before acquiring a worktree.
	SkipFetch bool
	// BaseBranch overrides the branch worktrees are cut from. Empty keeps the
	// branch inferred from the repository. A non-empty value that cannot be
	// resolved fails the acquisition rather than falling back.
	BaseBranch string
	// IncludeManifest replaces the committed manifest; nil keeps the default,
	// while a non-nil empty slice explicitly disables seeding.
	IncludeManifest []byte
}

// acquireOptions controls how Acquire reserves the worktree it hands out.
type acquireOptions struct {
	// skipFetch uses existing local refs without contacting origin.
	skipFetch bool
	// baseBranch is the explicitly requested base branch, or empty to infer it.
	baseBranch      string
	includeManifest []byte
	// lease records a durable, process-independent reservation instead of the
	// default short-lived owner reservation.
	lease bool
	// leaseHolder is an optional label stored with a lease.
	leaseHolder string
	// hookStdout/hookStderr receive post-create hook output. Lease mode routes
	// hook stdout to stderr so it cannot contaminate machine-readable CLI output.
	hookStdout io.Writer
	hookStderr io.Writer
}

// Acquire reserves a clean worktree from the pool with a short-lived owner
// reservation (the calling process). It is the backing call for the interactive
// `treehouse get` subshell.
func Acquire(repoRoot, poolDir string, poolSize int, postCreate []string) (string, error) {
	return AcquireWithOptions(repoRoot, poolDir, poolSize, postCreate, AcquireOptions{})
}

// AcquireWithOptions reserves a clean worktree with optional acquisition behavior.
func AcquireWithOptions(repoRoot, poolDir string, poolSize int, postCreate []string, options AcquireOptions) (string, error) {
	acquired, err := acquire(repoRoot, poolDir, poolSize, postCreate, acquireOptions{
		skipFetch:       options.SkipFetch,
		baseBranch:      options.BaseBranch,
		includeManifest: options.IncludeManifest,
		hookStdout:      os.Stdout,
		hookStderr:      os.Stderr,
	})
	return acquired.Path, err
}

// AcquireLease reserves a clean worktree and marks it durably LEASED so the
// reservation survives with zero processes running inside it. The lease persists
// until it is released by Release. holder is an optional label recorded with the
// lease for diagnostics. Post-create hook stdout is routed to stderr so callers
// can emit machine-readable allocation output without hook output on stdout.
func AcquireLease(repoRoot, poolDir string, poolSize int, postCreate []string, holder string) (string, error) {
	lease, err := AcquireLeaseInfo(repoRoot, poolDir, poolSize, postCreate, holder)
	return lease.Path, err
}

// AcquireLeaseInfo reserves a worktree exactly like AcquireLease and returns
// the immutable identity and metadata for that acquisition.
func AcquireLeaseInfo(repoRoot, poolDir string, poolSize int, postCreate []string, holder string) (LeaseInfo, error) {
	return AcquireLeaseInfoWithOptions(repoRoot, poolDir, poolSize, postCreate, holder, AcquireOptions{})
}

// AcquireLeaseInfoWithOptions reserves a durable lease with optional acquisition behavior.
func AcquireLeaseInfoWithOptions(repoRoot, poolDir string, poolSize int, postCreate []string, holder string, options AcquireOptions) (LeaseInfo, error) {
	return acquire(repoRoot, poolDir, poolSize, postCreate, acquireOptions{
		skipFetch:       options.SkipFetch,
		baseBranch:      options.BaseBranch,
		includeManifest: options.IncludeManifest,
		lease:           true,
		leaseHolder:     holder,
		hookStdout:      os.Stderr,
		hookStderr:      os.Stderr,
	})
}

var (
	seedWorktree   = vcs.SeedWorktree
	removeWorktree = vcs.RemoveWorktree
	writeState     = WriteState
)

const acquisitionIncompleteLeaseHolder = "quarantined: acquisition state incomplete"

func persistState(poolDir string, state State) error {
	err := writeState(poolDir, state)
	if err == nil {
		return nil
	}

	// Atomic replacement can succeed before the following directory sync
	// reports an error. Confirm the serialized state so callers do not overwrite
	// a committed acquisition while trying to recover from an ambiguous result.
	persisted, readErr := ReadState(poolDir)
	if readErr != nil {
		return err
	}
	state, marshalErr := prepareStateForWrite(poolDir, state)
	if marshalErr != nil {
		return err
	}
	want, marshalErr := json.Marshal(state)
	if marshalErr != nil {
		return err
	}
	got, marshalErr := json.Marshal(persisted)
	if marshalErr == nil && bytes.Equal(got, want) {
		return nil
	}
	return err
}

// LeaseExisting marks a worktree already registered in the pool as durably
// leased, state-only: no reset, fetch, clean, or checkout ever touches the
// worktree. It exists because AcquireLease can only hand out a fresh or
// recycled slot; a worktree that already holds live work (e.g. a long-lived
// agent home acquired with plain get) needs get --lease's protection applied
// in place so a later get or prune cannot hand it out or remove it once its
// owner process dies. Release clears it exactly like an acquired lease.
//
// State is healed first, like every other state-mutating pool path, so a name
// whose worktree directory is gone is refused by that name rather than stamped
// with a lease for a home that does not exist. Refuses an unknown name, a slot
// being destroyed, and an already-leased slot; a refusal writes no state.
func LeaseExisting(poolDir, name, holder string) (LeaseInfo, error) {
	var lease LeaseInfo
	err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		registered := false
		for _, wt := range state.Worktrees {
			if wt.Name == name {
				registered = true
				break
			}
		}

		state, err = healState(poolDir, state)
		if err != nil {
			return err
		}

		for i := range state.Worktrees {
			wt := &state.Worktrees[i]
			if wt.Name != name {
				continue
			}
			if wt.Destroying {
				return fmt.Errorf("worktree %s is being destroyed", name)
			}
			if wt.Leased {
				return fmt.Errorf("worktree %s is already leased (holder: %q)", name, wt.LeaseHolder)
			}
			// Best-effort reporting only: resolution failure degrades to an
			// empty base rather than refusing to protect the home. Dispatch is
			// on the slot's own marker, like every other per-worktree fact, so
			// a slot of the other flavor is never answered by the repository's
			// configured backend, and a markerless slot is left empty rather
			// than read through the fallback, which in an in-project pool would
			// answer with the default branch of the repository ENCLOSING the
			// pool. The persisted field still records only an explicit base, so
			// an inferred slot stays inferred.
			base := wt.BaseBranch
			if base == "" && vcs.WorktreeBackendName(wt.Path) != "" {
				if resolved, resolveErr := vcs.DefaultBranchForWorktree(wt.Path); resolveErr == nil {
					base = resolved
				}
			}
			if err := markAcquired(wt, acquireOptions{lease: true, leaseHolder: holder}); err != nil {
				return err
			}
			if err := WriteState(poolDir, state); err != nil {
				return err
			}
			lease = leaseInfoFromEntry(*wt, base)
			return nil
		}

		if registered {
			return fmt.Errorf("worktree %s is registered but its directory no longer exists; run 'treehouse status' to clear the stale entry", name)
		}
		return fmt.Errorf("no worktree named %q in pool", name)
	})
	return lease, err
}

func acquire(repoRoot, poolDir string, poolSize int, postCreate []string, opts acquireOptions) (LeaseInfo, error) {
	fmt.Fprintf(os.Stderr, "🌳 Setting up worktree...\n")
	if !opts.skipFetch && vcs.HasRemote(repoRoot, "origin") {
		if err := vcs.Fetch(repoRoot); err != nil {
			return LeaseInfo{}, fmt.Errorf("fetch failed: %w", err)
		}
	}

	// After the fetch, not before: a base that exists only on origin would be
	// rejected against pre-fetch refs.
	branch, err := resolveBaseBranch(repoRoot, opts.baseBranch)
	if err != nil {
		return LeaseInfo{}, err
	}

	var acquired LeaseInfo
	var runPostCreate bool

	err = WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		state, err = healState(poolDir, state)
		if err != nil {
			return err
		}

		// Try to find an available worktree (clean, not in-use, not leased,
		// and of the flavor the repository currently selects: a caller who
		// opted in to jj must not be handed a git worktree where jj commands
		// do not work, and vice versa; other-flavor slots are left intact
		// and leave the pool via the documented migration, destroy then
		// re-acquire).
		wantFlavor := vcs.BackendNameFor(repoRoot)
		otherFlavor := 0
		for i, wt := range state.Worktrees {
			if wt.Destroying || wt.Leased || ownerAlive(wt) {
				continue
			}
			flavor := vcs.WorktreeBackendName(wt.Path)
			if flavor == "" {
				// No .git or .jj marker: the slot is damaged or missing.
				// Every dispatch on such a path falls back to the
				// configured backend, which in an in-project pool resolves
				// the repository ENCLOSING the pool - the safety checks
				// would vouch for that repository and the reset would
				// rewrite it. Fail closed and leave the slot for destroy,
				// which classifies it unverified and removes it only with
				// --include-unlanded; prune skips it as unverifiable and
				// neither path ever resets it.
				continue
			}
			if flavor != wantFlavor {
				otherFlavor++
				continue
			}
			inUse, _ := process.IsWorktreeInUse(wt.Path)
			if inUse {
				continue
			}
			// Skip a slot that carries unlanded work. A crashed or rebooted owner
			// leaves the reservation empty while its worktree still holds committed
			// commits (a clean tree passes IsDirty), so availability alone must not
			// authorize a reset. Fail closed: if either the working tree or the
			// merge state cannot be proven safe, leave the slot untouched rather
			// than let ResetWorktree discard the work.
			dirty, err := vcs.IsDirty(wt.Path)
			if err != nil || dirty {
				continue
			}
			safe, resetRef, head, err := vcs.IsWorktreeSafeToReset(wt.Path, branch)
			if err != nil {
				continue
			}
			if !safe && !headMergedIntoRecordedBase(wt, branch, head) {
				continue
			}
			// Found an available one. Reset it to the verified commit only if
			// HEAD is still the one whose ancestry was checked and the tree is
			// still clean under the exclusive lock.
			seededPaths := wt.SeededPaths
			if !wt.SeedInventoryKnown {
				seededPaths = nil
			}
			if err := vcs.ResetWorktreeToRefWithSeededPaths(wt.Path, resetRef, head, true, seededPaths); err != nil {
				continue
			}
			state.Worktrees[i].BaseBranch = opts.baseBranch
			setSeedInventory(&state.Worktrees[i], nil, false)
			state.Worktrees[i].Leased = true
			state.Worktrees[i].LeaseHolder = acquisitionIncompleteLeaseHolder
			state.Worktrees[i].LeasedAt = time.Now()
			if err := persistState(poolDir, state); err != nil {
				return err
			}
			// Keep partial ignored files away from later acquisitions until a
			// human verifies and explicitly returns the worktree.
			seededPaths, err = seedWorktree(repoRoot, wt.Path, opts.includeManifest)
			if err != nil {
				// Remove every path the failed seed operation reports before relying
				// on another state write to preserve that partial inventory.
				cleanupErr := vcs.ResetWorktreeToRefWithSeededPaths(wt.Path, resetRef, resetRef, true, seededPaths)
				if cleanupErr == nil {
					seededPaths = []string{}
				}
				setSeedInventory(&state.Worktrees[i], seededPaths, cleanupErr == nil)
				state.Worktrees[i].Leased = true
				state.Worktrees[i].LeaseHolder = "quarantined: worktree seeding failed"
				state.Worktrees[i].LeasedAt = time.Now()
				if writeErr := WriteState(poolDir, state); writeErr != nil {
					if cleanupErr != nil {
						return fmt.Errorf("failed to seed .worktreeinclude into %s: %w (cleanup failed: %v; quarantine failed: %v)", wt.Path, err, cleanupErr, writeErr)
					}
					return fmt.Errorf("failed to seed .worktreeinclude into %s: %w (quarantine failed: %v)", wt.Path, err, writeErr)
				}
				if cleanupErr != nil {
					return fmt.Errorf("failed to seed .worktreeinclude into %s: %w (cleanup failed: %v)", wt.Path, err, cleanupErr)
				}
				return fmt.Errorf("failed to seed .worktreeinclude into %s: %w", wt.Path, err)
			}
			setSeedInventory(&state.Worktrees[i], seededPaths, true)
			clearLease(&state.Worktrees[i])
			if err := markAcquired(&state.Worktrees[i], opts); err != nil {
				return err
			}
			acquired = leaseInfoFromEntry(state.Worktrees[i], branch)
			if err := persistState(poolDir, state); err != nil {
				// Preserve the completed seed inventory outside the mutable
				// worktree before leaving this failed acquisition quarantined.
				state.Worktrees[i].OwnerPID = 0
				state.Worktrees[i].OwnerStartedAt = 0
				clearLease(&state.Worktrees[i])
				state.Worktrees[i].Leased = true
				state.Worktrees[i].LeaseHolder = acquisitionIncompleteLeaseHolder
				state.Worktrees[i].LeasedAt = time.Now()
				if quarantineErr := persistState(poolDir, state); quarantineErr != nil {
					return fmt.Errorf("%w (quarantine failed: %v)", err, quarantineErr)
				}
				return err
			}
			runPostCreate = true
			return nil
		}

		// No available worktree — create new if pool allows
		if len(state.Worktrees) >= poolSize {
			if otherFlavor > 0 {
				return fmt.Errorf("all %d worktrees are in use, dirty, or hold the other backend's worktrees (%d %s-flavored; the repository selects %s). Run 'treehouse status' to see details, destroy old-flavor worktrees to migrate the pool, or increase max_trees in treehouse.toml", len(state.Worktrees), otherFlavor, map[string]string{"git": "jj", "jj": "git"}[wantFlavor], wantFlavor)
			}
			return fmt.Errorf("all %d worktrees are in use or dirty (max_trees = %d). Run 'treehouse status' to see details, or increase max_trees in treehouse.toml", len(state.Worktrees), poolSize)
		}

		name := nextName(state)
		repoName := filepath.Base(repoRoot)
		wtPath := filepath.Join(poolDir, name, repoName)

		if err := os.MkdirAll(filepath.Dir(wtPath), 0755); err != nil {
			return err
		}

		// Clear any stale worktree bookkeeping left behind by a crashed or
		// forcibly removed worktree. Without this, git rejects the add with
		// "missing but already registered worktree". Prune is safe: it only
		// removes registrations whose target directories are already gone.
		//
		// Best-effort: prune is a self-healing optimization, not a precondition
		// for AddWorktree in the common (non-stale) case. A transient failure
		// (e.g. a temporary .git/worktrees lock or permission issue) must not
		// wedge a get that would otherwise succeed; let AddWorktree surface the
		// real error if one exists.
		if err := vcs.PruneWorktrees(repoRoot); err != nil {
			fmt.Fprintf(os.Stderr, "🌳 Warning: failed to prune stale worktrees: %v\n", err)
		}

		if err := vcs.AddWorktree(repoRoot, wtPath, branch); err != nil {
			return fmt.Errorf("failed to create worktree: %w", err)
		}
		seededPaths, err := seedWorktree(repoRoot, wtPath, opts.includeManifest)
		if err != nil {
			// A failed removal leaves a real Git worktree behind. Keep it in
			// state as quarantined so later acquisitions cannot reuse its slot.
			if cleanupErr := removeWorktree(repoRoot, wtPath); cleanupErr != nil {
				entry := WorktreeEntry{
					Name:        name,
					Path:        wtPath,
					CreatedAt:   time.Now(),
					BaseBranch:  opts.baseBranch,
					Leased:      true,
					LeaseHolder: "quarantined: worktree seeding cleanup failed",
					LeasedAt:    time.Now(),
				}
				setSeedInventory(&entry, seededPaths, true)
				state.Worktrees = append(state.Worktrees, entry)
				if writeErr := WriteState(poolDir, state); writeErr != nil {
					return fmt.Errorf("failed to seed .worktreeinclude into %s: %w (cleanup failed: %v; quarantine failed: %v)", wtPath, err, cleanupErr, writeErr)
				}
				return fmt.Errorf("failed to seed .worktreeinclude into %s: %w (cleanup failed: %v)", wtPath, err, cleanupErr)
			}
			return fmt.Errorf("failed to seed .worktreeinclude into %s: %w", wtPath, err)
		}
		entry := WorktreeEntry{
			Name:        name,
			Path:        wtPath,
			CreatedAt:   time.Now(),
			BaseBranch:  opts.baseBranch,
			Leased:      true,
			LeaseHolder: acquisitionIncompleteLeaseHolder,
			LeasedAt:    time.Now(),
		}
		setSeedInventory(&entry, seededPaths, true)
		state.Worktrees = append(state.Worktrees, entry)
		if err := persistState(poolDir, state); err != nil {
			return err
		}

		entry = state.Worktrees[len(state.Worktrees)-1]
		clearLease(&entry)
		if err := markAcquired(&entry, opts); err != nil {
			return err
		}
		state.Worktrees[len(state.Worktrees)-1] = entry

		acquired = leaseInfoFromEntry(entry, branch)
		if err := persistState(poolDir, state); err != nil {
			return err
		}
		runPostCreate = true
		return nil
	})
	if err != nil {
		return LeaseInfo{}, err
	}
	if runPostCreate {
		hooks.Run(postCreate, acquired.Path, opts.hookStdout, opts.hookStderr)
	}

	return acquired, nil
}

func leaseInfoFromEntry(wt WorktreeEntry, baseBranch string) LeaseInfo {
	return LeaseInfo{
		Path:        wt.Path,
		LeaseID:     wt.LeaseID,
		LeaseHolder: wt.LeaseHolder,
		LeasedAt:    wt.LeasedAt,
		BaseBranch:  baseBranch,
	}
}

// headMergedIntoRecordedBase reports whether a slot carries nothing beyond the
// base it was parked on. Acquisitions that mix bases would otherwise wedge the
// pool: a slot returned to develop is not merged into main, so a later plain
// get skips it and builds a new slot until max_trees, with nothing able to
// reclaim it. Work that only exists in the slot's own base is as disposable as
// work in the requested one; a slot holding commits beyond it is not, and is
// still skipped.
//
// An entry written before base_branch existed, and any inferred acquisition,
// records no base, so the repository default stands in as its implicit base
// HERE ONLY: prune and destroy deliberately give such a slot no second
// reading and stay on the origin-validated default ref. The asymmetry is
// safe because acquire only RESETS a slot whose HEAD stays reachable from a
// local branch, while prune and destroy DELETE and so must stay conservative
// - which is exactly what keeps non-opt-in pools on the pre-feature deletion
// semantics.
//
// Fails closed on an unresolvable base, an errored check, and a HEAD that moved
// between the two readings. A base equal to the requested branch answers false
// without asking git again: the caller reaches this only after that same query
// returned unsafe.
func headMergedIntoRecordedBase(wt WorktreeEntry, requested, head string) bool {
	base := wt.BaseBranch
	if base == "" {
		resolved, err := vcs.DefaultBranchForWorktree(wt.Path)
		if err != nil {
			return false
		}
		base = resolved
	}
	if base == requested {
		return false
	}
	safe, _, recordedHead, err := vcs.IsWorktreeSafeToReset(wt.Path, base)
	return err == nil && safe && recordedHead == head
}

// resolveBaseBranch picks the branch worktrees are cut from and reset to: the
// explicitly requested one, otherwise the inferred default.
//
// Only an explicit request is verified. GetDefaultBranch already errors when it
// cannot answer, but an unverified explicit branch would not surface at all:
// acquire SKIPS a slot whose safety check fails, so a typo would look like a
// pool with nothing reusable and burn a fresh slot per call.
func resolveBaseBranch(repoRoot, requested string) (string, error) {
	if requested == "" {
		return vcs.GetDefaultBranch(repoRoot)
	}
	if err := vcs.VerifyBaseBranch(repoRoot, requested); err != nil {
		return "", err
	}
	return requested, nil
}

// markAcquired stamps an acquired worktree entry: a durable lease in lease mode,
// otherwise the default short-lived owner reservation.
func markAcquired(wt *WorktreeEntry, opts acquireOptions) error {
	if opts.lease {
		leaseID, err := newLeaseID()
		if err != nil {
			return err
		}
		wt.Leased = true
		wt.LeaseID = leaseID
		wt.LeaseHolder = opts.leaseHolder
		wt.LeasedAt = time.Now()
		// A lease is process-independent, so it carries no owner reservation.
		wt.OwnerPID = 0
		wt.OwnerStartedAt = 0
		return nil
	}
	return reserveOwner(wt)
}

// ErrLeasePreconditionFailed reports that a conditional release no longer
// identifies the worktree's current lease.
var ErrLeasePreconditionFailed = errors.New("lease precondition failed")

// ErrOwnerPreconditionFailed reports that a release no longer identifies the
// calling process's own short-lived owner reservation.
var ErrOwnerPreconditionFailed = errors.New("owner precondition failed")

// ReleasePreconditions optionally constrain a release to the current lease.
// Pointer fields distinguish an omitted condition from an expected empty value.
type ReleasePreconditions struct {
	ExpectedLeaseID     *string
	ExpectedLeaseHolder *string
	// RequireOwnedByCaller limits the release to a worktree that still carries
	// the calling process's own owner reservation, which is what an acquiring
	// `treehouse get` holds until it returns the slot. Without it, a session
	// that released the slot to someone else - a durable lease taken over a
	// live agent home, or a later acquisition - would still reset the worktree
	// and clear that reservation when its subshell exits.
	RequireOwnedByCaller bool
}

// Release resets a managed worktree, clears its short-lived owner reservation or
// durable lease, and returns it to the available pool. It retains the legacy
// unconditional behavior of releasing by path.
func Release(poolDir, worktreePath string) error {
	return ReleaseConditional(poolDir, worktreePath, "", ReleasePreconditions{}, nil)
}

// ValidateReleasePreconditions checks that a managed worktree still matches
// the requested lease, then runs guarded (when non-nil) while still holding the
// state lock. No release effects are performed either way.
//
// guarded is how a caller performs a worktree action that must not run on a slot
// someone else has taken over - get's exit-time detach, which would move the
// HEAD of a home a concurrent 'treehouse lease' just protected. Checking and
// then acting outside the lock are two separate instants, and a takeover lands
// between them; under the lock they are one, exactly as ReleaseConditional
// already runs its beforeReset.
func ValidateReleasePreconditions(poolDir, worktreePath string, preconditions ReleasePreconditions, guarded func() error) error {
	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		if _, err := releasableWorktree(&state, worktreePath, preconditions); err != nil {
			return err
		}
		if guarded == nil {
			return nil
		}
		return guarded()
	})
}

// ReleaseConditional verifies any lease preconditions, runs beforeReset, resets
// the worktree, and clears its reservation while holding one state lock. The
// callback is invoked only after all preconditions match and runs under that
// lock so caller-side termination or detachment cannot race a later acquisition.
// A markerless slot (its .git/.jj marker is gone) is never reset or asked for a
// branch: dispatch on such a path falls back to the configured backend, which
// in an in-project pool resolves the repository ENCLOSING the pool. Its
// reservation is still cleared so the slot is not stuck leased, and the damaged
// slot is left for destroy; acquire refuses to reuse it.
//
// baseBranch parks the returned slot on the branch the pool cuts from; empty
// falls back to the base the slot was acquired with, then to the inferred
// default. Parking is what keeps the slot reusable: acquire recycles only when
// HEAD is merged into the base it resets to, so a slot parked off-base is never
// recycled and every acquire grows the pool until max_trees.
func ReleaseConditional(poolDir, worktreePath, baseBranch string, preconditions ReleasePreconditions, beforeReset func() error) error {
	markerless := vcs.WorktreeBackendName(worktreePath) == ""
	// Resolved before the state lock so a failure surfaces before beforeReset
	// kills the worktree's processes. It is only fatal when the slot has no
	// base of its own to park on instead.
	defaultBranch, defaultErr := "", error(nil)
	if !markerless {
		defaultBranch, defaultErr = vcs.DefaultBranchForWorktree(worktreePath)
	}
	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		wt, err := releasableWorktree(&state, worktreePath, preconditions)
		if err != nil {
			return err
		}
		// Clearing a safety quarantine without a trusted seed inventory could
		// expose ignored files hidden by a mutable manifest.
		if !wt.SeedInventoryKnown {
			return fmt.Errorf("worktree %s is quarantined without a trusted seed inventory; inspect it and use destroy --include-leased instead", worktreePath)
		}
		branch, fallback, requested := "", "", ""
		if !markerless {
			requested = baseBranch
			if requested == "" {
				requested = wt.BaseBranch
			}
			if defaultErr != nil && requested == "" {
				return defaultErr
			}
			branch, fallback = defaultBranch, defaultBranch
			if requested != "" {
				branch = requested
			}
		}
		if beforeReset != nil {
			if err := beforeReset(); err != nil {
				return err
			}
		}
		if !markerless {
			seededPaths := wt.SeededPaths
			if !wt.SeedInventoryKnown {
				seededPaths = nil
			}
			if err := vcs.ResetWorktreeWithSeededPaths(worktreePath, branch, seededPaths); err != nil {
				// The base resolved when the caller checked it but not now (it
				// was deleted in between). Park on the default rather than
				// strand the reservation with the processes already killed.
				if fallback == "" || fallback == branch {
					return err
				}
				fmt.Fprintf(os.Stderr, "🌳 Warning: cannot park the worktree on %q (%v); using %s instead.\n", branch, err, fallback)
				if err := vcs.ResetWorktreeWithSeededPaths(worktreePath, fallback, seededPaths); err != nil {
					return err
				}
				branch = fallback
				requested = ""
			}
			wt.BaseBranch = requested
		}

		wt.OwnerPID = 0
		wt.OwnerStartedAt = 0
		clearLease(wt)
		setSeedInventory(wt, nil, true)
		return WriteState(poolDir, state)
	})
}

func releasableWorktree(state *State, worktreePath string, preconditions ReleasePreconditions) (*WorktreeEntry, error) {
	for i := range state.Worktrees {
		wt := &state.Worktrees[i]
		if wt.Path != worktreePath {
			continue
		}
		if wt.Destroying {
			return nil, fmt.Errorf("worktree %s is being destroyed", worktreePath)
		}
		if err := validateReleasePreconditions(*wt, preconditions); err != nil {
			return nil, err
		}
		return wt, nil
	}
	return nil, fmt.Errorf("worktree %s is not managed by treehouse", worktreePath)
}

func validateReleasePreconditions(wt WorktreeEntry, preconditions ReleasePreconditions) error {
	if preconditions.RequireOwnedByCaller {
		if err := checkOwnedByCaller(wt); err != nil {
			return err
		}
	}
	if preconditions.ExpectedLeaseID == nil && preconditions.ExpectedLeaseHolder == nil {
		return nil
	}
	if !wt.Leased {
		return fmt.Errorf("%w: worktree %s is not leased", ErrLeasePreconditionFailed, wt.Path)
	}
	if preconditions.ExpectedLeaseID != nil && wt.LeaseID != *preconditions.ExpectedLeaseID {
		return fmt.Errorf("%w: lease identity does not match worktree %s", ErrLeasePreconditionFailed, wt.Path)
	}
	if preconditions.ExpectedLeaseHolder != nil && wt.LeaseHolder != *preconditions.ExpectedLeaseHolder {
		return fmt.Errorf("%w: lease holder does not match worktree %s", ErrLeasePreconditionFailed, wt.Path)
	}
	return nil
}

// List returns the current status of managed worktrees in poolDir.
// Leased worktrees are reported with StatusLeased and their optional holder.
// An idle slot whose .git/.jj marker is gone is reported StatusDamaged: its
// dirtiness is never read, because dispatch on a markerless path falls back to
// the configured backend, which in an in-project pool answers with the facts
// of the repository ENCLOSING the pool.
//
// Reported processes are the set `return` would terminate, not every process
// whose cwd is in the slot: run from inside a pooled worktree, the raw scan
// answers with the caller's own process tree, so the column listed the
// invoking shell and the status process itself as tenants of the slot they
// were merely observing. Those PIDs are gone by the time anyone checks them,
// which reads as a stale snapshot of real leftover processes.
func List(poolDir string) ([]WorktreeStatus, error) {
	var result []WorktreeStatus

	err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		state, err = healState(poolDir, state)
		if err != nil {
			return err
		}
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
				Flavor: vcs.WorktreeBackendName(wt.Path),
			}

			// The two failure modes get different answers, which is why the
			// scan and the filter run as separate steps here. An
			// ancestry-lookup failure keeps the raw scan rather than falling
			// back to an empty list: listing a process the caller owns costs a
			// confusing line, while reporting a slot quiet that is not is a
			// wrong answer to the only question this column exists to answer.
			// A failed process-table read cannot be answered at all: the slot
			// is reported StatusUnverified (the machine-readable half) and the
			// error is warned loudly on stderr (the diagnostic half), instead
			// of silently presenting every slot as quiet.
			procs, scanErr := findProcessesInWorktree(wt.Path)
			if scanErr != nil {
				fmt.Fprintf(os.Stderr, "treehouse: WARNING: could not read the process table to see what is running in %s (%v); it is reported %s with no processes.\n", wt.Path, scanErr, StatusUnverified)
			} else if unprotected, filterErr := dropProtectedProcesses(procs); filterErr == nil {
				procs = unprotected
			}
			ws.Processes = procs

			// "you're here" is now read from the caller's cwd alone. It used
			// to require a process in the slot, which was only ever the
			// caller's own shell - the very entry this list stopped reporting.
			//
			// Which checkout is in this slot. Empty means detached HEAD (the
			// default after `treehouse get`) or a backend that does not report
			// one; it never means the read failed.
			ws.Branch = vcs.CheckedOutBranch(wt.Path)
			if wt.Leased {
				ws.Status = StatusLeased
				ws.LeaseID = wt.LeaseID
				ws.LeaseHolder = wt.LeaseHolder
				ws.LeasedAt = wt.LeasedAt
			} else if ownerAlive(wt) {
				ws.Status = StatusInUse
			} else if process.WorktreeContainsCwd(wt.Path, cwd) {
				ws.Status = StatusHere
			} else if scanErr != nil {
				ws.Status = StatusUnverified
			} else if len(procs) > 0 {
				ws.Status = StatusInUse
			} else if ws.Flavor == "" {
				ws.Status = StatusDamaged
			} else if dirty, _ := vcs.IsDirty(wt.Path); dirty {
				ws.Status = StatusDirty
			}

			result = append(result, ws)
		}
		return nil
	})

	return result, err
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

func healState(poolDir string, state State) (State, error) {
	if err := removeAuthenticatedStaleJJSeedState(poolDir, state); err != nil {
		return state, err
	}
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
	return state, nil
}

func removeAuthenticatedStaleJJSeedState(poolDir string, state State) error {
	var key []byte
	for _, wt := range state.Worktrees {
		if !wt.SeedInventoryKnown || wt.SeedInventoryDigest == "" || wt.SeedBackend != "jj" || wt.SeedAuthIdentity == "" || len(wt.SeededPaths) == 0 {
			continue
		}
		if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
			continue
		}
		if key == nil {
			var err error
			key, err = readStateKey(poolDir)
			if err != nil {
				return err
			}
		}
		if !validSeedInventoryDigest(key, wt) {
			continue
		}
		if err := vcs.RemoveStaleJJSeedAuthentication(wt.Path, wt.SeedAuthIdentity); err != nil {
			return err
		}
	}
	return nil
}

func ownerAlive(wt WorktreeEntry) bool {
	if wt.OwnerPID == 0 || wt.OwnerStartedAt == 0 {
		return false
	}
	startedAt, ok := process.StartedAt(wt.OwnerPID)
	return ok && startedAt == wt.OwnerStartedAt
}

// checkOwnedByCaller reports whether wt still carries the reservation this very
// process took, naming which of the four distinct failures happened: the slot
// is now durably leased (markAcquired's lease path zeroes the owner fields, so
// this must be tested BEFORE an empty OwnerPID or a protected home reads as
// discarded), it was already released (a `treehouse return` run from inside the
// subshell leaves it free, not taken by anyone), it now carries a different
// reservation, or this process's own identity could not be read to compare
// against. Both owner fields are compared because a PID alone can be reused by
// an unrelated process whose reservation must not be mistaken for ours.
func checkOwnedByCaller(wt WorktreeEntry) error {
	if wt.Leased {
		if wt.LeaseHolder != "" {
			return fmt.Errorf("%w: it is now durably leased (holder: %q)", ErrOwnerPreconditionFailed, wt.LeaseHolder)
		}
		return fmt.Errorf("%w: it is now durably leased", ErrOwnerPreconditionFailed)
	}
	if wt.OwnerPID == 0 {
		return fmt.Errorf("%w: it was already released", ErrOwnerPreconditionFailed)
	}
	pid := int32(os.Getpid())
	startedAt, ok := process.StartedAt(pid)
	if !ok {
		return fmt.Errorf("%w: this process's own identity could not be read to confirm the reservation", ErrOwnerPreconditionFailed)
	}
	if wt.OwnerPID != pid || wt.OwnerStartedAt != startedAt {
		return fmt.Errorf("%w: it is now reserved by another session", ErrOwnerPreconditionFailed)
	}
	return nil
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

// clearLease removes any durable lease from a worktree entry.
func clearLease(wt *WorktreeEntry) {
	wt.Leased = false
	wt.LeaseID = ""
	wt.LeaseHolder = ""
	wt.LeasedAt = time.Time{}
}

func sameDestroyReservation(current, reserved WorktreeEntry) bool {
	return current.Path == reserved.Path &&
		current.Destroying &&
		current.OwnerPID == reserved.OwnerPID &&
		current.OwnerStartedAt == reserved.OwnerStartedAt
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
