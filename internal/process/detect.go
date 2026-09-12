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

// FindProcessesInWorktree returns processes whose current directory is the
// worktree root or a descendant after absolute path and symlink resolution.
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
		if !cwdWithinWorktree(absWorktree, cwd) {
			continue
		}
		name, _ := p.Name()
		result = append(result, ProcessInfo{
			PID:  p.Pid,
			Name: name,
		})
	}

	return result, nil
}

// WorktreeContainsCwd reports whether a working directory is the worktree root
// or a descendant of it, applying the same absolute-path and symlink
// resolution to both sides that the process scan applies to every process cwd.
// It is the one definition of that containment test: a caller comparing its own
// cwd against a stored worktree path has to resolve symlinks for the same
// reason the scan does, or a pool reached through a symlinked root never
// matches.
func WorktreeContainsCwd(worktreePath, cwd string) bool {
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return false
	}
	return cwdWithinWorktree(resolvePath(absWorktree), cwd)
}

// cwdWithinWorktree reports whether a process working directory falls inside the
// worktree root, which must already be absolute and symlink-resolved. An empty
// cwd never matches: gopsutil returns "" (with no error) for processes whose
// working directory cannot be read - notably Windows system processes such as
// System and csrss.exe - and filepath.Abs("") would otherwise resolve to the
// caller's own directory, wrongly matching every such process whenever the
// caller runs from inside the worktree.
func cwdWithinWorktree(absWorktree, cwd string) bool {
	if cwd == "" {
		return false
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	absCwd = resolvePath(absCwd)

	rel, err := filepath.Rel(absWorktree, absCwd)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
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
