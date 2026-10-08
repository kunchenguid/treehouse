package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/kunchenguid/treehouse/v3/internal/config"
)

func writeLifecycleConfig(t *testing.T, path string, hooks config.Hooks) {
	t.Helper()
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(struct {
		Hooks config.Hooks `toml:"hooks"`
	}{hooks}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func prepareHookLog(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("hook-events.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", ".gitignore")
	gitCmd(t, repo, "commit", "-m", "Ignore hook event log")
	gitCmd(t, repo, "push", "origin", "main")
}

func assertHookEvents(t *testing.T, wt string, want ...string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(wt, "hook-events.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("hook events = %q, want %q", lines, want)
	}
}

func requireTreehouseOK(t *testing.T, repo, home string, args ...string) (string, string) {
	t.Helper()
	out, stderr, code := runTreehouse(t, repo, home, nil, args...)
	if code != 0 {
		t.Fatalf("treehouse %v exited %d: %s", args, code, stderr)
	}
	return out, stderr
}

func TestLifecycleHooksFreshReuseAndReturn(t *testing.T) {
	repo, home := setupTestRepo(t)
	prepareHookLog(t, repo)
	path := filepath.Join(home, ".config", "treehouse", "config.toml")
	// Reading README in post_return proves the hook sees the reset checkout.
	readREADME := "cat README.md >> hook-events.txt"
	if runtime.GOOS == "windows" {
		readREADME = "type README.md >> hook-events.txt"
	}
	writeLifecycleConfig(t, path, config.Hooks{
		PostCreate: []string{"echo create >> hook-events.txt"},
		PostGet:    []string{"treehouse-nonexistent-hook-command", "echo get >> hook-events.txt", "echo hook-output"},
		PostReturn: []string{"treehouse-nonexistent-hook-command", readREADME, "echo returned >> hook-events.txt"},
	})
	first := ""
	for i := 0; i < 2; i++ {
		args := []string{"get", "--lease"}
		if i == 1 {
			args = append(args, "--json")
		}
		out, stderr := requireTreehouseOK(t, repo, home, args...)
		wt := strings.TrimSpace(out)
		if i == 1 {
			var lease leaseJSONResult
			if err := json.Unmarshal([]byte(out), &lease); err != nil {
				t.Fatalf("polluted lease JSON: %q: %v", out, err)
			}
			wt = lease.Path
		} else if out != wt+"\n" {
			t.Fatalf("polluted lease path: %q", out)
		}
		if !strings.Contains(stderr, "hook-output") || !strings.Contains(stderr, "hook command failed") {
			t.Fatalf("missing hook diagnostics: %s", stderr)
		}
		if first == "" {
			first = wt
		} else if wt != first {
			t.Fatalf("did not reuse slot: %s != %s", wt, first)
		}
		if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// A named return works outside the repo, with the target's configuration.
		_, stderr, code := runTreehouseFromDir(t, repo, t.TempDir(), home, nil, "return", "--force", wt)
		if code != 0 {
			t.Fatalf("return failed: %s", stderr)
		}
	}
	assertHookEvents(t, first, "create", "get", "hello", "returned", "create", "get", "hello", "returned")
}

func TestUserHooksReturnAllAndSubshell(t *testing.T) {
	for _, mode := range []string{"all", "subshell"} {
		t.Run(mode, func(t *testing.T) {
			repo, home := setupTestRepo(t)
			prepareHookLog(t, repo)
			writeLifecycleConfig(t, filepath.Join(home, ".config", "treehouse", "config.toml"), config.Hooks{
				PostGet:    []string{"echo get >> hook-events.txt"},
				PostReturn: []string{"echo returned >> hook-events.txt"},
			})
			if mode == "all" {
				var worktrees []string
				for i := 0; i < 2; i++ {
					out, _ := requireTreehouseOK(t, repo, home, "get", "--lease")
					worktrees = append(worktrees, strings.TrimSpace(out))
				}
				requireTreehouseOK(t, repo, home, "return", "--all", "--force")
				for _, wt := range worktrees {
					assertHookEvents(t, wt, "get", "returned")
				}
			} else {
				_, stderr, code := runTreehouse(t, repo, home, []string{"SHELL=" + exitShellBin}, "get")
				if code != 0 {
					t.Fatalf("get subshell failed: %s", stderr)
				}
				assertHookEvents(t, extractWorktreePath(stderr, home), "get", "returned")
			}
		})
	}
}

func TestPostReturnDoesNotRunWhenReturnRefused(t *testing.T) {
	repo, home := setupTestRepo(t)
	prepareHookLog(t, repo)
	writeLifecycleConfig(t, filepath.Join(home, ".config", "treehouse", "config.toml"), config.Hooks{
		PostGet: []string{"echo get >> hook-events.txt"}, PostReturn: []string{"echo returned >> hook-events.txt"},
	})
	out, _ := requireTreehouseOK(t, repo, home, "get", "--lease")
	wt := strings.TrimSpace(out)
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"return", wt}, {"return", wt, "--force", "--if-lease-id", "wrong-lease"}} {
		_, _, code := runTreehouse(t, repo, home, nil, args...)
		if code == 0 {
			t.Fatalf("unexpected successful return: %v", args)
		}
		assertHookEvents(t, wt, "get")
	}
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertHookEvents(t, wt, "get", "returned")
}
