package pool

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDestroyWorktree_RunsPreDestroyHookForEveryRemovedClass pins the pre_destroy
// hook to the removal itself rather than to one classification: the hook runs
// for every worktree destroy actually removes, and for none it only previews.
// executeDestroy runs the hook once per reserved target today, and nothing else
// asserts that a future refactor cannot quietly lose it for, say, the leased or
// dirty path.
func TestDestroyWorktree_RunsPreDestroyHookForEveryRemovedClass(t *testing.T) {
	tests := []struct {
		name      string
		acquire   func(t *testing.T, repoDir, poolDir string) string
		opts      DestroyOptions
		wantHook  bool
		wantGone  bool
		wantCount int
	}{
		{
			name:      "disposable",
			acquire:   acquireDisposable,
			wantHook:  true,
			wantGone:  true,
			wantCount: 1,
		},
		{
			name: "leased with --include-leased",
			acquire: func(t *testing.T, repoDir, poolDir string) string {
				t.Helper()
				wtPath, err := AcquireLease(repoDir, poolDir, 4, nil, "tester")
				if err != nil {
					t.Fatalf("AcquireLease failed: %v", err)
				}
				return wtPath
			},
			opts:      DestroyOptions{IncludeLeased: true},
			wantHook:  true,
			wantGone:  true,
			wantCount: 1,
		},
		{
			name: "dirty with --include-unlanded",
			acquire: func(t *testing.T, repoDir, poolDir string) string {
				t.Helper()
				wtPath := acquireDisposable(t, repoDir, poolDir)
				if err := os.WriteFile(filepath.Join(wtPath, "untracked.txt"), []byte("work\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return wtPath
			},
			opts:      DestroyOptions{IncludeUnlanded: true},
			wantHook:  true,
			wantGone:  true,
			wantCount: 1,
		},
		{
			name:      "dry run removes nothing and runs nothing",
			acquire:   acquireDisposable,
			opts:      DestroyOptions{DryRun: true},
			wantHook:  false,
			wantGone:  false,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir, poolDir := setupRepo(t)
			wtPath := tt.acquire(t, repoDir, poolDir)

			// The sentinel lives outside the worktree so it survives removal.
			sentinel := filepath.Join(t.TempDir(), "predestroy-ran.txt")
			opts := tt.opts
			opts.PreDestroy = []string{"echo ran > " + quoteForShell(sentinel)}

			result, err := DestroyWorktree(poolDir, wtPath, opts)
			if err != nil {
				t.Fatalf("DestroyWorktree failed: %v", err)
			}
			if len(result.Destroyed) != tt.wantCount {
				t.Fatalf("destroyed %d worktrees, want %d: %#v", len(result.Destroyed), tt.wantCount, result)
			}

			_, statErr := os.Stat(sentinel)
			if tt.wantHook && statErr != nil {
				t.Errorf("expected pre_destroy hook to run: %v", statErr)
			}
			if !tt.wantHook && statErr == nil {
				t.Errorf("pre_destroy hook ran for a worktree that was not removed")
			}

			_, wtErr := os.Stat(wtPath)
			if tt.wantGone && !os.IsNotExist(wtErr) {
				t.Errorf("worktree %s still exists after destroy", wtPath)
			}
			if !tt.wantGone && wtErr != nil {
				t.Errorf("worktree %s was removed by a dry run: %v", wtPath, wtErr)
			}
		})
	}
}
