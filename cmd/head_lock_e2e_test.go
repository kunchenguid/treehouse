package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/treehouse/v3/internal/process"
)

func headLockPath(t *testing.T, slot string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-path", "HEAD")
	cmd.Dir = slot
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(strings.TrimSpace(string(out))) + ".lock"
}

func TestHeadLockStatusReportsAnonymousLocks(t *testing.T) {
	for _, leased := range []bool{false, true} {
		t.Run(fmt.Sprintf("leased=%t", leased), func(t *testing.T) {
			repo, home := setupTestRepo(t)
			env := []string{"TREEHOUSE_ROOT=" + filepath.Join(t.TempDir(), "root")}
			if err := os.WriteFile(filepath.Join(repo, "treehouse.toml"), []byte("max_trees = 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			out, stderr, code := runTreehouse(t, repo, home, env, "get", "--lease", "--no-fetch")
			if code != 0 {
				t.Fatalf("get: %s", stderr)
			}
			slot := strings.TrimSpace(out)
			if !leased {
				if _, stderr, code := runTreehouse(t, repo, home, env, "return", slot); code != 0 {
					t.Fatalf("return: %s", stderr)
				}
			}
			lock := headLockPath(t, slot)
			if err := os.WriteFile(lock, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			out, stderr, code = runTreehouse(t, repo, home, env, "status")
			if code != 0 || !strings.Contains(out, "HEAD.lock") || !strings.Contains(out, "manual") {
				t.Fatalf("status must explain the anonymous lock and manual inspection: %s (%s)", out, stderr)
			}
			out, stderr, code = runTreehouse(t, repo, home, env, "status", "--json")
			var statuses []struct {
				Status    string `json:"status"`
				LockError string `json:"lock_error"`
			}
			if err := json.Unmarshal([]byte(out), &statuses); err != nil || code != 0 || len(statuses) != 1 {
				t.Fatalf("status JSON: %s (%s, %v)", out, stderr, err)
			}
			want := "locked"
			if leased {
				want = "leased"
			}
			if statuses[0].Status != want || !strings.Contains(statuses[0].LockError, lock) {
				t.Fatalf("status must retain the lease and expose the exact lock: %+v", statuses[0])
			}
			if !leased {
				inside, _, code := runTreehouseFromDir(t, repo, slot, home, env, "status", "--json")
				if code != 0 || !strings.Contains(inside, `"status":"locked"`) {
					t.Fatalf("standing in a locked slot must not hide its obstruction: %s", inside)
				}
				_, stderr, code = runTreehouse(t, repo, home, env, "get", "--lease", "--no-fetch")
				if code == 0 || !strings.Contains(stderr, "HEAD.lock") {
					t.Fatalf("full-pool get must explain the lock: code=%d %s", code, stderr)
				}
			}
			for _, args := range [][]string{{"return", slot}, {"return", "--force", slot}} {
				_, stderr, code = runTreehouse(t, repo, home, env, args...)
				if code == 0 || !strings.Contains(stderr, "manual") {
					t.Fatalf("anonymous lock must require manual inspection: code=%d %s", code, stderr)
				}
				if data, err := os.ReadFile(lock); err != nil || len(data) != 0 {
					t.Fatalf("anonymous lock changed: %q (%v)", data, err)
				}
			}
		})
	}
}

func TestHeadLockKilledResetIsRecoverable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git delay wrapper is a POSIX shell script")
	}
	for _, action := range []string{"return", "get"} {
		for _, written := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/written=%t", action, written), func(t *testing.T) {
				repo, home := setupTestRepo(t)
				root := filepath.Join(t.TempDir(), "root")
				env := []string{"TREEHOUSE_ROOT=" + root}
				if err := os.WriteFile(filepath.Join(repo, "treehouse.toml"), []byte("max_trees = 1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				out, stderr, code := runTreehouse(t, repo, home, env, "get", "--lease", "--no-fetch")
				if code != 0 {
					t.Fatalf("initial get: %s", stderr)
				}
				slot := strings.TrimSpace(out)
				args := []string{"return", slot}
				if action == "get" {
					if _, stderr, code := runTreehouse(t, repo, home, env, args...); code != 0 {
						t.Fatalf("initial return: %s", stderr)
					}
					args = []string{"get", "--lease", "--no-fetch"}
				}
				lock := headLockPath(t, slot)
				realGit, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				shim := t.TempDir()
				ready, release := filepath.Join(shim, "ready"), filepath.Join(shim, "release")
				script := "#!/bin/sh\nif [ \"$1\" = read-tree ]; then\n  : > \"$TREEHOUSE_TEST_READY\"\n  while [ ! -f \"$TREEHOUSE_TEST_RELEASE\" ]; do sleep 0.02; done\nfi\nexec \"$TREEHOUSE_TEST_GIT\" \"$@\"\n"
				if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(treehouseBin, args...)
				cmd.Dir = repo
				cmd.Env = buildEnv(home, append(env, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"),
					"TREEHOUSE_TEST_READY="+ready, "TREEHOUSE_TEST_RELEASE="+release, "TREEHOUSE_TEST_GIT="+realGit)...)
				var diagnostics bytes.Buffer
				cmd.Stdout, cmd.Stderr = &diagnostics, &diagnostics
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = os.WriteFile(release, nil, 0o644)
					_ = cmd.Process.Kill()
				})
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(ready); err == nil {
						break
					}
					if time.Now().After(deadline) {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						t.Fatalf("reset never reached read-tree: %s", diagnostics.String())
					}
					time.Sleep(20 * time.Millisecond)
				}
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
				if action == "return" {
					_, stderr, code := runTreehouse(t, repo, home, env, "return", slot)
					if code == 0 {
						t.Fatalf("return recovered while the killed reset's Git child was still running: %s", stderr)
					}
					if !strings.Contains(stderr, "treehouse return --force") {
						t.Fatalf("refusal must name the way out: %s", stderr)
					}
					if _, err := os.Stat(lock); err != nil {
						t.Fatalf("return removed the lock while the Git child was live: %v", err)
					}
				}
				if err := os.WriteFile(release, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				for {
					procs, err := process.FindProcessesInWorktree(slot)
					if err != nil {
						t.Fatal(err)
					}
					if len(procs) == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("Git child did not exit: %v", procs)
					}
					time.Sleep(20 * time.Millisecond)
				}
				if _, err := os.Stat(lock); err != nil {
					t.Fatalf("killed reset did not leave HEAD.lock: %v", err)
				}
				if written {
					head, err := exec.Command("git", "-C", slot, "rev-parse", "HEAD").Output()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(lock, head, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				out, stderr, code = runTreehouse(t, repo, home, env, args...)
				if code != 0 {
					t.Fatalf("%s must recover its killed reset: %s %s", action, out, stderr)
				}
				if action == "get" && strings.TrimSpace(out) != slot {
					t.Fatalf("get must reuse the only slot: %q, want %q", out, slot)
				}
				if _, err := os.Stat(lock); !os.IsNotExist(err) {
					t.Fatalf("successful recovery left HEAD.lock: %v", err)
				}
			})
		}
	}
}

func plantOwnedHeadLock(t *testing.T, slot string) string {
	t.Helper()
	lock := headLockPath(t, slot)
	witness := strings.TrimSuffix(lock, ".lock") + ".treehouse-witness"
	if err := os.WriteFile(witness, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(witness, lock); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	return lock
}

func TestHeadLockDirtyOwnedLockNamesReturnOnly(t *testing.T) {
	repo, home := setupTestRepo(t)
	env := []string{"TREEHOUSE_ROOT=" + filepath.Join(t.TempDir(), "root")}
	if err := os.WriteFile(filepath.Join(repo, "treehouse.toml"), []byte("max_trees = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runTreehouse(t, repo, home, env, "get", "--lease", "--no-fetch")
	if code != 0 {
		t.Fatalf("get: %s", stderr)
	}
	slot := strings.TrimSpace(out)
	if _, stderr, code := runTreehouse(t, repo, home, env, "return", slot); code != 0 {
		t.Fatalf("return: %s", stderr)
	}
	if err := os.WriteFile(filepath.Join(slot, "half-reset.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := plantOwnedHeadLock(t, slot)

	_, stderr, code = runTreehouse(t, repo, home, env, "get", "--lease", "--no-fetch")
	if code == 0 || !strings.Contains(stderr, lock) {
		t.Fatalf("full-pool get must name the lock of a dirty slot: code=%d %s", code, stderr)
	}
	out, stderr, code = runTreehouse(t, repo, home, env, "status", "--json")
	var statuses []struct {
		Status    string `json:"status"`
		LockError string `json:"lock_error"`
	}
	if err := json.Unmarshal([]byte(out), &statuses); err != nil || code != 0 || len(statuses) != 1 {
		t.Fatalf("status JSON: %s (%s, %v)", out, stderr, err)
	}
	if statuses[0].Status != "locked" || !strings.Contains(statuses[0].LockError, "run return") || strings.Contains(statuses[0].LockError, "retry get") {
		t.Fatalf("a dirty locked slot must name return only: %+v", statuses[0])
	}
}

func TestHeadLockReturnAllTargetsOnlyOwnedLocks(t *testing.T) {
	for _, owned := range []bool{true, false} {
		t.Run(fmt.Sprintf("owned=%t", owned), func(t *testing.T) {
			repo, home := setupTestRepo(t)
			env := []string{"TREEHOUSE_ROOT=" + filepath.Join(t.TempDir(), "root")}
			if err := os.WriteFile(filepath.Join(repo, "treehouse.toml"), []byte("max_trees = 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			out, stderr, code := runTreehouse(t, repo, home, env, "get", "--lease", "--no-fetch")
			if code != 0 {
				t.Fatalf("get: %s", stderr)
			}
			slot := strings.TrimSpace(out)
			if _, stderr, code := runTreehouse(t, repo, home, env, "return", slot); code != 0 {
				t.Fatalf("return: %s", stderr)
			}
			var lock string
			if owned {
				lock = plantOwnedHeadLock(t, slot)
			} else {
				lock = headLockPath(t, slot)
				if err := os.WriteFile(lock, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, stderr, code = runTreehouse(t, repo, home, env, "return", "--all")
			_, statErr := os.Stat(lock)
			if owned && (code != 0 || !os.IsNotExist(statErr)) {
				t.Fatalf("return --all must recover an owned lock: code=%d %s (%v)", code, stderr, statErr)
			}
			if !owned && (code != 0 || statErr != nil) {
				t.Fatalf("return --all must skip an anonymous lock: code=%d %s (%v)", code, stderr, statErr)
			}
		})
	}
}

func TestHeadLockHalfResetToNewerBaseIsDirtyAndNamesReturnOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git delay wrapper is a POSIX shell script")
	}
	repo, home := setupTestRepo(t)
	env := []string{"TREEHOUSE_ROOT=" + filepath.Join(t.TempDir(), "root")}
	if err := os.WriteFile(filepath.Join(repo, "treehouse.toml"), []byte("max_trees = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runTreehouse(t, repo, home, env, "get", "--lease", "--no-fetch")
	if code != 0 {
		t.Fatalf("initial get: %s", stderr)
	}
	slot := strings.TrimSpace(out)
	if _, stderr, code := runTreehouse(t, repo, home, env, "return", slot); code != 0 {
		t.Fatalf("initial return: %s", stderr)
	}
	if err := os.WriteFile(filepath.Join(repo, "newer.txt"), []byte("newer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "newer.txt")
	gitCmd(t, repo, "commit", "-m", "advance base")
	gitCmd(t, repo, "push", "origin", "main")
	lock := headLockPath(t, slot)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	done, hold := filepath.Join(shim, "done"), filepath.Join(shim, "hold")
	script := "#!/bin/sh\nif [ \"$1\" = read-tree ]; then\n  \"$TREEHOUSE_TEST_GIT\" \"$@\"\n  rc=$?\n  : > \"$TREEHOUSE_TEST_DONE\"\n  while [ ! -f \"$TREEHOUSE_TEST_HOLD\" ]; do sleep 0.02; done\n  exit $rc\nfi\nexec \"$TREEHOUSE_TEST_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(treehouseBin, "get", "--lease", "--no-fetch")
	cmd.Dir = repo
	cmd.Env = buildEnv(home, append(env, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TREEHOUSE_TEST_DONE="+done, "TREEHOUSE_TEST_HOLD="+hold, "TREEHOUSE_TEST_GIT="+realGit)...)
	var diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diagnostics, &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(hold, nil, 0o644)
		_ = cmd.Process.Kill()
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(done); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("read-tree never completed: %s", diagnostics.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		procs, err := process.FindProcessesInWorktree(slot)
		if err != nil {
			t.Fatal(err)
		}
		if len(procs) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Git child did not exit: %v", procs)
		}
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("killed reset did not leave HEAD.lock: %v", err)
	}
	if porcelain := gitCmd(t, slot, "status", "--porcelain"); strings.TrimSpace(porcelain) == "" {
		t.Fatal("a reset killed between two different commits must leave the slot dirty")
	}

	_, stderr, code = runTreehouse(t, repo, home, env, "get", "--lease", "--no-fetch")
	if code == 0 || !strings.Contains(stderr, lock) {
		t.Fatalf("full-pool get must name the lock: code=%d %s", code, stderr)
	}
	out, stderr, code = runTreehouse(t, repo, home, env, "status", "--json")
	var statuses []struct {
		Status    string `json:"status"`
		LockError string `json:"lock_error"`
	}
	if err := json.Unmarshal([]byte(out), &statuses); err != nil || code != 0 || len(statuses) != 1 {
		t.Fatalf("status JSON: %s (%s, %v)", out, stderr, err)
	}
	if statuses[0].Status != "locked" || !strings.Contains(statuses[0].LockError, "run return") || strings.Contains(statuses[0].LockError, "retry get") {
		t.Fatalf("a dirty half-reset slot must name return only: %+v", statuses[0])
	}
}

func TestHeadLockLeasedOwnedLockNamesReturnOnly(t *testing.T) {
	repo, home := setupTestRepo(t)
	env := []string{"TREEHOUSE_ROOT=" + filepath.Join(t.TempDir(), "root")}
	if err := os.WriteFile(filepath.Join(repo, "treehouse.toml"), []byte("max_trees = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runTreehouse(t, repo, home, env, "get", "--lease", "--no-fetch")
	if code != 0 {
		t.Fatalf("get: %s", stderr)
	}
	plantOwnedHeadLock(t, strings.TrimSpace(out))
	out, stderr, code = runTreehouse(t, repo, home, env, "status", "--json")
	var statuses []struct {
		Status    string `json:"status"`
		LockError string `json:"lock_error"`
	}
	if err := json.Unmarshal([]byte(out), &statuses); err != nil || code != 0 || len(statuses) != 1 {
		t.Fatalf("status JSON: %s (%s, %v)", out, stderr, err)
	}
	if statuses[0].Status != "leased" || !strings.Contains(statuses[0].LockError, "run return") || strings.Contains(statuses[0].LockError, "retry get") {
		t.Fatalf("a leased slot must name return only: %+v", statuses[0])
	}
}
