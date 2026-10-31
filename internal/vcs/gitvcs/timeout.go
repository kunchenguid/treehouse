package gitvcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// GitTimeoutEnvVar bounds how long any single git subprocess may run. The
// value is a Go duration ("90s", "45m"); "0" turns the limit off. An unset
// value uses defaultGitTimeout; an unparsable or negative one warns on stderr
// and uses it.
const GitTimeoutEnvVar = "TREEHOUSE_GIT_TIMEOUT"

// defaultGitTimeout is long enough for a large checkout, an LFS download, or a
// slow hook, and short enough that a git process stuck on a stale lock or a credential prompt fails
// loudly instead of hanging get or return forever.
const defaultGitTimeout = 30 * time.Minute

// gitTimeoutWarnings receives the unparsable-value warning.
var gitTimeoutWarnings io.Writer = os.Stderr

// gitWaitDelay caps how long Wait drains copy goroutines (such as the stdin
// copier) after the deadline kills git. It is not the bound on a hook child
// that inherited an output pipe: Output and CombinedOutput hand git *os.File
// pipes and bound their reads by the deadline themselves (see collect).
var gitWaitDelay = 5 * time.Second

// gitTimeout is read once per process so an unparsable value warns once
// rather than on every git subprocess. Tests that change the environment
// call resetGitTimeout.
var gitTimeout = sync.OnceValue(parseGitTimeout)

func resetGitTimeout() { gitTimeout = sync.OnceValue(parseGitTimeout) }

func parseGitTimeout() time.Duration {
	value := strings.TrimSpace(os.Getenv(GitTimeoutEnvVar))
	if value == "" {
		return defaultGitTimeout
	}
	if value == "0" {
		return 0
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		fmt.Fprintf(gitTimeoutWarnings, "treehouse: ignoring %s=%q (want a duration like 90s or 45m, or 0 to disable); using %s\n", GitTimeoutEnvVar, value, defaultGitTimeout)
		return defaultGitTimeout
	}
	return d
}

// gitCmd is a git subprocess bounded by the configured timeout. Callers must
// call cancel when done and pass the run error to deadlineErr before
// interpreting an exit status: a killed git reports an exit code that would
// otherwise read as an ordinary failure.
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
	cmd.WaitDelay = timeout
	cmd.Cancel = func() error {
		cmd.WaitDelay = gitWaitDelay
		return cmd.Process.Kill()
	}
	if dir != "" {
		cmd.Dir = dir
	}
	return &gitCmd{Cmd: cmd, ctx: ctx, cancel: cancel, args: args, timeout: timeout}
}

func (c *gitCmd) Output() ([]byte, error)         { return c.collect(false) }
func (c *gitCmd) CombinedOutput() ([]byte, error) { return c.collect(true) }

// collect waits until git's output pipes close or the configured deadline
// passes, whichever comes first. The pipes are *os.File values so Wait
// returns when git itself exits; a child that inherited a pipe cannot end
// the wait early or extend it past the deadline. A disabled timeout has no
// deadline and waits until the pipes close.
func (c *gitCmd) collect(combined bool) ([]byte, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer pr.Close()
	er, ew := pr, pw
	if !combined {
		if er, ew, err = os.Pipe(); err != nil {
			pw.Close()
			return nil, err
		}
		defer er.Close()
	}
	c.Stdout, c.Stderr = pw, ew
	if err = c.Start(); err != nil {
		pw.Close()
		if ew != pw {
			ew.Close()
		}
		return nil, err
	}
	pw.Close()
	if ew != pw {
		ew.Close()
	}
	done := make(chan struct {
		b   []byte
		err error
	}, 1)
	go func() {
		b, se, e1, e2 := readBoth(pr, er)
		err := c.Wait()
		if ee, ok := err.(*exec.ExitError); ok && er != pr {
			ee.Stderr = se
		} else if err == nil {
			err = errors.Join(e1, e2)
		}
		done <- struct {
			b   []byte
			err error
		}{b, err}
	}()
	select {
	case r := <-done:
		return r.b, r.err
	case <-c.ctx.Done():
	}
	if c.Process != nil {
		_ = c.Process.Kill()
	}
	pr.Close()
	if er != pr {
		er.Close()
	}
	r := <-done
	if r.err == nil {
		return r.b, nil
	}
	return nil, c.ctx.Err()
}

func readBoth(out, errPipe *os.File) (stdout, stderr []byte, err1, err2 error) {
	if errPipe == out {
		stdout, err1 = io.ReadAll(out)
		return stdout, nil, err1, nil
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); stdout, err1 = io.ReadAll(out) }()
	go func() {
		defer wg.Done()
		saved := &prefixSuffixSaver{n: stderrSaveLimit}
		_, err2 = io.Copy(saved, errPipe)
		stderr = saved.Bytes()
	}()
	wg.Wait()
	return stdout, stderr, err1, err2
}

// stderrSaveLimit bounds the stderr kept from each end, matching what
// exec.Cmd.Output keeps for ExitError.Stderr, so a noisy hook cannot grow
// memory while its pipe is still drained.
const stderrSaveLimit = 32 << 10

// prefixSuffixSaver keeps the first and last n bytes written to it and
// counts what it drops in between.
type prefixSuffixSaver struct {
	n       int
	prefix  []byte
	suffix  []byte
	skipped int64
}

func (w *prefixSuffixSaver) Write(p []byte) (int, error) {
	total := len(p)
	if room := w.n - len(w.prefix); room > 0 {
		take := min(room, len(p))
		w.prefix = append(w.prefix, p[:take]...)
		p = p[take:]
	}
	if len(p) > w.n {
		w.skipped += int64(len(w.suffix) + len(p) - w.n)
		w.suffix = append(w.suffix[:0], p[len(p)-w.n:]...)
		return total, nil
	}
	if over := len(w.suffix) + len(p) - w.n; over > 0 {
		w.skipped += int64(over)
		w.suffix = append(w.suffix[:0], w.suffix[over:]...)
	}
	w.suffix = append(w.suffix, p...)
	return total, nil
}

func (w *prefixSuffixSaver) Bytes() []byte {
	if w.skipped == 0 {
		return append(w.prefix, w.suffix...)
	}
	b := append([]byte(nil), w.prefix...)
	b = fmt.Appendf(b, "\n... omitting %d bytes ...\n", w.skipped)
	return append(b, w.suffix...)
}

// ErrGitTimeout marks a git subprocess that was killed at the deadline.
var ErrGitTimeout = errors.New("git command timed out")

// deadlineErr returns a diagnostic error when the deadline killed git, and nil
// otherwise. A command that returned no error finished in time, even if the
// deadline passed just afterwards.
func (c *gitCmd) deadlineErr(runErr error) error {
	if runErr == nil || !errors.Is(c.ctx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return fmt.Errorf("%w: git %s did not finish within %s; another git process or a stale lock may be holding the repository (check `git rev-parse --git-path index.lock`), or set %s to change the limit",
		ErrGitTimeout, strings.Join(c.args, " "), c.timeout, GitTimeoutEnvVar)
}
