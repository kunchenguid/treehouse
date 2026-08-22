package pool

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

const stateVersion = 4

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
	for i := range s.Worktrees {
		wt := &s.Worktrees[i]
		if s.Version != stateVersion || keyErr != nil || !validSeedInventoryDigest(key, *wt) {
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
			flavor, err := vcs.WorktreeBackendNameChecked(wtPath)
			if err != nil {
				return State{}, fmt.Errorf("inspecting untracked pool worktree %s: %w", wtPath, err)
			}
			if flavor == "" {
				continue
			}
			now := time.Now()
			s.Worktrees = append(s.Worktrees, WorktreeEntry{
				Name:        slot.Name(),
				Path:        wtPath,
				CreatedAt:   now,
				Leased:      true,
				LeaseHolder: recoveredLeaseHolder,
				LeasedAt:    now,
			})
		}
	}
	return s, nil
}

// recoveredLeaseHolder marks a WorktreeEntry reconstructed by recoverCorruptState
// so callers (status output, destroy) can explain why it is unexpectedly leased.
const recoveredLeaseHolder = "recovered: state file was corrupt or truncated; verify before reuse"

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
			flavor, err := vcs.WorktreeBackendNameChecked(wtPath)
			if err != nil {
				return State{}, fmt.Errorf("state file %s is corrupt or truncated (%v), and recovery could not inspect %s: %w", stateFilePath(poolDir), parseErr, wtPath, err)
			}
			if flavor == "" {
				continue
			}
			now := time.Now()
			recovered = append(recovered, WorktreeEntry{
				Name:        slot.Name(),
				Path:        wtPath,
				CreatedAt:   now,
				Leased:      true,
				LeaseHolder: recoveredLeaseHolder,
				LeasedAt:    now,
			})
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
