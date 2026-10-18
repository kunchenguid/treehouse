package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/config"
)

func TestGetLeftoverSlotDirectory(t *testing.T) {
	for _, layout := range []string{"builtin", "templated"} {
		for _, contents := range []string{"empty", "nonempty"} {
			t.Run(layout+"/"+contents, func(t *testing.T) {
				repo, home := setupTestRepo(t)
				pool, err := config.ResolvePoolDir(repo, home)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(pool, "1", filepath.Base(repo))
				args := []string{"get", "--lease"}
				if layout == "templated" {
					path = filepath.Join(filepath.Dir(repo), "myrepo-1")
					args = append(args, "--worktree-path", "{repo_parent}/{repo}-{slot}")
				}
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(path, "work.txt")
				if contents == "nonempty" {
					if err := os.WriteFile(file, []byte("keep"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				stdout, stderr, code := runTreehouse(t, repo, home, nil, args...)
				if layout == "builtin" && contents == "nonempty" {
					if code == 0 || !strings.Contains(stderr, path) || !strings.Contains(stderr, "inspect") {
						t.Fatalf("expected actionable failure, code=%d stdout=%q stderr=%q", code, stdout, stderr)
					}
					if strings.TrimSpace(stdout) != "" {
						t.Fatalf("failed get returned a path: %q", stdout)
					}
				} else {
					if code != 0 {
						t.Fatalf("get failed (%d): %s", code, stderr)
					}
					want := path
					if layout == "templated" {
						want = filepath.Join(filepath.Dir(repo), "myrepo-2")
					}
					if strings.TrimSpace(stdout) != want {
						t.Fatalf("get returned %q, want %s", stdout, want)
					}
					if _, err := os.Stat(filepath.Join(want, ".git")); err != nil {
						t.Fatal(err)
					}
					if layout == "templated" {
						if !strings.Contains(stderr, path) {
							t.Fatalf("skip omitted occupied path: %s", stderr)
						}
						if entries, err := os.ReadDir(path); err != nil || (contents == "empty" && len(entries) != 0) {
							t.Fatalf("occupant changed: %v, %v", entries, err)
						}
					}
				}
				if contents == "nonempty" {
					if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
						t.Fatalf("work changed: %q, %v", data, err)
					}
				}
			})
		}
	}
}
