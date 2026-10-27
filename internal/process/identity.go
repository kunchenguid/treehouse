package process

import (
	"errors"
	"fmt"
	"os"
)

// ErrNoProcess reports that no process holds the PID at all.
var ErrNoProcess = errors.New("no such process")

// ErrIdentityUnsupported reports a platform where a process identity cannot be
// established safely.
var ErrIdentityUnsupported = errors.New("process identity is not supported on this platform")

// Liveness is what can be proven about a recorded process identity.
type Liveness int

const (
	// LivenessUnknown means nothing was proven either way: no identity was
	// recorded, or the process table could not be read.
	LivenessUnknown Liveness = iota
	// LivenessAlive means the PID is still held by the recorded process.
	LivenessAlive
	// LivenessGone means the recorded process has exited: the PID is free,
	// or a process with a different start now holds it.
	LivenessGone
)

func (l Liveness) String() string {
	switch l {
	case LivenessAlive:
		return "alive"
	case LivenessGone:
		return "gone"
	default:
		return "unknown"
	}
}

// IdentityLiveness judges a process identity recorded as a PID plus its
// StartStamp. Only a free PID, or one whose current start differs from the
// recorded one, proves the recorded process gone; a missing identity or any
// read failure is unknown, never gone.
func IdentityLiveness(pid int32, stamp int64) Liveness {
	if pid <= 0 || stamp == 0 {
		return LivenessUnknown
	}
	current, err := StartStamp(pid)
	switch {
	case errors.Is(err, ErrNoProcess):
		return LivenessGone
	case err != nil:
		return LivenessUnknown
	case current == stamp:
		return LivenessAlive
	default:
		return LivenessGone
	}
}

// VerifyHolder checks that pid may be recorded as a lease holder and returns
// its StartStamp. The holder must be one of the calling process's ancestors,
// never the caller itself: a treehouse command exits as soon as it prints, so
// recording it would hand the slot back before anyone used it, and requiring
// ancestry means a PID that was already recycled to an unrelated process is
// refused rather than recorded. The start is read on both sides of the
// ancestry walk, so a holder replaced during the walk is refused too.
func VerifyHolder(pid int32) (int64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("invalid lease holder PID %d", pid)
	}
	if pid == int32(os.Getpid()) {
		return 0, fmt.Errorf("lease holder PID %d is this treehouse command, which exits immediately; name the long-lived process that keeps the worktree, such as your shell or agent", pid)
	}
	before, err := StartStamp(pid)
	if err != nil {
		return 0, fmt.Errorf("cannot identify lease holder process %d: %w", pid, err)
	}
	ancestor, err := isAncestor(pid)
	if err != nil {
		return 0, fmt.Errorf("cannot verify that lease holder process %d started this command: %w", pid, err)
	}
	if !ancestor {
		return 0, fmt.Errorf("lease holder process %d is not an ancestor of this treehouse command; name a process that started it, such as your shell ($$) or the agent that runs it", pid)
	}
	after, err := StartStamp(pid)
	if err != nil || after != before {
		return 0, fmt.Errorf("lease holder process %d changed while it was being verified", pid)
	}
	return before, nil
}

// isAncestor reports whether pid is one of the calling process's ancestors,
// using the same ancestry walk that protects them from termination.
func isAncestor(pid int32) (bool, error) {
	unprotected, err := filterProtectedProcesses([]ProcessInfo{{PID: pid}}, int32(os.Getpid()), parentPID)
	if err != nil {
		return false, err
	}
	return len(unprotected) == 0, nil
}
