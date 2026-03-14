//go:build darwin

package updater

import "os/exec"

// removeQuarantine removes the macOS quarantine extended attribute.
// Best-effort: errors are silently ignored.
func removeQuarantine(path string) {
	exec.Command("xattr", "-d", "com.apple.quarantine", path).Run()
}
