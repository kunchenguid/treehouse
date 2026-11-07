package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDestroyRefusesInvalidRegistration(t *testing.T) {
	for _, kind := range []string{"missing-backlink", "wrong-backlink", "malformed-marker", "unreadable-marker"} {
		for _, action := range []string{"named", "bulk", "remove"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				repo, poolDir := setupRepo(t)
				path := acquireDisposable(t, repo, poolDir)
				marker := filepath.Join(path, ".git")
				registration := t.TempDir()
				data := []byte("gitdir: " + registration + "\n")
				switch kind {
				case "wrong-backlink":
					live, err := AcquireLease(repo, filepath.Join(filepath.Dir(poolDir), "live-pool"), 1, nil, "live")
					if err != nil {
						t.Fatal(err)
					}
					data, err = os.ReadFile(filepath.Join(live, ".git"))
					if err != nil {
						t.Fatal(err)
					}
				case "malformed-marker":
					data = []byte("not a Git registration\n")
				}
				if kind == "unreadable-marker" {
					if err := os.Remove(marker); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(".git", marker); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				} else if err := os.WriteFile(marker, data, 0o644); err != nil {
					t.Fatal(err)
				}
				precious := filepath.Join(path, "unfinished.txt")
				if err := os.WriteFile(precious, []byte("keep this work\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if action == "remove" {
					state, err := ReadState(poolDir)
					if err != nil {
						t.Fatal(err)
					}
					if err := removeManagedWorktree(poolDir, state.Worktrees[0]); err == nil {
						t.Error("removal accepted an invalid registration")
					}
				} else {
					opts := DestroyOptions{IncludeUnlanded: true, IncludeInUse: true, IncludeLeased: true}
					var result DestroyResult
					var err error
					if action == "named" {
						result, err = DestroyWorktree(poolDir, path, opts)
					} else {
						result, err = DestroyPool(poolDir, opts)
					}
					if err != nil {
						t.Fatal(err)
					}
					if len(result.Planned) != 0 || len(result.Destroyed) != 0 || len(result.Skipped) != 1 {
						t.Errorf("invalid registration was not refused before planning: %+v", result)
					} else if result.Skipped[0].NeededFlag != "" || len(result.Skipped[0].NeededFlags) != 0 || !strings.Contains(result.Skipped[0].Target.Detail, "marker") {
						t.Errorf("refusal must preserve the marker diagnostic without offering an opt-in: %+v", result.Skipped[0])
					}
				}
				assertFileContents(t, precious, "keep this work\n")
				state, err := ReadState(poolDir)
				if err != nil || len(state.Worktrees) != 1 || state.Worktrees[0].Path != path || state.Worktrees[0].Destroying {
					t.Fatalf("refused slot lost its state: %+v, %v", state, err)
				}
			})
		}
	}
}

func TestDestroyRechecksInvalidRegistrationAfterHook(t *testing.T) {
	requireUnixShellHooks(t)
	repo, poolDir := setupRepo(t)
	path := acquireDisposable(t, repo, poolDir)
	registration := t.TempDir()
	hook := "echo " + quoteForShell("gitdir: "+registration) + " > " + quoteForShell(filepath.Join(path, ".git"))
	result, err := DestroyWorktree(poolDir, path, DestroyOptions{IncludeUnlanded: true, PreDestroy: []string{hook}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Destroyed) != 0 || len(result.Skipped) != 1 || result.Skipped[0].NeededFlag != "" {
		t.Fatalf("registration changed during hook was not refused: %+v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("hook's invalid registration caused deletion: %v", err)
	}
	state, err := ReadState(poolDir)
	if err != nil || len(state.Worktrees) != 1 || state.Worktrees[0].Destroying {
		t.Fatalf("refused slot lost its state or reservation was not restored: %+v, %v", state, err)
	}
}
