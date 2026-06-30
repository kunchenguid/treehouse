package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListNavigationWorktreesAcrossPools(t *testing.T) {
	poolRoot := t.TempDir()
	poolA := filepath.Join(poolRoot, "repo-a")
	poolB := filepath.Join(poolRoot, "repo-b")
	wtA := filepath.Join(poolA, "tree-1")
	wtB := filepath.Join(poolB, "tree-2")
	for _, dir := range []string{wtA, wtB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteState(poolA, State{Worktrees: []WorktreeEntry{{Name: "t1", Path: wtA}}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteState(poolB, State{Worktrees: []WorktreeEntry{{Name: "t2", Path: wtB, Leased: true, LeaseHolder: "agent"}}}); err != nil {
		t.Fatal(err)
	}

	worktrees, err := ListNavigationWorktrees(poolRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 2 {
		t.Fatalf("expected 2 worktrees, got %d: %#v", len(worktrees), worktrees)
	}
	if worktrees[0].Path != wtA || worktrees[1].Path != wtB {
		t.Fatalf("expected worktrees sorted by path, got %#v", worktrees)
	}
	if worktrees[1].Status != StatusLeased || worktrees[1].LeaseHolder != "agent" {
		t.Fatalf("expected leased status with holder, got %#v", worktrees[1])
	}
}

func TestResolveNavigationTarget(t *testing.T) {
	root := t.TempDir()
	alpha := filepath.Join(root, "alpha-session")
	beta := filepath.Join(root, "beta-session")
	worktrees := []NavigationWorktree{
		{Name: "t1", Path: alpha},
		{Name: "t2", Path: beta},
	}

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{name: "path", target: alpha, want: alpha},
		{name: "basename", target: "beta-session", want: beta},
		{name: "name", target: "t1", want: alpha},
		{name: "substring", target: "beta", want: beta},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveNavigationTarget(worktrees, tt.target)
			if err != nil {
				t.Fatal(err)
			}
			if got.Path != tt.want {
				t.Fatalf("expected %s, got %#v", tt.want, got)
			}
		})
	}
}

func TestResolveNavigationTargetErrors(t *testing.T) {
	root := t.TempDir()
	worktrees := []NavigationWorktree{
		{Name: "t1", Path: filepath.Join(root, "repo-one")},
		{Name: "t2", Path: filepath.Join(root, "repo-two")},
	}

	_, err := ResolveNavigationTarget(worktrees, "missing")
	if err == nil || !strings.Contains(err.Error(), "no worktree matches") {
		t.Fatalf("expected no-match error, got %v", err)
	}

	_, err = ResolveNavigationTarget(worktrees, "repo")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous error, got %v", err)
	}
}

func TestListNavigationWorktreesEmptyRoot(t *testing.T) {
	worktrees, err := ListNavigationWorktrees(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("expected no worktrees, got %#v", worktrees)
	}
}
