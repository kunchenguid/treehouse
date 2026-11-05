//go:build !windows

package pool

import (
	"os"
	"syscall"
	"time"
)

func lockFile(f *os.File) error {
	return lockFileWait(f, 0)
}

func lockFileWait(f *os.File, timeout time.Duration) error {
	if timeout <= 0 {
		return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	}

	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		if time.Now().After(deadline) {
			return ErrLockTimeout
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
