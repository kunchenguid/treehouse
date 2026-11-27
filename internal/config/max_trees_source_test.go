package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRecordsMaxTreesSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	repo := t.TempDir()
	userPath := filepath.Join(home, ".config", "treehouse", "config.toml")
	repoPath := filepath.Join(repo, "treehouse.toml")

	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check := func(name string, wantMax int, want MaxTreesSource) {
		t.Helper()
		cfg, err := Load(repo)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cfg.MaxTrees != wantMax || cfg.MaxTreesSource != want {
			t.Fatalf("%s: got %d from %+v, want %d from %+v", name, cfg.MaxTrees, cfg.MaxTreesSource, wantMax, want)
		}
	}

	check("no files", 16, MaxTreesSource{Origin: MaxTreesDefault})

	write(userPath, "max_trees = 5\n")
	check("user only", 5, MaxTreesSource{Origin: MaxTreesUser, File: userPath})

	write(userPath, "root = \"x\"\n")
	check("user file without key", 16, MaxTreesSource{Origin: MaxTreesDefault})

	write(userPath, "max_trees = 5\n")
	write(repoPath, "max_trees = 3\n")
	check("repo wins", 3, MaxTreesSource{Origin: MaxTreesRepo, File: repoPath})

	// Existing behavior kept: a repo file without the key shadows user config.
	write(repoPath, "base_branch = \"main\"\n")
	check("repo file without key", 16, MaxTreesSource{Origin: MaxTreesDefault})
}

func TestRaiseAdviceNamesFileToEdit(t *testing.T) {
	repo := t.TempDir()
	repoFile := filepath.Join(repo, "treehouse.toml")
	userFile := filepath.Join(t.TempDir(), "config.toml")

	cases := []struct {
		name string
		src  MaxTreesSource
		want string
	}{
		{"default points at repo file Load honors first", MaxTreesSource{Origin: MaxTreesDefault}, repoFile},
		{"repo origin names its file", MaxTreesSource{Origin: MaxTreesRepo, File: repoFile}, repoFile},
		{"user origin names its file", MaxTreesSource{Origin: MaxTreesUser, File: userFile}, userFile},
	}
	for _, c := range cases {
		if got, want := c.src.RaiseAdvice(repo), "set max_trees to a higher value in "+c.want; got != want {
			t.Errorf("%s: got %q, want %q", c.name, got, want)
		}
	}
}
