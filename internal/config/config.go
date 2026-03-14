package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/atinylittleshell/treehouse/internal/git"
)

type Config struct {
	MaxTrees int `toml:"max_trees"`
}

func DefaultConfig() Config {
	return Config{
		MaxTrees: 16,
	}
}

func Load(repoRoot string) (Config, error) {
	cfg := DefaultConfig()

	paths := []string{
		filepath.Join(repoRoot, "treehouse.toml"),
	}

	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "treehouse", "config.toml"))
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			if _, err := toml.DecodeFile(p, &cfg); err != nil {
				return cfg, err
			}
			return cfg, nil
		}
	}

	return cfg, nil
}

func ResolvePoolDir(repoRoot string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	remoteURL, err := git.GetRemoteURL(repoRoot)
	if err != nil {
		return "", err
	}

	repoName := filepath.Base(repoRoot)
	shortHash := git.ShortHash(remoteURL)

	return filepath.Join(home, ".treehouse", repoName+"-"+shortHash), nil
}
