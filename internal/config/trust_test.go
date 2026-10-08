package config

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func writeTrustUserConfig(t *testing.T, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(userConfigPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfigPath(), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTrustLifecycle(t *testing.T) {
	setUserHome(t, t.TempDir())
	repo := t.TempDir()
	writeTrustUserConfig(t, `max_trees = 7
[future_setting]
keep = ["unknown", "values"]
[hooks]
post_create = ["user create"]
pre_destroy = ["user destroy"]
post_get = ["user get"]
post_return = ["user return"]
`)
	writeRepoConfig(t, repo, `[hooks]
post_create = ["repo create"]
pre_destroy = ["repo destroy"]
post_get = ["repo get"]
post_return = ["repo return"]
`)
	userHooks := Hooks{PostCreate: []string{"user create"}, PreDestroy: []string{"user destroy"}, PostGet: []string{"user get"}, PostReturn: []string{"user return"}}
	cfg, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Hooks, userHooks) {
		t.Fatalf("untrusted hooks: %+v", cfg.Hooks)
	}
	identity, hash, err := Trust(repo)
	if err != nil {
		t.Fatal(err)
	}
	if identity != repo || !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("trust = %q, %q", identity, hash)
	}
	cfg, err = Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := Hooks{PostCreate: []string{"user create", "repo create"}, PreDestroy: []string{"user destroy", "repo destroy"}, PostGet: []string{"user get", "repo get"}, PostReturn: []string{"user return", "repo return"}}
	if !reflect.DeepEqual(cfg.Hooks, want) {
		t.Fatalf("trusted hooks: %+v, want %+v", cfg.Hooks, want)
	}
	warnedRepoHooks.Delete(filepath.Join(repo, "treehouse.toml"))
	var warning bytes.Buffer
	warnIfRepoHooksIgnored(&warning, repo)
	if warning.Len() != 0 {
		t.Fatalf("trusted config warned: %s", warning.String())
	}
	before, err := os.ReadFile(userConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Trust(repo); err == nil {
		t.Fatal("repeated trust should report no change")
	}
	after, err := os.ReadFile(userConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("no-op trust rewrote config")
	}
	if _, err := Untrust(repo); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Hooks, userHooks) {
		t.Fatalf("revoked hooks: %+v", cfg.Hooks)
	}
	if _, err := Untrust(repo); err == nil {
		t.Fatal("repeated untrust should report no change")
	}
	var raw map[string]any
	if _, err := toml.DecodeFile(userConfigPath(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["max_trees"] != int64(7) || raw["future_setting"] == nil {
		t.Fatalf("lost user settings: %+v", raw)
	}
}

func TestTrustHashDrift(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		trusted      bool
	}{
		{"formatting and settings", "max_trees = 3\n[hooks] # comment\npost_get=['echo one', 'echo two']\n", true},
		{"command changed", "[hooks]\npost_get=['echo changed', 'echo two']\n", false},
		{"order changed", "[hooks]\npost_get=['echo two', 'echo one']\n", false},
		{"new event", "[hooks]\npost_get=['echo one', 'echo two']\npost_return=['echo returned']\n", false},
		{"unknown key", "[hooks]\npost_get=['echo one', 'echo two']\nfuture=['echo future']\n", false},
		{"removed command", "[hooks]\npost_get=['echo one']\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setUserHome(t, t.TempDir())
			repo := t.TempDir()
			writeRepoConfig(t, repo, "[hooks]\npost_get = [\"echo one\", \"echo two\"]\n")
			if _, _, err := Trust(repo); err != nil {
				t.Fatal(err)
			}
			writeRepoConfig(t, repo, tc.config)
			cfg, err := Load(repo)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(cfg.Hooks.PostGet) != 0; got != tc.trusted {
				t.Fatalf("trusted = %v, want %v", got, tc.trusted)
			}
			if !tc.trusted {
				if _, _, err := Trust(repo); err != nil {
					t.Fatalf("retrust: %v", err)
				}
				cfg, err = Load(repo)
				if err != nil || len(cfg.Hooks.PostGet) == 0 {
					t.Fatalf("retrust did not enable hooks: %+v, %v", cfg, err)
				}
			}
		})
	}
}

func TestRepoCannotGrantTrust(t *testing.T) {
	setUserHome(t, t.TempDir())
	repo := t.TempDir()
	writeRepoConfig(t, repo, "[hooks]\npost_get=['echo repo']\n")
	_, _, hash, err := readRepoConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(Config{Hooks: Hooks{PostGet: []string{"echo repo"}}, TrustedRepos: map[string]string{repo: hash}}); err != nil {
		t.Fatal(err)
	}
	writeRepoConfig(t, repo, buf.String())
	cfg, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks.PostGet) != 0 || len(cfg.TrustedRepos) != 0 {
		t.Fatalf("repo granted trust: %+v", cfg)
	}
}

func TestTrustUsesOriginIdentityAndHash(t *testing.T) {
	setUserHome(t, t.TempDir())
	repos := []string{t.TempDir(), t.TempDir()}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	for _, repo := range repos {
		git(repo, "init")
		git(repo, "remote", "add", "origin", "https://example.invalid/owner/repo.git")
		writeRepoConfig(t, repo, "[hooks]\npost_get=['echo shared']\n")
	}
	if _, _, err := Trust(repos[0]); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(repos[1])
	if err != nil || len(cfg.Hooks.PostGet) != 1 {
		t.Fatalf("same identity/hash: %+v, %v", cfg, err)
	}
	git(repos[1], "remote", "set-url", "origin", "https://example.invalid/other/repo.git")
	cfg, err = Load(repos[1])
	if err != nil || len(cfg.Hooks.PostGet) != 0 {
		t.Fatalf("different identity: %+v, %v", cfg, err)
	}
}

func TestTrustInvalidConfigAndLockPreserveUserFile(t *testing.T) {
	setUserHome(t, t.TempDir())
	repo := t.TempDir()
	writeRepoConfig(t, repo, "[hooks]\npost_get=['echo repo']\n")
	for _, contents := range []string{"not valid [toml", "[trusted_repos]\ninvalid=42\n"} {
		writeTrustUserConfig(t, contents)
		if _, _, err := Trust(repo); err == nil {
			t.Fatal("trusted with broken user config")
		}
		data, err := os.ReadFile(userConfigPath())
		if err != nil || string(data) != contents {
			t.Fatalf("user config changed: %q, %v", data, err)
		}
	}
	writeTrustUserConfig(t, "max_trees=5\n")
	if err := os.WriteFile(userConfigPath()+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Trust(repo); err == nil {
		t.Fatal("ignored config lock")
	}
}

func TestUntrustWithBrokenRepoConfig(t *testing.T) {
	setUserHome(t, t.TempDir())
	repo := t.TempDir()
	writeRepoConfig(t, repo, "[hooks]\npost_get=['echo repo']\n")
	if _, _, err := Trust(repo); err != nil {
		t.Fatal(err)
	}
	writeRepoConfig(t, repo, "not valid [toml")
	if _, err := Untrust(repo); err != nil {
		t.Fatal(err)
	}
}

func TestTrustWithoutHooksDoesNotCreateConfig(t *testing.T) {
	setUserHome(t, t.TempDir())
	repo := t.TempDir()
	writeRepoConfig(t, repo, "max_trees=3\n")
	if _, _, err := Trust(repo); err == nil {
		t.Fatal("trusted without hooks")
	}
	if _, err := os.Stat(userConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("unexpected user config: %v", err)
	}
}

func TestTrustPreservesUserConfigSymlink(t *testing.T) {
	setUserHome(t, t.TempDir())
	repo := t.TempDir()
	writeRepoConfig(t, repo, "[hooks]\npost_get=['echo repo']\n")
	target := filepath.Join(t.TempDir(), "treehouse.toml")
	if err := os.WriteFile(target, []byte("max_trees=6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(userConfigPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, userConfigPath()); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := Trust(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := Untrust(repo); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(userConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("trust replaced the config symlink")
	}
	cfg, err := LoadGlobal()
	if err != nil || cfg.MaxTrees != 6 {
		t.Fatalf("lost target config: %+v, %v", cfg, err)
	}
}

// A table the struct decoder merges into Config.Hooks must never sit outside
// the table covered by the approval hash, regardless of spelling or TOML form.
func TestTrustRejectsHookTableAliases(t *testing.T) {
	const approved = "[hooks]\npost_get=['echo approved']\n"
	cases := map[string]string{
		"title case after":    approved + "[Hooks]\npost_create=['echo unapproved']\n",
		"title case before":   "[Hooks]\npost_create=['echo unapproved']\n" + approved,
		"uppercase":           approved + "[HOOKS]\npost_return=['echo unapproved']\n",
		"mixed case":          approved + "[hOoKs]\npre_destroy=['echo unapproved']\n",
		"Unicode Kelvin sign": approved + "[\"hooKs\"]\npost_get=['echo unapproved']\n",
		"Unicode long s":      approved + "[\"hookſ\"]\npost_create=['echo unapproved']\n",
		"dotted key":          "Hooks.post_create=['echo unapproved']\n" + approved,
		"inline table":        "Hooks={post_create=['echo unapproved']}\n" + approved,
		"alias only":          "[Hooks]\npost_create=['echo unapproved']\n",
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			setUserHome(t, t.TempDir())
			repo := t.TempDir()
			writeRepoConfig(t, repo, approved)
			if _, _, err := Trust(repo); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(userConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			writeRepoConfig(t, repo, contents)
			cfg, err := Load(repo)
			if err == nil {
				t.Fatalf("accepted unapproved hook alias: %+v", cfg.Hooks)
			}
			if !strings.Contains(err.Error(), "[hooks]") {
				t.Fatalf("missing canonical table guidance: %v", err)
			}
			if _, _, err := Trust(repo); err == nil {
				t.Fatal("trust accepted ambiguous hook tables")
			}
			after, err := os.ReadFile(userConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("rejected config changed stored approvals")
			}
		})
	}
}
