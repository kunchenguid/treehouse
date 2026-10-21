//go:build !windows

package config

import (
	"os"
	"syscall"
)

// openApprovalFile opens path for reading under root. O_NONBLOCK keeps the open
// from waiting for a writer when the entry became a FIFO or a device after the
// precheck; the caller rejects anything that is not a regular file before
// reading. Rooted opens keep in-repository relative symlinks working while
// refusing traversal outside the repository.
func openApprovalFile(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
