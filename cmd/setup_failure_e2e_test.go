package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetLeasePostCreate(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("reuse=%t/fail=%t", reuse, fail), func(t *testing.T) {
				repoDir, homeDir := setupTestRepo(t)
				if err := os.WriteFile(filepath.Join(repoDir, "treehouse.toml"), []byte("max_trees = 1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				var previous string
				if reuse {
					stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
					if code != 0 {
						t.Fatalf("initial get failed: %s", stderr)
					}
					previous = strings.TrimSpace(stdout)
					if _, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", previous); code != 0 {
						t.Fatalf("return failed: %s", stderr)
					}
				}
				configDir := filepath.Join(homeDir, ".config", "treehouse")
				if err := os.MkdirAll(configDir, 0o755); err != nil {
					t.Fatal(err)
				}
				hook := "echo setup-passed"
				if fail {
					hook = "exit 42"
				}
				if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(fmt.Sprintf("[hooks]\npost_create = [%q]\n", hook)), 0o644); err != nil {
					t.Fatal(err)
				}
				stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--json", "--lease-holder", "worker")
				if fail {
					if code == 0 || stdout != "" || !strings.Contains(stderr, "post_create") || !strings.Contains(stderr, "exit 42") {
						t.Fatalf("failed setup granted acquisition: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
					}
				} else {
					var lease struct {
						Path    string `json:"path"`
						LeaseID string `json:"lease_id"`
					}
					if code != 0 || json.Unmarshal([]byte(stdout), &lease) != nil || lease.LeaseID == "" || lease.Path == "" || !strings.Contains(stderr, "setup-passed") {
						t.Fatalf("passing setup failed: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
					}
				}
				stdout, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "status", "--json")
				var statuses []struct {
					Path        string `json:"path"`
					Status      string `json:"status"`
					LeaseID     string `json:"lease_id"`
					LeaseHolder string `json:"lease_holder"`
				}
				if code != 0 || json.Unmarshal([]byte(stdout), &statuses) != nil || len(statuses) != 1 {
					t.Fatalf("status failed: exit=%d stdout=%q stderr=%s", code, stdout, stderr)
				}
				if reuse && statuses[0].Path != previous {
					t.Fatalf("expected reused slot %s, got %+v", previous, statuses)
				}
				if fail && (statuses[0].Status == "available" || statuses[0].LeaseID != "" || !strings.HasPrefix(statuses[0].LeaseHolder, "quarantined:")) {
					t.Fatalf("failed setup reads as ready: %+v", statuses)
				}
				if !fail && (statuses[0].LeaseID == "" || statuses[0].LeaseHolder != "worker") {
					t.Fatalf("passing setup did not grant the lease: %+v", statuses)
				}
				if fail {
					if stdout, _, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--json"); code == 0 || stdout != "" {
						t.Fatalf("failed slot reused: exit=%d stdout=%q", code, stdout)
					}
				}
			})
		}
	}
}
