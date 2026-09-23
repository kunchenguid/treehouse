package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetBranchFlagsCreateBranchForLease(t *testing.T) {
	for _, flag := range []string{"-b", "--branch"} {
		t.Run(flag, func(t *testing.T) {
			repoDir, homeDir := setupTestRepo(t)
			stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", flag, "agent-work")
			if code != 0 {
				t.Fatalf("get --lease %s failed (code %d): %s", flag, code, stderr)
			}
			wtPath := strings.TrimSpace(stdout)
			if got := gitCmd(t, wtPath, "branch", "--show-current"); got != "agent-work" {
				t.Fatalf("checked-out branch = %q, want agent-work", got)
			}
		})
	}
}

func TestGetBranchExplicitEmptyFailsBeforeAcquisition(t *testing.T) {
	for _, args := range [][]string{{"-b", ""}, {"--branch", ""}, {"--branch="}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			repoDir, homeDir := setupTestRepo(t)
			command := append([]string{"get", "--lease"}, args...)
			stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, command...)
			if code == 0 {
				t.Fatalf("explicit empty branch unexpectedly succeeded: %s", stdout)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("failed acquire wrote stdout %q", stdout)
			}
			if !strings.Contains(stderr, "non-empty branch name") {
				t.Fatalf("error = %q, want explicit empty diagnosis", stderr)
			}
			poolRoot := filepath.Join(homeDir, ".treehouse")
			entries, err := os.ReadDir(poolRoot)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("explicit empty branch created acquisition state: %v", entries)
			}
		})
	}
}

func TestGetBranchStartsAtRequestedBase(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	developTip := addE2EBranchAndReturnTip(t, repoDir, "develop", "develop-only.txt")

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--base", "develop", "--branch", "feature")
	if code != 0 {
		t.Fatalf("get --base --branch failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	if got := gitCmd(t, wtPath, "rev-parse", "HEAD"); got != developTip {
		t.Fatalf("branch starts at %s, want develop tip %s", got, developTip)
	}
}

func TestGetBranchFailureHasNoStdoutAndDoesNotConsumeCapacity(t *testing.T) {
	for _, branch := range []string{"main", "invalid branch name"} {
		t.Run(branch, func(t *testing.T) {
			repoDir, homeDir := setupTestRepo(t)
			configDir := filepath.Join(homeDir, ".config", "treehouse")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("max_trees = 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			stdout, _, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--branch", branch)
			if code == 0 {
				t.Fatalf("branch creation unexpectedly succeeded: %s", stdout)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("failed machine acquire wrote stdout %q", stdout)
			}
			stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
			if code != 0 {
				t.Fatalf("capacity was stranded after branch failure (code %d): %s", code, stderr)
			}
			if got := gitCmd(t, strings.TrimSpace(stdout), "branch", "--show-current"); got != "" {
				t.Fatalf("recovered slot is on branch %q, want detached", got)
			}
		})
	}
}

func TestGetBranchInteractiveHookObservesBranchAndReturnKeepsIt(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	tipFile := filepath.Join(homeDir, "feature-tip")
	configDir := filepath.Join(homeDir, ".config", "treehouse")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := fmt.Sprintf("git branch --show-current && git -c user.name=Test -c user.email=test@example.com commit --allow-empty -m feature-tip && git rev-parse HEAD > %q", tipFile)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(fmt.Sprintf("[hooks]\npost_create = [%q]\n", hook)), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, []string{"SHELL=" + exitShellBin}, "get", "--branch", "interactive-work")
	if code != 0 {
		t.Fatalf("interactive get --branch failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "interactive-work") {
		t.Fatalf("post_create did not observe interactive-work before handoff: stdout=%q stderr=%q", stdout, stderr)
	}
	tip, err := os.ReadFile(tipFile)
	if err != nil {
		t.Fatalf("post_create did not record committed tip: %v", err)
	}
	if got := gitCmd(t, repoDir, "rev-parse", "refs/heads/interactive-work"); got != strings.TrimSpace(string(tip)) {
		t.Fatalf("returned branch tip = %s, want committed tip %s", got, strings.TrimSpace(string(tip)))
	}
	statusOut, statusErr, code := runTreehouse(t, repoDir, homeDir, nil, "status", "--json")
	if code != 0 {
		t.Fatalf("status after return failed (code %d): %s", code, statusErr)
	}
	var statuses []statusJSONResult
	if err := json.Unmarshal([]byte(statusOut), &statuses); err != nil || len(statuses) != 1 {
		t.Fatalf("returned worktree status = %q, err %v", statusOut, err)
	}
	if got := gitCmd(t, statuses[0].Path, "branch", "--show-current"); got != "" {
		t.Fatalf("returned worktree remains on branch %q", got)
	}
}

func TestGetWithoutBranchRemainsDetached(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	if got := gitCmd(t, strings.TrimSpace(stdout), "branch", "--show-current"); got != "" {
		t.Fatalf("default get checked out %q, want detached HEAD", got)
	}
}

func addE2EBranchAndReturnTip(t *testing.T, repoDir, branch, marker string) string {
	t.Helper()
	addE2EBranch(t, repoDir, branch, marker)
	return gitCmd(t, repoDir, "rev-parse", branch)
}
