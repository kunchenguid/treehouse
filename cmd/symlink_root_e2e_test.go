package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSymlinkRootKeepsOneLeasePerWorktree(t *testing.T) {
	for _, returnFromCwd := range []bool{false, true} {
		name := "explicit-path"
		if returnFromCwd {
			name = "physical-cwd"
		}
		t.Run(name, func(t *testing.T) {
			sourceRepo, homeDir := setupTestRepo(t)
			repoDir := filepath.Join(filepath.Dir(sourceRepo), "clone", "myrepo")
			gitCmd(t, "", "clone", gitCmd(t, sourceRepo, "remote", "get-url", "origin"), repoDir)
			store := filepath.Join(filepath.Dir(homeDir), "real-store")
			if err := os.MkdirAll(store, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(store, filepath.Join(homeDir, ".treehouse")); err != nil {
				t.Skipf("directory symlinks unavailable: %v", err)
			}
			if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("max_trees = 2\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			env := []string{"TREEHOUSE_ROOT=" + homeDir}
			run := func(args ...string) (string, string, int) {
				return runTreehouse(t, repoDir, homeDir, env, args...)
			}
			acquire := func(holder string) leaseJSONResult {
				t.Helper()
				stdout, stderr, code := run("get", "--lease", "--lease-holder", holder, "--json")
				if code != 0 {
					t.Fatalf("get --lease failed (%d): %s", code, stderr)
				}
				var lease leaseJSONResult
				if err := json.Unmarshal([]byte(stdout), &lease); err != nil {
					t.Fatal(err)
				}
				return lease
			}
			first := acquire("first")
			second := acquire("second")
			physical, err := filepath.EvalSymlinks(first.Path)
			if err != nil {
				t.Fatal(err)
			}
			if physical == first.Path {
				t.Fatal("fixture did not create alternate pool spellings")
			}
			// A dry run through the physical spelling must not recover twins.
			if _, stderr, code := run("destroy", physical, "--include-leased"); code != 0 {
				t.Fatalf("destroy dry run failed (%d): %s", code, stderr)
			}
			assertLeases := func(firstID, firstStatus string) {
				t.Helper()
				entries := statusEntriesFromDir(t, repoDir, repoDir, homeDir, env)
				if len(entries) != 2 {
					t.Fatalf("status has %d entries for two physical worktrees: %+v", len(entries), entries)
				}
				for _, entry := range entries {
					switch entry.Path {
					case first.Path:
						if entry.Status != firstStatus || entry.LeaseID != firstID {
							t.Fatalf("wrong first lease: %+v", entry)
						}
					case second.Path:
						if entry.Status != "leased" || entry.LeaseID != second.LeaseID || entry.LeaseHolder != "second" {
							t.Fatalf("second lease changed: %+v", entry)
						}
					default:
						t.Fatalf("unexpected recorded path: %+v", entry)
					}
				}
			}
			assertLeases(first.LeaseID, "leased")
			// Keep uncommitted work under the other holder to detect a reset.
			sentinel := filepath.Join(second.Path, "README.md")
			if err := os.WriteFile(sentinel, []byte("second holder's work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(first.Path, "README.md"), []byte("first holder's work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"return", "--if-lease-id", first.LeaseID}
			workDir := repoDir
			if returnFromCwd {
				workDir = physical
			} else {
				args = append(args, physical)
			}
			if _, stderr, code := runTreehouseFromDir(t, repoDir, workDir, homeDir, env, args...); code != ExitNotReturned || !strings.Contains(stderr, physical) {
				t.Fatalf("dirty return did not retain the caller's path (%d): %s", code, stderr)
			}
			assertLeases(first.LeaseID, "leased")
			args = append(args, "--force")
			if _, stderr, code := runTreehouseFromDir(t, repoDir, workDir, homeDir, env, args...); code != 0 {
				t.Fatalf("physical return failed (%d): %s", code, stderr)
			}
			assertLeases("", "available")
			third := acquire("third")
			if third.Path != first.Path || third.LeaseID == first.LeaseID {
				t.Fatalf("released slot was not re-leased: %+v", third)
			}
			// Each invocation is a fresh CLI process reading the persisted state.
			for i := 0; i < 2; i++ {
				assertLeases(third.LeaseID, "leased")
				if _, stderr, code := run("get", "--lease", "--lease-holder", "fourth"); code == 0 {
					t.Fatalf("acquired a leased physical directory: %s", stderr)
				}
			}
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "second holder's work\n" {
				t.Fatalf("other holder's work changed: %q, %v", data, err)
			}
		})
	}
}
