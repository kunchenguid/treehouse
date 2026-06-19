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

func Exists(pid int32) bool {
	exists, err := process.PidExists(pid)
	return err == nil && exists
}

func StartedAt(pid int32) (int64, bool) {
	proc, err := process.NewProcess(pid)
	if err != nil {
		return 0, false
	}
	startedAt, err := proc.CreateTime()
	return startedAt, err == nil
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
	absWorktree = resolvePath(absWorktree)

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
		absCwd = resolvePath(absCwd)

		rel, err := filepath.Rel(absWorktree, absCwd)
		if err != nil {
			continue
		}

		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			name, _ := p.Name()
			result = append(result, ProcessInfo{
				PID:  p.Pid,
				Name: name,
			})
		}
	}

	return result, nil
}

// resolvePath returns the symlink-resolved path, or the input if resolution
// fails (e.g. path doesn't exist). This lets us match process cwds (which
// gopsutil returns canonicalized, e.g. /private/var/... on macOS) against
// caller-supplied worktree paths that may still contain symlinks.
func resolvePath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
