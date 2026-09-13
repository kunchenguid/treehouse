package config

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestLoad_IgnoresRepoHooks(t *testing.T) {
	repoDir := t.TempDir()
	setUserHome(t, t.TempDir())

	cfgTOML := `max_trees = 4

[hooks]
post_create = ["echo a", "echo b"]
pre_destroy = ["echo c"]
`
	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte(cfgTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.MaxTrees != 4 {
		t.Errorf("MaxTrees: got %d, want 4", cfg.MaxTrees)
	}
	if len(cfg.Hooks.PostCreate) != 0 {
		t.Errorf("expected repo post_create hooks to be ignored, got %v", cfg.Hooks.PostCreate)
	}
	if len(cfg.Hooks.PreDestroy) != 0 {
		t.Errorf("expected repo pre_destroy hooks to be ignored, got %v", cfg.Hooks.PreDestroy)
	}
}

func TestLoad_UserHooks(t *testing.T) {
	repoDir := t.TempDir()
	userHome := t.TempDir()
	setUserHome(t, userHome)

	configDir := filepath.Join(userHome, ".config", "treehouse")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgTOML := `[hooks]
post_create = ["echo a", "echo b"]
pre_destroy = ["echo c"]
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(cfgTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	wantPost := []string{"echo a", "echo b"}
	wantPre := []string{"echo c"}
	if !reflect.DeepEqual(cfg.Hooks.PostCreate, wantPost) {
		t.Errorf("PostCreate: got %v, want %v", cfg.Hooks.PostCreate, wantPost)
	}
	if !reflect.DeepEqual(cfg.Hooks.PreDestroy, wantPre) {
		t.Errorf("PreDestroy: got %v, want %v", cfg.Hooks.PreDestroy, wantPre)
	}
}

func TestLoad_HooksDefaultEmpty(t *testing.T) {
	repoDir := t.TempDir()
	setUserHome(t, t.TempDir())

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(cfg.Hooks.PostCreate) != 0 {
		t.Errorf("expected empty PostCreate, got %v", cfg.Hooks.PostCreate)
	}
	if len(cfg.Hooks.PreDestroy) != 0 {
		t.Errorf("expected empty PreDestroy, got %v", cfg.Hooks.PreDestroy)
	}
}

func setUserHome(t *testing.T, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
}

func writeRepoConfig(t *testing.T, repoDir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWarnIfRepoHooksIgnored_NamesFileAndKeys(t *testing.T) {
	repoDir := t.TempDir()
	setUserHome(t, t.TempDir())
	writeRepoConfig(t, repoDir, `[hooks]
post_create = ["./scripts/setup.sh"]
pre_destroy = ["./scripts/teardown.sh"]
`)

	var buf bytes.Buffer
	warnIfRepoHooksIgnored(&buf, repoDir)

	got := buf.String()
	for _, want := range []string{
		filepath.Join(repoDir, "treehouse.toml"),
		"post_create",
		"pre_destroy",
		filepath.Join(".config", "treehouse", "config.toml"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warning does not mention %q; got:\n%s", want, got)
		}
	}
}

func TestWarnIfRepoHooksIgnored_NamesOnlyDeclaredKeys(t *testing.T) {
	repoDir := t.TempDir()
	setUserHome(t, t.TempDir())
	writeRepoConfig(t, repoDir, `[hooks]
pre_destroy = ["./scripts/teardown.sh"]
`)

	var buf bytes.Buffer
	warnIfRepoHooksIgnored(&buf, repoDir)

	got := buf.String()
	if !strings.Contains(got, "pre_destroy") {
		t.Errorf("warning does not mention pre_destroy; got:\n%s", got)
	}
	if strings.Contains(got, "post_create") {
		t.Errorf("warning names post_create, which the file never declared; got:\n%s", got)
	}
}

func TestWarnIfRepoHooksIgnored_SilentWithoutHooks(t *testing.T) {
	setUserHome(t, t.TempDir())

	tests := []struct {
		name     string
		contents string
		write    bool
	}{
		{name: "no repo config at all"},
		{name: "repo config without hooks", contents: "max_trees = 4\n", write: true},
		{name: "empty hooks table declares nothing", contents: "[hooks]\n", write: true},
		{name: "malformed repo config", contents: "invalid toml <<<\n", write: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir := t.TempDir()
			if tt.write {
				writeRepoConfig(t, repoDir, tt.contents)
			}

			var buf bytes.Buffer
			warnIfRepoHooksIgnored(&buf, repoDir)

			if got := buf.String(); got != "" {
				t.Errorf("expected no warning, got:\n%s", got)
			}
		})
	}
}

func TestWarnIfRepoHooksIgnored_WarnsOncePerFile(t *testing.T) {
	repoDir := t.TempDir()
	setUserHome(t, t.TempDir())
	writeRepoConfig(t, repoDir, `[hooks]
post_create = ["./scripts/setup.sh"]
`)

	var first, second bytes.Buffer
	warnIfRepoHooksIgnored(&first, repoDir)
	warnIfRepoHooksIgnored(&second, repoDir)

	if first.Len() == 0 {
		t.Fatal("expected the first call to warn")
	}
	if got := second.String(); got != "" {
		t.Errorf("expected the repeated warning to be deduped, got:\n%s", got)
	}
}

func TestLoad_WarnsAboutIgnoredRepoHooks(t *testing.T) {
	repoDir := t.TempDir()
	setUserHome(t, t.TempDir())
	writeRepoConfig(t, repoDir, `[hooks]
post_create = ["./scripts/setup.sh"]
`)

	stderr, err := captureStderr(t, func() error {
		_, err := Load(repoDir)
		return err
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !strings.Contains(stderr, "post_create") {
		t.Errorf("expected Load to warn about the ignored repo hooks, got:\n%s", stderr)
	}
}

// captureStderr redirects os.Stderr for the duration of fn and returns what was
// written to it.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	orig := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	err := fn()

	os.Stderr = orig
	if closeErr := w.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	out := <-done
	if closeErr := r.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return out, err
}
