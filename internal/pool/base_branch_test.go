package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/vcs"
)

// addBranch creates branch at HEAD plus one commit carrying marker, then
// returns to main, so tests can tell which branch a worktree was cut from.
func addBranch(t *testing.T, repoDir, branch, marker string) string {
	t.Helper()
	runGit(t, repoDir, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(repoDir, marker), []byte(marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", marker)
	runGit(t, repoDir, "commit", "-m", "on "+branch)
	tip := gitOut(t, repoDir, "rev-parse", "HEAD")
	runGit(t, repoDir, "checkout", "main")
	return tip
}

func TestAcquire_UsesConfiguredBaseBranch(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	developTip := addBranch(t, repoDir, "develop", "develop-only.txt")

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("AcquireWithOptions failed: %v", err)
	}

	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != developTip {
		t.Errorf("worktree HEAD = %s, want develop tip %s", got, developTip)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "develop-only.txt")); err != nil {
		t.Errorf("expected the worktree to be cut from develop: %v", err)
	}
}

func TestAcquire_WithoutBaseBranchKeepsDefaultBranch(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	addBranch(t, repoDir, "develop", "develop-only.txt")
	mainTip := gitOut(t, repoDir, "rev-parse", "HEAD")

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != mainTip {
		t.Errorf("worktree HEAD = %s, want main tip %s", got, mainTip)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "develop-only.txt")); !os.IsNotExist(err) {
		t.Error("expected the worktree to be cut from main, not develop")
	}
}

func TestAcquire_UnknownBaseBranchFailsClosedWithoutCreatingAWorktree(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	_, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "no-such-branch"})
	if err == nil {
		t.Fatal("expected an unresolvable base branch to fail closed")
	}
	if !strings.Contains(err.Error(), "no-such-branch") {
		t.Errorf("error %q does not name the requested branch", err)
	}

	state, readErr := ReadState(poolDir)
	if readErr == nil && len(state.Worktrees) != 0 {
		t.Errorf("expected no worktree to be created, got %d", len(state.Worktrees))
	}
	if _, err := os.Stat(filepath.Join(poolDir, "1")); err == nil {
		t.Error("expected no worktree directory to be left behind")
	}
}

func TestAcquire_RecyclesExistingSlotOntoNewBaseBranch(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	clearOwnerReservation(t, poolDir, wtPath)

	developTip := addBranch(t, repoDir, "develop", "develop-only.txt")

	reused, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("expected the existing slot to be recycled onto develop: %v", err)
	}
	if reused != wtPath {
		t.Fatalf("expected reuse of slot %s, got %s", wtPath, reused)
	}
	if got := gitOut(t, reused, "rev-parse", "HEAD"); got != developTip {
		t.Errorf("recycled worktree HEAD = %s, want develop tip %s", got, developTip)
	}
}

func TestAcquire_SkipsSlotHoldingWorkNotMergedIntoNewBaseBranch(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "unlanded.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "unlanded.txt")
	runGit(t, wtPath, "commit", "-m", "committed but unlanded")
	head := gitOut(t, wtPath, "rev-parse", "HEAD")
	clearOwnerReservation(t, poolDir, wtPath)

	// develop is a sibling of main and does not contain the slot's commit.
	addBranch(t, repoDir, "develop", "develop-only.txt")

	if _, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"}); err == nil {
		t.Fatal("expected acquire to fail closed rather than reset unlanded work onto the new base")
	}
	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("expected unlanded HEAD %s preserved, got %s", head, got)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "unlanded.txt")); err != nil {
		t.Fatalf("expected unlanded commit preserved on disk: %v", err)
	}
}

func TestAcquire_ResolvesBaseBranchThatExistsOnlyOnOrigin(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	developTip := addBranch(t, repoDir, "develop", "develop-only.txt")
	runGit(t, repoDir, "push", "origin", "develop")
	runGit(t, repoDir, "branch", "-D", "develop")

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("AcquireWithOptions failed for a remote-only base branch: %v", err)
	}
	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != developTip {
		t.Errorf("worktree HEAD = %s, want origin/develop tip %s", got, developTip)
	}
}

func TestAcquireLeaseInfo_ReportsResolvedBaseBranch(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	addBranch(t, repoDir, "develop", "develop-only.txt")

	lease, err := AcquireLeaseInfoWithOptions(repoDir, poolDir, 1, nil, "holder", AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("AcquireLeaseInfoWithOptions failed: %v", err)
	}
	if lease.BaseBranch != "develop" {
		t.Errorf("lease BaseBranch = %q, want develop", lease.BaseBranch)
	}
}

func TestAcquireLeaseInfo_ReportsInferredDefaultBranch(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	lease, err := AcquireLeaseInfo(repoDir, poolDir, 1, nil, "holder")
	if err != nil {
		t.Fatalf("AcquireLeaseInfo failed: %v", err)
	}
	if lease.BaseBranch != "main" {
		t.Errorf("lease BaseBranch = %q, want main", lease.BaseBranch)
	}
}

// Acquire recycles a slot only when its HEAD is merged into the base it resets
// to, so a slot parked off-base is unreusable forever.
func TestRelease_ParksWorktreeOnConfiguredBaseBranch(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	developTip := addBranch(t, repoDir, "develop", "develop-only.txt")

	// main advances past develop, so main's tip is no longer an ancestor of it.
	if err := os.WriteFile(filepath.Join(repoDir, "hotfix.txt"), []byte("hotfix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "hotfix.txt")
	runGit(t, repoDir, "commit", "-m", "hotfix on main")

	first, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if err := ReleaseConditional(poolDir, first, "develop", ReleasePreconditions{}, nil); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	if got := gitOut(t, first, "rev-parse", "HEAD"); got != developTip {
		t.Errorf("returned worktree HEAD = %s, want develop tip %s", got, developTip)
	}

	second, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("second acquire failed: %v", err)
	}
	if second != first {
		t.Errorf("second acquire created a new slot %s instead of recycling %s", second, first)
	}
}

func TestRelease_WithoutBaseBranchParksOnRepositoryDefault(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	addBranch(t, repoDir, "develop", "develop-only.txt")
	mainTip := gitOut(t, repoDir, "rev-parse", "HEAD")

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != mainTip {
		t.Errorf("returned worktree HEAD = %s, want main tip %s", got, mainTip)
	}
}

// A pool with no configured base, driven entirely by --base, must still recycle.
// Parking such a slot on the repository default made every acquire build a new
// slot until max_trees, then report a pool that status showed as available.
func TestRelease_ParksSlotOnTheBaseItWasAcquiredFrom(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	developTip := addBranch(t, repoDir, "develop", "develop-only.txt")
	if err := os.WriteFile(filepath.Join(repoDir, "hotfix.txt"), []byte("hotfix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "hotfix.txt")
	runGit(t, repoDir, "commit", "-m", "hotfix on main")

	var first string
	for i := 0; i < 3; i++ {
		got, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{BaseBranch: "develop"})
		if err != nil {
			t.Fatalf("acquire %d failed: %v", i+1, err)
		}
		if i == 0 {
			first = got
		} else if got != first {
			t.Fatalf("acquire %d created slot %s instead of recycling %s", i+1, got, first)
		}
		// No configured base: the caller passes "" exactly as `treehouse return`
		// does when treehouse.toml has no base_branch.
		if err := ReleaseConditional(poolDir, got, "", ReleasePreconditions{}, nil); err != nil {
			t.Fatalf("release %d failed: %v", i+1, err)
		}
		if head := gitOut(t, got, "rev-parse", "HEAD"); head != developTip {
			t.Fatalf("release %d parked the slot at %s, want develop tip %s", i+1, head, developTip)
		}
	}
}

// An explicitly configured base must not be held hostage by the inference it
// exists to replace: returning has to work even where no default can be found.
func TestRelease_WithBaseBranchSurvivesUnresolvableDefault(t *testing.T) {
	repoDir, poolDir := setupLocalRepo(t)
	developTip := addBranch(t, repoDir, "develop", "develop-only.txt")

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	// Detach the main repository and remove every default-branch signal.
	runGit(t, repoDir, "checkout", "--detach")
	runGit(t, repoDir, "config", "init.defaultBranch", "")
	if _, err := vcs.DefaultBranchForWorktree(wtPath); err == nil {
		t.Skip("default branch is still resolvable in this environment")
	}

	if err := ReleaseConditional(poolDir, wtPath, "develop", ReleasePreconditions{}, nil); err != nil {
		t.Fatalf("release must not fail over an unresolvable default when a base is set: %v", err)
	}
	if head := gitOut(t, wtPath, "rev-parse", "HEAD"); head != developTip {
		t.Errorf("slot parked at %s, want develop tip %s", head, developTip)
	}
}

// The base can vanish between the caller's check and the reset. Returning must
// degrade to the default rather than strand the reservation.
func TestRelease_FallsBackWhenBaseBranchDisappears(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	addBranch(t, repoDir, "develop", "develop-only.txt")
	mainTip := gitOut(t, repoDir, "rev-parse", "HEAD")

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	runGit(t, repoDir, "branch", "-D", "develop")

	if err := ReleaseConditional(poolDir, wtPath, "develop", ReleasePreconditions{}, nil); err != nil {
		t.Fatalf("release must degrade to the default, not fail: %v", err)
	}
	if head := gitOut(t, wtPath, "rev-parse", "HEAD"); head != mainTip {
		t.Errorf("slot parked at %s, want main tip %s", head, mainTip)
	}
	entry, err := FindByPath(poolDir, wtPath)
	if err != nil || entry == nil {
		t.Fatalf("slot missing from state after release: %v", err)
	}
	if entry.Leased || entry.OwnerPID != 0 {
		t.Errorf("reservation not cleared: %+v", entry)
	}
}

// A pool with no configured base_branch parks a --base slot on that base. A
// later plain get must still recycle it: work reachable from the base the slot
// was cut from is as disposable as work reachable from the requested one.
// Without this the slot is unreusable forever and the pool grows to max_trees.
func TestAcquire_RecyclesSlotParkedOnAnotherBase(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	addBranch(t, repoDir, "develop", "develop-only.txt")
	mainTip := gitOut(t, repoDir, "rev-parse", "HEAD")

	first, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if err := ReleaseConditional(poolDir, first, "", ReleasePreconditions{}, nil); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	second, err := Acquire(repoDir, poolDir, 2, nil)
	if err != nil {
		t.Fatalf("plain acquire failed: %v", err)
	}
	if second != first {
		t.Fatalf("plain acquire built a new slot %s instead of recycling %s", second, first)
	}
	if got := gitOut(t, second, "rev-parse", "HEAD"); got != mainTip {
		t.Errorf("recycled worktree HEAD = %s, want main tip %s", got, mainTip)
	}
	if _, err := os.Stat(filepath.Join(second, "develop-only.txt")); !os.IsNotExist(err) {
		t.Error("expected the recycled slot to be reset onto main")
	}
}

// The recorded-base fallback must not become an escape hatch for unlanded work:
// a commit beyond the slot's own base is still unreachable from any base.
func TestAcquire_SkipsSlotHoldingWorkBeyondItsRecordedBase(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	addBranch(t, repoDir, "develop", "develop-only.txt")

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "unlanded.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "unlanded.txt")
	runGit(t, wtPath, "commit", "-m", "committed but unlanded")
	head := gitOut(t, wtPath, "rev-parse", "HEAD")
	clearOwnerReservation(t, poolDir, wtPath)

	if _, err := Acquire(repoDir, poolDir, 1, nil); err == nil {
		t.Fatal("expected acquire to fail closed rather than discard work beyond the recorded base")
	}
	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("expected unlanded HEAD %s preserved, got %s", head, got)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "unlanded.txt")); err != nil {
		t.Fatalf("expected unlanded commit preserved on disk: %v", err)
	}
}

// Prune must judge a slot against the base it is parked on. Against the
// repository default, every pristine develop-based slot reads as unmerged and
// prune becomes a no-op that mislabels clean slots as holding unlanded work.
func TestPrune_TreatsSlotParkedOnItsBaseAsDisposable(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	addBranch(t, repoDir, "develop", "develop-only.txt")

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := ReleaseConditional(poolDir, wtPath, "develop", ReleasePreconditions{}, nil); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	result, err := Prune(repoDir, poolDir, true, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("expected no skips for a slot parked on its own base, got %#v", result.Skipped)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Path != wtPath {
		t.Fatalf("expected prune candidate %s, got %#v", wtPath, result.Candidates)
	}
}

// Prune still refuses a slot carrying commits beyond its own base.
func TestPrune_SkipsSlotHoldingWorkBeyondItsBase(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	addBranch(t, repoDir, "develop", "develop-only.txt")

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	runGit(t, wtPath, "commit", "--allow-empty", "-m", "beyond develop")
	clearOwnerReservation(t, poolDir, wtPath)

	result, err := Prune(repoDir, poolDir, true, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("expected no prune candidate, got %#v", result.Candidates)
	}
	if !hasSkippedCategory(result.Skipped, wtPath, PruneSkipUnmerged) {
		t.Fatalf("expected an unmerged skip, got %#v", result.Skipped)
	}
}

// Destroy shares prune's classification, so it must agree on which base a slot
// belongs to; otherwise `destroy <pool> --all --yes` skips every healthy slot
// asking for --include-unlanded.
func TestDestroyPool_TreatsSlotParkedOnItsBaseAsDisposable(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	addBranch(t, repoDir, "develop", "develop-only.txt")

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := ReleaseConditional(poolDir, wtPath, "develop", ReleasePreconditions{}, nil); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	result, err := DestroyPool(poolDir, DestroyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("DestroyPool failed: %v", err)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("expected no skips for a slot parked on its own base, got %#v", result.Skipped)
	}
	if len(result.Planned) != 1 || result.Planned[0].Path != wtPath {
		t.Fatalf("expected planned destroy of %s, got %#v", wtPath, result.Planned)
	}
}

// Destroy still classifies work beyond the slot's own base as unlanded.
func TestDestroyPool_SkipsSlotHoldingWorkBeyondItsBase(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	addBranch(t, repoDir, "develop", "develop-only.txt")

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	runGit(t, wtPath, "commit", "--allow-empty", "-m", "beyond develop")
	clearOwnerReservation(t, poolDir, wtPath)

	result, err := DestroyPool(poolDir, DestroyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("DestroyPool failed: %v", err)
	}
	if len(result.Planned) != 0 {
		t.Fatalf("expected no planned destroy, got %#v", result.Planned)
	}
	if !hasDestroySkip(result.Skipped, wtPath, DestroyUnmerged, IncludeUnlandedFlag) {
		t.Fatalf("expected an unmerged skip needing --include-unlanded, got %#v", result.Skipped)
	}
}

// clearRecordedBaseBranch rewrites the slot's state entry the way a binary
// predating base_branch wrote it, so upgrade behavior is exercised against a
// real pre-feature state file rather than a hypothetical one.
func clearRecordedBaseBranch(t *testing.T, poolDir, wtPath string) {
	t.Helper()
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	found := false
	for i := range state.Worktrees {
		if state.Worktrees[i].Path == wtPath {
			state.Worktrees[i].BaseBranch = ""
			found = true
		}
	}
	if !found {
		t.Fatalf("worktree %s is not in the pool state", wtPath)
	}
	if err := WriteState(poolDir, state); err != nil {
		t.Fatalf("WriteState failed: %v", err)
	}
}

// Every pool that exists today was written without base_branch, so an empty
// recorded base is the normal upgrade state. Failing closed on it left the
// original wedge reachable for exactly those pools.
func TestAcquire_RecyclesLegacySlotWithNoRecordedBase(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	// The slot must park at a main tip develop does not contain, or it would be
	// recycled by the ordinary requested-base check and never reach the fallback.
	developTip := addBranch(t, repoDir, "develop", "develop-only.txt")
	if err := os.WriteFile(filepath.Join(repoDir, "hotfix.txt"), []byte("hotfix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "hotfix.txt")
	runGit(t, repoDir, "commit", "-m", "hotfix on main")

	wtPath, err := Acquire(repoDir, poolDir, 2, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	clearRecordedBaseBranch(t, poolDir, wtPath)

	reused, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{BaseBranch: "develop"})
	if err != nil {
		t.Fatalf("expected the legacy slot to be recycled onto develop: %v", err)
	}
	if reused != wtPath {
		t.Fatalf("acquire built a new slot %s instead of recycling %s", reused, wtPath)
	}
	if got := gitOut(t, reused, "rev-parse", "HEAD"); got != developTip {
		t.Errorf("recycled worktree HEAD = %s, want develop tip %s", got, developTip)
	}
}

// The implicit default must not become an escape hatch either: a legacy slot
// carrying commits beyond it still holds unlanded work.
func TestAcquire_SkipsLegacySlotHoldingWorkBeyondTheDefault(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "unlanded.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "unlanded.txt")
	runGit(t, wtPath, "commit", "-m", "committed but unlanded")
	head := gitOut(t, wtPath, "rev-parse", "HEAD")
	clearOwnerReservation(t, poolDir, wtPath)
	clearRecordedBaseBranch(t, poolDir, wtPath)

	addBranch(t, repoDir, "develop", "develop-only.txt")

	if _, err := AcquireWithOptions(repoDir, poolDir, 1, nil, AcquireOptions{BaseBranch: "develop"}); err == nil {
		t.Fatal("expected acquire to fail closed rather than discard work beyond the implicit default")
	}
	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("expected unlanded HEAD %s preserved, got %s", head, got)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "unlanded.txt")); err != nil {
		t.Fatalf("expected unlanded commit preserved on disk: %v", err)
	}
}

// A pool that never opted into base_branch must keep the origin-validated
// default ref as its only merge target. Acquire records a base on every slot,
// so consulting it here would start deleting slots parked on an unpushed local
// default for users who never touched this feature.
func TestPrune_SkipsSlotMergedOnlyIntoALocalAheadDefault(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "unpushed.txt"), []byte("unpushed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "unpushed.txt")
	runGit(t, repoDir, "commit", "-m", "unpushed commit on local main")
	localTip := gitOut(t, repoDir, "rev-parse", "HEAD")

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if got := gitOut(t, wtPath, "rev-parse", "HEAD"); got != localTip {
		t.Fatalf("fixture needs the slot cut from the local-ahead main: HEAD = %s, want %s", got, localTip)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	result, err := Prune(repoDir, poolDir, true, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("expected no prune candidate for an unpushed local default, got %#v", result.Candidates)
	}
	if !hasSkippedCategory(result.Skipped, wtPath, PruneSkipUnmerged) {
		t.Fatalf("expected an unmerged skip, got %#v", result.Skipped)
	}
}

func TestDestroyPool_SkipsSlotMergedOnlyIntoALocalAheadDefault(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "unpushed.txt"), []byte("unpushed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "unpushed.txt")
	runGit(t, repoDir, "commit", "-m", "unpushed commit on local main")

	wtPath, err := Acquire(repoDir, poolDir, 1, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	result, err := DestroyPool(poolDir, DestroyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("DestroyPool failed: %v", err)
	}
	if len(result.Planned) != 0 {
		t.Fatalf("expected no planned destroy for an unpushed local default, got %#v", result.Planned)
	}
	if !hasDestroySkip(result.Skipped, wtPath, DestroyUnmerged, IncludeUnlandedFlag) {
		t.Fatalf("expected an unmerged skip needing --include-unlanded, got %#v", result.Skipped)
	}
}

// A pool that never opted into base_branch must keep the deletion semantics it
// had before this feature: merged into the origin-validated default ref, and
// nothing else. Recording an inferred base on every slot would give prune and
// destroy a second reading against a local-ahead branch and widen what they
// delete for users who never touched the option.
func TestPruneSkipsNonOptInSlotMergedOnlyIntoLocalDefault(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	// Unpushed commits on local main: refs/heads/main is ahead of
	// refs/remotes/origin/main, and acquire cuts from the ahead ref.
	if err := os.WriteFile(filepath.Join(repoDir, "unpushed.txt"), []byte("unpushed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "unpushed.txt")
	runGit(t, repoDir, "commit", "-m", "unpushed on local main")

	wtPath, err := Acquire(repoDir, poolDir, 2, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	// The main repo moves off the default, which is what made the old
	// recorded-base-vs-default comparison answer differently.
	runGit(t, repoDir, "checkout", "-b", "feature/x")

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 0 {
		t.Fatalf("a non-opt-in slot merged only into local main must not be pruned, got %#v", result.Pruned)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected the worktree to remain: %v", err)
	}
}

// Same guarantee with no branch switch at all: a plain get taken while the main
// repo sits on a feature branch must not become prunable once the default is
// checked back out. No --base and no base_branch anywhere in this sequence.
func TestPruneSkipsNonOptInSlotAcquiredOffTheDefaultBranch(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	// Without origin/HEAD the default-branch inference falls through to the
	// branch the main repo has checked out, so the slot is cut from feature/x.
	runGit(t, repoDir, "remote", "set-head", "origin", "--delete")
	runGit(t, repoDir, "checkout", "-b", "feature/x")
	if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "feature.txt")
	runGit(t, repoDir, "commit", "-m", "on feature/x")

	// --no-fetch, because a fetch restores the origin/HEAD just deleted.
	wtPath, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{SkipFetch: true})
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if head, tip := gitOut(t, wtPath, "rev-parse", "HEAD"), gitOut(t, repoDir, "rev-parse", "feature/x"); head != tip {
		t.Fatalf("fixture did not cut the slot from feature/x: slot=%s feature/x=%s", head, tip)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	runGit(t, repoDir, "checkout", "main")

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 0 {
		t.Fatalf("a slot inferred off a feature branch must not be pruned, got %#v", result.Pruned)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected the worktree to remain: %v", err)
	}
}

// The same slot must also survive a bulk destroy without --include-unlanded.
func TestDestroyPoolSkipsNonOptInSlotMergedOnlyIntoLocalDefault(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "unpushed.txt"), []byte("unpushed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "unpushed.txt")
	runGit(t, repoDir, "commit", "-m", "unpushed on local main")

	wtPath, err := Acquire(repoDir, poolDir, 2, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	runGit(t, repoDir, "checkout", "-b", "feature/x")

	result, err := DestroyPool(poolDir, DestroyOptions{})
	if err != nil {
		t.Fatalf("DestroyPool failed: %v", err)
	}
	if len(result.Destroyed) != 0 {
		t.Fatalf("a non-opt-in slot merged only into local main must not be destroyed, got %#v", result.Destroyed)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected the worktree to remain: %v", err)
	}
}
