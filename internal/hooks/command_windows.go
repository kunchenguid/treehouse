//go:build windows

package hooks

import (
	"os"
	"os/exec"
	"syscall"
)

func newHookCommand(command string) *exec.Cmd {
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}

	cmd := exec.Command(shell)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: windowsShellCommandLine(shell, command)}
	return cmd
}
