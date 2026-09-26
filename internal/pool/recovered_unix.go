//go:build !windows

package pool

import (
	"fmt"
	"os"
	"syscall"
)

// ensureOwnerOnlyDir keeps a recovery backup as private as the owner-only
// folders recovery creates: a broader folder of the current user's is tightened
// to 0700, and any other owner's folder is refused.
func ensureOwnerOnlyDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is not owned by the current user", dir)
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("cannot restrict %s to owner-only access: %w", dir, err)
	}
	return nil
}
