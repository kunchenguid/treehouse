package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/kunchenguid/treehouse/v3/internal/config"
)

func writeApprovalHooks(t *testing.T, repo, marker string, paths *[]string) {
	t.Helper()
	table := map[string]any{
		"post_create": []string{"echo create >> hook-events.txt"},
		"post_get":    []string{"echo get >> hook-events.txt"},
		"post_return": []string{"echo return >> hook-events.txt"},
		"pre_destroy": []string{"echo destroy > " + quoteReturnPath(marker)},
	}
	if paths != nil {
		table["approval_files"] = *paths
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(map[string]any{"hooks": table}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "treehouse.toml"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupApprovalFilesRepo(t *testing.T) (repo, home, marker string) {
	t.Helper()
	repo, home = setupTestRepo(t)
	// Approval compares dependency bytes between the main and execution
	// worktrees, so keep checkouts free of the host's line-ending conversion.
	gitCmd(t, repo, "config", "core.autocrlf", "false")
	for _, path := range []string{"setup.sh", "package.json"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte("approved\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prepareHookLog(t, repo, "setup.sh", "package.json")
	marker = filepath.Join(t.TempDir(), "destroy-ran")
	paths := []string{"setup.sh", "package.json"}
	writeApprovalHooks(t, repo, marker, &paths)
	return repo, home, marker
}

func getApprovalLease(t *testing.T, repo, home string, flags ...string) string {
	t.Helper()
	out, _ := requireTreehouseOK(t, repo, home, append([]string{"get", "--lease"}, flags...)...)
	return strings.TrimSpace(out)
}

func assertNoApprovalEvents(t *testing.T, worktree string) {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(worktree, "hook-events.txt")); !os.IsNotExist(err) {
		t.Fatalf("unexpected repository hook events %q: %v", data, err)
	}
}

func TestApprovalFileDowngradesDisableEveryLifecycleUntilTrust(t *testing.T) {
	t.Parallel()
	one, empty := []string{"setup.sh"}, []string{}
	for name, paths := range map[string]*[]string{"remove one": &one, "empty list": &empty, "delete key": nil} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo, home, marker := setupApprovalFilesRepo(t)
			requireTreehouseOK(t, repo, home, "trust")
			wt := getApprovalLease(t, repo, home)
			assertHookEvents(t, wt, "create", "get")
			requireTreehouseOK(t, repo, home, "return", wt, "--force")
			assertHookEvents(t, wt, "create", "get", "return")

			writeApprovalHooks(t, repo, marker, paths)
			reused := getApprovalLease(t, repo, home)
			if reused != wt {
				t.Fatal("expected slot reuse")
			}
			assertHookEvents(t, wt, "create", "get", "return")
			requireTreehouseOK(t, repo, home, "return", wt, "--force")
			assertHookEvents(t, wt, "create", "get", "return")
			requireTreehouseOK(t, repo, home, "destroy", wt, "--yes")
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("downgraded approval ran deletion hook")
			}

			requireTreehouseOK(t, repo, home, "trust")
			wt = getApprovalLease(t, repo, home)
			assertHookEvents(t, wt, "create", "get")
			requireTreehouseOK(t, repo, home, "return", wt, "--force")
			assertHookEvents(t, wt, "create", "get", "return")
			requireTreehouseOK(t, repo, home, "destroy", wt, "--yes")
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("reapproved deletion hook did not run: %v", err)
			}
		})
	}
}

func TestApprovalFilesCheckedInExecutionWorktree(t *testing.T) {
	t.Parallel()
	for _, difference := range []string{"changed", "missing"} {
		for _, deletion := range []string{"destroy", "prune"} {
			t.Run(difference+"/"+deletion, func(t *testing.T) {
				t.Parallel()
				repo, home, marker := setupApprovalFilesRepo(t)
				gitCmd(t, repo, "checkout", "-b", "alternate")
				if difference == "changed" {
					if err := os.WriteFile(filepath.Join(repo, "setup.sh"), []byte("different\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Remove(filepath.Join(repo, "setup.sh")); err != nil {
						t.Fatal(err)
					}
				}
				gitCmd(t, repo, "add", "setup.sh")
				gitCmd(t, repo, "commit", "-m", "Change hook dependency on alternate base")
				gitCmd(t, repo, "checkout", "main")
				requireTreehouseOK(t, repo, home, "trust")
				wt := getApprovalLease(t, repo, home, "--base", "alternate")
				assertNoApprovalEvents(t, wt)
				// Before return the file matches approval, but the reset restores the
				// alternate base. Verification must occur AFTER that reset.
				if err := os.WriteFile(filepath.Join(wt, "setup.sh"), []byte("approved\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				requireTreehouseOK(t, repo, home, "return", wt, "--force")
				assertNoApprovalEvents(t, wt)
				if deletion == "destroy" {
					requireTreehouseOK(t, repo, home, "destroy", wt, "--yes")
				} else {
					requireTreehouseOK(t, repo, home, "prune", "--yes")
				}
				if _, err := os.Stat(wt); !os.IsNotExist(err) {
					t.Fatal("expected deletion to complete")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("mismatched execution files ran deletion hook")
				}
			})
		}
	}
}

func TestApprovalFilesRequireMainAndWorktreeContents(t *testing.T) {
	t.Parallel()
	repo, home, _ := setupApprovalFilesRepo(t)
	requireTreehouseOK(t, repo, home, "trust")
	// Main changes while a new worktree still checks out the approved commit.
	if err := os.WriteFile(filepath.Join(repo, "setup.sh"), []byte("new contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := getApprovalLease(t, repo, home)
	assertNoApprovalEvents(t, wt)
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertNoApprovalEvents(t, wt)
	// Reapproving main is insufficient while the execution worktree differs.
	requireTreehouseOK(t, repo, home, "trust")
	wt = getApprovalLease(t, repo, home)
	assertNoApprovalEvents(t, wt)
	gitCmd(t, repo, "add", "setup.sh")
	gitCmd(t, repo, "commit", "-m", "Land approved dependency contents")
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertHookEvents(t, wt, "return")
	wt = getApprovalLease(t, repo, home)
	assertHookEvents(t, wt, "return", "create", "get")
}

func TestApprovalFilesCheckedAfterEarlierHooks(t *testing.T) {
	t.Parallel()
	for _, earlier := range []string{"user", "repository"} {
		t.Run(earlier, func(t *testing.T) {
			t.Parallel()
			repo, home, _ := setupApprovalFilesRepo(t)
			mutation := "echo changed > setup.sh"
			if earlier == "user" {
				writeLifecycleConfig(t, filepath.Join(home, ".config", "treehouse", "config.toml"), config.Hooks{PostCreate: []string{mutation}})
			} else {
				writeLifecycleConfig(t, filepath.Join(repo, "treehouse.toml"), config.Hooks{
					ApprovalFiles: []string{"setup.sh", "package.json"},
					PostCreate:    []string{mutation, "echo unapproved >> hook-events.txt"},
					PostGet:       []string{"echo unapproved >> hook-events.txt"},
				})
			}
			requireTreehouseOK(t, repo, home, "trust")
			wt := getApprovalLease(t, repo, home)
			assertNoApprovalEvents(t, wt)
			data, err := os.ReadFile(filepath.Join(wt, "setup.sh"))
			if err != nil || !strings.Contains(string(data), "changed") {
				t.Fatalf("earlier hook did not execute: %q, %v", data, err)
			}
		})
	}
}
