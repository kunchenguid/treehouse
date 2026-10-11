package gitvcs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitRunnersIgnoreGlobalSubmoduleRecurse(t *testing.T) {
	base := t.TempDir()
	config := filepath.Join(base, "gitconfig")
	if err := os.WriteFile(config, []byte("[submodule]\n\trecurse = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("TREEHOUSE_ROOT", filepath.Join(base, "pool"))

	sub := initRepo(t)
	writeTestFile(t, sub, "f.txt", "submodule\n")
	gitRun(t, sub, "add", "f.txt")
	gitRun(t, sub, "commit", "-m", "submodule content")
	repo := initRepo(t)
	gitRun(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", sub, filepath.Join("vendor", "libfoo"))
	gitRun(t, repo, "commit", "-m", "add submodule")

	runners := []struct {
		name string
		run  func(string, ...string) ([]byte, error)
	}{
		{"runGit", func(dir string, args ...string) ([]byte, error) {
			out, err := runGit(dir, args...)
			return []byte(out), err
		}},
		{"runGitRaw", runGitRaw},
		{"gitOutput", func(dir string, args ...string) ([]byte, error) {
			return gitOutput(dir, nil, args...)
		}},
		{"gitOutputEnv", func(dir string, args ...string) ([]byte, error) {
			return gitOutputEnv(dir, os.Environ(), nil, args...)
		}},
	}
	for _, runner := range runners {
		t.Run(runner.name, func(t *testing.T) {
			worktree := filepath.Join(t.TempDir(), "worktree")
			if err := AddWorktree(repo, worktree, "main"); err != nil {
				t.Fatal(err)
			}
			if _, err := runner.run(worktree, "read-tree", "--reset", "-u", "HEAD"); err != nil {
				t.Fatalf("reset with global submodule.recurse=true: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(worktree, "vendor", "libfoo", ".git")); !os.IsNotExist(err) {
				t.Fatalf("reset left a submodule gitfile: %v", err)
			}
			out, err := runner.run(worktree, "status", "--porcelain", "--untracked-files=all")
			if err != nil || len(out) != 0 {
				t.Fatalf("expected clean worktree after reset, got %q, %v", out, err)
			}
			if _, err := runner.run(worktree, "checkout", "--detach"); err != nil {
				t.Fatalf("detach with global submodule.recurse=true: %v", err)
			}
		})
	}
}
