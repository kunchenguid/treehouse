package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRepoHooksConfig writes a repo-level treehouse.toml declaring lifecycle
// hooks: the configuration that treehouse deliberately ignores and, before the
// warning existed, discarded without a word.
func writeRepoHooksConfig(t *testing.T, repoDir string) {
	t.Helper()
	contents := `[hooks]
post_create = ["./scripts/setup.sh"]
pre_destroy = ["./scripts/teardown.sh"]
`
	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertWarnsAboutIgnoredHooks(t *testing.T, stderr string) {
	t.Helper()
	for _, want := range []string{"[hooks]", "treehouse.toml", "post_create", "pre_destroy", "config.toml"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("expected the ignored-hooks warning to mention %q, got stderr:\n%s", want, stderr)
		}
	}
}

func ignoredHooksWarningPaths(stderr string) []string {
	const prefix = "ignoring [hooks] in "
	var paths []string
	for _, line := range strings.Split(stderr, "\n") {
		start := strings.Index(line, prefix)
		if start < 0 {
			continue
		}
		rest := line[start+len(prefix):]
		end := strings.LastIndex(rest, ": ")
		if end >= 0 {
			paths = append(paths, rest[:end])
		}
	}
	return paths
}

func assertWarningPathsReferTo(t *testing.T, stderr string, expected ...string) {
	t.Helper()
	actual := ignoredHooksWarningPaths(stderr)
	if len(actual) != len(expected) {
		t.Fatalf("expected %d ignored-hooks warning paths, got %d in stderr:\n%s", len(expected), len(actual), stderr)
	}

	matched := make([]bool, len(actual))
	for _, expectedPath := range expected {
		expectedInfo, err := os.Stat(expectedPath)
		if err != nil {
			t.Fatalf("stat expected warning path %q: %v", expectedPath, err)
		}
		found := false
		for i, actualPath := range actual {
			if matched[i] {
				continue
			}
			actualInfo, err := os.Stat(actualPath)
			if err == nil && os.SameFile(expectedInfo, actualInfo) {
				matched[i] = true
				found = true
				break
			}
		}
		if !found {
			t.Errorf("warning does not name config %q; got stderr:\n%s", expectedPath, stderr)
		}
	}
}

func TestGetLeaseWarnsAboutIgnoredRepoHooksOnStderrOnly(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	writeRepoHooksConfig(t, repoDir)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}

	assertWarnsAboutIgnoredHooks(t, stderr)

	// stdout stays exactly the leased path: the warning must never pollute
	// the contract machine callers depend on.
	wtPath := strings.TrimSpace(stdout)
	if stdout != wtPath+"\n" {
		t.Fatalf("expected stdout to be exactly the leased path, got %q", stdout)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected stdout to name a real worktree: %v", err)
	}
}

func TestDestroyWarnsAboutIgnoredRepoHooks(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)

	// Declared only now, so the warning cannot have been carried over from
	// the acquiring command: destroy has to find it on its own. Destroy
	// resolves the pool by path and never loads repo-level config, which is
	// how the silent discard shipped.
	writeRepoHooksConfig(t, repoDir)

	_, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "destroy", wtPath, "--include-leased", "--yes")
	if code != 0 {
		t.Fatalf("destroy failed (code %d): %s", code, stderr)
	}

	assertWarnsAboutIgnoredHooks(t, stderr)

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree directory still exists after destroy: %s", wtPath)
	}
}

func TestDestroyWarnsForTargetRepositoryOutsideIt(t *testing.T) {
	tests := []struct {
		name          string
		configuredCwd bool
	}{
		{name: "from non-repository directory"},
		{name: "from another configured repository", configuredCwd: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targetRepo, homeDir := setupTestRepo(t)
			stdout, stderr, code := runTreehouse(t, targetRepo, homeDir, nil, "get", "--lease")
			if code != 0 {
				t.Fatalf("get --lease failed (code %d): %s", code, stderr)
			}
			wtPath := strings.TrimSpace(stdout)
			writeRepoHooksConfig(t, targetRepo)

			workDir := t.TempDir()
			if tt.configuredCwd {
				workDir = setupTestRepoWithHome(t, homeDir, "otherrepo")
				writeRepoHooksConfig(t, workDir)
			}

			_, stderr, code = runTreehouseFromDir(t, targetRepo, workDir, homeDir, nil, "destroy", wtPath)
			if code != 0 {
				t.Fatalf("destroy dry run failed (code %d): %s", code, stderr)
			}
			targetConfig := filepath.Join(targetRepo, "treehouse.toml")
			assertWarningPathsReferTo(t, stderr, targetConfig)
		})
	}
}

func TestDestroyWarnsForCorrectRepositoryInSharedPool(t *testing.T) {
	for _, tt := range []struct {
		name string
		all  bool
	}{
		{name: "single worktree"},
		{name: "bulk pool", all: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repoA, homeDir := setupTestRepo(t)
			remote := gitCmd(t, repoA, "config", "--get", "remote.origin.url")
			repoB := filepath.Join(t.TempDir(), "myrepo")
			gitCmd(t, "", "clone", remote, repoB)

			stdout, stderr, code := runTreehouse(t, repoA, homeDir, nil, "get", "--lease")
			if code != 0 {
				t.Fatalf("first get --lease failed (code %d): %s", code, stderr)
			}
			wtA := strings.TrimSpace(stdout)

			stdout, stderr, code = runTreehouse(t, repoB, homeDir, nil, "get", "--lease")
			if code != 0 {
				t.Fatalf("second get --lease failed (code %d): %s", code, stderr)
			}
			wtB := strings.TrimSpace(stdout)
			if filepath.Dir(filepath.Dir(wtA)) != filepath.Dir(filepath.Dir(wtB)) {
				t.Fatalf("expected clones to share a pool, got %s and %s", wtA, wtB)
			}

			writeRepoHooksConfig(t, repoB)
			target := wtB
			args := []string{"destroy", target}
			if tt.all {
				target = filepath.Dir(filepath.Dir(wtB))
				args = []string{"destroy", target, "--all"}
			}

			stdout, stderr, code = runTreehouseFromDir(t, repoA, t.TempDir(), homeDir, nil, args...)
			if code != 0 {
				t.Fatalf("destroy dry run failed (code %d): %s", code, stderr)
			}
			targetConfig := filepath.Join(repoB, "treehouse.toml")
			assertWarningPathsReferTo(t, stderr, targetConfig)
			if len(ignoredHooksWarningPaths(stdout)) != 0 {
				t.Errorf("warning polluted stdout:\n%s", stdout)
			}
		})
	}
}

func TestDestroyAllWarnsOnceForEachRepositoryInLockedTargetSet(t *testing.T) {
	repoA, homeDir := setupTestRepo(t)
	remote := gitCmd(t, repoA, "config", "--get", "remote.origin.url")
	repoB := filepath.Join(t.TempDir(), "myrepo")
	gitCmd(t, "", "clone", remote, repoB)

	stdout, stderr, code := runTreehouse(t, repoA, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("first get --lease failed (code %d): %s", code, stderr)
	}
	wtA := strings.TrimSpace(stdout)
	stdout, stderr, code = runTreehouse(t, repoB, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("second get --lease failed (code %d): %s", code, stderr)
	}
	wtB := strings.TrimSpace(stdout)
	poolDir := filepath.Dir(filepath.Dir(wtA))
	if poolDir != filepath.Dir(filepath.Dir(wtB)) {
		t.Fatalf("expected clones to share a pool, got %s and %s", wtA, wtB)
	}

	writeRepoHooksConfig(t, repoA)
	writeRepoHooksConfig(t, repoB)
	_, stderr, code = runTreehouseFromDir(t, repoA, t.TempDir(), homeDir, nil, "destroy", poolDir, "--all")
	if code != 0 {
		t.Fatalf("destroy dry run failed (code %d): %s", code, stderr)
	}
	assertWarningPathsReferTo(t, stderr,
		filepath.Join(repoA, "treehouse.toml"),
		filepath.Join(repoB, "treehouse.toml"),
	)
}

func TestDestroySurvivesUnparsableRepoConfig(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)

	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("invalid toml <<<\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Destroy resolves its pool by path and must keep working against a
	// repository whose config no longer parses; the warning is best-effort
	// and must not turn a broken repo config into a failed destroy.
	stdout, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "destroy", wtPath, "--include-leased", "--yes")
	if code != 0 {
		t.Fatalf("destroy failed against an unparsable repo config (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Destroyed 1 worktree") {
		t.Fatalf("expected destroyed summary, got stdout:\n%s", stdout)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree directory still exists after destroy: %s", wtPath)
	}
}
