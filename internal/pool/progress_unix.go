//go:build !windows

package pool

import (
	"os"

	"golang.org/x/sys/unix"
)

func progressTerminalWidth(f *os.File) int {
	size, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(size.Col)
}
