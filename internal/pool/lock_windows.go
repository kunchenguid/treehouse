//go:build windows

package pool

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File) error {
	return lockFileWait(f, 0)
}

func lockFileWait(f *os.File, timeout time.Duration) error {
	h := windows.Handle(f.Fd())
	if timeout <= 0 {
		ol := new(windows.Overlapped)
		return windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol)
	}

	deadline := time.Now().Add(timeout)
	for {
		ol := new(windows.Overlapped)
		err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
		if err == nil {
			return nil
		}
		if err != windows.ERROR_LOCK_VIOLATION && err != windows.ERROR_IO_PENDING {
			return err
		}
		if time.Now().After(deadline) {
			return ErrLockTimeout
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func unlockFile(f *os.File) error {
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(h, 0, 1, 0, ol)
}
