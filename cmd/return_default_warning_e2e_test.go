package cmd

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests pin the warning period before plain `treehouse return` becomes
// clean-only by default: a plain return warns on stderr wherever --if-clean
// would refuse, and otherwise behaves exactly as before - same stdout, same
// exit status, same effect on the worktree and its lease.

// futureDefaultWarnings returns the stderr lines announcing the default change.
func futureDefaultWarnings(stderr string) []string {
	var lines []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "future major release") {
			lines = append(lines, line)
		}
	}
	return lines
}

// slotSuffix is the tail of a slot path that a warning line names, whichever
// home prefix it was shortened against.
func slotSuffix(path string) string {
	return string(filepath.Separator) + filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path)) + " "
}

// assertOneWarning checks a single warning naming the slot, the blocker, and
// both ways to choose explicitly.
func assertOneWarning(t *testing.T, stderr, path, blocker string) {
	t.Helper()
	warnings := futureDefaultWarnings(stderr)
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one default-change warning, got %d:\n%s", len(warnings), stderr)
	}
	w := warnings[0]
	for _, want := range []string{"🌳 Warning:", slotSuffix(path), "(" + blocker + ":", "exit 4", "--if-clean", "--force"} {
		if !strings.Contains(w, want) {
			t.Fatalf("warning does not mention %q:\n%s", want, w)
		}
	}
}

func assertNoWarning(t *testing.T, stderr string) {
	t.Helper()
	if warnings := futureDefaultWarnings(stderr); len(warnings) != 0 {
		t.Fatalf("expected no default-change warning, got:\n%s", strings.Join(warnings, "\n"))
	}
}

func commitInSlot(t *testing.T, path, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(path, name), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, path, "add", name)
	gitCmd(t, path, "commit", "-m", "agent work")
	return headOf(t, path)
}

func slotStatus(t *testing.T, repoDir, homeDir, path string) string {
	t.Helper()
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Path == path {
			return entry.Status
		}
	}
	t.Fatalf("worktree %s vanished from status", path)
	return ""
}

// A clean worktree whose HEAD never landed is the case the default change
// exists for: today the return resets the commit away, and it still does.
func TestPlainReturnWarnsAboutAnUnlandedHeadAndStillReturnsIt(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
	head := commitInSlot(t, lease.Path, "feature.txt")

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path)
	if code != 0 || stdout != "" {
		t.Fatalf("plain return must keep exit 0 and empty stdout, got exit %d stdout %q\nstderr: %s", code, stdout, stderr)
	}
	assertOneWarning(t, stderr, lease.Path, "unpushed")
	if !strings.Contains(stderr, "Worktree returned to pool.") {
		t.Fatalf("expected the usual success report, got: %s", stderr)
	}
	if got := slotStatus(t, repoDir, homeDir, lease.Path); got != "available" {
		t.Fatalf("expected the slot returned, got %q", got)
	}
	if got := headOf(t, lease.Path); got == head {
		t.Fatalf("plain return must still reset the unlanded commit, HEAD is still %s", got)
	}
}

// A dirty worktree keeps today's confirmation and its exit statuses: an
// unanswerable confirmation still aborts with 3, and a confirmed one still
// cleans. Only the warning in front of the confirmation is new.
func TestPlainReturnWarnsAboutADirtyWorktreeAndKeepsItsOutcomes(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
	if err := os.WriteFile(filepath.Join(lease.Path, "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path)
	if code != ExitNotReturned || stdout != "" {
		t.Fatalf("an unanswered dirty return must keep exit %d and empty stdout, got exit %d stdout %q\nstderr: %s", ExitNotReturned, code, stdout, stderr)
	}
	assertOneWarning(t, stderr, lease.Path, "dirty")
	if strings.Index(stderr, "future major release") > strings.Index(stderr, "Clean and return?") {
		t.Fatalf("the warning must precede the confirmation it informs:\n%s", stderr)
	}
	if got := slotStatus(t, repoDir, homeDir, lease.Path); got != "leased" {
		t.Fatalf("the aborted return must keep the lease, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(lease.Path, "notes.txt")); err != nil {
		t.Fatalf("the aborted return must keep the change: %v", err)
	}

	ret := exec.Command(treehouseBin, "return", lease.Path)
	ret.Dir = repoDir
	ret.Env = buildEnv(homeDir)
	ret.Stdin = strings.NewReader("y\n")
	out, err := ret.CombinedOutput()
	if err != nil {
		t.Fatalf("a confirmed dirty return must still succeed: %v\n%s", err, out)
	}
	assertOneWarning(t, string(out), lease.Path, "dirty")
	if got := slotStatus(t, repoDir, homeDir, lease.Path); got != "available" {
		t.Fatalf("expected the confirmed slot returned, got %q", got)
	}
}

// Nothing would be lost, so there is nothing to warn about: a slot parked as
// acquired, and one whose commit landed on a remote-tracking branch other than
// the base.
func TestPlainReturnWarnsAboutLiveProcessBeforeTerminatingIt(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
	signals := t.TempDir()
	readyFile := filepath.Join(signals, "ready")
	releaseFile := filepath.Join(signals, "release")
	agent := exec.Command(waitShellBin)
	agent.Dir = lease.Path
	agent.Env = append(os.Environ(), "TREEHOUSE_TEST_READY="+readyFile, "TREEHOUSE_TEST_RELEASE="+releaseFile)
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var agentErr error
	go func() { agentErr = agent.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = os.WriteFile(releaseFile, nil, 0o644)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Error("worker did not exit during cleanup")
		}
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the worker process never started in the worktree")
		}
		time.Sleep(20 * time.Millisecond)
	}

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path)
	if code != 0 || stdout != "" {
		t.Fatalf("plain return must keep exit 0 and empty stdout, got exit %d stdout %q\nstderr: %s", code, stdout, stderr)
	}
	assertOneWarning(t, stderr, lease.Path, "processes")
	warningAt := strings.Index(stderr, "future major release")
	successAt := strings.Index(stderr, "Worktree returned to pool.")
	if warningAt < 0 || successAt < 0 || warningAt > successAt {
		t.Fatalf("warning must precede success: %s", stderr)
	}
	select {
	case <-exited:
		if agentErr == nil {
			t.Fatal("plain return should terminate the live worker")
		}
	default:
		t.Fatal("plain return left the live worker running")
	}
	if got := slotStatus(t, repoDir, homeDir, lease.Path); got != "available" {
		t.Fatalf("expected the slot returned, got %q", got)
	}
}

func TestAdvisoryCheckFailureUsesGenericWarning(t *testing.T) {
	oldStderr := os.Stderr
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writeEnd
	warnFutureCleanOnlyDefault(returnTarget{poolDir: filepath.Join(t.TempDir(), "missing-pool"), path: filepath.Join(t.TempDir(), "slot")})
	_ = writeEnd.Close()
	os.Stderr = oldStderr
	stderr, err := io.ReadAll(readEnd)
	_ = readEnd.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stderr), "future major release will refuse this by default (exit 4)") || !strings.Contains(string(stderr), "cannot verify that") {
		t.Fatalf("expected generic advisory-check failure warning, got %s", stderr)
	}
}

func TestPlainReturnDoesNotWarnWhenNothingWouldBeLost(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	parked := acquireLeaseJSON(t, repoDir, homeDir, "parked")
	pushed := acquireLeaseJSON(t, repoDir, homeDir, "pushed")
	commitInSlot(t, pushed.Path, "feature.txt")
	gitCmd(t, pushed.Path, "push", "origin", "HEAD:refs/heads/landed")

	for _, lease := range []leaseJSONResult{parked, pushed} {
		stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path)
		if code != 0 || stdout != "" {
			t.Fatalf("return of %s failed: exit %d stdout %q\nstderr: %s", lease.Path, code, stdout, stderr)
		}
		assertNoWarning(t, stderr)
	}
}

// A caller that already chose - to keep work or to discard it - is not warned.
func TestExplicitReturnModesDoNotWarn(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
	commitInSlot(t, lease.Path, "feature.txt")

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path, "--if-clean")
	if code != ExitNotClean || strings.TrimSpace(stdout) != "unpushed" {
		t.Fatalf("--if-clean must refuse as before, got exit %d stdout %q\nstderr: %s", code, stdout, stderr)
	}
	assertNoWarning(t, stderr)

	stdout, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path, "--force")
	if code != 0 || stdout != "" {
		t.Fatalf("--force must return as before, got exit %d stdout %q\nstderr: %s", code, stdout, stderr)
	}
	assertNoWarning(t, stderr)
}

// --all warns once per slot that --if-clean would refuse, and its aggregate
// outcome is unchanged: the dirty slot still aborts the run with 3 while the
// clean ones, landed or not, are returned. --all --force warns about none.
func TestReturnAllWarnsPerSlotAndKeepsItsOutcome(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	landed := acquireLeaseJSON(t, repoDir, homeDir, "landed")
	unlanded := acquireLeaseJSON(t, repoDir, homeDir, "unlanded")
	dirty := acquireLeaseJSON(t, repoDir, homeDir, "dirty")
	commitInSlot(t, unlanded.Path, "feature.txt")
	if err := os.WriteFile(filepath.Join(dirty.Path, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--all")
	if code != ExitNotReturned || stdout != "" {
		t.Fatalf("--all with a dirty slot must keep exit %d and empty stdout, got exit %d stdout %q\nstderr: %s", ExitNotReturned, code, stdout, stderr)
	}
	warnings := futureDefaultWarnings(stderr)
	if len(warnings) != 2 {
		t.Fatalf("expected one warning each for the unlanded and dirty slots, got %d:\n%s", len(warnings), stderr)
	}
	for _, w := range warnings {
		if strings.Contains(w, slotSuffix(landed.Path)) {
			t.Fatalf("the landed slot must not be warned about:\n%s", w)
		}
	}
	assertOneWarning(t, strings.Join(filterLines(warnings, slotSuffix(unlanded.Path)), "\n"), unlanded.Path, "unpushed")
	assertOneWarning(t, strings.Join(filterLines(warnings, slotSuffix(dirty.Path)), "\n"), dirty.Path, "dirty")

	for path, want := range map[string]string{landed.Path: "available", unlanded.Path: "available", dirty.Path: "leased"} {
		if got := slotStatus(t, repoDir, homeDir, path); got != want {
			t.Fatalf("%s: expected %q after --all, got %q", path, want, got)
		}
	}

	stdout, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "return", "--all", "--force")
	if code != 0 || stdout != "" {
		t.Fatalf("--all --force must return as before, got exit %d stdout %q\nstderr: %s", code, stdout, stderr)
	}
	assertNoWarning(t, stderr)
}

func filterLines(lines []string, substr string) []string {
	var out []string
	for _, line := range lines {
		if strings.Contains(line, substr) {
			out = append(out, line)
		}
	}
	return out
}

// A jj workspace has no clean-only check at all, so even a pristine one warns
// that the new default will refuse it as unsupported, and --force is the way
// to keep returning it. The plain return itself still succeeds.
func TestJJPlainReturnWarnsItWillBeUnsupported(t *testing.T) {
	requireJJ(t)
	repoDir, homeDir := setupJJTestRepo(t)

	lease := acquireLeaseJSON(t, repoDir, homeDir, "dibs")
	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path)
	if code != 0 || stdout != "" {
		t.Fatalf("plain jj return must keep exit 0 and empty stdout, got exit %d stdout %q\nstderr: %s", code, stdout, stderr)
	}
	warnings := futureDefaultWarnings(stderr)
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one default-change warning, got %d:\n%s", len(warnings), stderr)
	}
	for _, want := range []string{slotSuffix(lease.Path), "jj workspace", "unsupported", "--force"} {
		if !strings.Contains(warnings[0], want) {
			t.Fatalf("jj warning does not mention %q:\n%s", want, warnings[0])
		}
	}
	if got := slotStatus(t, repoDir, homeDir, lease.Path); got != "available" {
		t.Fatalf("expected the jj slot returned, got %q", got)
	}

	lease = acquireLeaseJSON(t, repoDir, homeDir, "dibs")
	stdout, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "return", lease.Path, "--force")
	if code != 0 || stdout != "" {
		t.Fatalf("jj return --force failed: exit %d stdout %q\nstderr: %s", code, stdout, stderr)
	}
	assertNoWarning(t, stderr)
}
