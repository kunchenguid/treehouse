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
	// for a detached HEAD (reported separately as Detached), for a jj slot,
	// for a markerless slot, and for a slot whose branch could not be read
	// (reported as BranchErr).
	Branch string
	// Detached reports that this git slot's HEAD is detached, which is what
	// `treehouse get` leaves by default. It is false for slots that are not
	// git or hold no marker.
	Detached bool
	// BranchErr reports that reading this slot's branch failed. It is set
	// instead of leaving Branch empty, so a read failure is never mistaken for
	// a detached HEAD.
	BranchErr string
	// HeldOnlyByCwd reports a StatusHere slot that nobody is actually holding:
	// it is unleased, idle, clean, quiet and undamaged, and the only reason it
	// is not reported available is that the caller is standing in it. Status
	// alone cannot answer that, because StatusHere hides whichever
	// classification the slot would otherwise have carried.
	HeldOnlyByCwd bool
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
	// Branch creates and checks out a new Git branch at the acquired commit.
	// Empty preserves the default detached-HEAD behavior.
	Branch string
	// UniqueLeaf gives a newly created worktree a directory name unique within
	// the pool ("<repo>-<slot>") instead of the repository name every slot
	// shares. It only affects creation: a recycled slot keeps the path already
	// recorded in pool state, so enabling it never moves or renames a worktree
	// that already exists. WorktreePath supersedes it, because a template names
	// every segment of the path including the leaf.
	UniqueLeaf bool
	// WorktreePath templates the directory a newly created slot is placed in.
	// Empty keeps the built-in {pool}/{slot}/{repo} layout. It is read only when
	// a slot is created, so it never moves a worktree already in the pool.
	WorktreePath string
	// IncludeManifest replaces the committed manifest; nil keeps the default,
	// while a non-nil empty slice explicitly disables seeding.
	IncludeManifest []byte
}

// acquireOptions controls how Acquire reserves the worktree it hands out.
type acquireOptions struct {
	// skipFetch uses existing local refs without contacting origin.
	skipFetch bool
	// baseBranch is the explicitly requested base branch, or empty to infer it.
	baseBranch string
	// branch is the opt-in Git branch to create at the acquired commit.
	branch string
	// worktreePath templates where a newly created slot is placed, or empty for
	// the built-in layout.
	worktreePath    string
	includeManifest []byte
	// uniqueLeaf makes a newly created worktree's own directory name unique
	// within the pool instead of the repository name every slot shares.
	uniqueLeaf bool
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
		branch:          options.Branch,
		worktreePath:    options.WorktreePath,
		includeManifest: options.IncludeManifest,
		uniqueLeaf:      options.UniqueLeaf,
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
		branch:          options.Branch,
		worktreePath:    options.WorktreePath,
		includeManifest: options.IncludeManifest,
		uniqueLeaf:      options.UniqueLeaf,
		lease:           true,
		leaseHolder:     holder,
		hookStdout:      os.Stderr,
		hookStderr:      os.Stderr,
	})
}

var (
	seedWorktree   = vcs.SeedWorktree
	removeWorktree = vcs.RemoveWorktree
	createBranch   = vcs.CreateBranch
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

// freeTemplatedSlot picks the name and path a new templated worktree is created
// under, skipping every candidate whose path something already occupies.
// treehouse never adopts an existing directory, and a templated path can be one
// the pool no longer records - state recovery reads the pool directory only, so
// an out-of-pool worktree survives a lost state file while name allocation
// restarts at the first name. Refusing that one name would hand out nothing at
// all; skipping it keeps the pool usable while the operator clears the
// leftovers. Each skip is stated on stderr so the leftover is visible, and it
// stops there, because nothing here reads the occupant and so cannot vouch for
// deleting it. Only when every candidate is occupied does the acquisition fail,
// pointing at the repository's own worktree list rather than prescribing a
// command whose preconditions treehouse cannot see from here.
func freeTemplatedSlot(repoRoot, poolDir string, state State, poolSize int, opts acquireOptions) (string, string, error) {
	first := nextSlotNumber(state)
	var occupied []string
	for n := first; n < first+poolSize; n++ {
		name := strconv.Itoa(n)
		wtPath, err := resolveWorktreePath(repoRoot, poolDir, name, opts.worktreePath, opts.uniqueLeaf)
		if err != nil {
			return "", "", err
		}
		_, statErr := os.Lstat(wtPath)
		if os.IsNotExist(statErr) {
			return name, wtPath, nil
		}
		if statErr != nil {
			return "", "", statErr
		}
		fmt.Fprintf(os.Stderr, "🌳 Warning: skipped slot %s because its worktree path %s already exists.\n", name, wtPath)
		occupied = append(occupied, wtPath)
	}
	return "", "", fmt.Errorf("every worktree path this pool would create already exists (%d checked, %s through %s); treehouse only creates a worktree at a path it can own and never adopts an existing directory. List this repository's worktrees with 'git worktree list' in %s and see the README section on recovering missing pool state to decide what to do with them",
		len(occupied), occupied[0], occupied[len(occupied)-1], repoRoot)
}

// acquisitionCommonGitDir returns a physical clone identity: the file the
// common Git dir resolves to after symlinks, compared with os.SameFile so
// neither a symlink alias nor letter case on a case-insensitive filesystem
// splits one clone. A clone without one (including non-colocated jj, which
// has no common Git dir) is an error: ownership that cannot be proven is
// never treated as a match.
func acquisitionCommonGitDir(dir string) (os.FileInfo, error) {
	commonDir, err := vcs.CommonGitDir(dir)
	if err != nil {
		return nil, err
	}
	return os.Stat(commonDir)
}

func acquire(repoRoot, poolDir string, poolSize int, postCreate []string, opts acquireOptions) (LeaseInfo, error) {
	// Before the fetch and before any slot is inspected, so a template that is
	// wrong on its own text costs nothing. The placement rules need a slot name
	// and run under the state lock below.
	if _, err := validateWorktreePathTemplate(opts.worktreePath); err != nil {
		return LeaseInfo{}, err
	}
	if opts.branch != "" && vcs.BackendNameFor(repoRoot) != "git" {
		return LeaseInfo{}, fmt.Errorf("cannot create branch %q: --branch is only supported by the git backend; remove --branch to acquire a jj workspace", opts.branch)
	}

	// Said out loud rather than resolved silently: a template names the leaf
	// itself, so unique_leaf has nothing left to rename, and a pool that sets
	// both would otherwise get whichever leaf the template happens to end with.
	if opts.worktreePath != "" && opts.uniqueLeaf {
		fmt.Fprintf(os.Stderr, "🌳 Warning: worktree_path is set, so unique_leaf is ignored - the template names every segment of the path. Write %s in it for a unique leaf.\n", placeholderRepo+"-"+placeholderSlot)
	}

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
	// An unverifiable requester identity disables reuse, not allocation.
	commonDir, identityErr := acquisitionCommonGitDir(repoRoot)

	if opts.branch != "" {
		exists, err := vcs.LocalBranchExists(repoRoot, opts.branch)
		if err != nil {
			return LeaseInfo{}, fmt.Errorf("failed to check branch %q: %w", opts.branch, err)
		}
		if exists {
			return LeaseInfo{}, fmt.Errorf("branch %q already exists", opts.branch)
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

		// Placement rules run before the reuse loop so a recycling acquisition
		// still reports a template that escapes into the repository or the pool -
		// that branch never expands the template, so rules reached only from the
		// creation branch would accept a bad template or reject it depending on how
		// full the pool is. A template is only validated here; the creation branch
		// resolves the path it actually uses, skipping names whose directories are
		// occupied.
		name := nextName(state)
		var wtPath string
		if opts.worktreePath == "" {
			wtPath, err = resolveWorktreePath(repoRoot, poolDir, name, "", opts.uniqueLeaf)
			if err != nil {
				return err
			}
		} else if _, err := resolveWorktreePath(repoRoot, poolDir, name, opts.worktreePath, opts.uniqueLeaf); err != nil {
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
		otherClone := 0
		unverifiedClone := 0
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
			// Pools are shared by origin URL, but a linked worktree still
			// belongs to one physical clone. Never reset or acquire another
			// clone's slot, even when both clones have identical refs, and
			// never one whose owner (or our own identity) cannot be proven.
			if identityErr != nil {
				unverifiedClone++
				continue
			}
			candidateDir, err := acquisitionCommonGitDir(wt.Path)
			if err != nil {
				unverifiedClone++
				continue
			}
			if !os.SameFile(candidateDir, commonDir) {
				otherClone++
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
			if opts.branch != "" {
				if branchErr := createBranch(wt.Path, opts.branch); branchErr != nil {
					created := errors.Is(branchErr, vcs.ErrBranchCreated)
					// Branch creation failures normally leave HEAD detached. A
					// redundant detach runs post-checkout hooks and may create
					// ignored output in a slot about to be reused.
					_, detached, headErr := vcs.CheckedOutBranch(wt.Path)
					// A rejecting reference-transaction hook can write ignored
					// files while leaving HEAD detached and the tree clean.
					unknown, inspectErr := vcs.HasUnseededBranchCreationOutput(wt.Path, seededPaths)
					state.Worktrees[i].OwnerPID = 0
					state.Worktrees[i].OwnerStartedAt = 0
					if created || headErr != nil || !detached || inspectErr != nil || unknown {
						state.Worktrees[i].Leased = true
						state.Worktrees[i].LeaseHolder = "quarantined: branch creation cleanup failed"
						if created {
							state.Worktrees[i].LeaseHolder = "quarantined: branch checkout failed"
						}
						state.Worktrees[i].LeasedAt = time.Now()
					} else {
						clearLease(&state.Worktrees[i])
					}
					if writeErr := WriteState(poolDir, state); writeErr != nil {
						return fmt.Errorf("failed to create branch %q in %s: %w (state cleanup failed: %v)", opts.branch, wt.Path, branchErr, writeErr)
					}
					if inspectErr != nil {
						return fmt.Errorf("failed to create branch %q in %s: %w (worktree inspection failed: %v; worktree quarantined for inspection)", opts.branch, wt.Path, branchErr, inspectErr)
					}
					if headErr != nil {
						return fmt.Errorf("failed to create branch %q in %s: %w (HEAD inspection failed: %v; worktree quarantined for inspection)", opts.branch, wt.Path, branchErr, headErr)
					}
					if created || !detached || unknown {
						return fmt.Errorf("failed to create branch %q in %s: %w (worktree quarantined for inspection)", opts.branch, wt.Path, branchErr)
					}
					return fmt.Errorf("failed to create branch %q in %s: %w", opts.branch, wt.Path, branchErr)
				}
			}
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
			if otherClone > 0 || unverifiedClone > 0 {
				msg := fmt.Sprintf("all %d worktrees are in use, dirty, or not provably this clone's (%d belong to another clone; %d whose clone identity cannot be verified; max_trees = %d). A worktree is reused only by the clone it belongs to", len(state.Worktrees), otherClone, unverifiedClone, poolSize)
				if identityErr != nil {
					msg += fmt.Sprintf(", and this repository's clone identity cannot be verified: %v", identityErr)
				}
				return fmt.Errorf("%s. Run 'treehouse status' to see details, or increase max_trees in treehouse.toml", msg)
			}
			return fmt.Errorf("all %d worktrees are in use or dirty (max_trees = %d). Run 'treehouse status' to see details, or increase max_trees in treehouse.toml", len(state.Worktrees), poolSize)
		}

		// A templated path can point anywhere, including at a directory another
		// pool or checkout already owns - two pools whose templates agree would
		// otherwise register the same worktree and each feel free to delete it.
		// Occupied candidates are skipped rather than adopted. Only the templated
		// path is checked: the built-in layout keeps whatever AddWorktree does with
		// a leftover directory today.
		if opts.worktreePath != "" {
			name, wtPath, err = freeTemplatedSlot(repoRoot, poolDir, state, poolSize, opts)
			if err != nil {
				return err
			}
		}

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
		if opts.branch != "" {
			if branchErr := createBranch(wtPath, opts.branch); branchErr != nil {
				if errors.Is(branchErr, vcs.ErrBranchCreated) {
					entry := &state.Worktrees[len(state.Worktrees)-1]
					entry.Leased = true
					entry.LeaseHolder = "quarantined: branch checkout failed"
					entry.LeasedAt = time.Now()
					if writeErr := WriteState(poolDir, state); writeErr != nil {
						return fmt.Errorf("failed to create branch %q in %s: %w (quarantine failed: %v)", opts.branch, wtPath, branchErr, writeErr)
					}
					return fmt.Errorf("failed to create branch %q in %s: %w (worktree quarantined for inspection)", opts.branch, wtPath, branchErr)
				}
				// Git removes untracked and ignored files even without --force.
				// Only the authenticated seed inventory may be discarded here.
				unknown, inspectErr := vcs.HasUnseededWorktreeOutput(wtPath, seededPaths)
				if inspectErr != nil || unknown {
					entry := &state.Worktrees[len(state.Worktrees)-1]
					entry.Leased = true
					entry.LeaseHolder = "quarantined: branch creation cleanup failed"
					entry.LeasedAt = time.Now()
					if writeErr := WriteState(poolDir, state); writeErr != nil {
						return fmt.Errorf("failed to create branch %q in %s: %w (quarantine failed: %v)", opts.branch, wtPath, branchErr, writeErr)
					}
					if inspectErr != nil {
						return fmt.Errorf("failed to create branch %q in %s: %w (worktree inspection failed: %v; worktree quarantined for inspection)", opts.branch, wtPath, branchErr, inspectErr)
					}
					return fmt.Errorf("failed to create branch %q in %s: %w (worktree quarantined for inspection)", opts.branch, wtPath, branchErr)
				}
				if cleanupErr := removeWorktree(repoRoot, wtPath); cleanupErr != nil {
					entry := &state.Worktrees[len(state.Worktrees)-1]
					clearLease(entry)
					entry.Leased = true
					entry.LeaseHolder = "quarantined: branch creation cleanup failed"
					entry.LeasedAt = time.Now()
					if writeErr := WriteState(poolDir, state); writeErr != nil {
						return fmt.Errorf("failed to create branch %q in %s: %w (cleanup failed: %v; quarantine failed: %v)", opts.branch, wtPath, branchErr, cleanupErr, writeErr)
					}
					return fmt.Errorf("failed to create branch %q in %s: %w (cleanup failed: %v)", opts.branch, wtPath, branchErr, cleanupErr)
				}
				state.Worktrees = state.Worktrees[:len(state.Worktrees)-1]
				if writeErr := WriteState(poolDir, state); writeErr != nil {
					return fmt.Errorf("failed to create branch %q in %s: %w (worktree removed but state cleanup failed: %v)", opts.branch, wtPath, branchErr, writeErr)
				}
				return fmt.Errorf("failed to create branch %q in %s: %w", opts.branch, wtPath, branchErr)
			}
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

// ErrInvalidReleasePreconditions reports preconditions no worktree could ever
// satisfy: an empty ExpectedLeaseID, or RequireUnleased asked for alongside a
// lease identity or holder. It describes the CALL, not the worktree, so it is a
// programming error to surface loudly rather than one of the states a release
// classifies and skips.
var ErrInvalidReleasePreconditions = errors.New("invalid release preconditions")

// ErrSeedInventoryUntrusted reports that a worktree is quarantined: its seed
// inventory could not be authenticated, so no release may clear it. A state
// version bump or a rotated state key puts a whole pool in this state at once,
// which is why callers classify it with errors.Is: a bulk return has to report
// such a slot as skipped rather than as a failure it should retry forever.
var ErrSeedInventoryUntrusted = errors.New("untrusted seed inventory")

// ReleasePreconditions optionally constrain a release to the current lease.
// Pointer fields distinguish an omitted condition from an expected empty value.
//
// The two things a caller can assert about a lease are separate fields, not two
// readings of one value: "still exactly this acquisition" and "still nobody's"
// are opposite predicates, and a single field carrying both would read
// correctly and behave oppositely the day a caller passes an identity variable
// that happens to be empty. They cannot be combined, and an empty
// ExpectedLeaseID is a programming error rather than the other predicate in
// disguise - both are rejected with ErrInvalidReleasePreconditions.
type ReleasePreconditions struct {
	// ExpectedLeaseID requires the worktree to still carry EXACTLY this lease
	// identity, so a release that names one acquisition can never act on a
	// later one. Nil omits the condition. The empty string is not an identity
	// any acquisition can have; use RequireUnleased to assert the absence of a
	// lease.
	ExpectedLeaseID *string
	// ExpectedLeaseHolder requires the current lease to carry this holder. Like
	// ExpectedLeaseID it asserts that a lease EXISTS, so it refuses an unleased
	// worktree.
	ExpectedLeaseHolder *string
	// RequireUnleased requires the worktree to still carry NO lease. It is what
	// an observation of an unleased slot replays: a bulk release that set out
	// to reclaim a slot nobody had leased must refuse it once somebody has.
	RequireUnleased bool
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
// the requested lease AND that a release of it is possible at all, then runs
// guarded (when non-nil) while still holding the state lock. No release effects
// are performed either way.
//
// It shares releasableWorktree with ReleaseConditional, so the two always agree:
// a caller that passes here is refused later only by something that changed
// since, never by a condition the release knew about all along.
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

// ReleaseConditional verifies any lease preconditions and the quarantine state
// (both through releasableWorktree, under the lock), runs beforeReset, resets
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
		// Clearing a safety quarantine without a trusted seed inventory could
		// expose ignored files hidden by a mutable manifest. It is judged here,
		// with the preconditions, so that every caller learns a release is
		// impossible BEFORE it prepares one: `return` would otherwise offer to
		// discard a worktree's uncommitted changes and then refuse it anyway.
		// It is judged AFTER the preconditions so a caller that named a lease,
		// or `get` confirming its own reservation, still gets the answer to the
		// question it asked.
		if !wt.SeedInventoryKnown {
			return nil, fmt.Errorf("%w: worktree %s is quarantined without a trusted seed inventory; inspect it and use destroy --include-leased instead", ErrSeedInventoryUntrusted, worktreePath)
		}
		return wt, nil
	}
	return nil, fmt.Errorf("worktree %s is not managed by treehouse", worktreePath)
}

// check rejects preconditions that describe no reachable worktree state, before
// any state is read and long before anything is released.
func (p ReleasePreconditions) check() error {
	if p.RequireUnleased && (p.ExpectedLeaseID != nil || p.ExpectedLeaseHolder != nil) {
		return fmt.Errorf("%w: RequireUnleased asserts the worktree carries no lease and cannot be combined with an expected lease identity or holder", ErrInvalidReleasePreconditions)
	}
	if p.ExpectedLeaseID != nil && *p.ExpectedLeaseID == "" {
		return fmt.Errorf("%w: the empty string is not a lease identity; use RequireUnleased to require that the worktree carries no lease", ErrInvalidReleasePreconditions)
	}
	return nil
}

func validateReleasePreconditions(wt WorktreeEntry, preconditions ReleasePreconditions) error {
	if preconditions.RequireOwnedByCaller {
		if err := checkOwnedByCaller(wt); err != nil {
			return err
		}
	}
	if err := preconditions.check(); err != nil {
		return err
	}
	if preconditions.RequireUnleased {
		if wt.Leased {
			return fmt.Errorf("%w: worktree %s was observed unleased and is leased now", ErrLeasePreconditionFailed, wt.Path)
		}
		return nil
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
			// Which checkout is in this slot. A markerless (damaged) slot is
			// never read, so the branch of a repository enclosing the pool can
			// never be inherited. Detached, jj, and markerless slots report an
			// empty branch; a failed read is reported as BranchErr instead of
			// collapsing into that empty value.
			branch, detached, branchErr := vcs.CheckedOutBranch(wt.Path)
			ws.Branch = branch
			ws.Detached = detached
			if branchErr != nil {
				ws.BranchErr = branchErr.Error()
			}
			// The slot is classified WITHOUT the cwd, which is then laid over
			// the answer. The ranking the operator sees is unchanged - leased
			// and a live owner reservation still outrank "you're here", and it
			// still outranks everything below - but the classification it hides
			// is now known, so a caller can tell a slot somebody holds from one
			// whose only claim is the shell standing in it.
			owned := ownerAlive(wt)
			switch {
			case wt.Leased:
				ws.Status = StatusLeased
				ws.LeaseID = wt.LeaseID
				ws.LeaseHolder = wt.LeaseHolder
				ws.LeasedAt = wt.LeasedAt
			case owned:
				ws.Status = StatusInUse
			case scanErr != nil:
				ws.Status = StatusUnverified
			case len(procs) > 0:
				ws.Status = StatusInUse
			case ws.Flavor == "":
				ws.Status = StatusDamaged
			default:
				if dirty, _ := vcs.IsDirty(wt.Path); dirty {
					ws.Status = StatusDirty
				}
			}
			// Damaged is not overlaid. A missing marker is a different kind of
			// fact from the reservations above: it says the slot's own contents
			// cannot be judged at all, which is what `destroy` - not `return` -
			// answers, and standing in such a slot does not make it any more
			// readable. The recovery-scan case below already forces damaged
			// back over the overlay for exactly this reason.
			if !wt.Leased && !owned && ws.Status != StatusDamaged && process.WorktreeContainsCwd(wt.Path, cwd) {
				ws.HeldOnlyByCwd = ws.Status == StatusAvailable
				ws.Status = StatusHere
			}

			// A slot the recovery scan could not inspect (its marker exists but
			// could not be read) is reported damaged rather than leased, so the
			// read failure is never mistaken for an available or ordinarily leased
			// home. It remains leased underneath (LeaseHolder is already set
			// above), so Acquire and prune keep skipping it exactly like every
			// other recovered entry.
			if wt.RecoveryError != "" {
				ws.Status = StatusDamaged
				ws.BranchErr = wt.RecoveryError
			}

			result = append(result, ws)
		}
		return nil
	})

	return result, err
}

// FindByName returns the pool entry registered under name, or nil when this
// pool has no such slot. The name is the identity `treehouse status` prints in
// its first column and `treehouse lease <name>` already accepts: it belongs to
// the slot for the slot's whole lifetime, unlike a position in a listing, which
// moves whenever another slot is created or destroyed. Lookup is by exact
// match, and a pool never registers two slots under one name.
//
// Unlike a path, a name is meaningful only inside the pool that issued it, so
// callers must resolve the pool from the repository first.
func FindByName(poolDir, name string) (*WorktreeEntry, error) {
	state, err := ReadState(poolDir)
	if err != nil {
		return nil, err
	}
	for _, wt := range state.Worktrees {
		if wt.Name == name {
			return &wt, nil
		}
	}
	return nil, nil
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

// dropStaleJJSeedAuthentication unlinks the authentication a jj slot left beside
// its worktree, for the removal routes that never call vcs.RemoveWorktree - the
// orphan and markerless ones - and so never reach the removal that normally does
// it. Under the built-in layout the slot container took the file with it; a
// worktree_path worktree outside the pool has only itself removed, and its state
// entry is dropped in the same transaction, so nothing would ever look for the
// file again. Left behind it is not inert: a later acquisition on that path
// cannot seed, and the slot it leaves cannot be destroyed.
//
// BEST EFFORT, AND SAID OUT LOUD. A failure here is not fatal: the caller has
// already deleted the worktree, so failing the removal would strand a slot that
// is gone, and keeping its state entry so cleanup could be retried would make
// every later operation fail during state healing. It is warned rather than
// swallowed, and the leftover is not permanent either - the entry is keyed on
// the worktree's own path, so gitvcs.PrepareJJSeededCleanup relinks over it and
// the next acquisition there seeds normally.
//
// It acts only on an entry whose signed inventory validates, and
// vcs.RemoveStaleJJSeedAuthentication verifies the file's own identity, and that
// the workspace is really gone, before unlinking it. The shared directory is
// deliberately left in place: pools that share it take independent state locks,
// so no pool can prove it is unused.
func dropStaleJJSeedAuthentication(poolDir string, wt WorktreeEntry) {
	if !wt.SeedInventoryKnown || wt.SeedInventoryDigest == "" || wt.SeedBackend != "jj" || wt.SeedAuthIdentity == "" || len(wt.SeededPaths) == 0 {
		return
	}
	key, err := readStateKey(poolDir)
	if err != nil {
		warnStaleJJSeedAuthentication(wt.Path, err)
		return
	}
	if !validSeedInventoryDigest(key, wt) {
		return
	}
	if err := vcs.RemoveStaleJJSeedAuthentication(wt.Path, wt.SeedAuthIdentity); err != nil {
		warnStaleJJSeedAuthentication(wt.Path, err)
	}
}

var warnStaleJJSeedAuthentication = func(worktreePath string, err error) {
	fmt.Fprintf(os.Stderr, "treehouse: WARNING: could not remove the jj seed authentication left beside %s (%v); the worktree and its state entry are gone, and the next acquisition at that path relinks the leftover itself.\n", worktreePath, err)
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
	return strconv.Itoa(nextSlotNumber(state))
}

func nextSlotNumber(state State) int {
	max := 0
	for _, wt := range state.Worktrees {
		if n, err := strconv.Atoi(wt.Name); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}
