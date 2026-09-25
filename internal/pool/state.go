package pool

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/kunchenguid/treehouse/internal/vcs"
)

type WorktreeEntry struct {
	Name           string    `json:"name"`
	Path           string    `json:"path"`
	CreatedAt      time.Time `json:"created_at"`
	Destroying     bool      `json:"destroying,omitempty"`
	OwnerPID       int32     `json:"owner_pid,omitempty"`
	OwnerStartedAt int64     `json:"owner_started_at,omitempty"`
	// Leased marks a worktree as durably reserved by an external consumer that
	// keeps no live process inside it. Unlike OwnerPID/OwnerStartedAt (which are
	// process-derived and self-heal when the owner dies), a lease persists until
	// it is explicitly released by `treehouse return`. A missing field decodes to
	// false, so pre-lease state files keep today's behavior.
	Leased bool `json:"leased,omitempty"`
	// LeaseID is an immutable identity for one acquisition. It is empty only
	// when loading state written by a Treehouse version that predates lease IDs
	// or when conservatively recovering a corrupt state file.
	LeaseID string `json:"lease_id,omitempty"`
	// LeaseHolder is an optional human-readable label for who holds the lease.
	LeaseHolder string `json:"lease_holder,omitempty"`
	// LeasedAt records when the lease was taken.
	LeasedAt time.Time `json:"leased_at,omitempty,omitzero"`
	// BaseBranch is the EXPLICITLY requested base this slot was last cut from,
	// empty for an inferred acquisition. Release parks the slot back on it so
	// the next acquire naming the same base can recycle it. Only an explicit
	// base is recorded: prune and destroy give a slot a second reading against
	// this field, and recording an inferred default here would widen what they
	// delete for pools that never opted in.
	BaseBranch string `json:"base_branch,omitempty"`
	// SeededPaths is the trusted inventory of ignored files copied for this
	// acquisition. It must live outside the mutable worktree so reset cannot be
	// bypassed by changing or committing .worktreeinclude there.
	SeededPaths []string `json:"seeded_paths,omitempty"`
	// SeedInventoryKnown distinguishes a verified empty inventory from an
	// incomplete or recovered acquisition that must fail closed.
	SeedInventoryKnown  bool   `json:"seed_inventory_known,omitempty"`
	SeedInventoryDigest string `json:"seed_inventory_digest,omitempty"`
	SeedBackend         string `json:"seed_backend,omitempty"`
	SeedAuthIdentity    string `json:"seed_auth_identity,omitempty"`
	// RecoveryError records why this entry could not be recovered from disk
	// during a state-file recovery scan: its .git/.jj marker exists but could
	// not be resolved (a dangling or self-referential symlink, a permission
	// failure). The entry is otherwise a normal recovered entry - leased so
	// Acquire and prune skip it and destroy removes it only via an explicit
	// --include-leased target - but List reports it as damaged with this
	// reason, so a skipped slot is visible and never reads as available or an
	// ordinarily leased home.
	RecoveryError string `json:"recovery_error,omitempty"`
}

func newLeaseID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generating lease identity: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

type State struct {
	Version   int             `json:"version,omitempty"`
	Worktrees []WorktreeEntry `json:"worktrees"`
}

// stateVersion is the state format this build writes. Version 5 has the same
// shape as version 4; the bump only marks files written by a build that no
// longer misreads pre-3.0 state, so a version-4 file is known to come from
// treehouse 3.0.0 and ReadState can undo the quarantine that release applied
// while upgrading (see upgradeQuarantineStamp).
const stateVersion = 5

// upgradeQuarantineStateVersion is the version treehouse 3.0.0 wrote. Its first
// run over pre-3.0 state quarantined every entry as recovered even though that
// state was valid.
const upgradeQuarantineStateVersion = 4

// upgradeQuarantineWindow bounds how long before its pool's state key was
// created 3.0.0 can have stamped its upgrade quarantine. It stamped the lease
// time while reading pre-3.0 state and created the key on the same command's
// first write, so the gap is only the work in between - at most a fresh
// worktree checkout.
const upgradeQuarantineWindow = 10 * time.Minute

// upgradeQuarantineSpread bounds how far apart 3.0.0 stamped the entries of one
// pool: it stamped them all in one pass over the state, microseconds apart.
const upgradeQuarantineSpread = time.Second

// coarseTimestampSlack absorbs a file system that records whole-second (or,
// like FAT, two-second) modification times, so the state key's mtime can read
// earlier than a lease time stamped just before the key was written.
const coarseTimestampSlack = 2 * time.Second

// upgradeQuarantineLeaseHolder replaces recoveredLeaseHolder on an entry that
// was idle or in use when treehouse 3.0.0 quarantined it while upgrading pre-3.0
// state. healState releases it once the worktree proves idle; until then it
// stays leased, and `return` releases it like any other lease.
const upgradeQuarantineLeaseHolder = "quarantined by the 3.0.0 upgrade; freed once detached, clean, and idle"

// upgradeLeaseHolder replaces recoveredLeaseHolder on an entry that was already
// leased when treehouse 3.0.0 upgraded pre-3.0 state, which overwrote the
// holder. It is never released automatically.
const upgradeLeaseHolder = "leased before the 3.0.0 upgrade, which lost the holder"

func stateFilePath(poolDir string) string {
	return filepath.Join(poolDir, "treehouse-state.json")
}

func stateKeyPath(poolDir string) string {
	return filepath.Join(poolDir, "treehouse-state.key")
}

// IsPoolDir reports whether dir is a managed pool directory (it holds a
// treehouse state file). It lets callers resolve a pool from a path without
// knowing treehouse's internal state-file layout.
func IsPoolDir(dir string) bool {
	_, err := os.Stat(stateFilePath(dir))
	return err == nil
}

func lockFilePath(poolDir string) string {
	return filepath.Join(poolDir, "treehouse-state.lock")
}

// ReadState loads the pool state file. A missing file is a fresh, empty pool
// unless worktree directories already exist and must be recovered.
// A file that exists but fails to parse - most likely a state file truncated
// by a crash mid-write - is NOT a hard failure: it would otherwise brick every
// pool command. Instead ReadState logs a loud warning and reconstructs a
// conservative state from the worktree directories still present on disk (see
// recoverCorruptState), so on-disk worktrees are never silently handed out,
// pruned, or destroyed while their real reservation state is unknown. If that
// scan cannot complete, ReadState fails closed rather than returning an
// incomplete state.
func ReadState(poolDir string) (State, error) {
	data, err := os.ReadFile(stateFilePath(poolDir))
	if err != nil {
		if os.IsNotExist(err) {
			if _, statErr := os.Stat(poolDir); os.IsNotExist(statErr) {
				return State{}, nil
			} else if statErr != nil {
				return State{}, statErr
			}
			return recoverMissingStateEntries(poolDir, State{})
		}
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return recoverCorruptState(poolDir, err)
	}
	if s.Version < 0 || s.Version > stateVersion {
		return State{}, fmt.Errorf("unsupported treehouse state version %d", s.Version)
	}
	key, keyErr := readStateKey(poolDir)
	// Before 3.0 no release seeded ignored files, and every 3.0 write creates
	// the key before it records a seed inventory. Unversioned state with no key
	// beside it therefore came from a pre-3.0 release and has nothing seeded.
	// Unversioned state beside a key was rewritten by an older binary after 3.0
	// ran, and may have dropped a real inventory, so it stays quarantined.
	legacy := s.Version == 0 && errors.Is(keyErr, fs.ErrNotExist)
	var keyCreated, upgradeStamp time.Time
	if s.Version == upgradeQuarantineStateVersion && keyErr == nil {
		if info, err := os.Stat(stateKeyPath(poolDir)); err == nil {
			keyCreated = info.ModTime()
			if keyCreated.Nanosecond() == 0 {
				keyCreated = keyCreated.Add(coarseTimestampSlack)
			}
			upgradeStamp = upgradeQuarantineStamp(s.Worktrees, keyCreated)
		}
	}
	for i := range s.Worktrees {
		wt := &s.Worktrees[i]
		if legacy && !hasSeedState(*wt) {
			setSeedInventory(wt, nil, true)
			continue
		}
		if !keyCreated.IsZero() && upgradedPre30Entry(*wt, keyCreated) {
			wt.LeaseHolder = upgradeLeaseHolder
			if !upgradeStamp.IsZero() && !wt.LeasedAt.Before(upgradeStamp.Add(-upgradeQuarantineSpread)) {
				wt.LeaseHolder = upgradeQuarantineLeaseHolder
			}
			setSeedInventory(wt, nil, true)
			continue
		}
		if s.Version < upgradeQuarantineStateVersion || keyErr != nil || !validSeedInventoryDigest(key, *wt) {
			wt.Leased = true
			wt.LeaseHolder = recoveredLeaseHolder
			wt.SeededPaths = nil
			wt.SeedInventoryKnown = false
			wt.SeedInventoryDigest = ""
			wt.SeedBackend = ""
			wt.SeedAuthIdentity = ""
			if wt.LeasedAt.IsZero() {
				wt.LeasedAt = time.Now()
			}
		}
	}
	s.Version = stateVersion
	return recoverMissingStateEntries(poolDir, s)
}

func hasSeedState(wt WorktreeEntry) bool {
	return wt.SeedInventoryKnown || len(wt.SeededPaths) > 0 || wt.SeedInventoryDigest != "" || wt.SeedBackend != "" || wt.SeedAuthIdentity != ""
}

// upgradedPre30Entry reports whether a version-4 entry is a pre-3.0 entry that
// treehouse 3.0.0 relabeled recoveredLeaseHolder while upgrading, keeping any
// lease identity and lease time and stamping a lease time where there was
// none. It stamped them before it created the state key (keyCreated) on that
// command's first write; everything it leased or quarantined later was stamped
// after. Such an entry carries no lease identity (a lease taken by 2.1 or later
// has one, and 3.0.0 kept it), no seed state, and no recovery error, and it was
// not created at the instant it was leased, as both recovery scans stamp their
// entries.
//
// Nothing before 3.0 seeded ignored files, so its inventory is known empty.
func upgradedPre30Entry(wt WorktreeEntry, keyCreated time.Time) bool {
	return wt.Leased && wt.LeaseHolder == recoveredLeaseHolder && wt.LeaseID == "" &&
		wt.RecoveryError == "" && !wt.Destroying && !hasSeedState(wt) &&
		!wt.LeasedAt.IsZero() && !wt.CreatedAt.Equal(wt.LeasedAt) &&
		wt.LeasedAt.Before(keyCreated)
}

// upgradeQuarantineStamp returns the lease time treehouse 3.0.0 stamped on the
// pre-3.0 entries that had none, or the zero time when it finds none. Those
// were idle or in use; the rest had a lease of their own, whose time 3.0.0 kept.
// The stamp was taken after every such lease, in the command that created the
// state key, so it is the latest lease time among upgraded entries, provided it
// falls within upgradeQuarantineWindow of the key's creation. ReadState treats
// the entries stamped within upgradeQuarantineSpread of it as the ones 3.0.0
// stamped.
//
// A pool in which every entry was leased before the upgrade has no stamp, and
// its latest lease, if taken inside the window, is taken for one. That entry
// is still released only once it proves idle.
func upgradeQuarantineStamp(entries []WorktreeEntry, keyCreated time.Time) time.Time {
	var stamp time.Time
	for _, wt := range entries {
		if upgradedPre30Entry(wt, keyCreated) && wt.LeasedAt.After(stamp) &&
			!wt.LeasedAt.Before(keyCreated.Add(-upgradeQuarantineWindow)) {
			stamp = wt.LeasedAt
		}
	}
	return stamp
}

func validSeedInventoryDigest(key []byte, wt WorktreeEntry) bool {
	return wt.SeedInventoryKnown && validSeedInventory(wt.SeededPaths) && validSeedMetadata(wt) && hmac.Equal([]byte(wt.SeedInventoryDigest), []byte(seedInventoryDigest(key, wt)))
}

func setSeedInventory(wt *WorktreeEntry, paths []string, known bool) {
	wt.SeededPaths = paths
	wt.SeedInventoryKnown = known
	wt.SeedInventoryDigest = ""
	wt.SeedBackend = ""
	wt.SeedAuthIdentity = ""
}

func seedInventoryDigest(key []byte, wt WorktreeEntry) string {
	if len(wt.SeededPaths) == 0 {
		wt.SeededPaths = []string{}
	}
	data, _ := json.Marshal(struct {
		Name        string   `json:"name"`
		Path        string   `json:"path"`
		SeededPaths []string `json:"seeded_paths"`
		SeedBackend string   `json:"seed_backend"`
		SeedAuthID  string   `json:"seed_auth_identity"`
	}{wt.Name, filepath.Clean(wt.Path), wt.SeededPaths, wt.SeedBackend, wt.SeedAuthIdentity})
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write(data)
	return hex.EncodeToString(digest.Sum(nil))
}

func readStateKey(poolDir string) ([]byte, error) {
	key, err := os.ReadFile(stateKeyPath(poolDir))
	if err != nil {
		return nil, err
	}
	if len(key) != sha256.Size {
		return nil, fmt.Errorf("invalid treehouse state key")
	}
	return key, nil
}

func ensureStateKey(poolDir string) ([]byte, error) {
	key, err := readStateKey(poolDir)
	if err == nil {
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key = make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(stateKeyPath(poolDir), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return readStateKey(poolDir)
	}
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(key); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func prepareStateForWrite(poolDir string, s State) (State, error) {
	key, err := ensureStateKey(poolDir)
	if err != nil {
		for i := range s.Worktrees {
			if s.Worktrees[i].SeedInventoryKnown {
				return State{}, err
			}
		}
		key = make([]byte, sha256.Size)
		if _, err := rand.Read(key); err != nil {
			return State{}, err
		}
		if err := atomicWriteFile(stateKeyPath(poolDir), key, 0o600); err != nil {
			return State{}, err
		}
	}
	for i := range s.Worktrees {
		wt := &s.Worktrees[i]
		if wt.SeedInventoryKnown {
			if !validSeedInventory(wt.SeededPaths) {
				return State{}, fmt.Errorf("invalid seeded path inventory")
			}
			if len(wt.SeededPaths) > 0 && wt.SeedBackend == "" {
				wt.SeedBackend = vcs.WorktreeBackendName(wt.Path)
				if wt.SeedBackend == "jj" {
					wt.SeedAuthIdentity, err = vcs.JJSeedAuthenticationIdentity(wt.Path)
					if err != nil {
						return State{}, err
					}
				}
			}
			if !validSeedMetadata(*wt) {
				return State{}, fmt.Errorf("invalid seeded authentication metadata")
			}
			wt.SeedInventoryDigest = seedInventoryDigest(key, *wt)
		} else {
			wt.SeedInventoryDigest = ""
		}
	}
	s.Version = stateVersion
	return s, nil
}

func validSeedMetadata(wt WorktreeEntry) bool {
	if len(wt.SeededPaths) == 0 {
		return wt.SeedBackend == "" && wt.SeedAuthIdentity == ""
	}
	return (wt.SeedBackend == "git" && wt.SeedAuthIdentity == "") || (wt.SeedBackend == "jj" && wt.SeedAuthIdentity != "")
}

func validSeedInventory(paths []string) bool {
	for _, name := range paths {
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") {
			return false
		}
		first := strings.SplitN(name, "/", 2)[0]
		if strings.EqualFold(first, ".git") || strings.EqualFold(first, ".jj") {
			return false
		}
	}
	return true
}

// recoverMissingStateEntries covers the narrow window where creating a Git
// worktree succeeds but persisting its quarantine entry fails. Such a worktree
// must remain unavailable even though the otherwise-valid state file omits it.
func recoverMissingStateEntries(poolDir string, s State) (State, error) {
	known := make(map[string]bool, len(s.Worktrees))
	for _, wt := range s.Worktrees {
		known[filepath.Clean(wt.Path)] = true
	}

	slots, err := os.ReadDir(poolDir)
	if err != nil {
		return State{}, err
	}
	for _, slot := range slots {
		if !slot.IsDir() {
			continue
		}
		slotDir := filepath.Join(poolDir, slot.Name())
		nested, err := os.ReadDir(slotDir)
		if err != nil {
			return State{}, fmt.Errorf("scanning pool slot %s: %w", slotDir, err)
		}
		for _, entry := range nested {
			if !entry.IsDir() {
				continue
			}
			wtPath := filepath.Join(slotDir, entry.Name())
			if known[filepath.Clean(wtPath)] {
				continue
			}
			if wt, ok := recoverOneWorktree(slot.Name(), wtPath); ok {
				s.Worktrees = append(s.Worktrees, wt)
			}
		}
	}
	return s, nil
}

// recoveredLeaseHolder marks a WorktreeEntry reconstructed by either recovery
// scan (recoverMissingStateEntries or recoverCorruptState) so callers (status
// output, destroy) can explain why it is unexpectedly leased.
const recoveredLeaseHolder = "recovered: state file was corrupt or truncated; verify before reuse"

// quarantineEntry builds the conservative entry both recovery scans write for a
// worktree whose reservation state was lost. It is leased under
// recoveredLeaseHolder, so Acquire and prune skip it and destroy removes it only
// via an explicit single-target --include-leased. recoveryError is empty for a
// worktree whose marker resolved normally, and non-empty for one whose marker
// exists but could not be read.
func quarantineEntry(slotName, wtPath, recoveryError string) WorktreeEntry {
	now := time.Now()
	return WorktreeEntry{
		Name:          slotName,
		Path:          wtPath,
		CreatedAt:     now,
		Leased:        true,
		LeaseHolder:   recoveredLeaseHolder,
		LeasedAt:      now,
		RecoveryError: recoveryError,
	}
}

// recoverOneWorktree resolves one on-disk worktree directory into a
// conservative quarantine entry, or reports that the directory is not a
// worktree at all. It is the single place both recovery scans decide what an
// unreadable marker means, so the two paths can never again treat the same
// condition differently - the asymmetry Greptile flagged was exactly that.
//
// Three outcomes:
//   - no VCS marker at all: not a pooled worktree; skip it (ok == false).
//   - marker readable ("git"/"jj"): recovered as a leased, quarantined entry.
//   - marker present but unreadable (a dangling or self-referential symlink, a
//     permission or loop error): recovered too, as a leased entry carrying the
//     read error. It stays unusable - Acquire and prune skip it exactly like
//     every other recovered entry - but visible: List reports it damaged with
//     the recorded reason, and a warning is printed so the failure is not
//     silent.
//
// Only an os.ReadDir failure is fatal, and that stays in the callers: it means
// the pool (or a slot) directory cannot be scanned at all, so there is no entry
// to recover and no way to know which healthy slots exist. A single unreadable
// marker is a per-slot problem and must not hide the healthy slots around it,
// which is why it is recovered here rather than surfaced as an error.
func recoverOneWorktree(slotName, wtPath string) (WorktreeEntry, bool) {
	flavor, err := vcs.WorktreeBackendNameChecked(wtPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "treehouse: WARNING: cannot read the VCS marker for %s (%v); recovered as leased and reported damaged - see `treehouse status`\n", wtPath, err)
		return quarantineEntry(slotName, wtPath, err.Error()), true
	}
	if flavor == "" {
		return WorktreeEntry{}, false
	}
	return quarantineEntry(slotName, wtPath, ""), true
}

// recoverCorruptState rebuilds a State from the worktree directories that exist
// under poolDir when the on-disk state file could not be parsed. The original
// state - including who owned or leased each worktree - is gone, so on-disk
// evidence alone cannot tell an idle spare from a live, process-independent
// lease. Every recovered entry is therefore marked leased: Acquire and prune
// skip it, and destroy only removes it via an explicit, single-target
// --include-leased. Return cannot safely clear the lease because recovery also
// loses the trusted inventory of ignored files seeded into the worktree.
func recoverCorruptState(poolDir string, parseErr error) (State, error) {
	slots, err := os.ReadDir(poolDir)
	if err != nil {
		return State{}, fmt.Errorf("state file %s is corrupt or truncated (%v), and recovery could not scan pool directory: %w", stateFilePath(poolDir), parseErr, err)
	}

	var recovered []WorktreeEntry
	for _, slot := range slots {
		if !slot.IsDir() {
			continue
		}
		slotDir := filepath.Join(poolDir, slot.Name())
		nested, err := os.ReadDir(slotDir)
		if err != nil {
			return State{}, fmt.Errorf("state file %s is corrupt or truncated (%v), and recovery could not scan %s: %w", stateFilePath(poolDir), parseErr, slotDir, err)
		}
		for _, n := range nested {
			if !n.IsDir() {
				continue
			}
			wtPath := filepath.Join(slotDir, n.Name())
			if wt, ok := recoverOneWorktree(slot.Name(), wtPath); ok {
				recovered = append(recovered, wt)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "treehouse: WARNING: state file %s is corrupt or truncated (%v); recovering from worktrees found on disk. They are marked leased because their seeded-file inventory is unknown - see `treehouse status`, then remove one with `treehouse destroy <path> --include-leased --yes`.\n", stateFilePath(poolDir), parseErr)
	return State{Worktrees: recovered}, nil
}

// WriteState persists the pool state file atomically: it writes to a temp file
// in the same directory, fsyncs it, commits it with the platform's replacement
// primitive, and syncs the parent directory where the platform supports that.
func WriteState(poolDir string, s State) error {
	s, err := prepareStateForWrite(poolDir, s)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(stateFilePath(poolDir), data, 0644)
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	fileMode, targetExists, err := replacementFileMode(path, perm)
	if err != nil {
		return err
	}

	tmp, tmpPath, err := createTempStateFile(dir, filepath.Base(path), fileMode)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(tmpPath)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if targetExists {
		if err = tmp.Chmod(fileMode); err != nil {
			tmp.Close()
			return err
		}
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = commitStateFile(tmpPath, path, targetExists); err != nil {
		return err
	}
	return nil
}

func replacementFileMode(path string, perm os.FileMode) (os.FileMode, bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Mode().Perm(), true, nil
	}
	if os.IsNotExist(err) {
		return perm.Perm(), false, nil
	}
	return 0, false, err
}

func createTempStateFile(dir, base string, perm os.FileMode) (*os.File, string, error) {
	for range 100 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, "", err
		}
		path := filepath.Join(dir, fmt.Sprintf("%s.tmp-%x", base, suffix))
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			return f, path, nil
		}
		if os.IsExist(err) {
			continue
		}
		return nil, "", err
	}
	return nil, "", fmt.Errorf("creating temporary state file: too many name collisions")
}

func WithStateLock(poolDir string, fn func() error) error {
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		return err
	}

	lockPath := lockFilePath(poolDir)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return err
	}
	defer unlockFile(f)

	return fn()
}
