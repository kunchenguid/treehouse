package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalApprovalFingerprintsRemainCompatible(t *testing.T) {
	// These fingerprints are captured from d874b6a6343e. Keep raw TOML values,
	// unknown hook keys, and the distinction between absent and empty file lists.
	for _, tc := range []struct{ name, contents, expected string }{
		{"commands", "[hooks]\npost_create=['echo ready']\npost_get=['echo get']\nfuture={enabled=true,count=9007199254740993,labels=['a','b'],stamp=2026-10-08T00:00:00Z}\n", "sha256:17970fc5ad819223a2a73736a96aab86ede27ebc3b6e8654a2fabb91822e107c"},
		{"empty files", "[hooks]\napproval_files=[]\npost_create=['echo ready']\n", "sha256:7911e6112915ead4db0f940e3a6165a67b291b1a4f2044ced43bfc464586037c"},
		{"dependency files", "[hooks]\napproval_files=['setup.sh','package.json']\npost_create=['./setup.sh']\n", "sha256:f7502fa93719a5d899123bc806b7d14ce6d30c3e15d082375890da75ebb1e5f7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setUserHome(t, t.TempDir())
			repo := t.TempDir()
			writeRepoConfig(t, repo, tc.contents)
			for path, contents := range map[string]string{"setup.sh": "echo setup\n", "package.json": "{\"name\":\"fixture\"}\n"} {
				if err := os.WriteFile(filepath.Join(repo, path), []byte(contents), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cfg, _, tableHash, err := readRepoConfig(repo)
			if err != nil {
				t.Fatal(err)
			}
			got, err := hookFingerprint(tableHash, cfg.Hooks.ApprovalFiles, repo)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.expected {
				t.Fatalf("fingerprint changed: got %s, want %s", got, tc.expected)
			}
			if err := updateTrust(repo, tc.expected); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(repo)
			if err != nil || len(loaded.Hooks.PostCreate) != 1 {
				t.Fatalf("existing approval stopped working: %+v, %v", loaded, err)
			}
			if _, _, err := Trust(repo); err == nil || !strings.Contains(err.Error(), "already trusted") {
				t.Fatalf("unchanged config unexpectedly required reapproval: %v", err)
			}
		})
	}
}
