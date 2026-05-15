//go:build !windows

package hooks

import "os/exec"

func newHookCommand(command string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", command)
}
