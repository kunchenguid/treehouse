//go:build windows

package config

import "os"

// openApprovalFile opens path for reading under root. Windows has no
// O_NONBLOCK, and a rooted relative path cannot reach a named pipe, so a plain
// rooted open cannot block on a replacement; the caller still rejects anything
// that is not a regular file before reading.
func openApprovalFile(root *os.Root, path string) (*os.File, error) {
	return root.Open(path)
}
