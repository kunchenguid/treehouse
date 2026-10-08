package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errWorkspaceBusy = errors.New("workspace is busy; retry after the current operation finishes")

func workspaceLockPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		// Creation and commands arriving after return must use the same key.
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return "", err
		}
		physical = filepath.Join(parent, filepath.Base(path))
	}
	return filepath.Join(filepath.Dir(physical), "."+filepath.Base(physical)+".treehouse-workspace.lock"), nil
}

func withWorkspaceLock(path string, fn func() error) error {
	lockPath, err := workspaceLockPath(path)
	if err != nil {
		return fmt.Errorf("resolve workspace lock: %w", err)
	}
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workspace lock %s is not a regular file", lockPath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect workspace lock: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open workspace lock: %w", err)
	}
	// Descriptor close releases the native lock, including when the callback fails.
	defer f.Close()
	if err := tryWorkspaceLock(f); err != nil {
		return fmt.Errorf("lock workspace %s: %w", path, err)
	}
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(lockPath)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return fmt.Errorf("workspace lock %s changed during acquisition", lockPath)
	}
	// Never unlink this sibling file: replacement would split callers across inodes.
	return fn()
}
