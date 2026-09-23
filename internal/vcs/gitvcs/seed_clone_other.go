//go:build !darwin

package gitvcs

import "os"

// No byte-copy fallback: unsupported platforms leave the selected cache cold.
func cloneSeedFile(_, _ *os.Root, _ string, _ os.FileInfo) (bool, error) {
	return false, nil
}
