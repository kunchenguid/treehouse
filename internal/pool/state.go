package pool

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
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
}

func newLeaseID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generating lease identity: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

type State struct {
	Worktrees []WorktreeEntry `json:"worktrees"`
}

func stateFilePath(poolDir string) string {
	return filepath.Join(poolDir, "treehouse-state.json")
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

// ReadState loads the pool state file. A missing file is a fresh, empty pool.
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
			return State{}, nil
		}
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return recoverCorruptState(poolDir, err)
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
// --include-leased. A human clears the false lease with `treehouse status` to
// see it and `treehouse return` (or `treehouse destroy --include-leased`) once
// verified.
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
			if _, err := os.Stat(filepath.Join(wtPath, ".git")); err != nil {
				if !os.IsNotExist(err) {
					return State{}, fmt.Errorf("state file %s is corrupt or truncated (%v), and recovery could not inspect %s: %w", stateFilePath(poolDir), parseErr, wtPath, err)
				}
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
	fmt.Fprintf(os.Stderr, "treehouse: WARNING: state file %s is corrupt or truncated (%v); recovering from worktrees found on disk. They are marked leased until verified - see `treehouse status`, then `treehouse return` or `treehouse destroy --include-leased`.\n", stateFilePath(poolDir), parseErr)
	return State{Worktrees: recovered}, nil
}

// WriteState persists the pool state file atomically: it writes to a temp file
// in the same directory, fsyncs it, commits it with the platform's replacement
// primitive, and syncs the parent directory where the platform supports that.
func WriteState(poolDir string, s State) error {
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
