//go:build !darwin

package fileclone

import "context"

const Supported = false

func FilesystemReason(_, _ string) string { return "requires macOS APFS" }

// Share does not inspect paths on unsupported platforms.
func Share(_ context.Context, _, _ string, _ []string) (Report, error) {
	return Report{Reason: "requires macOS APFS"}, nil
}
