package process

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

type ProcessInfo struct {
	PID  int32
	Name string
}

func (p ProcessInfo) String() string {
	return fmt.Sprintf("%s (%d)", p.Name, p.PID)
}

func IsWorktreeInUse(worktreePath string) (bool, error) {
	procs, err := FindProcessesInWorktree(worktreePath)
	if err != nil {
		return false, err
	}
	return len(procs) > 0, nil
}

func FindProcessesInWorktree(worktreePath string) ([]ProcessInfo, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}

	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return nil, err
	}

	var result []ProcessInfo

	for _, p := range procs {
		cwd, err := p.Cwd()
		if err != nil {
			continue
		}

		absCwd, err := filepath.Abs(cwd)
		if err != nil {
			continue
		}

		rel, err := filepath.Rel(absWorktree, absCwd)
		if err != nil {
			continue
		}

		if !strings.HasPrefix(rel, "..") {
			name, _ := p.Name()
			result = append(result, ProcessInfo{
				PID:  p.Pid,
				Name: name,
			})
		}
	}

	return result, nil
}
