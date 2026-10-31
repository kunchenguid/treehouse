package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/kunchenguid/treehouse/v3/internal/config"
	"github.com/kunchenguid/treehouse/v3/internal/pool"
)

type workspaceTestProcess struct {
	stdout, stderr bytes.Buffer
	done           chan struct{}
	err            error
}

func startWorkspaceTestProcess(t *testing.T, repo, home, ready, release string, args ...string) *workspaceTestProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	cmd := exec.CommandContext(ctx, treehouseBin, args...)
	cmd.Dir = repo
	cmd.Env = buildEnv(home, "TREEHOUSE_TEST_READY="+ready, "TREEHOUSE_TEST_RELEASE="+release)
	p := &workspaceTestProcess{done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = &p.stdout, &p.stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		if err := os.WriteFile(release, nil, 0o644); err != nil {
			t.Error(err)
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			cancel()
			<-p.done
		}
		cancel()
	})
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(ready)
		if err == nil && len(data) > 0 {
			return p
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-p.done:
			t.Fatalf("owner exited before hook barrier: %v\n%s", p.err, p.stderr.String())
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("owner did not reach post-create hook barrier")
	return nil
}

func (p *workspaceTestProcess) assertRunning(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		t.Fatalf("owner exited while hook should be paused: %v\n%s", p.err, p.stderr.String())
	default:
	}
}

func (p *workspaceTestProcess) releaseAndWait(t *testing.T, release string) {
	t.Helper()
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
		if p.err != nil {
			t.Fatalf("owner failed: %v\n%s", p.err, p.stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("owner did not finish after hook release")
	}
}

func installWorkspaceWaitHook(t *testing.T, home string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Hooks.PostCreate = []string{quoteReturnPath(waitShellBin)}
	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".config", "treehouse")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), data.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceLeases(t *testing.T, state workspaceState, count int) {
	t.Helper()
	if len(state.Repositories) != count {
		t.Fatalf("membership has %d children, want %d", len(state.Repositories), count)
	}
	for _, child := range state.Repositories {
		actual, err := pool.ReadState(child.PoolDir)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range actual.Worktrees {
			if entry.Path == child.Path && entry.Leased && entry.LeaseID == child.LeaseID {
				found = true
			}
		}
		if !found {
			t.Fatalf("membership child %s no longer owns its recorded lease", child.Name)
		}
		if _, err := os.Stat(filepath.Join(state.Path, child.Name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkspaceMutationRejectsConcurrentLifecycle(t *testing.T) {
	for _, contender := range []string{"modify", "return", "bare-return", "shell-exit"} {
		t.Run(contender, func(t *testing.T) {
			repo, home := setupTestRepo(t)
			firstAdd := setupTestRepoWithHome(t, home, "first-add")
			secondAdd := setupTestRepoWithHome(t, home, "second-add")
			state := createLeasedWorkspace(t, repo, home)
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			installWorkspaceWaitHook(t, home)
			signals := t.TempDir()
			ready, release := filepath.Join(signals, "ready"), filepath.Join(signals, "release")
			secondRelease := filepath.Join(signals, "second-release")
			if err := os.WriteFile(secondRelease, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			extra := []string{"TREEHOUSE_TEST_READY=" + filepath.Join(signals, "second-ready"), "TREEHOUSE_TEST_RELEASE=" + secondRelease}
			owner := startWorkspaceTestProcess(t, repo, home, ready, release, "workspace", "modify", "--add", firstAdd, "--no-fetch", state.Path)
			before, err := os.ReadFile(workspaceStatePath(state.Path))
			if err != nil {
				t.Fatal(err)
			}
			owner.assertRunning(t)
			if contender == "modify" {
				independent := setupTestRepoWithHome(t, home, "independent")
				stdout, stderr, code := runTreehouse(t, repo, home, extra, "workspace", "get", "--lease", "--json", "--no-fetch", "--workspace-root", filepath.Dir(state.Path), independent)
				if code != 0 {
					t.Fatalf("independent workspace was blocked: %s", stderr)
				}
				var other workspaceState
				if err := json.Unmarshal([]byte(stdout), &other); err != nil {
					t.Fatal(err)
				}
				assertWorkspaceLeases(t, other, 1)
				owner.assertRunning(t)
			}
			var failure string
			var code int
			switch contender {
			case "modify":
				_, failure, code = runTreehouse(t, repo, home, extra, "workspace", "modify", "--add", secondAdd, "--no-fetch", state.Path)
			case "return":
				_, failure, code = runTreehouse(t, repo, home, nil, "workspace", "return", "--force", state.Path)
			case "bare-return":
				shellEnv := []string{"TREEHOUSE_WORKSPACE=" + state.Path, "TREEHOUSE_WORKSPACE_ID=" + state.ID}
				_, failure, code = runTreehouse(t, state.Path, home, shellEnv, "return", "--force")
			case "shell-exit":
				if err := finishWorkspaceShell(state.Path); err != nil {
					failure = err.Error()
					code = 1
				}
			}
			if code == 0 || !strings.Contains(failure, "workspace is busy") {
				owner.releaseAndWait(t, release)
				t.Fatalf("concurrent %s was not refused before side effects: %s", contender, failure)
			}
			owner.assertRunning(t)
			after, err := os.ReadFile(workspaceStatePath(state.Path))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("busy contender changed membership: %v", err)
			}
			assertWorkspaceLeases(t, state, 1)
			if contender == "modify" {
				poolDir, err := config.ResolvePoolDir(secondAdd, filepath.Join(home, ".treehouse"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(poolDir); !os.IsNotExist(err) {
					t.Fatalf("busy modifier allocated a pool: %v", err)
				}
			}
			owner.releaseAndWait(t, release)
			updated, err := readWorkspaceState(state.Path)
			if err != nil {
				t.Fatal(err)
			}
			assertWorkspaceLeases(t, updated, 2)
			if contender == "modify" {
				_, stderr, code := runTreehouse(t, repo, home, extra, "workspace", "modify", "--add", secondAdd, "--no-fetch", state.Path)
				if code != 0 {
					t.Fatalf("retry failed: %s", stderr)
				}
				updated, err = readWorkspaceState(state.Path)
				if err != nil {
					t.Fatal(err)
				}
				assertWorkspaceLeases(t, updated, 3)
			}
			_, stderr, code := runTreehouse(t, repo, home, nil, "workspace", "return", "--force", state.Path)
			if code != 0 {
				t.Fatalf("return retry failed: %s", stderr)
			}
		})
	}
}

func TestWorkspaceCreationRejectsConcurrentModify(t *testing.T) {
	repo, home := setupTestRepo(t)
	added := setupTestRepoWithHome(t, home, "added")
	installWorkspaceWaitHook(t, home)
	root, signals := t.TempDir(), t.TempDir()
	ready, release := filepath.Join(signals, "ready"), filepath.Join(signals, "release")
	owner := startWorkspaceTestProcess(t, repo, home, ready, release, "workspace", "get", "--lease", "--json", "--no-fetch", "--workspace-root", root, repo)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	path := ""
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "ws-") {
			if path != "" {
				t.Fatal("multiple new workspace directories")
			}
			path = filepath.Join(root, entry.Name())
		}
	}
	if path == "" {
		t.Fatal("workspace was not created before the hook")
	}
	secondRelease := filepath.Join(signals, "second-release")
	if err := os.WriteFile(secondRelease, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	extra := []string{"TREEHOUSE_TEST_READY=" + filepath.Join(signals, "second-ready"), "TREEHOUSE_TEST_RELEASE=" + secondRelease}
	_, stderr, code := runTreehouse(t, repo, home, extra, "workspace", "modify", "--add", added, "--no-fetch", path)
	owner.assertRunning(t)
	owner.releaseAndWait(t, release)
	if code == 0 || !strings.Contains(stderr, "workspace is busy") {
		t.Fatalf("modify during creation was not refused (code %d): %s", code, stderr)
	}
	var state workspaceState
	if err := json.Unmarshal(owner.stdout.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceLeases(t, state, 1)
	_, stderr, code = runTreehouse(t, repo, home, extra, "workspace", "modify", "--add", added, "--no-fetch", path)
	if code != 0 {
		t.Fatalf("modify retry failed: %s", stderr)
	}
	state, err = readWorkspaceState(path)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceLeases(t, state, 2)
}
