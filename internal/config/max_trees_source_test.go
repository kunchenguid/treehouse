package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadRecordsMaxTreesSource checks Load's precedence (repo file, else
// user file, else default) and that the raise advice names the file Load
// already reads, so following it cannot shadow other user settings.
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
	check := func(name string, wantMax int, wantOrigin MaxTreesOrigin, wantDescribe, wantAdviceFile string) {
		t.Helper()
		cfg, err := Load(repo)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		src := cfg.MaxTreesSource
		if cfg.MaxTrees != wantMax || src.Origin != wantOrigin {
			t.Fatalf("%s: got %d from %+v, want %d from %s", name, cfg.MaxTrees, src, wantMax, wantOrigin)
		}
		if got := src.Describe(); got != wantDescribe {
			t.Fatalf("%s: Describe() = %q, want %q", name, got, wantDescribe)
		}
		if got, want := src.RaiseAdvice(repo), "set max_trees to a higher value in "+wantAdviceFile; got != want {
			t.Fatalf("%s: RaiseAdvice() = %q, want %q", name, got, want)
		}
	}

	check("no files", 16, MaxTreesDefault, "built-in default", repoPath)

	write(userPath, "max_trees = 5\n")
	check("user only", 5, MaxTreesUser, "user config "+userPath, userPath)

	// Creating a repo file here would shadow root, so the advice must
	// name the user file instead.
	write(userPath, "root = \"x\"\n")
	check("user file without key", 16, MaxTreesDefault, "built-in default", userPath)

	write(userPath, "max_trees = 5\n")
	write(repoPath, "max_trees = 3\n")
	check("repo wins", 3, MaxTreesRepo, "repo config "+repoPath, repoPath)

	// Existing behavior kept: a repo file without the key shadows user config.
	write(repoPath, "base_branch = \"main\"\n")
	check("repo file without key", 16, MaxTreesDefault, "built-in default", repoPath)
}
