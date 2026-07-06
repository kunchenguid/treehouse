package pool

import (
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
	// LeaseHolder is an optional human-readable label for who holds the lease.
	LeaseHolder string `json:"lease_holder,omitempty"`
	// LeasedAt records when the lease was taken.
	LeasedAt time.Time `json:"leased_at,omitempty,omitzero"`
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
// pruned, or destroyed while their real reservation state is unknown.
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
		return recoverCorruptState(poolDir, err), nil
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
func recoverCorruptState(poolDir string, parseErr error) State {
	fmt.Fprintf(os.Stderr, "treehouse: WARNING: state file %s is corrupt or truncated (%v); recovering from worktrees found on disk. They are marked leased until verified - see `treehouse status`, then `treehouse return` or `treehouse destroy --include-leased`.\n", stateFilePath(poolDir), parseErr)

	slots, err := os.ReadDir(poolDir)
	if err != nil {
		return State{}
	}

	var recovered []WorktreeEntry
	for _, slot := range slots {
		if !slot.IsDir() {
			continue
		}
		slotDir := filepath.Join(poolDir, slot.Name())
		nested, err := os.ReadDir(slotDir)
		if err != nil {
			continue
		}
		for _, n := range nested {
			if !n.IsDir() {
				continue
			}
			wtPath := filepath.Join(slotDir, n.Name())
			if _, err := os.Stat(filepath.Join(wtPath, ".git")); err != nil {
				continue
			}
			recovered = append(recovered, WorktreeEntry{
				Name:        slot.Name(),
				Path:        wtPath,
				CreatedAt:   time.Now(),
				Leased:      true,
				LeaseHolder: recoveredLeaseHolder,
				LeasedAt:    time.Now(),
			})
		}
	}
	return State{Worktrees: recovered}
}

// WriteState persists the pool state file atomically: it writes to a temp file
// in the same directory, fsyncs it, and renames it into place, so a crash
// mid-write can never leave a truncated or empty state file behind (rename is
// atomic on POSIX, and os.Rename on Windows replaces the destination too).
func WriteState(poolDir string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(stateFilePath(poolDir), data, 0644)
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpPath)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
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
