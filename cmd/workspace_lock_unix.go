//go:build !windows

package cmd

import (
	"errors"
	"os"
	"syscall"
)

func tryWorkspaceLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errWorkspaceBusy
	}
	return err
}
