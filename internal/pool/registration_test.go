package pool

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/vcs"
)

// A checkout interrupted after Git removed its registration can retain a .git
// file that later names a different, live slot's HEAD and index.
func TestAbandonedRegistrationCannotModifyLiveSlot(t *testing.T) {
	for _, stateKind := range []string{"missing", "corrupt", "recorded"} {
		t.Run(stateKind, func(t *testing.T) {
			repo, livePool := setupLocalRepo(t)
			live, err := AcquireLease(repo, livePool, 1, nil, "live")
			if err != nil {
				t.Fatal(err)
			}
			marker, err := os.ReadFile(filepath.Join(live, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			poolDir := filepath.Join(filepath.Dir(livePool), "abandoned-pool")
			leftover := filepath.Join(poolDir, "1", filepath.Base(repo))
			if err := os.MkdirAll(leftover, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(leftover, ".git"), marker, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(leftover, "README.md"), []byte("hi\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			switch stateKind {
			case "corrupt":
				if err := os.WriteFile(stateFilePath(poolDir), []byte("{"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "recorded":
				entry := quarantineEntry("1", leftover, "")
				entry.LeaseHolder = acquisitionIncompleteLeaseHolder
				if err := WriteState(poolDir, State{Worktrees: []WorktreeEntry{entry}}); err != nil {
					t.Fatal(err)
				}
			}
			runGit(t, live, "commit", "--allow-empty", "-m", "live work")
			if err := os.WriteFile(filepath.Join(live, "README.md"), []byte("staged live work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, live, "add", "README.md")
			gitdir := strings.TrimSpace(strings.TrimPrefix(string(marker), "gitdir: "))
			beforeHead, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
			if err != nil {
				t.Fatal(err)
			}
			beforeIndex, err := os.ReadFile(filepath.Join(gitdir, "index"))
			if err != nil {
				t.Fatal(err)
			}
			statuses, err := List(poolDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(statuses) != 1 || statuses[0].Status != StatusDamaged || !strings.Contains(statuses[0].BranchErr, "registration") {
				t.Errorf("abandoned registration must be damaged with a diagnostic: %+v", statuses)
			}
			// A named return may clear the reservation, but must not reset the
			// registration that belongs to the live slot in the other pool.
			_ = Release(poolDir, leftover)
			for _, dryRun := range []bool{true, false} {
				result, err := PrunePoolWithOptions(poolDir, PruneOptions{DryRun: dryRun, PruneOrphans: true})
				if err != nil || len(result.Candidates) != 0 || len(result.Pruned) != 0 {
					t.Fatalf("prune accepted another slot's live registration: %+v, %v", result, err)
				}
				if _, err := os.Stat(leftover); err != nil {
					t.Fatalf("prune removed an unverifiable slot: %v", err)
				}
			}
			if err := vcs.ResetWorktree(leftover, "main"); err == nil {
				t.Error("direct reset accepted another slot's registration")
			}
			for name, before := range map[string][]byte{"HEAD": beforeHead, "index": beforeIndex} {
				after, err := os.ReadFile(filepath.Join(gitdir, name))
				if err != nil || !bytes.Equal(before, after) {
					t.Errorf("live slot's %s changed through abandoned slot: %v", name, err)
				}
			}
			assertFileContents(t, filepath.Join(live, "README.md"), "staged live work\n")
		})
	}
}

func TestIncompleteAcquisitionRequiresNamedReturn(t *testing.T) {
	for _, action := range []string{"validate", "release"} {
		t.Run(action, func(t *testing.T) {
			repo, poolDir := setupLocalRepo(t)
			path, err := Acquire(repo, poolDir, 1, nil)
			if err != nil {
				t.Fatal(err)
			}
			state, err := ReadState(poolDir)
			if err != nil {
				t.Fatal(err)
			}
			state.Worktrees[0].Leased = true
			state.Worktrees[0].LeaseHolder = acquisitionIncompleteLeaseHolder
			setSeedInventory(&state.Worktrees[0], nil, false)
			if err := WriteState(poolDir, state); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(path, "unfinished.txt")
			if err := os.WriteFile(marker, []byte("unfinished\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			called := false
			guarded := func() error { called = true; return nil }
			preconditions := ReleasePreconditions{RefuseRecovered: true}
			if action == "validate" {
				err = ValidateReleasePreconditions(poolDir, path, preconditions, guarded)
			} else {
				err = ReleaseConditional(poolDir, path, "", preconditions, guarded)
			}
			if !errors.Is(err, ErrRecoveredEntry) || called {
				t.Errorf("bulk %s = %v, guarded action called=%t", action, err, called)
			}
			state, err = ReadState(poolDir)
			if err != nil || !state.Worktrees[0].Leased || state.Worktrees[0].LeaseHolder != acquisitionIncompleteLeaseHolder || state.Worktrees[0].SeedInventoryKnown {
				t.Errorf("bulk return changed incomplete state: %+v, %v", state, err)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "unfinished\n" {
				t.Errorf("bulk return reset incomplete slot: %q, %v", data, err)
			}
			if err := Release(poolDir, path); err != nil {
				t.Fatalf("named return: %v", err)
			}
			state, err = ReadState(poolDir)
			if err != nil || state.Worktrees[0].Leased || !state.Worktrees[0].SeedInventoryKnown {
				t.Fatalf("named return did not release slot: %+v, %v", state, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("named return did not reset slot: %v", err)
			}
		})
	}
}

func TestAcquireRecordsIncompleteBeforeWorktreeAdd(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	// A failing post-checkout hook observes the state while worktree add is
	// still running, then leaves a real checkout behind on Git's error path.
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("post-checkout shell unavailable")
	}
	snapshot := filepath.Join(t.TempDir(), "during-add.json")
	hook := "#!/bin/sh\ncat '" + filepath.ToSlash(stateFilePath(poolDir)) + "' > '" + filepath.ToSlash(snapshot) + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(repo, poolDir, 1, nil); err == nil {
		t.Fatal("expected checkout hook failure")
	}
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var during State
	if err := json.Unmarshal(data, &during); err != nil {
		t.Fatalf("invalid pre-checkout state: %v, %s", err, data)
	}
	if len(during.Worktrees) != 1 || !during.Worktrees[0].Leased || during.Worktrees[0].LeaseHolder != acquisitionIncompleteLeaseHolder || during.Worktrees[0].SeedInventoryKnown {
		t.Fatalf("no incomplete acquisition with unknown seeds before worktree add: %+v", during)
	}
	state, err := ReadState(poolDir)
	if err != nil || len(state.Worktrees) != 1 || state.Worktrees[0].LeaseHolder != acquisitionIncompleteLeaseHolder || !state.Worktrees[0].Leased {
		t.Fatalf("failed add lost its incomplete acquisition: %+v, %v", state, err)
	}
}
