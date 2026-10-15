// Package hooks runs user-configured shell commands at worktree lifecycle
// points. Commands run sequentially in a given working directory. A failing
// command is logged but does not stop later commands or fail the caller.
package hooks

import (
	"fmt"
	"io"
	"os/exec"
)

// Event identifies a lifecycle point for a runner that resolves commands and
// checks their approval at execution time.
type Event string

const (
	PostCreate Event = "post_create"
	PostGet    Event = "post_get"
	PostReturn Event = "post_return"
	PreDestroy Event = "pre_destroy"
)

type Runner func(event Event, workDir string, stdout, stderr io.Writer)

// Run uses the runner when supplied, or the static commands for callers which
// do not resolve configuration. Output routing stays owned by the lifecycle.
func (r Runner) Run(event Event, commands []string, workDir string, stdout, stderr io.Writer) {
	if r != nil {
		r(event, workDir, stdout, stderr)
		return
	}
	Run(commands, workDir, stdout, stderr)
}

// Run executes each command in commands sequentially in workDir. Each command
// is passed to the OS shell (/bin/sh -c on Unix, %COMSPEC% /d /s /c on Windows).
// Stdout and stderr from the commands are streamed to the given writers.
// Failures are logged to stderr and do not stop subsequent commands.
func Run(commands []string, workDir string, stdout, stderr io.Writer) {
	for _, command := range commands {
		runOne(command, workDir, stdout, stderr)
	}
}

func runOne(command, workDir string, stdout, stderr io.Writer) {
	cmd := newHookCommand(command)
	cmd.Dir = workDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		exitCode := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "🌳 hook command failed: %q (exit %d): %v\n", command, exitCode, err)
	}
}

func windowsShellCommandLine(shell, command string) string {
	return `"` + shell + `" /d /s /c "` + command + `"`
}
