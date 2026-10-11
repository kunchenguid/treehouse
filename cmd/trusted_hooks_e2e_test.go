package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
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

// prepareHookLog keeps the hook event log untracked. Any extra paths are
// committed in the same round trip, so a caller that also needs seeded files
// does not pay for a second commit and push.
func prepareHookLog(t *testing.T, repo string, extra ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("hook-events.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, append([]string{"add", ".gitignore"}, extra...)...)
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
	t.Parallel()
	for _, trusted := range []bool{false, true} {
		name := "user"
		if trusted {
			name = "trusted repo"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo, home := setupTestRepo(t)
			prepareHookLog(t, repo)
			path := filepath.Join(home, ".config", "treehouse", "config.toml")
			if trusted {
				path = filepath.Join(repo, "treehouse.toml")
			}
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
			if trusted {
				requireTreehouseOK(t, repo, home, "trust")
			}
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
		})
	}
}

func TestTrustedHooksReturnAllAndSubshell(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"all", "subshell"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			repo, home := setupTestRepo(t)
			prepareHookLog(t, repo)
			writeLifecycleConfig(t, filepath.Join(repo, "treehouse.toml"), config.Hooks{
				PostGet:    []string{"echo get >> hook-events.txt"},
				PostReturn: []string{"echo returned >> hook-events.txt"},
			})
			requireTreehouseOK(t, repo, home, "trust")
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
	t.Parallel()
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

func TestTrustCommandsAndHookDrift(t *testing.T) {
	t.Parallel()
	repo, home := setupTestRepo(t)
	prepareHookLog(t, repo)
	path := filepath.Join(repo, "treehouse.toml")
	writeLifecycleConfig(t, path, config.Hooks{PostGet: []string{"echo original >> hook-events.txt"}})
	out, stderr := requireTreehouseOK(t, repo, home, "get", "--lease")
	wt := strings.TrimSpace(out)
	if !strings.Contains(stderr, "treehouse trust") {
		t.Fatalf("warning has no trust guidance: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(wt, "hook-events.txt")); !os.IsNotExist(err) {
		t.Fatal("untrusted hook executed")
	}
	// Trust invoked in a slot approves its main checkout's config.
	requireTreehouseOK(t, wt, home, "trust")
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	out, _ = requireTreehouseOK(t, repo, home, "get", "--lease")
	wt = strings.TrimSpace(out)
	assertHookEvents(t, wt, "original")
	writeLifecycleConfig(t, path, config.Hooks{PostGet: []string{"echo changed >> hook-events.txt"}, PostReturn: []string{"echo changed-return >> hook-events.txt"}})
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	out, stderr = requireTreehouseOK(t, repo, home, "get", "--lease")
	wt = strings.TrimSpace(out)
	assertHookEvents(t, wt, "original")
	if !strings.Contains(stderr, "treehouse trust") {
		t.Fatalf("drift did not warn: %s", stderr)
	}
	requireTreehouseOK(t, repo, home, "trust")
	requireTreehouseOK(t, wt, home, "untrust")
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertHookEvents(t, wt, "original")
}

func TestTrustedPreDestroyTargetsOwningClone(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"destroy", "destroy all", "prune", "prune all"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			repoA, home := setupTestRepo(t)
			repoB := filepath.Join(t.TempDir(), "myrepo")
			gitCmd(t, "", "clone", gitCmd(t, repoA, "config", "--get", "remote.origin.url"), repoB)
			markerA, markerB := filepath.Join(t.TempDir(), "A"), filepath.Join(t.TempDir(), "B")
			writeLifecycleConfig(t, filepath.Join(repoA, "treehouse.toml"), config.Hooks{PreDestroy: []string{"echo A > " + quoteReturnPath(markerA)}})
			writeLifecycleConfig(t, filepath.Join(repoB, "treehouse.toml"), config.Hooks{PreDestroy: []string{"echo B > " + quoteReturnPath(markerB)}})
			requireTreehouseOK(t, repoB, home, "trust") // Same URL, but only B's hash.
			out, _ := requireTreehouseOK(t, repoA, home, "get", "--lease")
			wtA := strings.TrimSpace(out)
			out, _ = requireTreehouseOK(t, repoB, home, "get", "--lease")
			wtB := strings.TrimSpace(out)
			requireTreehouseOK(t, repoA, home, "return", wtA, "--force")
			requireTreehouseOK(t, repoB, home, "return", wtB, "--force")
			args := []string{"destroy", wtB, "--yes"}
			switch mode {
			case "destroy all":
				args = []string{"destroy", filepath.Dir(filepath.Dir(wtB)), "--all", "--yes"}
			case "prune":
				args = []string{"prune", "--yes"}
			case "prune all":
				args = []string{"prune", "--all", "--yes"}
			}
			// Dry runs must not execute either hook.
			requireTreehouseOK(t, repoA, home, args[:len(args)-1]...)
			if _, err := os.Stat(markerB); !os.IsNotExist(err) {
				t.Fatal("dry run executed hook")
			}
			workDir := repoA
			if mode != "prune" {
				workDir = t.TempDir()
			}
			_, stderr, code := runTreehouseFromDir(t, repoA, workDir, home, nil, args...)
			if code != 0 {
				t.Fatalf("%s failed: %s", mode, stderr)
			}
			if _, err := os.Stat(markerB); err != nil {
				t.Fatalf("B hook did not run: %v; %s", err, stderr)
			}
			if _, err := os.Stat(markerA); !os.IsNotExist(err) {
				t.Fatal("untrusted clone A hook ran")
			}
		})
	}
}

func TestHookTableAliasCannotBypassTrust(t *testing.T) {
	t.Parallel()
	repo, home := setupTestRepo(t)
	prepareHookLog(t, repo)
	marker := filepath.Join(t.TempDir(), "unapproved-destroy")
	userMarker := filepath.Join(t.TempDir(), "user-destroy")
	writeLifecycleConfig(t, filepath.Join(home, ".config", "treehouse", "config.toml"), config.Hooks{
		PostReturn: []string{"echo user-return >> hook-events.txt"},
		PreDestroy: []string{"echo ran > " + quoteReturnPath(userMarker)},
	})
	path := filepath.Join(repo, "treehouse.toml")
	const approved = "[hooks]\npost_get=['echo approved >> hook-events.txt']\n"
	if err := os.WriteFile(path, []byte(approved), 0o644); err != nil {
		t.Fatal(err)
	}
	requireTreehouseOK(t, repo, home, "trust")
	out, _ := requireTreehouseOK(t, repo, home, "get", "--lease")
	wt := strings.TrimSpace(out)
	assertHookEvents(t, wt, "approved")
	// Keep an existing lease to exercise cleanup as well as fresh acquisition.
	// User hooks must still run if the repository config becomes invalid.
	alias := "\n[Hooks]\npost_create=['echo unapproved-create >> hook-events.txt']\npost_return=['echo unapproved-return >> hook-events.txt']\npre_destroy=[" + strconv.Quote("echo ran > "+quoteReturnPath(marker)) + "]\n"
	if err := os.WriteFile(path, []byte(approved+alias), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runTreehouse(t, repo, home, nil, "get", "--lease")
	if code == 0 {
		t.Fatalf("get executed an unapproved alias: stdout=%q, stderr=%q", out, stderr)
	}
	if out != "" || !strings.Contains(stderr, "[hooks]") {
		t.Fatalf("unexpected rejection: stdout=%q, stderr=%q", out, stderr)
	}
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	assertHookEvents(t, wt, "approved", "user-return")
	requireTreehouseOK(t, repo, home, "destroy", wt, "--yes")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unapproved destroy hook ran")
	}
	if _, err := os.Stat(userMarker); err != nil {
		t.Fatalf("user destroy hook did not run: %v", err)
	}
}

func TestUntrustAliasPreventsSubsequentHookExecution(t *testing.T) {
	t.Parallel()
	repo, home := setupTestRepo(t)
	prepareHookLog(t, repo)
	writeLifecycleConfig(t, filepath.Join(repo, "treehouse.toml"), config.Hooks{
		PostGet: []string{"echo repository >> hook-events.txt"},
	})
	requireTreehouseOK(t, repo, home, "trust")
	userPath := filepath.Join(home, ".config", "treehouse", "config.toml")
	data, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	aliased := bytes.ReplaceAll(data, []byte("[trusted_repos]"), []byte("[Trusted_Repos]"))
	if bytes.Equal(data, aliased) {
		t.Fatal("test did not rename trust table")
	}
	if err := os.WriteFile(userPath, aliased, 0o600); err != nil {
		t.Fatal(err)
	}
	// Noncanonical keys must block both reads and updates. A failed untrust
	// cannot claim revocation or rewrite an alias as an unknown setting.
	for _, args := range [][]string{{"get", "--lease"}, {"untrust"}} {
		out, stderr, code := runTreehouse(t, repo, home, nil, args...)
		if code == 0 || out != "" || !strings.Contains(stderr, "[trusted_repos]") {
			t.Fatalf("alias accepted by %v: code=%d stdout=%q stderr=%q", args, code, out, stderr)
		}
	}
	unchanged, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchanged, aliased) {
		t.Fatal("rejected alias was rewritten")
	}
	// After the user explicitly corrects the key, read -> untrust -> write ->
	// reload must remove the approval and leave only the already recorded event.
	if err := os.WriteFile(userPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := requireTreehouseOK(t, repo, home, "get", "--lease")
	wt := strings.TrimSpace(out)
	assertHookEvents(t, wt, "repository")
	requireTreehouseOK(t, repo, home, "return", wt, "--force")
	requireTreehouseOK(t, repo, home, "untrust")
	out, stderr := requireTreehouseOK(t, repo, home, "get", "--lease")
	if strings.TrimSpace(out) != wt {
		t.Fatal("expected slot reuse")
	}
	assertHookEvents(t, wt, "repository")
	if !strings.Contains(stderr, "treehouse trust") {
		t.Fatalf("missing revoked-hooks warning: %s", stderr)
	}
}
