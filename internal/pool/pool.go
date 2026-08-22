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
)

// WorktreeStatus describes one managed worktree as reported by List.
type WorktreeStatus struct {
	Name   string
	Path   string
	Status string
	// Flavor is the backend the worktree's own marker identifies ("git" or
	// "jj"), independent of what the repository currently selects.
	Flavor    string
	Processes []process.ProcessInfo
	// LeaseID identifies the current acquisition of a leased worktree.
	LeaseID string
	// LeaseHolder is the recorded holder for a leased worktree, if any.
	LeaseHolder string
	// LeasedAt records when the current lease was acquired.
	LeasedAt time.Time
}

// LeaseInfo is the stable machine-readable identity of one lease acquisition.
type LeaseInfo struct {
	Path        string    `json:"path"`
	LeaseID     string    `json:"lease_id"`
	LeaseHolder string    `json:"lease_holder"`
	LeasedAt    time.Time `json:"leased_at"`
}

// AcquireOptions controls optional acquisition behavior.
type AcquireOptions struct {
	// SkipFetch uses the repository's existing local refs instead of fetching
	// origin before acquiring a worktree.
	SkipFetch bool
}

// acquireOptions controls how Acquire reserves the worktree it hands out.
type acquireOptions struct {
	// skipFetch uses existing local refs without contacting origin.
	skipFetch bool
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
		skipFetch:  options.SkipFetch,
		hookStdout: os.Stdout,
		hookStderr: os.Stderr,
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
		skipFetch:   options.SkipFetch,
		lease:       true,
		leaseHolder: holder,
		hookStdout:  os.Stderr,
		hookStderr:  os.Stderr,
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

func acquire(repoRoot, poolDir string, poolSize int, postCreate []string, opts acquireOptions) (LeaseInfo, error) {
	branch, err := vcs.GetDefaultBranch(repoRoot)
	if err != nil {
		return LeaseInfo{}, err
	}

	fmt.Fprintf(os.Stderr, "🌳 Setting up worktree...\n")
	if !opts.skipFetch && vcs.HasRemote(repoRoot, "origin") {
		if err := vcs.Fetch(repoRoot); err != nil {
			return LeaseInfo{}, fmt.Errorf("fetch failed: %w", err)
		}
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
			if err != nil || !safe {
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
			setSeedInventory(&state.Worktrees[i], nil, false)
			state.Worktrees[i].Leased = true
			state.Worktrees[i].LeaseHolder = acquisitionIncompleteLeaseHolder
			state.Worktrees[i].LeasedAt = time.Now()
			if err := persistState(poolDir, state); err != nil {
				return err
			}
			// Keep partial ignored files away from later acquisitions until a
			// human verifies and explicitly returns the worktree.
			seededPaths, err = seedWorktree(repoRoot, wt.Path)
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
			acquired = leaseInfoFromEntry(state.Worktrees[i])
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
		seededPaths, err := seedWorktree(repoRoot, wtPath)
		if err != nil {
			// A failed removal leaves a real Git worktree behind. Keep it in
			// state as quarantined so later acquisitions cannot reuse its slot.
			if cleanupErr := removeWorktree(repoRoot, wtPath); cleanupErr != nil {
				entry := WorktreeEntry{
					Name:        name,
					Path:        wtPath,
					CreatedAt:   time.Now(),
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

		acquired = leaseInfoFromEntry(entry)
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

func leaseInfoFromEntry(wt WorktreeEntry) LeaseInfo {
	return LeaseInfo{
		Path:        wt.Path,
		LeaseID:     wt.LeaseID,
		LeaseHolder: wt.LeaseHolder,
		LeasedAt:    wt.LeasedAt,
	}
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

// ReleasePreconditions optionally constrain a release to the current lease.
// Pointer fields distinguish an omitted condition from an expected empty value.
type ReleasePreconditions struct {
	ExpectedLeaseID     *string
	ExpectedLeaseHolder *string
}

// Release resets a managed worktree, clears its short-lived owner reservation or
// durable lease, and returns it to the available pool. It retains the legacy
// unconditional behavior of releasing by path.
func Release(poolDir, worktreePath string) error {
	return ReleaseConditional(poolDir, worktreePath, ReleasePreconditions{}, nil)
}

// ValidateReleasePreconditions checks that a managed worktree still matches
// the requested lease without performing any release effects.
func ValidateReleasePreconditions(poolDir, worktreePath string, preconditions ReleasePreconditions) error {
	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		_, err = releasableWorktree(&state, worktreePath, preconditions)
		return err
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
func ReleaseConditional(poolDir, worktreePath string, preconditions ReleasePreconditions, beforeReset func() error) error {
	markerless := vcs.WorktreeBackendName(worktreePath) == ""
	branch := ""
	if !markerless {
		resolved, err := vcs.DefaultBranchForWorktree(worktreePath)
		if err != nil {
			return err
		}
		branch = resolved
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
				return err
			}
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

			procs, _ := process.FindProcessesInWorktree(wt.Path)
			ws.Processes = procs

			if wt.Leased {
				ws.Status = StatusLeased
				ws.LeaseID = wt.LeaseID
				ws.LeaseHolder = wt.LeaseHolder
				ws.LeasedAt = wt.LeasedAt
			} else if ownerAlive(wt) {
				ws.Status = StatusInUse
			} else if len(procs) > 0 {
				ws.Status = StatusInUse
				if cwdInWorktree(cwd, wt.Path) {
					ws.Status = StatusHere
				}
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
