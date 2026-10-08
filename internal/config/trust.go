package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/kunchenguid/treehouse/v3/internal/vcs"
)

// RepoIdentity is shared by pool naming and hook trust. It uses the origin URL,
// falling back to the main repository's absolute path for local repositories.
func RepoIdentity(repoRoot string) string {
	if remote, err := vcs.GetRemoteURL(repoRoot); err == nil && remote != "" {
		return remote
	}
	return repoRoot
}

// readRepoConfig hashes the entire parsed hooks table, including unknown keys.
// Formatting and unrelated settings do not invalidate trust; command order does.
// Decode and hash the same bytes so a concurrent edit cannot bless other commands.
func readRepoConfig(repoRoot string) (Config, toml.MetaData, string, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(filepath.Join(repoRoot, "treehouse.toml"))
	if err != nil {
		return cfg, toml.MetaData{}, "", err
	}
	cfg, raw, md, err := decodeConfig(data)
	if err != nil {
		return cfg, md, "", err
	}
	encoded, err := json.Marshal(raw["hooks"])
	if err != nil {
		return cfg, md, "", err
	}
	return cfg, md, fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)), nil
}

func effectiveHooks(repoRoot string, repo Hooks, md toml.MetaData, hash string, user Config, w io.Writer) Hooks {
	if !repoHooksApproved(repoRoot, md, hash, user, w) {
		return user.Hooks
	}
	return Hooks{
		PostCreate: appendCommands(user.Hooks.PostCreate, repo.PostCreate),
		PreDestroy: appendCommands(user.Hooks.PreDestroy, repo.PreDestroy),
		PostGet:    appendCommands(user.Hooks.PostGet, repo.PostGet),
		PostReturn: appendCommands(user.Hooks.PostReturn, repo.PostReturn),
	}
}

func appendCommands(user, repo []string) []string {
	return append(append([]string(nil), user...), repo...)
}

// repoHooksApproved matches the immutable command-table snapshot against the
// user-owned approval for the target's owning repository.
func repoHooksApproved(repoRoot string, md toml.MetaData, tableHash string, user Config, w io.Writer) bool {
	if user.TrustedRepos[RepoIdentity(repoRoot)] != tableHash {
		warnRepoHooks(w, filepath.Join(repoRoot, "treehouse.toml"), md)
		return false
	}
	return true
}

// Trust records approval for the current hooks table without running commands.
// An already matching approval is an error because no state changed.
func Trust(repoRoot string) (string, string, error) {
	_, md, hash, err := readRepoConfig(repoRoot)
	if err != nil {
		return "", "", err
	}
	if len(declaredHookKeys(md)) == 0 {
		return "", "", fmt.Errorf("no lifecycle hooks declared in %s", filepath.Join(repoRoot, "treehouse.toml"))
	}
	identity := RepoIdentity(repoRoot)
	err = updateTrust(identity, hash)
	return identity, hash, err
}

// Untrust also works after hooks have been deleted or the repo config is broken.
func Untrust(repoRoot string) (string, error) {
	identity := RepoIdentity(repoRoot)
	return identity, updateTrust(identity, "")
}

func updateTrust(identity, hash string) error {
	path := userConfigPath()
	if path == "" {
		return fmt.Errorf("cannot resolve user config directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Follow an existing user config symlink so a dotfiles-managed config keeps
	// its link. A dangling link is an error, not permission to replace it.
	if _, err := os.Lstat(path); err == nil {
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Refuse concurrent writers rather than silently losing another approval.
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot lock user config %s: %w", lockPath, err)
	}
	defer os.Remove(lockPath)
	if err := lock.Close(); err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	cfg, raw, _, err := decodeConfig(data)
	if err != nil {
		return err
	}
	if hash == "" {
		if _, exists := cfg.TrustedRepos[identity]; !exists {
			return fmt.Errorf("repository %q is not trusted", identity)
		}
	} else if cfg.TrustedRepos[identity] == hash {
		return fmt.Errorf("current hooks for repository %q are already trusted", identity)
	}
	if cfg.TrustedRepos == nil {
		cfg.TrustedRepos = make(map[string]string)
	}
	if hash == "" {
		delete(cfg.TrustedRepos, identity)
	} else {
		cfg.TrustedRepos[identity] = hash
	}
	raw["trusted_repos"] = cfg.TrustedRepos
	// Keep unknown user settings instead of round-tripping only known fields.
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
