package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStatusNamesReturnForThreeZeroUpgradeQuarantine rewrites an idle slot as
// treehouse 3.0.0 left it after upgrading pre-3.0 state, then checks that
// status keeps it leased and prints the command that frees it, that
// `return --all` returns other held slots but leaves it leased, and that the
// named command frees it.
func TestStatusNamesReturnForThreeZeroUpgradeQuarantine(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "setup")
	if _, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path); code != 0 {
		t.Fatalf("return failed, code=%d stderr=%q", code, stderr)
	}
	poolDir := filepath.Dir(filepath.Dir(lease.Path))

	stamp := time.Now().Add(-time.Hour).Round(0)
	state := map[string]any{
		"version": 4,
		"worktrees": []map[string]any{{
			"name":         filepath.Base(filepath.Dir(lease.Path)),
			"path":         lease.Path,
			"created_at":   stamp.Add(-48 * time.Hour),
			"leased":       true,
			"lease_id":     "0123456789abcdef0123456789abcdef",
			"lease_holder": "recovered: state file was corrupt or truncated; verify before reuse",
			"leased_at":    stamp,
		}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(poolDir, "treehouse-state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	keyCreated := stamp.Add(200 * time.Millisecond)
	if err := os.Chtimes(filepath.Join(poolDir, "treehouse-state.key"), keyCreated, keyCreated); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "status")
	if code != 0 {
		t.Fatalf("status failed, code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "leased") || !strings.Contains(stdout, "quarantined by the 3.0.0 upgrade") {
		t.Fatalf("status does not report the upgrade quarantine:\n%s", stdout)
	}
	if want := "treehouse return " + quoteReturnPath(lease.Path); !strings.Contains(stdout, want) {
		t.Fatalf("status does not name %q:\n%s", want, stdout)
	}

	other := acquireLeaseJSON(t, repoDir, homeDir, "other")
	if other.Path == lease.Path {
		t.Fatalf("get --lease reused %s, which the 3.0.0 upgrade quarantined", lease.Path)
	}
	_, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "return", "--all")
	if code != 0 {
		t.Fatalf("return --all failed, code=%d stderr=%q", code, stderr)
	}
	if want := "treehouse return " + quoteReturnPath(lease.Path); !strings.Contains(stderr, want) {
		t.Fatalf("return --all does not name %q:\n%s", want, stderr)
	}
	if !strings.Contains(stderr, "Returned 1 of 2 held worktree(s); 1 skipped") {
		t.Fatalf("return --all summary does not count the skipped quarantine:\n%s", stderr)
	}
	stdout, _, _ = runTreehouse(t, repoDir, homeDir, nil, "status")
	if strings.Count(stdout, "leased") != 1 || !strings.Contains(stdout, "quarantined by the 3.0.0 upgrade") {
		t.Fatalf("return --all did not return only the other slot:\n%s", stdout)
	}

	if _, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path); code != 0 {
		t.Fatalf("return of a slot quarantined by the upgrade failed, code=%d stderr=%q", code, stderr)
	}
	stdout, _, _ = runTreehouse(t, repoDir, homeDir, nil, "status")
	if strings.Contains(stdout, "leased") || !strings.Contains(stdout, "available") {
		t.Fatalf("returned slot is not available:\n%s", stdout)
	}
}
