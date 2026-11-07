//go:build windows

package gitvcs

import (
	"os"

	"golang.org/x/sys/windows"
)

func tryLockHeadGuard(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, new(windows.Overlapped))
}
