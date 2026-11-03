//go:build !windows

package gitvcs

import (
	"os"
	"syscall"
)

func tryLockHeadGuard(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
