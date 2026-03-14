package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type WorktreeEntry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
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
