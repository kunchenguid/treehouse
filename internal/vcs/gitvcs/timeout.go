package gitvcs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GitTimeoutEnvVar bounds how long any single git subprocess may run. The
// value is a Go duration ("90s", "5m"); "0" turns the limit off. An unset or
// unparsable value uses defaultGitTimeout.
const GitTimeoutEnvVar = "TREEHOUSE_GIT_TIMEOUT"

// defaultGitTimeout is long enough for a large fetch on a slow link and short
// enough that a git process stuck on a stale lock or a credential prompt fails
// loudly instead of hanging get or return forever.
const defaultGitTimeout = 5 * time.Minute

// gitWaitDelay caps how long Wait blocks on output pipes after the deadline
// kills git, since a hook or credential helper it spawned can keep them open.
var gitWaitDelay = 5 * time.Second

func gitTimeout() time.Duration {
	value := strings.TrimSpace(os.Getenv(GitTimeoutEnvVar))
	if value == "" {
		return defaultGitTimeout
	}
	if value == "0" {
		return 0
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return defaultGitTimeout
	}
	return d
}

// gitCmd is a git subprocess bounded by the configured timeout. Callers must
// call cancel when done and check deadlineErr before interpreting an exit
// status: a killed git reports an exit code that would otherwise read as an
// ordinary failure.
type gitCmd struct {
	*exec.Cmd
	ctx     context.Context
	cancel  context.CancelFunc
	args    []string
	timeout time.Duration
}

func newGitCmd(dir string, args ...string) *gitCmd {
	timeout := gitTimeout()
	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = gitWaitDelay
	if dir != "" {
		cmd.Dir = dir
	}
	return &gitCmd{Cmd: cmd, ctx: ctx, cancel: cancel, args: args, timeout: timeout}
}

// ErrGitTimeout marks a git subprocess that was killed at the deadline.
var ErrGitTimeout = errors.New("git command timed out")

// deadlineErr returns a diagnostic error when the deadline killed git, and nil
// otherwise.
func (c *gitCmd) deadlineErr() error {
	if !errors.Is(c.ctx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return fmt.Errorf("%w: git %s did not finish within %s; another git process or a stale lock may be holding the repository (check `git rev-parse --git-path index.lock`), or set %s to change the limit",
		ErrGitTimeout, strings.Join(c.args, " "), c.timeout, GitTimeoutEnvVar)
}
