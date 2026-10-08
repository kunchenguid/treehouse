//go:build !darwin

package fileclone

import (
	"context"
	"strings"
	"testing"
)

func TestUnsupportedPlatformDoesNotInspectPaths(t *testing.T) {
	r, err := Share(context.Background(), "nonexistent-source", "nonexistent-destination", []string{"asset"})
	if err != nil || r.Cloned != 0 || !strings.Contains(r.Reason, "requires macOS") {
		t.Fatalf("unsupported platform: %+v %v", r, err)
	}
}
