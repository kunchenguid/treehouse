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
			otherConfig := ""
			if tt.configuredCwd {
				workDir = setupTestRepoWithHome(t, homeDir, "otherrepo")
				writeRepoHooksConfig(t, workDir)
				otherConfig = filepath.Join(workDir, "treehouse.toml")
			}

			_, stderr, code = runTreehouseFromDir(t, targetRepo, workDir, homeDir, nil, "destroy", wtPath)
			if code != 0 {
				t.Fatalf("destroy dry run failed (code %d): %s", code, stderr)
			}
			targetConfig := filepath.Join(targetRepo, "treehouse.toml")
			if !strings.Contains(stderr, targetConfig) {
				t.Errorf("warning does not name target config %q; got stderr:\n%s", targetConfig, stderr)
			}
			if otherConfig != "" && strings.Contains(stderr, otherConfig) {
				t.Errorf("warning incorrectly names current repository config %q; got stderr:\n%s", otherConfig, stderr)
			}
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
			otherConfig := filepath.Join(repoA, "treehouse.toml")
			if !strings.Contains(stderr, targetConfig) {
				t.Errorf("warning does not name target config %q; got stderr:\n%s", targetConfig, stderr)
			}
			if strings.Contains(stderr, otherConfig) {
				t.Errorf("warning incorrectly names other clone config %q; got stderr:\n%s", otherConfig, stderr)
			}
			if strings.Contains(stdout, targetConfig) {
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
	for _, repoDir := range []string{repoA, repoB} {
		configPath := filepath.Join(repoDir, "treehouse.toml")
		if got := strings.Count(stderr, configPath); got != 1 {
			t.Errorf("expected one warning naming %q, got %d in stderr:\n%s", configPath, got, stderr)
		}
	}
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
