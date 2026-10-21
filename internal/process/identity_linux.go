package process

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// readStartStamp reads the process start in clock ticks since boot, field 22
// of /proc/<pid>/stat. gopsutil's CreateTime adds the boot time, which Linux
// recomputes from the current wall clock on every read, so it moves whenever
// the clock is stepped; the raw ticks never change for a process's lifetime.
func readStartStamp(pid int32) (int64, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(int(pid)), "stat"))
	if err != nil {
		return 0, err
	}
	return parseStatStartTicks(data)
}

// parseStatStartTicks extracts starttime from a /proc/<pid>/stat line. The
// command name in field 2 may itself contain spaces and parentheses, so fields
// are counted from the last closing parenthesis, after which field 3 begins.
func parseStatStartTicks(data []byte) (int64, error) {
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return 0, fmt.Errorf("malformed /proc stat line")
	}
	fields := bytes.Fields(data[end+1:])
	const startTimeIndex = 22 - 3
	if len(fields) <= startTimeIndex {
		return 0, fmt.Errorf("malformed /proc stat line: %d fields after the command name", len(fields))
	}
	ticks, err := strconv.ParseInt(string(fields[startTimeIndex]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("malformed /proc stat start time: %w", err)
	}
	return ticks, nil
}
