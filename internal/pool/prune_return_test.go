package pool

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/vcs"
)

// landedSlotFixture is a pool slot whose owner exited without returning it,
// leaving a feature branch checked out next to the ignored state a real slot
// accumulates: dependencies, incremental build output, and a per-slot
// database, plus one ignored file treehouse itself seeded.
type landedSlotFixture struct {
	repoDir string
	poolDir string
	wtPath  string
}

const (
	pruneReturnDependency = "node_modules/dep/index.js"
	pruneReturnBuildInfo  = "tsconfig.tsbuildinfo"
	pruneReturnDatabase   = "data/slot.db"
	pruneReturnSeed       = "local.env"
)

// newWorkedSlot acquires a slot from a repository that ignores the usual
// reusable state, writes that state, commits feature work on a branch, and
// clears the owner reservation the way a closed terminal does.
func newWorkedSlot(t *testing.T) landedSlotFixture {
	t.Helper()
	repoDir, poolDir := setupRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte("node_modules/\n*.tsbuildinfo\ndata/\n*.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".worktreeinclude"), []byte(pruneReturnSeed+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, pruneReturnSeed), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", ".gitignore", ".worktreeinclude")
	runGit(t, repoDir, "commit", "-m", "ignore reusable state")
	runGit(t, repoDir, "push", "origin", "main")

	wtPath, err := Acquire(repoDir, poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	assertFileContents(t, filepath.Join(wtPath, pruneReturnSeed), "SECRET=1\n")
	for name, contents := range map[string]string{
		pruneReturnDependency: "module.exports = 1\n",
		pruneReturnBuildInfo:  "{\"version\":\"5\"}\n",
		pruneReturnDatabase:   "rows\n",
	} {
		path := filepath.Join(wtPath, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runGit(t, wtPath, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "feature.txt")
	runGit(t, wtPath, "commit", "-m", "feature one")
	if err := os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "commit", "-am", "feature two")

	clearOwnerReservation(t, poolDir, wtPath)
	return landedSlotFixture{repoDir: repoDir, poolDir: poolDir, wtPath: wtPath}
}

// advanceMainUnrelated lands an unrelated commit on main, so a squash or
// rebase merge has to be recognized by content rather than ancestry.
func advanceMainUnrelated(t *testing.T, repoDir string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(repoDir, "other.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("other\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "other.txt")
	runGit(t, repoDir, "commit", "-m", "unrelated change")
}

func landByMergeCommit(t *testing.T, f landedSlotFixture) {
	t.Helper()
	advanceMainUnrelated(t, f.repoDir)
	runGit(t, f.repoDir, "merge", "--no-ff", "-m", "merge feature", "feature")
	runGit(t, f.repoDir, "push", "origin", "main")
}

func landBySquash(t *testing.T, f landedSlotFixture) {
	t.Helper()
	advanceMainUnrelated(t, f.repoDir)
	runGit(t, f.repoDir, "merge", "--squash", "feature")
	runGit(t, f.repoDir, "commit", "-m", "squash feature")
	runGit(t, f.repoDir, "push", "origin", "main")
}

// landByRebase replays the feature commits onto main the way a forge's
// "rebase and merge" does, so main carries equal content under new commits.
func landByRebase(t *testing.T, f landedSlotFixture) {
	t.Helper()
	advanceMainUnrelated(t, f.repoDir)
	runGit(t, f.repoDir, "cherry-pick", "main..feature")
	runGit(t, f.repoDir, "push", "origin", "main")
}

func pruneReturn(t *testing.T, f landedSlotFixture, dryRun bool) PruneResult {
	t.Helper()
	result, err := PruneWithOptions(f.repoDir, f.poolDir, PruneOptions{DryRun: dryRun, Return: true})
	if err != nil {
		t.Fatalf("PruneWithOptions(Return) failed: %v", err)
	}
	return result
}

func hasPruneWorktree(worktrees []PruneWorktree, path string) bool {
	for _, wt := range worktrees {
		if wt.Path == path {
			return true
		}
	}
	return false
}

func slotStatus(t *testing.T, poolDir, wtPath string) WorktreeStatus {
	t.Helper()
	statuses, err := List(poolDir)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	for _, st := range statuses {
		if st.Path == wtPath {
			return st
		}
	}
	t.Fatalf("slot %s missing from status: %#v", wtPath, statuses)
	return WorktreeStatus{}
}

func slotEntry(t *testing.T, poolDir, wtPath string) WorktreeEntry {
	t.Helper()
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	for _, wt := range state.Worktrees {
		if wt.Path == wtPath {
			return wt
		}
	}
	t.Fatalf("slot %s missing from state: %#v", wtPath, state.Worktrees)
	return WorktreeEntry{}
}

// assertReturnedToPool checks every observable fact of a returned slot: it is
// still registered and on disk, parked detached on the base, available in
// status, and its reusable ignored state survived while treehouse's own seed
// was cleaned up exactly as 'treehouse return' cleans it.
func assertReturnedToPool(t *testing.T, f landedSlotFixture, result PruneResult) {
	t.Helper()
	if !hasPruneWorktree(result.Returned, f.wtPath) {
		t.Fatalf("expected %s returned, got returned=%#v skipped=%#v", f.wtPath, result.Returned, result.Skipped)
	}
	if len(result.Pruned) != 0 {
		t.Fatalf("return mode must not delete worktrees, got pruned=%#v", result.Pruned)
	}
	if got, want := gitOut(t, f.wtPath, "rev-parse", "HEAD"), gitOut(t, f.repoDir, "rev-parse", "origin/main"); got != want {
		t.Fatalf("returned slot HEAD = %s, want base %s", got, want)
	}
	if _, detached, err := vcs.CheckedOutBranch(f.wtPath); err != nil || !detached {
		t.Fatalf("returned slot must be detached, detached=%v err=%v", detached, err)
	}
	gitOut(t, f.repoDir, "rev-parse", "--verify", "refs/heads/feature")
	assertFileContents(t, filepath.Join(f.wtPath, filepath.FromSlash(pruneReturnDependency)), "module.exports = 1\n")
	assertFileContents(t, filepath.Join(f.wtPath, pruneReturnBuildInfo), "{\"version\":\"5\"}\n")
	assertFileContents(t, filepath.Join(f.wtPath, filepath.FromSlash(pruneReturnDatabase)), "rows\n")
	if _, err := os.Stat(filepath.Join(f.wtPath, pruneReturnSeed)); !os.IsNotExist(err) {
		t.Fatalf("seeded file must be cleaned up like a return, stat err: %v", err)
	}
	if st := slotStatus(t, f.poolDir, f.wtPath); st.Status != StatusAvailable {
		t.Fatalf("returned slot status = %q, want %q", st.Status, StatusAvailable)
	}
	entry := slotEntry(t, f.poolDir, f.wtPath)
	if entry.Leased || entry.OwnerPID != 0 || entry.Destroying || !entry.SeedInventoryKnown || len(entry.SeededPaths) != 0 {
		t.Fatalf("returned slot must be released in state, got %#v", entry)
	}
}

// assertUntouched checks that a slot return mode refused kept its HEAD, its
// branch, and its files.
func assertUntouched(t *testing.T, f landedSlotFixture, result PruneResult, head string) {
	t.Helper()
	if hasPruneWorktree(result.Returned, f.wtPath) || hasPruneWorktree(result.Pruned, f.wtPath) {
		t.Fatalf("slot %s must be left alone, got returned=%#v pruned=%#v", f.wtPath, result.Returned, result.Pruned)
	}
	if got := gitOut(t, f.wtPath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("slot HEAD moved from %s to %s", head, got)
	}
	if branch, _, err := vcs.CheckedOutBranch(f.wtPath); err != nil || branch != "feature" {
		t.Fatalf("slot must keep its branch checked out, got %q err=%v", branch, err)
	}
	assertFileContents(t, filepath.Join(f.wtPath, pruneReturnSeed), "SECRET=1\n")
}

func TestPruneReturn_ReturnsMergeCommitLandedSlot(t *testing.T) {
	f := newWorkedSlot(t)
	landByMergeCommit(t, f)

	assertReturnedToPool(t, f, pruneReturn(t, f, false))
}

func TestPruneReturn_ReturnsSquashMergedSlot(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)

	assertReturnedToPool(t, f, pruneReturn(t, f, false))
}

func TestPruneReturn_ReturnsRebaseMergedSlot(t *testing.T) {
	f := newWorkedSlot(t)
	landByRebase(t, f)

	assertReturnedToPool(t, f, pruneReturn(t, f, false))
}

// A squash-merged slot is provably landed only until the base changes a file
// the branch also changed; after that acquire skips it forever. Returning it
// while the proof holds is what keeps it in service.
func TestPruneReturn_KeepsSquashMergedSlotReusableAfterBaseMovesOn(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	pruneReturn(t, f, false)

	if err := os.WriteFile(filepath.Join(f.repoDir, "feature.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.repoDir, "commit", "-am", "base edits the squashed file")
	runGit(t, f.repoDir, "push", "origin", "main")

	reused, err := Acquire(f.repoDir, f.poolDir, 4, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if reused != f.wtPath {
		t.Fatalf("acquire created %s instead of reusing returned slot %s", reused, f.wtPath)
	}
	assertFileContents(t, filepath.Join(reused, filepath.FromSlash(pruneReturnDependency)), "module.exports = 1\n")
}

// A slot already parked where 'treehouse return' leaves one - detached, on a
// commit the base contains - has nothing to give back, even after the base
// moves on, so a recurring sweep must not report it again.
func TestPruneReturn_IgnoresAlreadyParkedSlot(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	assertReturnedToPool(t, f, pruneReturn(t, f, false))

	for _, dryRun := range []bool{true, false} {
		result := pruneReturn(t, f, dryRun)
		if len(result.Candidates) != 0 || len(result.Returned) != 0 || len(result.Skipped) != 0 {
			t.Fatalf("parked slot must not be listed again (dryRun=%v), got %#v", dryRun, result)
		}
	}

	advanceMainUnrelated(t, f.repoDir)
	runGit(t, f.repoDir, "push", "origin", "main")
	parked := gitOut(t, f.wtPath, "rev-parse", "HEAD")
	result := pruneReturn(t, f, false)
	if len(result.Candidates) != 0 || len(result.Returned) != 0 {
		t.Fatalf("slot parked on an older base commit must not be returned again, got %#v", result)
	}
	if got := gitOut(t, f.wtPath, "rev-parse", "HEAD"); got != parked {
		t.Fatalf("parked slot moved from %s to %s", parked, got)
	}
}

// A detached HEAD holding commits the base got only by squash is not parked:
// its landing proof can still expire, so return mode must take it back.
func TestPruneReturn_ReturnsDetachedSquashLandedSlot(t *testing.T) {
	f := newWorkedSlot(t)
	runGit(t, f.wtPath, "checkout", "--detach")
	landBySquash(t, f)

	result := pruneReturn(t, f, false)
	if !hasPruneWorktree(result.Returned, f.wtPath) {
		t.Fatalf("expected detached squash-landed slot returned, got %#v", result)
	}
}

// A merge-landed branch is permanently reachable from the base, but a slot
// that keeps it checked out still blocks the branch everywhere else.
func TestPruneReturn_ReturnsSlotHoldingMergedBranchWithoutNewCommits(t *testing.T) {
	f := newWorkedSlot(t)
	runGit(t, f.repoDir, "merge", "--ff-only", "feature")
	runGit(t, f.repoDir, "push", "origin", "main")

	result := pruneReturn(t, f, false)
	if !hasPruneWorktree(result.Returned, f.wtPath) {
		t.Fatalf("expected slot holding a merged branch returned, got %#v", result)
	}
	if _, detached, err := vcs.CheckedOutBranch(f.wtPath); err != nil || !detached {
		t.Fatalf("returned slot must release its branch, detached=%v err=%v", detached, err)
	}
}

func TestPruneReturn_DryRunListsCandidateWithoutChangingIt(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	head := gitOut(t, f.wtPath, "rev-parse", "HEAD")

	result := pruneReturn(t, f, true)
	if !hasPruneWorktree(result.Candidates, f.wtPath) {
		t.Fatalf("expected dry-run candidate %s, got %#v", f.wtPath, result)
	}
	if len(result.Returned) != 0 || len(result.Pruned) != 0 {
		t.Fatalf("dry run must not act, got %#v", result)
	}
	assertUntouched(t, f, result, head)
}

func TestPruneReturn_SkipsSlotWithUnlandedCommits(t *testing.T) {
	f := newWorkedSlot(t)
	head := gitOut(t, f.wtPath, "rev-parse", "HEAD")

	result := pruneReturn(t, f, false)
	if !hasSkippedCategory(result.Skipped, f.wtPath, PruneSkipUnmerged) {
		t.Fatalf("expected unmerged skip, got %#v", result.Skipped)
	}
	assertUntouched(t, f, result, head)
}

// A branch can look finished while still holding a commit the base never got.
func TestPruneReturn_SkipsLandedBranchWithExtraLocalCommit(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	if err := os.WriteFile(filepath.Join(f.wtPath, "followup.txt"), []byte("not landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.wtPath, "add", "followup.txt")
	runGit(t, f.wtPath, "commit", "-m", "follow-up after merge")
	head := gitOut(t, f.wtPath, "rev-parse", "HEAD")

	result := pruneReturn(t, f, false)
	if !hasSkippedCategory(result.Skipped, f.wtPath, PruneSkipUnmerged) {
		t.Fatalf("expected unmerged skip, got %#v", result.Skipped)
	}
	assertUntouched(t, f, result, head)
	assertFileContents(t, filepath.Join(f.wtPath, "followup.txt"), "not landed\n")
}

func TestPruneReturn_SkipsDirtySlot(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	runGit(t, f.wtPath, "config", "status.showUntrackedFiles", "no")
	if err := os.WriteFile(filepath.Join(f.wtPath, "notes.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitOut(t, f.wtPath, "rev-parse", "HEAD")

	result := pruneReturn(t, f, false)
	if !hasSkippedCategory(result.Skipped, f.wtPath, PruneSkipUncommitted) {
		t.Fatalf("expected uncommitted skip, got %#v", result.Skipped)
	}
	assertUntouched(t, f, result, head)
	assertFileContents(t, filepath.Join(f.wtPath, "notes.txt"), "keep me\n")
}

func TestPruneReturn_LeavesSlotWithLiveProcess(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	head := gitOut(t, f.wtPath, "rev-parse", "HEAD")
	startCwdHolder(t, f.wtPath)

	assertUntouched(t, f, pruneReturn(t, f, false), head)
}

func TestPruneReturn_LeavesSlotWithLiveOwnerReservation(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	head := gitOut(t, f.wtPath, "rev-parse", "HEAD")
	setOwnerToCaller(t, f.poolDir, f.wtPath)

	assertUntouched(t, f, pruneReturn(t, f, false), head)
	if entry := slotEntry(t, f.poolDir, f.wtPath); entry.OwnerPID != int32(os.Getpid()) {
		t.Fatalf("owner reservation must be kept, got %#v", entry)
	}
}

func TestPruneReturn_LeavesLeasedSlot(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	head := gitOut(t, f.wtPath, "rev-parse", "HEAD")
	lease, err := LeaseExisting(f.poolDir, slotEntry(t, f.poolDir, f.wtPath).Name, "agent")
	if err != nil {
		t.Fatalf("LeaseExisting failed: %v", err)
	}

	assertUntouched(t, f, pruneReturn(t, f, false), head)
	if entry := slotEntry(t, f.poolDir, f.wtPath); !entry.Leased || entry.LeaseID != lease.LeaseID {
		t.Fatalf("lease must be kept, got %#v", entry)
	}
}

// The plan and the reset are separated by the whole planning pass; work that
// lands in the slot in between must stop the reset.
func TestPruneReturn_RechecksSlotThatGainedWorkAfterPlanning(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	options := PruneOptions{Return: true}
	plan, err := planPrunePool(f.poolDir, worktreePruneContextResolver(), options)
	if err != nil {
		t.Fatalf("planPrunePool failed: %v", err)
	}
	if !hasPruneWorktree(plan.Result.Candidates, f.wtPath) {
		t.Fatalf("expected planned candidate, got %#v", plan.Result)
	}

	if err := os.WriteFile(filepath.Join(f.wtPath, "late.txt"), []byte("late work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.wtPath, "add", "late.txt")
	runGit(t, f.wtPath, "commit", "-m", "late work")
	head := gitOut(t, f.wtPath, "rev-parse", "HEAD")

	result, err := executePrune(f.poolDir, plan, options)
	if err != nil {
		t.Fatalf("executePrune failed: %v", err)
	}
	if !hasSkippedCategory(result.Skipped, f.wtPath, PruneSkipUnmerged) {
		t.Fatalf("expected late work to be skipped as unmerged, got %#v", result.Skipped)
	}
	assertUntouched(t, f, result, head)
	assertFileContents(t, filepath.Join(f.wtPath, "late.txt"), "late work\n")
}

// The reset runs after the under-lock landing proof, outside any lock a
// worktree process honors; a commit or an edit that lands in that window must
// make the reset refuse rather than discard work nothing proved landed.
func TestPruneReturn_RefusesResetAfterLateWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		work func(t *testing.T, wtPath string)
		file string
		want string
	}{
		{
			name: "new commit",
			work: func(t *testing.T, wtPath string) {
				if err := os.WriteFile(filepath.Join(wtPath, "late.txt"), []byte("late commit\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				runGit(t, wtPath, "add", "late.txt")
				runGit(t, wtPath, "commit", "-m", "late commit")
			},
			file: "late.txt",
			want: "late commit\n",
		},
		{
			name: "dirtied tracked file",
			work: func(t *testing.T, wtPath string) {
				if err := os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("late edit\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			file: "feature.txt",
			want: "late edit\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkedSlot(t)
			landBySquash(t, f)
			before := slotEntry(t, f.poolDir, f.wtPath)

			var head string
			origReset := resetPruneReturnWorktree
			resetPruneReturnWorktree = func(worktreePath, ref, expectedHead string, requireClean bool, seededPaths []string) error {
				tc.work(t, worktreePath)
				head = gitOut(t, worktreePath, "rev-parse", "HEAD")
				return origReset(worktreePath, ref, expectedHead, requireClean, seededPaths)
			}
			t.Cleanup(func() { resetPruneReturnWorktree = origReset })

			result := pruneReturn(t, f, false)
			if head == "" {
				t.Fatalf("reset was never reached, got %#v", result)
			}
			if !hasSkippedCategory(result.Skipped, f.wtPath, pruneSkipReturnFailed) {
				t.Fatalf("expected return failed skip, got %#v", result.Skipped)
			}
			assertUntouched(t, f, result, head)
			assertFileContents(t, filepath.Join(f.wtPath, tc.file), tc.want)
			assertFileContents(t, filepath.Join(f.wtPath, filepath.FromSlash(pruneReturnDependency)), "module.exports = 1\n")
			if after := slotEntry(t, f.poolDir, f.wtPath); !reflect.DeepEqual(after, before) {
				t.Fatalf("refused slot entry must not be released, before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestPruneReturn_RechecksSlotLeasedAfterPlanning(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	head := gitOut(t, f.wtPath, "rev-parse", "HEAD")
	options := PruneOptions{Return: true}
	plan, err := planPrunePool(f.poolDir, worktreePruneContextResolver(), options)
	if err != nil {
		t.Fatalf("planPrunePool failed: %v", err)
	}
	if _, err := LeaseExisting(f.poolDir, slotEntry(t, f.poolDir, f.wtPath).Name, "agent"); err != nil {
		t.Fatalf("LeaseExisting failed: %v", err)
	}

	result, err := executePrune(f.poolDir, plan, options)
	if err != nil {
		t.Fatalf("executePrune failed: %v", err)
	}
	assertUntouched(t, f, result, head)
	if entry := slotEntry(t, f.poolDir, f.wtPath); !entry.Leased {
		t.Fatalf("lease taken after planning must be kept, got %#v", entry)
	}
}

func TestPruneReturn_ParksOnConfiguredBase(t *testing.T) {
	f := newWorkedSlot(t)
	landBySquash(t, f)
	runGit(t, f.repoDir, "branch", "develop", "main~1")
	runGit(t, f.repoDir, "push", "origin", "develop")
	develop := gitOut(t, f.repoDir, "rev-parse", "develop")

	result, err := PruneWithOptions(f.repoDir, f.poolDir, PruneOptions{
		Return: true,
		ReturnBaseBranch: func(worktreePath string) string {
			if worktreePath != f.wtPath {
				t.Errorf("ReturnBaseBranch asked for %s, want %s", worktreePath, f.wtPath)
			}
			return "develop"
		},
	})
	if err != nil {
		t.Fatalf("PruneWithOptions failed: %v", err)
	}
	if !hasPruneWorktree(result.Returned, f.wtPath) {
		t.Fatalf("expected slot returned, got %#v", result)
	}
	if got := gitOut(t, f.wtPath, "rev-parse", "HEAD"); got != develop {
		t.Fatalf("slot parked on %s, want develop %s", got, develop)
	}
	if entry := slotEntry(t, f.poolDir, f.wtPath); entry.BaseBranch != "develop" {
		t.Fatalf("slot must record the base it parked on, got %q", entry.BaseBranch)
	}
}

func TestPruneReturn_RefusesOrphanDeletion(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	_, err := PruneWithOptions(repoDir, poolDir, PruneOptions{Return: true, PruneOrphans: true})
	if !errors.Is(err, ErrPruneReturnWithOrphans) {
		t.Fatalf("expected ErrPruneReturnWithOrphans, got %v", err)
	}
	_, err = PruneAllWithOptions(filepath.Dir(poolDir), PruneOptions{Return: true, PruneOrphans: true})
	if !errors.Is(err, ErrPruneReturnWithOrphans) {
		t.Fatalf("expected ErrPruneReturnWithOrphans from PruneAll, got %v", err)
	}
}

func setOwnerToCaller(t *testing.T, poolDir, wtPath string) {
	t.Helper()
	if err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		for i := range state.Worktrees {
			if state.Worktrees[i].Path == wtPath {
				if err := reserveOwner(&state.Worktrees[i]); err != nil {
					return err
				}
			}
		}
		return WriteState(poolDir, state)
	}); err != nil {
		t.Fatalf("set owner reservation failed: %v", err)
	}
}
