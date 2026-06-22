package pool

import (
	"encoding/json"
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

func lockFilePath(poolDir string) string {
	return filepath.Join(poolDir, "treehouse-state.lock")
}

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
		return State{}, err
	}
	return s, nil
}

func WriteState(poolDir string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stateFilePath(poolDir), data, 0644)
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
