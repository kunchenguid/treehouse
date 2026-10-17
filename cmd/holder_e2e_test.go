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
	LeaseHolderPID int32  `json:"lease_holder_pid"`
	RecoveryReason string `json:"recovery_reason"`
}

type holderLeaseResult struct {
	Path           string `json:"path"`
	LeaseID        string `json:"lease_id"`
	LeaseHolderPID int32  `json:"lease_holder_pid"`
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

func TestGetLeaseFromAnExitedHolderIsFreedByTheNextCommand(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := leaseFromExitingShell(t, repoDir, homeDir, ":")
	statuses := holderStatus(t, repoDir, homeDir)
	if len(statuses) != 1 || statuses[0].Path != lease.Path || statuses[0].Status != "available" || statuses[0].LeaseID != "" || statuses[0].LeaseHolderPID != 0 {
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
	for name, args := range map[string]struct {
		flags []string
		env   []string
	}{
		"flag":        {flags: []string{"--lease-holder-pid", self}},
		"environment": {env: []string{"TREEHOUSE_LEASE_HOLDER_PID=" + self}},
	} {
		t.Run(name, func(t *testing.T) {
			out, stderr, code := runTreehouse(t, repoDir, homeDir, args.env, append([]string{"get", "--lease", "--json"}, args.flags...)...)
			if code != 0 {
				t.Fatalf("get --lease failed (code %d): %s", code, stderr)
			}
			var lease holderLeaseResult
			if err := json.Unmarshal([]byte(out), &lease); err != nil {
				t.Fatal(err)
			}
			if lease.LeaseHolderPID != int32(os.Getpid()) {
				t.Fatalf("lease holder PID = %d, want %s", lease.LeaseHolderPID, self)
			}
			for _, st := range holderStatus(t, repoDir, homeDir) {
				if st.Path == lease.Path && (st.Status != "leased" || st.LeaseID != lease.LeaseID || st.LeaseHolderPID != lease.LeaseHolderPID) {
					t.Fatalf("status = %+v, want lease %+v kept", st, lease)
				}
			}
			human, _, _ := runTreehouse(t, repoDir, homeDir, nil, "status")
			if !strings.Contains(human, "pid "+self) {
				t.Fatalf("status does not show the holder PID:\n%s", human)
			}
		})
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
	if len(statuses) != 1 || statuses[0].Status != "leased" || statuses[0].LeaseHolderPID != 0 {
		t.Fatalf("status = %+v", statuses)
	}
}

func TestExitedHolderLeaseOnUnsafeSlotIsKeptWithAReason(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := leaseFromExitingShell(t, repoDir, homeDir, `echo edit >> "$p/README.md"`)
	statuses := holderStatus(t, repoDir, homeDir)
	if len(statuses) != 1 || statuses[0].Status != "leased" || statuses[0].LeaseHolderPID != int32(lease.ShellPID) ||
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
	for name, tc := range map[string]struct {
		env  []string
		args []string
		want string
	}{
		"without --lease":           {args: []string{"get", "--lease-holder-pid", strconv.Itoa(os.Getpid())}, want: "requires --lease"},
		"not a number":              {args: []string{"get", "--lease"}, env: []string{"TREEHOUSE_LEASE_HOLDER_PID=abc"}, want: "TREEHOUSE_LEASE_HOLDER_PID"},
		"not an ancestor":           {args: []string{"get", "--lease", "--lease-holder-pid", strconv.Itoa(stranger.Process.Pid)}, want: "not an ancestor"},
		"zero names no holder here": {args: []string{"get", "--lease", "--lease-holder-pid", "0"}, want: "invalid lease holder PID"},
	} {
		t.Run(name, func(t *testing.T) {
			out, stderr, code := runTreehouse(t, repoDir, homeDir, tc.env, tc.args...)
			if code == 0 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code %d, stdout %q, stderr %q; want a refusal mentioning %q", code, out, stderr, tc.want)
			}
		})
	}
	if statuses := holderStatus(t, repoDir, homeDir); len(statuses) != 0 {
		t.Fatalf("refused acquisitions left worktrees: %+v", statuses)
	}
}
