//go:build !windows

package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type holderStatusResult struct {
	Path           string `json:"path"`
	Status         string `json:"status"`
	LeaseID        string `json:"lease_id"`
	RecoveryReason string `json:"recovery_reason"`
}

type holderLeaseResult struct {
	Path    string `json:"path"`
	LeaseID string `json:"lease_id"`
}

type shellLease struct {
	Path     string
	ShellPID int
}

// leaseFromExitingShell runs `get --lease` from a shell that names itself as
// the holder ($$) and then exits, the way a crashed or finished agent leaves
// its lease. The trailing command keeps the shell from exec'ing treehouse in
// its own place. extra runs in the shell after the lease, with $p as the path.
func leaseFromExitingShell(t *testing.T, repoDir, homeDir, extra string) shellLease {
	t.Helper()
	script := `p=$("$0" get --lease --lease-holder-pid $$) || exit 1; echo "$p"; echo $$; ` + extra + `; true`
	cmd := exec.Command("/bin/sh", "-c", script, treehouseBin)
	cmd.Dir = repoDir
	cmd.Env = buildEnv(homeDir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("leasing from a shell failed: %v (%s)", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		t.Fatalf("shell output %q, want path and PID", out)
	}
	pid, err := strconv.Atoi(lines[1])
	if err != nil || pid == os.Getpid() {
		t.Fatalf("shell PID %q: %v", lines[1], err)
	}
	return shellLease{Path: lines[0], ShellPID: pid}
}

func holderStatus(t *testing.T, repoDir, homeDir string) []holderStatusResult {
	t.Helper()
	out, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "status", "--json")
	if code != 0 {
		t.Fatalf("status --json failed (code %d): %s", code, stderr)
	}
	var statuses []holderStatusResult
	if err := json.Unmarshal([]byte(out), &statuses); err != nil {
		t.Fatalf("status --json %q: %v", out, err)
	}
	return statuses
}

// recordedHolderPID reads the holder PID persisted for the slot at path from
// the pool state file, the only place the holder identity is recorded.
func recordedHolderPID(t *testing.T, path string) int32 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(path)), "treehouse-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Worktrees []struct {
			Path           string `json:"path"`
			LeaseHolderPID int32  `json:"lease_holder_pid"`
		} `json:"worktrees"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state file %q: %v", data, err)
	}
	for _, wt := range state.Worktrees {
		if wt.Path == path {
			return wt.LeaseHolderPID
		}
	}
	t.Fatalf("state file has no entry for %s:\n%s", path, data)
	return 0
}

func TestGetLeaseFromAnExitedHolderIsFreedByTheNextCommand(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := leaseFromExitingShell(t, repoDir, homeDir, ":")
	statuses := holderStatus(t, repoDir, homeDir)
	if len(statuses) != 1 || statuses[0].Path != lease.Path || statuses[0].Status != "available" || statuses[0].LeaseID != "" || recordedHolderPID(t, lease.Path) != 0 {
		t.Fatalf("status after the holder exited = %+v", statuses)
	}
	path, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 || strings.TrimSpace(path) != lease.Path {
		t.Fatalf("get --lease = %q (code %d, %s), want the freed slot %s", path, code, stderr, lease.Path)
	}
}

func TestGetLeaseHolderStillRunningKeepsTheLease(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	// The test process starts treehouse, so it is the caller's parent and
	// stays alive for the whole test.
	self := strconv.Itoa(os.Getpid())
	out, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--json", "--lease-holder-pid", self)
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	var lease holderLeaseResult
	if err := json.Unmarshal([]byte(out), &lease); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "lease_holder_pid") {
		t.Fatalf("get --lease --json exposes the holder PID: %s", out)
	}
	if got := recordedHolderPID(t, lease.Path); got != int32(os.Getpid()) {
		t.Fatalf("recorded holder PID = %d, want %s", got, self)
	}
	statuses := holderStatus(t, repoDir, homeDir)
	if len(statuses) != 1 || statuses[0].Path != lease.Path || statuses[0].Status != "leased" || statuses[0].LeaseID != lease.LeaseID {
		t.Fatalf("status = %+v, want lease %+v kept", statuses, lease)
	}
}

func TestGetLeaseWithoutHolderPIDIsNeverFreedAutomatically(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	cmd := exec.Command("/bin/sh", "-c", `"$0" get --lease; true`, treehouseBin)
	cmd.Dir = repoDir
	cmd.Env = buildEnv(homeDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("get --lease: %v %s", err, out)
	}
	statuses := holderStatus(t, repoDir, homeDir)
	if len(statuses) != 1 || statuses[0].Status != "leased" || recordedHolderPID(t, statuses[0].Path) != 0 {
		t.Fatalf("status = %+v", statuses)
	}
}

func TestExitedHolderLeaseOnUnsafeSlotIsKeptWithAReason(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := leaseFromExitingShell(t, repoDir, homeDir, `echo edit >> "$p/README.md"`)
	statuses := holderStatus(t, repoDir, homeDir)
	if len(statuses) != 1 || statuses[0].Status != "leased" || recordedHolderPID(t, lease.Path) != int32(lease.ShellPID) ||
		!strings.Contains(statuses[0].RecoveryReason, "holder process "+strconv.Itoa(lease.ShellPID)+" exited") ||
		!strings.Contains(statuses[0].RecoveryReason, "tracked changes") {
		t.Fatalf("status = %+v", statuses)
	}
	human, _, _ := runTreehouse(t, repoDir, homeDir, nil, "status")
	if !strings.Contains(human, "kept leased") || !strings.Contains(human, "treehouse return") {
		t.Fatalf("status does not explain the kept lease:\n%s", human)
	}
	if data, err := os.ReadFile(filepath.Join(lease.Path, "README.md")); err != nil || !strings.Contains(string(data), "edit") {
		t.Fatalf("slot contents changed: %q, %v", data, err)
	}
}

func TestLeaseHolderPIDIsRefusedWhenItCannotBeHonored(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	stranger := exec.Command("sleep", "60")
	if err := stranger.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = stranger.Process.Kill()
		_ = stranger.Wait()
	})
	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Skipf("cannot run true: %v", err)
	}
	excludePath := filepath.Join(repoDir, ".git", "info", "exclude")
	excludeBefore, excludeErr := os.ReadFile(excludePath)
	// An in-project root, so a refusal that reached EnsureExcluded would
	// create the root and its .gitignore and edit .git/info/exclude.
	inProjectRoot := filepath.Join(repoDir, ".treehouse")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"without --lease":           {args: []string{"get", "--lease-holder-pid", strconv.Itoa(os.Getpid())}, want: "requires --lease"},
		"not a number":              {args: []string{"get", "--lease", "--lease-holder-pid", "abc"}, want: "invalid argument"},
		"not an ancestor":           {args: []string{"get", "--lease", "--lease-holder-pid", strconv.Itoa(stranger.Process.Pid)}, want: "not an ancestor"},
		"exited":                    {args: []string{"get", "--lease", "--lease-holder-pid", strconv.Itoa(exited.Process.Pid)}, want: "lease holder process"},
		"zero names no holder here": {args: []string{"get", "--lease", "--lease-holder-pid", "0"}, want: "invalid lease holder PID"},
		"negative":                  {args: []string{"get", "--lease", "--lease-holder-pid=-1"}, want: "invalid lease holder PID"},
	} {
		t.Run(name, func(t *testing.T) {
			out, stderr, code := runTreehouse(t, repoDir, homeDir, nil, append(tc.args, "--root", ".")...)
			if code == 0 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code %d, stdout %q, stderr %q; want a refusal mentioning %q", code, out, stderr, tc.want)
			}
			if _, err := os.Stat(inProjectRoot); !os.IsNotExist(err) {
				t.Fatalf("refusal created the in-project pool root %s: %v", inProjectRoot, err)
			}
			if _, err := os.Stat(filepath.Join(inProjectRoot, ".gitignore")); !os.IsNotExist(err) {
				t.Fatalf("refusal wrote the pool root's .gitignore: %v", err)
			}
			excludeAfter, err := os.ReadFile(excludePath)
			if (err == nil) != (excludeErr == nil) || string(excludeAfter) != string(excludeBefore) {
				t.Fatalf("refusal changed .git/info/exclude: %q (%v), was %q (%v)", excludeAfter, err, excludeBefore, excludeErr)
			}
		})
	}
	if statuses := holderStatus(t, repoDir, homeDir); len(statuses) != 0 {
		t.Fatalf("refused acquisitions left worktrees: %+v", statuses)
	}
}
