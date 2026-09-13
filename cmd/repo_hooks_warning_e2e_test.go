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
