package cmd

import (
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/pool"
)

// TestDestroySkipHintFramesOtherFlavorAsMigration pins the skip wording: an
// other-flavor worktree whose only unlanded class is unverified holds work
// nothing proved unlanded, so the hint frames --include-unlanded as the pool
// migration step. A genuinely dirty other-flavor worktree keeps the plain
// data-loss hint.
func TestDestroySkipHintFramesOtherFlavorAsMigration(t *testing.T) {
	skip := pool.DestroySkip{
		Target: pool.DestroyTarget{
			Name:        "1",
			Flavor:      "git",
			OtherFlavor: true,
			Classes:     []pool.DestroyClass{pool.DestroyUnverified},
		},
		NeededFlag:  pool.IncludeUnlandedFlag,
		NeededFlags: []string{pool.IncludeUnlandedFlag},
	}
	hint := destroySkipHint(skip)
	if !strings.Contains(hint, "migrate") || !strings.Contains(hint, pool.IncludeUnlandedFlag) {
		t.Fatalf("expected a migration-framed hint naming the flag, got %q", hint)
	}

	skip.Target.Classes = []pool.DestroyClass{pool.DestroyDirty}
	if hint := destroySkipHint(skip); strings.Contains(hint, "migrate") {
		t.Fatalf("a dirty worktree must keep the data-loss hint, got %q", hint)
	}

	skip.Target.Classes = []pool.DestroyClass{pool.DestroyLeased, pool.DestroyUnverified}
	if hint := destroySkipHint(skip); strings.Contains(hint, "migrate") {
		t.Fatalf("a leased worktree must keep the protective hint, got %q", hint)
	}

	skip.Target.Classes = []pool.DestroyClass{pool.DestroyInUse, pool.DestroyUnverified}
	if hint := destroySkipHint(skip); strings.Contains(hint, "migrate") {
		t.Fatalf("an in-use worktree must keep the protective hint, got %q", hint)
	}

	skip.Target.Classes = []pool.DestroyClass{pool.DestroyUnverified}
	skip.Target.OtherFlavor = false
	if hint := destroySkipHint(skip); strings.Contains(hint, "migrate") {
		t.Fatalf("a same-flavor worktree must keep the plain hint, got %q", hint)
	}
}

// TestDestroySingleExitFramesOtherFlavorAsMigration pins the single-target
// error to the same migration framing as the bulk skip hint, so both surfaces
// give consistent guidance for an unverified other-flavor slot.
func TestDestroySingleExitFramesOtherFlavorAsMigration(t *testing.T) {
	skip := pool.DestroySkip{
		Target: pool.DestroyTarget{
			Name:        "1",
			Flavor:      "git",
			OtherFlavor: true,
			Class:       pool.DestroyUnverified,
			Classes:     []pool.DestroyClass{pool.DestroyUnverified},
		},
		NeededFlag:  pool.IncludeUnlandedFlag,
		NeededFlags: []string{pool.IncludeUnlandedFlag},
	}
	result := pool.DestroyResult{Skipped: []pool.DestroySkip{skip}}

	err := destroySingleExit(result, pool.DestroyOptions{})
	if err == nil {
		t.Fatal("expected a non-nil error for a skipped single target")
	}
	if !strings.Contains(err.Error(), "migrate") || !strings.Contains(err.Error(), pool.IncludeUnlandedFlag) {
		t.Fatalf("expected a migration-framed error naming the flag, got %q", err)
	}

	result.Skipped[0].Target.OtherFlavor = false
	err = destroySingleExit(result, pool.DestroyOptions{})
	if err == nil || strings.Contains(err.Error(), "migrate") {
		t.Fatalf("a same-flavor unverified worktree must keep the plain error, got %v", err)
	}
	if !strings.Contains(err.Error(), pool.IncludeUnlandedFlag) {
		t.Fatalf("the plain error must still name the flag, got %v", err)
	}

	result.Skipped[0].Target.OtherFlavor = true
	result.Skipped[0].Target.Class = pool.DestroyLeased
	result.Skipped[0].Target.Classes = []pool.DestroyClass{pool.DestroyLeased, pool.DestroyUnverified}
	result.Skipped[0].NeededFlags = []string{pool.IncludeLeasedFlag, pool.IncludeUnlandedFlag}
	err = destroySingleExit(result, pool.DestroyOptions{})
	if err == nil || strings.Contains(err.Error(), "migrate") {
		t.Fatalf("a leased other-flavor worktree must keep the protective error, got %v", err)
	}
	if !strings.Contains(err.Error(), string(pool.DestroyLeased)) {
		t.Fatalf("the protective error must name the leased class, got %v", err)
	}

	result.Skipped[0].Target.Class = pool.DestroyInUse
	result.Skipped[0].Target.Classes = []pool.DestroyClass{pool.DestroyInUse, pool.DestroyUnverified}
	result.Skipped[0].NeededFlags = []string{pool.IncludeInUseFlag, pool.IncludeUnlandedFlag}
	err = destroySingleExit(result, pool.DestroyOptions{})
	if err == nil || strings.Contains(err.Error(), "migrate") {
		t.Fatalf("an in-use other-flavor worktree must keep the protective error, got %v", err)
	}
	if !strings.Contains(err.Error(), string(pool.DestroyInUse)) {
		t.Fatalf("the protective error must name the in-use class, got %v", err)
	}
}
