package cmd

import "errors"

// Process exit statuses. A distinct code exists only where a caller has to
// branch on the outcome; everything else is the generic failure. 2 is left
// unused because shells and flag parsers conventionally reserve it for usage
// errors.
const (
	// ExitFailure reports that the command failed.
	ExitFailure = 1

	// ExitNotReturned reports that the worktree, and any lease on it, was
	// left exactly as it was found. Both `treehouse return`'s aborts and
	// `treehouse get`'s exit-time dirty bail-out emit it. It is distinct from
	// ExitFailure because the two demand different responses: a failure is
	// worth retrying, while an unreturned dirty worktree stays unreturned
	// until someone cleans it or passes --force.
	ExitNotReturned = 3

	// ExitNotClean reports that `treehouse return --if-clean` refused because
	// a reset would have destroyed work or interrupted a live process; the
	// worktree and any lease on it were left exactly as found. It is distinct
	// from ExitNotReturned because the blocker is named on stdout and clears
	// on its own once the work lands or the process exits, so a caller may
	// retry later, while --force would destroy exactly what the caller asked
	// treehouse to protect.
	ExitNotClean = 4
)

// exitCodeError pairs an error with the exit status the process should carry.
// Without it a caller can only tell outcomes apart by parsing stderr.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }

func (e *exitCodeError) Unwrap() error { return e.err }

// withExitCode tags err with a process exit status. A nil error stays nil so
// callers can wrap unconditionally.
func withExitCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &exitCodeError{code: code, err: err}
}

// ExitCode maps an error returned by Execute to a process exit status. Errors
// that carry no status are generic failures.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded *exitCodeError
	if errors.As(err, &coded) {
		return coded.code
	}
	return ExitFailure
}
