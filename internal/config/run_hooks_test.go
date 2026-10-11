package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/kunchenguid/treehouse/v3/internal/hooks"
	"github.com/kunchenguid/treehouse/v3/internal/vcs"
)

const hookLogName = "hook-log.txt"

// appendHookCommand appends one word to the worktree's hook log. `echo x>>file`
// means the same thing in /bin/sh and in cmd.exe, which are the shells
// hooks.Run uses.
func appendHookCommand(word string) string {
	return "echo " + word + ">>" + hookLogName
}

// copyCommand replaces dst with src from the OS shell, so a hook can change the
// user config exactly as `treehouse trust` would while the runner is executing.
func copyCommand(src, dst string) string {
	if runtime.GOOS == "windows" {
		return `copy /y "` + src + `" "` + dst + `"`
	}
	return `cp "` + src + `" "` + dst + `"`
}

func hookLog(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, hookLogName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func writeUserHooks(t *testing.T, user Hooks) {
	t.Helper()
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(map[string]any{"hooks": user}); err != nil {
		t.Fatal(err)
	}
	writeTrustUserConfig(t, buf.String())
}

// runHooksRepo creates a repository whose own VCS marker the runner can resolve
// back to it. The root comes from git, exactly as the trust command resolves
// it, so the recorded identity is the one the runner derives at each event.
func runHooksRepo(t *testing.T, user Hooks, repoConfig string) string {
	t.Helper()
	setUserHome(t, t.TempDir())
	repo := t.TempDir()
	run(t, repo, "git", "init", "-q")
	repo, err := vcs.FindMainRepoRootFrom(repo)
	if err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "config", "user.email", "test@test.com")
	run(t, repo, "git", "config", "user.name", "Test")
	writeUserHooks(t, user)
	writeRepoConfig(t, repo, repoConfig)
	return repo
}

func assertHookLog(t *testing.T, dir string, stderr string, want ...string) {
	t.Helper()
	if got := hookLog(t, dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("hook log = %q, want %q; stderr:\n%s", got, want, stderr)
	}
}

const everyEventRepoHooks = `[hooks]
post_create = ['echo repo-create>>hook-log.txt']
post_get = ['echo repo-get>>hook-log.txt']
post_return = ['echo repo-return>>hook-log.txt']
pre_destroy = ['echo repo-destroy>>hook-log.txt']
`

func everyEventUserHooks() Hooks {
	return Hooks{
		PostCreate: []string{appendHookCommand("user-create")},
		PostGet:    []string{appendHookCommand("user-get")},
		PostReturn: []string{appendHookCommand("user-return")},
		PreDestroy: []string{appendHookCommand("user-destroy")},
	}
}

func runEveryEvent(t *testing.T, worktree string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	for _, event := range []hooks.Event{hooks.PostCreate, hooks.PostGet, hooks.PostReturn, hooks.PreDestroy} {
		RunWorktreeHooks(event, worktree, &stdout, &stderr)
	}
	return stderr.String()
}

func TestRunWorktreeHooksRunsUserThenApprovedRepositoryCommands(t *testing.T) {
	repo := runHooksRepo(t, everyEventUserHooks(), everyEventRepoHooks)
	if _, _, err := Trust(repo); err != nil {
		t.Fatal(err)
	}
	stderr := runEveryEvent(t, repo)
	assertHookLog(t, repo, stderr,
		"user-create", "repo-create",
		"user-get", "repo-get",
		"user-return", "repo-return",
		"user-destroy", "repo-destroy",
	)
}

func TestRunWorktreeHooksWithoutApprovalRunsOnlyUserHooks(t *testing.T) {
	repo := runHooksRepo(t, everyEventUserHooks(), everyEventRepoHooks)
	stderr := runEveryEvent(t, repo)
	assertHookLog(t, repo, stderr, "user-create", "user-get", "user-return", "user-destroy")
}

// An approval that no longer matches the hooks table is the case a user has to
// act on, so it must still name the file and the command that fixes it.
func TestRunWorktreeHooksWarnsAndSkipsWhenApprovalDrifts(t *testing.T) {
	repo := runHooksRepo(t, everyEventUserHooks(), everyEventRepoHooks)
	if _, _, err := Trust(repo); err != nil {
		t.Fatal(err)
	}
	writeRepoConfig(t, repo, strings.Replace(everyEventRepoHooks, "repo-get", "drifted", 1))
	stderr := runEveryEvent(t, repo)
	assertHookLog(t, repo, stderr, "user-create", "user-get", "user-return", "user-destroy")
	for _, want := range []string{"treehouse.toml", "treehouse trust"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("drift warning does not mention %q; stderr:\n%s", want, stderr)
		}
	}
}

// A user hook can edit the user config, so approval must be read after those
// commands run rather than from the snapshot taken before them.
func TestRunWorktreeHooksHonorsApprovalChangedByAUserHook(t *testing.T) {
	for _, tc := range []struct {
		name         string
		trustStaged  bool
		wantCommands []string
	}{
		{name: "granted by a user hook", trustStaged: true, wantCommands: []string{"user-get", "repo-get"}},
		{name: "revoked by a user hook", wantCommands: []string{"user-get"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			staged := filepath.Join(t.TempDir(), "staged.toml")
			repo := runHooksRepo(t, Hooks{}, everyEventRepoHooks)
			user := Hooks{PostGet: []string{
				copyCommand(staged, userConfigPath()),
				appendHookCommand("user-get"),
			}}
			writeUserHooks(t, user)
			if !tc.trustStaged {
				// The staged config is the state before approval; the run
				// starts from the approved one.
				stageUserConfig(t, staged)
			}
			if _, _, err := Trust(repo); err != nil {
				t.Fatal(err)
			}
			if tc.trustStaged {
				// The staged config carries the approval; the run starts
				// without it.
				stageUserConfig(t, staged)
				writeUserHooks(t, user)
			}

			var stdout, stderr bytes.Buffer
			RunWorktreeHooks(hooks.PostGet, repo, &stdout, &stderr)
			assertHookLog(t, repo, stderr.String(), tc.wantCommands...)
		})
	}
}

func stageUserConfig(t *testing.T, staged string) {
	t.Helper()
	data, err := os.ReadFile(userConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Every repository command is verified on its own, so one that invalidates the
// approval stops the commands queued behind it.
func TestRunWorktreeHooksRechecksBeforeEachRepositoryCommand(t *testing.T) {
	for _, tc := range []struct{ name, invalidate string }{
		{name: "edits a listed dependency", invalidate: "echo edited>setup.sh"},
		{name: "revokes the approval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			staged := filepath.Join(t.TempDir(), "staged.toml")
			repo := runHooksRepo(t, Hooks{PostGet: []string{appendHookCommand("user-get")}}, "[hooks]\n")
			// userConfigPath resolves against the home runHooksRepo just set.
			invalidate := tc.invalidate
			if invalidate == "" {
				invalidate = copyCommand(staged, userConfigPath())
				stageUserConfig(t, staged)
			}
			writeRepoConfig(t, repo, "[hooks]\napproval_files = ['setup.sh']\n"+
				"post_get = ['echo repo-first>>hook-log.txt', '"+invalidate+"', 'echo repo-last>>hook-log.txt']\n")
			if err := os.WriteFile(filepath.Join(repo, "setup.sh"), []byte("approved\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Trust(repo); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			RunWorktreeHooks(hooks.PostGet, repo, &stdout, &stderr)
			assertHookLog(t, repo, stderr.String(), "user-get", "repo-first")
		})
	}
}

// Approval binds the bytes a listed dependency has in the directory commands
// actually execute in, not only in the owning main checkout.
func TestRunWorktreeHooksChecksExecutionWorktreeContents(t *testing.T) {
	const repoConfig = `[hooks]
approval_files = ['setup.sh']
post_get = ['echo repo-get>>hook-log.txt']
`
	for _, divergence := range []string{"changed", "missing"} {
		t.Run(divergence, func(t *testing.T) {
			user := Hooks{PostGet: []string{appendHookCommand("user-get")}}
			repo := runHooksRepo(t, user, repoConfig)
			// Approval compares dependency bytes between the main and the
			// execution worktree, so keep checkouts free of the host's
			// line-ending conversion.
			run(t, repo, "git", "config", "core.autocrlf", "false")
			dependency := filepath.Join(repo, "setup.sh")
			if err := os.WriteFile(dependency, []byte("approved\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			run(t, repo, "git", "add", "setup.sh")
			run(t, repo, "git", "commit", "-q", "-m", "Add hook dependency")
			worktree := filepath.Join(t.TempDir(), "slot")
			run(t, repo, "git", "worktree", "add", "-q", "--detach", worktree, "HEAD")
			if _, _, err := Trust(repo); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer
			RunWorktreeHooks(hooks.PostGet, worktree, &stdout, &stderr)
			assertHookLog(t, worktree, stderr.String(), "user-get", "repo-get")

			checkedOut := filepath.Join(worktree, "setup.sh")
			if divergence == "changed" {
				if err := os.WriteFile(checkedOut, []byte("different\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(checkedOut); err != nil {
				t.Fatal(err)
			}
			stderr.Reset()
			RunWorktreeHooks(hooks.PostGet, worktree, &stdout, &stderr)
			assertHookLog(t, worktree, stderr.String(), "user-get", "repo-get", "user-get")
			if !strings.Contains(stderr.String(), worktree) {
				t.Fatalf("warning does not name the execution worktree; stderr:\n%s", stderr.String())
			}
		})
	}
}
