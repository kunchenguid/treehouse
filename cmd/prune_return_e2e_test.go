package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// squashLandedSlot leaves the repository's one slot holding a squash-merged
// feature branch plus an ignored dependency directory, idle and unleased, the
// way a finished agent session that never returned it leaves a slot behind.
func squashLandedSlot(t *testing.T, repoDir, homeDir string) string {
	t.Helper()
	_, getErr, code := runTreehouse(t, repoDir, homeDir, []string{"SHELL=" + exitShellBin}, "get")
	if code != 0 {
		t.Fatalf("get failed (code %d): %s", code, getErr)
	}
	wtPath := extractWorktreePath(getErr, homeDir)
	if wtPath == "" {
		t.Fatal("could not extract worktree path")
	}

	exclude := filepath.Join(repoDir, ".git", "info", "exclude")
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("node_modules/\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wtPath, "node_modules", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "node_modules", "dep", "index.js"), []byte("module.exports = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gitCmd(t, wtPath, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, wtPath, "add", "feature.txt")
	gitCmd(t, wtPath, "commit", "-m", "feature")
	gitCmd(t, repoDir, "merge", "--squash", "feature")
	gitCmd(t, repoDir, "commit", "-m", "squash feature")
	gitCmd(t, repoDir, "push", "origin", "main")
	return wtPath
}

func statusOf(t *testing.T, repoDir, homeDir, wtPath string) string {
	t.Helper()
	out, errOut, code := runTreehouse(t, repoDir, homeDir, nil, "status", "--json")
	if code != 0 {
		t.Fatalf("status --json failed (code %d): %s", code, errOut)
	}
	var statuses []struct {
		Path   string `json:"path"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &statuses); err != nil {
		t.Fatalf("status --json is not a JSON array: %v\n%s", err, out)
	}
	for _, st := range statuses {
		if st.Path == wtPath {
			return st.Status
		}
	}
	t.Fatalf("status --json does not list %s:\n%s", wtPath, out)
	return ""
}

func TestPruneReturnDryRunAndYes(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	wtPath := squashLandedSlot(t, repoDir, homeDir)
	headBefore := gitCmd(t, wtPath, "rev-parse", "HEAD")

	out, errOut, code := runTreehouse(t, repoDir, homeDir, nil, "prune", "--return")
	if code != 0 {
		t.Fatalf("prune --return dry run failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "Dry run: would return 1 landed worktree to the pool.") {
		t.Fatalf("expected dry-run return summary, got stdout:\n%s\nstderr:\n%s", out, errOut)
	}
	if !strings.Contains(out, "~"+wtPath[len(homeDir):]) || !strings.Contains(out, "Re-run with --return --yes to return these worktrees.") {
		t.Fatalf("expected dry run to list the slot and the next command, got:\n%s", out)
	}
	if strings.Contains(out, "reclaim") || strings.Contains(out, "delete") {
		t.Fatalf("return mode must not talk about deleting or reclaiming space, got:\n%s", out)
	}
	if got := gitCmd(t, wtPath, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("dry run moved HEAD from %s to %s", headBefore, got)
	}

	out, errOut, code = runTreehouse(t, repoDir, homeDir, nil, "prune", "--return", "--yes")
	if code != 0 {
		t.Fatalf("prune --return --yes failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "Returned 1 landed worktree to the pool.") {
		t.Fatalf("expected return summary, got stdout:\n%s\nstderr:\n%s", out, errOut)
	}
	if got, want := gitCmd(t, wtPath, "rev-parse", "HEAD"), gitCmd(t, repoDir, "rev-parse", "origin/main"); got != want {
		t.Fatalf("returned slot HEAD = %s, want %s", got, want)
	}
	if data, err := os.ReadFile(filepath.Join(wtPath, "node_modules", "dep", "index.js")); err != nil || string(data) != "module.exports = 1\n" {
		t.Fatalf("ignored dependency must survive the return, got %q err=%v", data, err)
	}
	if got := statusOf(t, repoDir, homeDir, wtPath); got != "available" {
		t.Fatalf("returned slot status = %q, want available", got)
	}

	out, errOut, code = runTreehouse(t, repoDir, homeDir, nil, "prune", "--return", "--yes")
	if code != 0 {
		t.Fatalf("repeated prune --return --yes failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "No landed worktrees returned.") {
		t.Fatalf("a slot already parked must not be returned again, got:\n%s", out)
	}
}

func TestPruneReturnLeavesUnlandedSlotAndSaysWhy(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	wtPath := squashLandedSlot(t, repoDir, homeDir)
	if err := os.WriteFile(filepath.Join(wtPath, "followup.txt"), []byte("not landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, wtPath, "add", "followup.txt")
	gitCmd(t, wtPath, "commit", "-m", "follow-up")
	head := gitCmd(t, wtPath, "rev-parse", "HEAD")

	out, errOut, code := runTreehouse(t, repoDir, homeDir, nil, "prune", "--return", "--yes")
	if code != 0 {
		t.Fatalf("prune --return --yes failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "No landed worktrees returned.") || !strings.Contains(out, "not merged") {
		t.Fatalf("expected an empty result naming the unmerged skip, got:\n%s", out)
	}
	if got := gitCmd(t, wtPath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("unlanded slot HEAD moved from %s to %s", head, got)
	}
}

func TestPruneReturnRejectsPruneOrphans(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	out, errOut, code := runTreehouse(t, repoDir, homeDir, nil, "prune", "--return", "--prune-orphans")
	if code == 0 {
		t.Fatalf("prune --return --prune-orphans must fail, got stdout:\n%s", out)
	}
	if !strings.Contains(errOut, "return") || !strings.Contains(errOut, "prune-orphans") {
		t.Fatalf("expected the error to name both flags, got stderr:\n%s", errOut)
	}
}

func TestPruneAllReturnAcrossPoolsFromAnywhere(t *testing.T) {
	repoA, homeDir := setupTestRepo(t)
	repoB := setupTestRepoWithHome(t, homeDir, "otherrepo")
	wtA := squashLandedSlot(t, repoA, homeDir)
	wtB := squashLandedSlot(t, repoB, homeDir)

	outside := t.TempDir()
	out, errOut, code := runTreehouseFromDir(t, repoA, outside, homeDir, nil, "prune", "--all", "--return")
	if code != 0 {
		t.Fatalf("prune --all --return failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "would return 2 landed worktrees across 2 pools") || !strings.Contains(out, "Re-run with --all --return --yes") {
		t.Fatalf("expected global dry-run return summary, got:\n%s", out)
	}

	out, errOut, code = runTreehouseFromDir(t, repoA, outside, homeDir, nil, "prune", "--all", "--return", "--yes")
	if code != 0 {
		t.Fatalf("prune --all --return --yes failed (code %d): %s", code, errOut)
	}
	if !strings.Contains(out, "Returned 2 landed worktrees across 2 pools.") {
		t.Fatalf("expected global return summary, got:\n%s", out)
	}
	for _, wt := range []string{wtA, wtB} {
		if _, err := os.Stat(filepath.Join(wt, "node_modules", "dep", "index.js")); err != nil {
			t.Fatalf("returned slot %s lost its ignored state: %v", wt, err)
		}
	}
}
