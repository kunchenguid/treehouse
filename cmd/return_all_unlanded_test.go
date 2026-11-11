package cmd

import (
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/pool"
)

func TestReturnableStatus(t *testing.T) {
	for status, want := range map[string]bool{
		pool.StatusAvailable:  false,
		pool.StatusUnlanded:   false,
		pool.StatusDamaged:    false,
		pool.StatusDirty:      true,
		pool.StatusInUse:      true,
		pool.StatusLeased:     true,
		pool.StatusUnverified: true,
		pool.StatusHere:       true,
	} {
		if got := returnableStatus(pool.WorktreeStatus{Status: status}); got != want {
			t.Errorf("returnableStatus(%q) = %v, want %v", status, got, want)
		}
	}
}
