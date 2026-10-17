//go:build !windows

package process

import (
	"errors"
	"fmt"
	"syscall"
)

// StartStamp returns an opaque value that stays the same for as long as the
// process holding pid lives and differs for any later process given that PID.
// It is comparable only with stamps read on the same machine. ErrNoProcess
// means the PID is free.
func StartStamp(pid int32) (int64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}
	exists, err := processExists(pid)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, ErrNoProcess
	}
	stamp, err := readStartStamp(pid)
	if err != nil {
		// The process may have exited between the two reads.
		if exists, existsErr := processExists(pid); existsErr == nil && !exists {
			return 0, ErrNoProcess
		}
		return 0, err
	}
	if stamp == 0 {
		return 0, fmt.Errorf("process %d reported no start time", pid)
	}
	return stamp, nil
}

// processExists asks the kernel directly, so a process the caller may not
// inspect (EPERM, or hidden from /proc) still counts as existing.
func processExists(pid int32) (bool, error) {
	err := syscall.Kill(int(pid), 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}
