//go:build windows

package pool

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File) error {
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	return windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol)
}

func unlockFile(f *os.File) error {
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(h, 0, 1, 0, ol)
}
