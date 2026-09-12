package process

import (
	"errors"
	"fmt"
	"os"
	"time"

	gopsutilprocess "github.com/shirou/gopsutil/v4/process"
)

// TerminateWorktreeProcesses finds every process whose cwd is within the given
// worktree path and terminates them.
//
// On unix it sends SIGTERM and waits up to gracePeriod for processes to exit.
// It then sends SIGKILL to any survivors and waits up to gracePeriod again for
// them to exit. On windows it uses TerminateProcess.
//
// Returns the list of processes that were targeted. It returns an error when
// process discovery or caller ancestry lookup fails. Individual kill failures
// (e.g. a process already gone) are swallowed.
func TerminateWorktreeProcesses(worktreePath string, gracePeriod time.Duration) ([]ProcessInfo, error) {
	procs, err := FindProcessesInWorktree(worktreePath)
	if err != nil {
		return nil, err
	}
	procs, err = DropProtectedProcesses(procs)
	if err != nil {
		return nil, err
	}
	if len(procs) == 0 {
		return nil, nil
	}

	pids := make([]int32, len(procs))
	for i, p := range procs {
		pids[i] = p.PID
	}

	terminate(pids, gracePeriod)
	return procs, nil
}

// UnprotectedProcessesInWorktree returns processes whose cwd is within the
// worktree, excluding the caller and its ancestors. These are exactly the
// processes TerminateWorktreeProcesses would target, so a non-empty result
// after termination means a foreign live writer remains inside the worktree.
// Callers re-scan with it before resetting a slot so a process that started
// during the grace period, or one an ancestry-lookup failure spared, is not
// mistaken for a quiet worktree.
func UnprotectedProcessesInWorktree(worktreePath string) ([]ProcessInfo, error) {
	procs, err := FindProcessesInWorktree(worktreePath)
	if err != nil {
		return nil, err
	}
	return DropProtectedProcesses(procs)
}

// DropProtectedProcesses removes the caller and its ancestors from an
// already-scanned process list. It is the second half of
// UnprotectedProcessesInWorktree, exposed separately so a caller that must
// distinguish a failed process-table scan from a failed ancestry walk can run
// the two steps itself instead of scanning twice to tell them apart.
func DropProtectedProcesses(procs []ProcessInfo) ([]ProcessInfo, error) {
	return filterProtectedProcesses(procs, int32(os.Getpid()), parentPID)
}

// filterProtectedProcesses drops the caller and its ancestors from procs so
// termination never signals the process running return or its parents. A
// failure walking the ancestry is returned as an error rather than swallowed:
// silently protecting every process would let the caller mistake "the filter
// gave up" for "nothing needed killing" and reset the worktree with live
// writers still inside it. The one benign exception is an ancestor that has
// already exited: it ends the walk (everything below it is protected and its
// own ancestors are gone), which is common on Windows where a parent can exit
// and leave a dangling parent PID.
func filterProtectedProcesses(procs []ProcessInfo, currentPID int32, lookupParent func(int32) (int32, error)) ([]ProcessInfo, error) {
	protected := map[int32]struct{}{
		currentPID: {},
	}

	for pid := currentPID; pid > 0; {
		parent, err := lookupParent(pid)
		if err != nil {
			if errors.Is(err, gopsutilprocess.ErrorProcessNotRunning) {
				break
			}
			return nil, fmt.Errorf("cannot resolve ancestry of process %d: %w", pid, err)
		}
		if parent <= 0 {
			break
		}
		if _, seen := protected[parent]; seen {
			break
		}
		protected[parent] = struct{}{}
		pid = parent
	}

	var filtered []ProcessInfo
	for _, proc := range procs {
		if _, skip := protected[proc.PID]; skip {
			continue
		}
		filtered = append(filtered, proc)
	}
	return filtered, nil
}

func parentPID(pid int32) (int32, error) {
	proc, err := gopsutilprocess.NewProcess(pid)
	if err != nil {
		return 0, err
	}
	return proc.Ppid()
}
