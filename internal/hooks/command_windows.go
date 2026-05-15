//go:build windows

package hooks

import (
	"os"
	"os/exec"
)

func newHookCommand(command string) *exec.Cmd {
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}

	return exec.Command(shell, windowsShellArgs(command)...)
}
