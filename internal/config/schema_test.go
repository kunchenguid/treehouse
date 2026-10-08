package config

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSchemaAliasesRejectedBeforeReadsHashesAndWrites(t *testing.T) {
	cases := map[string]string{
		"max_trees":      "Max_Trees=7\n",
		"root":           "Root='.'\n",
		"base_branch":    "Base_Branch='main'\n",
		"worktree_path":  "Worktree_Path='{pool}/{slot}/{repo}'\n",
		"apfs_sharing":   "APFS_Sharing='off'\n",
		"vcs":            "VCS='git'\n",
		"unique_leaf":    "Unique_Leaf=true\n",
		"hooks":          "[Hooks]\npost_get=['echo repo']\n",
		"trusted_repos":  "[Trusted_Repos]\nrepo='sha256:test'\n",
		"approval_files": "[hooks]\nApproval_Files=[]\n",
		"post_create":    "[hooks]\nPost_Create=['echo repo']\n",
		"post_get":       "[hooks]\nPost_Get=['echo repo']\n",
		"post_return":    "[hooks]\nPost_Return=['echo repo']\n",
		"pre_destroy":    "[hooks]\nPre_Destroy=['echo repo']\n",
		"Unicode table":  "[\"truſted_repoſ\"]\nrepo='sha256:test'\n",
		"Unicode field":  "[hooks]\n\"poſt_get\"=['echo repo']\n",
		"dotted":         "Trusted_Repos.repo='sha256:test'\n",
		"inline":         "Trusted_Repos={repo='sha256:test'}\n",
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			setUserHome(t, t.TempDir())
			repo := t.TempDir()
			check := func(err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "noncanonical configuration key") {
					t.Fatalf("expected explicit key correction, got %v", err)
				}
			}
			writeRepoConfig(t, repo, contents)
			_, _, hash, err := readRepoConfig(repo)
			check(err)
			if hash != "" {
				t.Fatal("hashed noncanonical repository config")
			}
			_, err = Load(repo)
			check(err)
			_, _, err = Trust(repo)
			check(err)

			writeRepoConfig(t, repo, "[hooks]\npost_get=['echo repo']\n")
			writeTrustUserConfig(t, contents)
			_, err = LoadGlobal()
			check(err)
			_, err = Load(repo)
			check(err)
			_, _, err = Trust(repo)
			check(err)
			_, err = Untrust(repo)
			check(err)
			after, err := os.ReadFile(userConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != contents {
				t.Fatal("rejected alias was preserved through a config rewrite")
			}
		})
	}
}

func TestSchemaRejectsDuplicateSpellingsEvenWithIdenticalValues(t *testing.T) {
	for name, contents := range map[string]string{
		"trust tables":  "[trusted_repos]\nrepo='hash'\n[Trusted_Repos]\nrepo='hash'\n",
		"two aliases":   "[Trusted_Repos]\nrepo='hash'\n[TRUSTED_REPOS]\nrepo='hash'\n",
		"Unicode alias": "[trusted_repos]\nrepo='hash'\n[\"truſted_repoſ\"]\nrepo='hash'\n",
		"hook tables":   "[hooks]\npost_get=['echo repo']\n[Hooks]\npost_get=['echo repo']\n",
		"hook fields":   "[hooks]\npost_get=['echo repo']\nPost_Get=['echo repo']\n",
		"file lists":    "[hooks]\napproval_files=[]\nApproval_Files=[]\n",
		"scalar fields": "max_trees=7\nMax_Trees=7\n",
	} {
		t.Run(name, func(t *testing.T) {
			setUserHome(t, t.TempDir())
			repo := t.TempDir()
			writeRepoConfig(t, repo, contents)
			_, _, hash, err := readRepoConfig(repo)
			if err == nil || !strings.Contains(err.Error(), "noncanonical configuration key") || hash != "" {
				t.Fatalf("ambiguous repository accepted: hash=%q, err=%v", hash, err)
			}
			writeRepoConfig(t, repo, "[hooks]\npost_get=['echo repo']\n")
			writeTrustUserConfig(t, contents)
			if _, err := LoadGlobal(); err == nil {
				t.Fatal("read accepted ambiguous fields")
			}
			if _, err := Untrust(repo); err == nil {
				t.Fatal("untrust accepted ambiguous fields")
			}
			if _, _, err := Trust(repo); err == nil {
				t.Fatal("trust accepted ambiguous fields")
			}
			after, err := os.ReadFile(userConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != contents {
				t.Fatal("ambiguous tables were merged or rewritten")
			}
		})
	}
}

func TestCorrectedTrustTableReadUntrustWriteReload(t *testing.T) {
	for _, spelling := range []string{"Trusted_Repos", "TRUSTED_REPOS", "truſted_repoſ"} {
		t.Run(spelling, func(t *testing.T) {
			setUserHome(t, t.TempDir())
			repo := t.TempDir()
			writeRepoConfig(t, repo, "[hooks]\npost_get=['echo repo']\n")
			_, hash, err := Trust(repo)
			if err != nil {
				t.Fatal(err)
			}
			approvals := map[string]string{repo: hash, "https://example.invalid/Owner/Repo.git": "sha256:upper", "https://example.invalid/owner/repo.git": "sha256:lower", "Trusted_Repos": "sha256:literal"}
			future := map[string]any{"Trusted_Repos": "untouched", "Post_Get": "also untouched"}
			raw := map[string]any{spelling: approvals, "future_settings": future, "hooks": Hooks{PostGet: []string{"echo user"}}}
			var buf bytes.Buffer
			if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
				t.Fatal(err)
			}
			writeTrustUserConfig(t, buf.String())
			if _, err := LoadGlobal(); err == nil {
				t.Fatal("read accepted alias")
			}
			if _, err := Untrust(repo); err == nil {
				t.Fatal("untrust accepted alias")
			}
			after, err := os.ReadFile(userConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, buf.Bytes()) {
				t.Fatal("failed untrust rewrote alias")
			}

			// The user explicitly fixes the schema name. No URL or unknown setting
			// changes case, and a later successful revoke removes only this repository.
			delete(raw, spelling)
			raw["trusted_repos"] = approvals
			buf.Reset()
			if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
				t.Fatal(err)
			}
			writeTrustUserConfig(t, buf.String())
			cfg, err := Load(repo)
			if err != nil || !reflect.DeepEqual(cfg.Hooks.PostGet, []string{"echo user", "echo repo"}) {
				t.Fatalf("corrected config failed to load: %+v, %v", cfg, err)
			}
			if _, err := Untrust(repo); err != nil {
				t.Fatal(err)
			}
			cfg, err = Load(repo)
			if err != nil || !reflect.DeepEqual(cfg.Hooks.PostGet, []string{"echo user"}) {
				t.Fatalf("revoked hooks restored: %+v, %v", cfg, err)
			}
			delete(approvals, repo)
			if !reflect.DeepEqual(cfg.TrustedRepos, approvals) {
				t.Fatalf("dynamic repository keys changed: %+v", cfg.TrustedRepos)
			}
			var saved map[string]any
			if _, err := toml.DecodeFile(userConfigPath(), &saved); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(saved["future_settings"], future) {
				t.Fatalf("unknown setting changed: %+v", saved)
			}
			if _, err := Untrust(repo); err == nil {
				t.Fatal("repeated revoke must report no change")
			}
		})
	}
}
