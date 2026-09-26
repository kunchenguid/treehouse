//go:build windows

package pool

// ensureOwnerOnlyDir is a no-op on Windows, where access is governed by ACLs
// inherited from the pool's parent folder rather than Unix permission bits.
func ensureOwnerOnlyDir(string) error {
	return nil
}
