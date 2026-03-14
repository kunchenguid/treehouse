//go:build !darwin

package updater

// removeQuarantine is a no-op on non-macOS platforms.
func removeQuarantine(_ string) {}
