package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSchemaAliasesRejectedBeforeConfigReads(t *testing.T) {
	for name, contents := range map[string]string{
		"max_trees": "Max_Trees=7\n", "root": "Root='.'\n",
		"base_branch": "Base_Branch='main'\n", "worktree_path": "Worktree_Path='{pool}/{slot}/{repo}'\n",
		"apfs_sharing": "APFS_Sharing='off'\n", "vcs": "VCS='git'\n", "unique_leaf": "Unique_Leaf=true\n",
		"hooks": "[Hooks]\npost_create=['echo hook']\n", "Unicode table": "[\"hooKs\"]\npost_create=['echo hook']\n",
		"post_create": "[hooks]\nPost_Create=['echo hook']\n", "pre_destroy": "[hooks]\nPre_Destroy=['echo hook']\n",
		"dotted": "Hooks.post_create=['echo hook']\n", "inline": "Hooks={post_create=['echo hook']}\n",
		"multiple spellings": "[hooks]\npost_create=['echo hook']\n[Hooks]\npost_create=['echo hook']\n",
	} {
		t.Run(name, func(t *testing.T) {
			setUserHome(t, t.TempDir())
			repo := t.TempDir()
			writeRepoConfig(t, repo, contents)
			if _, err := Load(repo); err == nil || !strings.Contains(err.Error(), "noncanonical configuration key") {
				t.Fatalf("repository alias accepted: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(userConfigPath()), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(userConfigPath(), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadGlobal(); err == nil || !strings.Contains(err.Error(), "noncanonical configuration key") {
				t.Fatalf("user alias accepted: %v", err)
			}
		})
	}
}

func TestSchemaKeepsUnknownSettingsExact(t *testing.T) {
	_, raw, _, err := decodeConfig([]byte("max_trees=4\n[future_settings]\nTrusted_Repos='exact'\nPost_Get='also exact'\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"Trusted_Repos": "exact", "Post_Get": "also exact"}
	if !reflect.DeepEqual(raw["future_settings"], want) {
		t.Fatalf("unknown keys changed: %+v", raw)
	}
}
