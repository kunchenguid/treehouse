package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/kunchenguid/treehouse/internal/git"
)

type Config struct {
	MaxTrees int    `toml:"max_trees"`
	Root     string `toml:"root"`
	Hooks    Hooks  `toml:"hooks,omitempty"`
}

type Hooks struct {
	PostCreate []string `toml:"post_create,omitempty"`
	PreDestroy []string `toml:"pre_destroy,omitempty"`
}

// RootEnvVar is the environment variable that overrides the configured
// worktree root. It sits below the --root flag but above repo/user config in
// the resolution precedence (see ResolveRoot).
const RootEnvVar = "TREEHOUSE_ROOT"

func DefaultConfig() Config {
	return Config{
		MaxTrees: 16,
	}
}

// ResolveRoot returns the effective worktree root, honoring override precedence:
// an explicit flag value, then the TREEHOUSE_ROOT environment variable, then the
// root from repo/user config, then the built-in default (empty string, which
// ResolvePoolRoot maps to ~/.treehouse). It keeps ResolvePoolRoot pure while
// letting callers layer command-line and environment overrides on top of the
// loaded config. A relative override is resolved from the repo root exactly like
// a relative config root, so `--root .` selects an in-project pool.
func ResolveRoot(flagRoot string, cfg Config) string {
	if flagRoot != "" {
		return flagRoot
	}
	if env := os.Getenv(RootEnvVar); env != "" {
		return env
	}
	return cfg.Root
}

func Load(repoRoot string) (Config, error) {
	cfg := DefaultConfig()

	repoPath := filepath.Join(repoRoot, "treehouse.toml")
	hasRepoConfig := false
	if _, err := os.Stat(repoPath); err == nil {
		hasRepoConfig = true
		if _, err := toml.DecodeFile(repoPath, &cfg); err != nil {
			return cfg, err
		}
		cfg.Hooks = Hooks{}
	}

	userCfg, hasUserConfig, err := loadUser()
	if err != nil {
		return cfg, err
	}
	if hasUserConfig {
		if !hasRepoConfig {
			cfg = userCfg
		} else {
			cfg.Hooks = userCfg.Hooks
		}
	}

	return cfg, nil
}

// LoadGlobal returns the default configuration merged with user-level config.
// It intentionally ignores repo-level config because callers may run without a
// repository context.
func LoadGlobal() (Config, error) {
	cfg := DefaultConfig()
	userCfg, hasUserConfig, err := loadUser()
	if err != nil {
		return cfg, err
	}
	if hasUserConfig {
		cfg = userCfg
	}
	return cfg, nil
}

func loadUser() (Config, bool, error) {
	cfg := DefaultConfig()
	if home, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(home, ".config", "treehouse", "config.toml")
		if _, err := os.Stat(userPath); err == nil {
			if _, err := toml.DecodeFile(userPath, &cfg); err != nil {
				return cfg, false, err
			}
			return cfg, true, nil
		}
	}

	return cfg, false, nil
}

func ResolvePoolDir(repoRoot string, root string) (string, error) {
	// Use remote URL for the hash when available; fall back to the
	// absolute repo path for purely-local repositories.
	hashInput, err := git.GetRemoteURL(repoRoot)
	if err != nil {
		hashInput = repoRoot
	}

	repoName := filepath.Base(repoRoot)
	shortHash := git.ShortHash(hashInput)
	poolName := repoName + "-" + shortHash

	poolRoot, err := ResolvePoolRoot(repoRoot, root)
	if err != nil {
		return "", err
	}
	return filepath.Join(poolRoot, poolName), nil
}

// ResolvePoolRoot resolves the directory that contains per-repository pools.
// Relative roots require repoRoot because they are resolved from the repository
// root.
func ResolvePoolRoot(repoRoot string, root string) (string, error) {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".treehouse"), nil
	}

	expanded := os.ExpandEnv(root)
	if !filepath.IsAbs(expanded) {
		if repoRoot == "" {
			return "", fmt.Errorf("relative treehouse root %q requires a repository", root)
		}
		expanded = filepath.Join(repoRoot, expanded)
	}
	return filepath.Join(expanded, ".treehouse"), nil
}
