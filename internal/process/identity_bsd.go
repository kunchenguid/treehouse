//go:build !windows && !linux

package process

import "github.com/shirou/gopsutil/v4/process"

// readStartStamp returns the start time the kernel recorded when the process
// was created (p_starttime on macOS and the BSDs), in milliseconds. It is read
// back verbatim rather than derived from the current clock, so it is stable.
func readStartStamp(pid int32) (int64, error) {
	proc, err := process.NewProcess(pid)
	if err != nil {
		return 0, err
	}
	return proc.CreateTime()
}
