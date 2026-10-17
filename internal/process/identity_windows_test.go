package process

import (
	"errors"
	"os"
	"testing"
)

func TestHolderIdentityIsUnsupportedOnWindows(t *testing.T) {
	parent := int32(os.Getppid())
	if _, err := VerifyHolder(parent); !errors.Is(err, ErrIdentityUnsupported) {
		t.Fatalf("VerifyHolder error = %v, want ErrIdentityUnsupported", err)
	}
	if got := IdentityLiveness(parent, 1); got != LivenessUnknown {
		t.Fatalf("liveness = %v, want unknown", got)
	}
}
