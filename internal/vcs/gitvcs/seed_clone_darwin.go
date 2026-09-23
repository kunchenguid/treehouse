//go:build darwin

package gitvcs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var cloneFileAt = unix.Fclonefileat

func cloneSeedFile(source, destination *os.Root, rel string, info os.FileInfo) (bool, error) {
	src, err := source.OpenFile(rel, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	defer src.Close()
	actual, err := src.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return false, fmt.Errorf("source cache file changed: %s: %v", rel, err)
	}
	parent, err := destination.OpenFile(filepath.Dir(rel), os.O_RDONLY, 0)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	// The source and destination are held by rooted descriptors; clonefileat
	// creates the basename atomically and refuses an existing destination.
	err = cloneFileAt(int(src.Fd()), int(parent.Fd()), filepath.Base(rel), 0)
	if err == nil {
		return true, nil
	}
	// A failed clone may have created a destination. Report it in the
	// inventory so the caller can quarantine and clean up safely.
	if _, statErr := destination.Lstat(rel); statErr == nil {
		return true, err
	} else if !os.IsNotExist(statErr) {
		return false, fmt.Errorf("clone failed: %w (destination check: %v)", err, statErr)
	}
	if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
		return false, nil
	}
	return false, err
}
