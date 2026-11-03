package gitvcs

import (
	"errors"
	"fmt"
	"os"

	"github.com/kunchenguid/treehouse/v3/internal/process"
)

var (
	ErrHeadLocked    = errors.New("worktree HEAD is locked")
	ErrHeadLockOwned = errors.New("HEAD.lock is Treehouse-owned")
)

// HeadLockError reports an obstruction without attempting recovery.
func HeadLockError(worktreePath string) error {
	headPath, err := gitPath(worktreePath, "HEAD")
	if err != nil {
		return err
	}
	return headLockError(worktreePath, headPath)
}

func headLockError(worktreePath, headPath string) error {
	lockPath := headPath + ".lock"
	if _, err := os.Lstat(lockPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("cannot inspect HEAD.lock: %w", err)
	}
	if sameRegularFile(lockPath, headPath+".treehouse-witness") {
		action := "retry get or return"
		if dirty, err := IsDirty(worktreePath); err != nil || dirty {
			action = "run return"
		}
		return fmt.Errorf("%w: %w: Treehouse-owned HEAD.lock %s; %s after the reset and its Git children exit", ErrHeadLocked, ErrHeadLockOwned, lockPath, action)
	}
	return fmt.Errorf("%w: anonymous HEAD.lock %s; inspect Git and Treehouse processes before manual removal", ErrHeadLocked, lockPath)
}

// The persistent guard serializes Treehouse's lock creation and recovery. A
// matching hard link proves we created HEAD.lock; acquiring the OS guard proves
// that its reset no longer owns it. Git never inherits these Go file handles.
// The guard must not be unlinked: a waiting process must lock the same inode.
func takeHeadGuard(headPath string) (*os.File, error) {
	path := headPath + ".treehouse-guard"
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular reset guard %s", path)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := tryLockHeadGuard(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: reset guard for %s is busy or unverifiable: %v", ErrHeadLocked, headPath+".lock", err)
	}
	opened, statErr := f.Stat()
	current, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		_ = f.Close()
		return nil, fmt.Errorf("refusing replaced reset guard %s", path)
	}
	return f, nil
}

func sameRegularFile(left, right string) bool {
	l, lerr := os.Lstat(left)
	r, rerr := os.Lstat(right)
	return lerr == nil && rerr == nil && l.Mode().IsRegular() && r.Mode().IsRegular() && os.SameFile(l, r)
}

// recoverHeadLock is called only while holding the reset guard. An anonymous
// lock, including a legacy Treehouse lock, is never removed automatically.
func recoverHeadLock(worktreePath, headPath string) error {
	lockPath, witness := headPath+".lock", headPath+".treehouse-witness"
	if err := headLockError(worktreePath, headPath); err != nil {
		if !sameRegularFile(lockPath, witness) {
			return err
		}
		procs, scanErr := process.UnprotectedProcessesInWorktree(worktreePath)
		if scanErr != nil {
			return fmt.Errorf("%w: cannot verify remaining worktree processes for %s: %v", ErrHeadLocked, lockPath, scanErr)
		}
		if len(procs) != 0 {
			return fmt.Errorf("%w: cannot recover %s while worktree processes are running: %v; run 'treehouse return --force <slot>' to terminate them and recover", ErrHeadLocked, lockPath, procs)
		}
		if !sameRegularFile(lockPath, witness) {
			return fmt.Errorf("%w: %s changed during recovery; inspect it before retrying", ErrHeadLocked, lockPath)
		}
		if err := os.Remove(lockPath); err != nil {
			return err
		}
	}
	// A kill before publication or after committing HEAD can leave only the
	// witness. It is reserved metadata; unlinking it cannot remove HEAD.
	if info, err := os.Lstat(witness); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular reset witness %s", witness)
	}
	return os.Remove(witness)
}

func openHeadLock(worktreePath, headPath string) (*os.File, func(), error) {
	guard, err := takeHeadGuard(headPath)
	if err != nil {
		return nil, nil, err
	}
	if err := recoverHeadLock(worktreePath, headPath); err != nil {
		_ = guard.Close()
		return nil, nil, err
	}
	witness, lockPath := headPath+".treehouse-witness", headPath+".lock"
	f, err := os.OpenFile(witness, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		_ = guard.Close()
		return nil, nil, err
	}
	if err := os.Link(witness, lockPath); err != nil {
		_ = f.Close()
		if err := os.Remove(witness); err != nil {
			_ = guard.Close()
			return nil, nil, err
		}
		// Filesystems without hard links retain the anonymous Git protocol.
		// They can still reset, but interrupted locks require manual inspection.
		f, err = os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if err != nil {
			_ = guard.Close()
			if os.IsExist(err) {
				return nil, nil, fmt.Errorf("%w: cannot lock worktree HEAD: %w", ErrHeadLocked, err)
			}
			return nil, nil, fmt.Errorf("cannot lock worktree HEAD: %w", err)
		}
	}
	identity, err := f.Stat()
	if err != nil {
		_ = f.Close()
		_ = guard.Close()
		return nil, nil, err
	}
	return f, func() {
		_ = f.Close()
		if current, err := os.Lstat(lockPath); err == nil && current.Mode().IsRegular() && os.SameFile(identity, current) {
			_ = os.Remove(lockPath)
		}
		_ = os.Remove(witness)
		_ = guard.Close()
	}, nil
}
