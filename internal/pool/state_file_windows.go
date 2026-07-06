//go:build windows

package pool

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const replaceFileWriteThrough = 0x1

var procReplaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func commitStateFile(tmpPath, path string, targetExists bool) error {
	if targetExists {
		return replaceExistingFile(path, tmpPath)
	}
	return moveFileThrough(tmpPath, path)
}

func replaceExistingFile(path, tmpPath string) error {
	replaced, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	replacement, err := windows.UTF16PtrFromString(tmpPath)
	if err != nil {
		return err
	}
	r1, _, callErr := procReplaceFileW.Call(
		uintptr(unsafe.Pointer(replaced)),
		uintptr(unsafe.Pointer(replacement)),
		0,
		replaceFileWriteThrough,
		0,
		0,
	)
	if r1 == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return syscall.EINVAL
	}
	return nil
}

func moveFileThrough(tmpPath, path string) error {
	from, err := windows.UTF16PtrFromString(tmpPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
