package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// statusEntries reads the machine-readable pool status, which is what these
// tests judge a return by: the name column `return <name>` accepts and the
// status `return --all` selects on are the same two fields.
func statusEntries(t *testing.T, repoDir, homeDir string) []statusJSONResult {
	t.Helper()
	return statusEntriesFromDir(t, repoDir, repoDir, homeDir, nil)
}

// statusEntriesFromDir is the same reading taken from a chosen working
// directory and environment, which is how the tests below observe a slot whose
// classification depends on where the caller is standing.
func statusEntriesFromDir(t *testing.T, repoDir, workDir, homeDir string, extraEnv []string) []statusJSONResult {
	t.Helper()
	stdout, stderr, code := runTreehouseFromDir(t, repoDir, workDir, homeDir, extraEnv, "status", "--json")
	if code != 0 {
		t.Fatalf("status --json failed (code %d): %s", code, stderr)
	}
	var entries []statusJSONResult
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("status --json returned invalid JSON: %v\n%s", err, stdout)
	}
	return entries
}

func statusEntry(t *testing.T, repoDir, homeDir, name string) statusJSONResult {
	t.Helper()
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("no worktree named %q in status", name)
	return statusJSONResult{}
}

// TestReturnByNameReleasesTheNamedSlot pins the name vocabulary: the first
// column of `treehouse status` is the same identity `treehouse lease` takes,
// and `return` now resolves it too, so what status shows can be returned
// without transcribing a path.
func TestReturnByNameReleasesTheNamedSlot(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	first := acquireLeaseJSON(t, repoDir, homeDir, "first")
	second := acquireLeaseJSON(t, repoDir, homeDir, "second")
	if first.Path == second.Path {
		t.Fatalf("expected two distinct slots, both are %s", first.Path)
	}

	var secondName string
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Path == second.Path {
			secondName = entry.Name
		}
	}
	if secondName == "" {
		t.Fatal("second lease is missing from status")
	}

	_, returnErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", secondName)
	if code != 0 {
		t.Fatalf("return %s failed (code %d): %s", secondName, code, returnErr)
	}

	if got := statusEntry(t, repoDir, homeDir, secondName); got.Status != "available" {
		t.Fatalf("expected the named slot released, got %+v", got)
	}
	// The name must select exactly one slot: the other lease is untouched.
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Path == first.Path && entry.Status != "leased" {
			t.Fatalf("returning %s released the wrong slot: %+v", secondName, entry)
		}
	}
}

// TestReturnByNameResolvesWhenASameNamedDirectoryExists covers the collision
// between the two vocabularies. A name is read only after the path reading
// found no managed worktree, so an unrelated directory that happens to share
// the name does not shadow the slot.
func TestReturnByNameResolvesWhenASameNamedDirectoryExists(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	lease := acquireLeaseJSON(t, repoDir, homeDir, "agent")
	name := statusEntry(t, repoDir, homeDir, "1").Name
	if statusEntry(t, repoDir, homeDir, name).Path != lease.Path {
		t.Fatalf("expected slot %s to be the leased worktree %s", name, lease.Path)
	}

	// A directory named like the slot, sitting in the caller's working
	// directory. It is not a managed worktree, so it must not shadow the slot.
	if err := os.MkdirAll(filepath.Join(repoDir, name), 0o755); err != nil {
		t.Fatal(err)
	}

	_, returnErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", name)
	if code != 0 {
		t.Fatalf("return %s failed (code %d): %s", name, code, returnErr)
	}
	if got := statusEntry(t, repoDir, homeDir, name); got.Status != "available" {
		t.Fatalf("expected slot %s released, got %+v", name, got)
	}
}

// TestReturnRejectsUnknownNameAndKeepsThePathDiagnosis pins which of the two
// diagnoses each refusal gets. An argument that could be a name reports the
// name failure and points at status; a path-shaped argument keeps reporting
// that it is not a managed worktree, because reading it as a name would be a
// misleading answer to a question the user did not ask.
func TestReturnRejectsUnknownNameAndKeepsThePathDiagnosis(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	acquireLeaseJSON(t, repoDir, homeDir, "agent")

	_, nameErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "999")
	if code == 0 {
		t.Fatalf("expected refusal for an unknown name, stderr=%q", nameErr)
	}
	if !strings.Contains(nameErr, "treehouse status") {
		t.Fatalf("expected the unknown-name refusal to point at status, got: %s", nameErr)
	}

	stray := filepath.Join(repoDir, "not-a-worktree")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	_, pathErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", stray)
	if code == 0 {
		t.Fatalf("expected refusal for an unmanaged path, stderr=%q", pathErr)
	}
	if !strings.Contains(pathErr, "is not managed by treehouse") {
		t.Fatalf("expected the path diagnosis for a path-shaped argument, got: %s", pathErr)
	}
}

// TestReturnAllReleasesHeldSlotsAndSkipsAvailableOnes pins --all's target set:
// every slot status does not report available or damaged, and nothing else.
func TestReturnAllReleasesHeldSlotsAndSkipsAvailableOnes(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	first := acquireLeaseJSON(t, repoDir, homeDir, "first")
	second := acquireLeaseJSON(t, repoDir, homeDir, "second")

	// A third slot returned up front, so the pool holds an available slot
	// --all has to skip rather than reset.
	third := acquireLeaseJSON(t, repoDir, homeDir, "third")
	if _, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", third.Path); code != 0 {
		t.Fatalf("seeding an available slot failed (code %d): %s", code, stderr)
	}

	_, allErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--all")
	if code != 0 {
		t.Fatalf("return --all failed (code %d): %s", code, allErr)
	}
	if !strings.Contains(allErr, "Returned 2 of 2") {
		t.Fatalf("expected a summary of the two held slots, got: %s", allErr)
	}

	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Status != "available" {
			t.Fatalf("expected every slot available after --all, got %+v", entry)
		}
	}
	if first.Path == second.Path {
		t.Fatalf("expected two distinct held slots, both are %s", first.Path)
	}

	// A pool with nothing held is not a failure: there is simply nothing to do.
	_, againErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--all")
	if code != 0 {
		t.Fatalf("return --all on an idle pool failed (code %d): %s", code, againErr)
	}
	if !strings.Contains(againErr, "No held worktrees to return") {
		t.Fatalf("expected the idle-pool report, got: %s", againErr)
	}
}

// TestReturnAllContinuesPastAnAbortAndReportsIt pins the bulk failure
// contract: one slot left dirty never stops the slots after it, and the exit
// status still reports that something was not returned.
func TestReturnAllContinuesPastAnAbortAndReportsIt(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	first := acquireLeaseJSON(t, repoDir, homeDir, "dirty-agent")
	second := acquireLeaseJSON(t, repoDir, homeDir, "clean-agent")

	if err := os.WriteFile(filepath.Join(first.Path, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stdin is not a terminal here, so the dirty confirmation cannot be
	// answered and that slot aborts - the same outcome as declining it.
	_, allErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--all")
	if code != ExitNotReturned {
		t.Fatalf("expected --all with a dirty slot to exit %d, got %d: %s", ExitNotReturned, code, allErr)
	}
	if !strings.Contains(allErr, "prune will not reclaim those slots") {
		t.Fatalf("expected the unreclaimable-slot explanation, got: %s", allErr)
	}
	if !strings.Contains(allErr, "return --all --force") {
		t.Fatalf("expected the --force hint, got: %s", allErr)
	}

	var dirtyStatus, cleanStatus string
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		switch entry.Path {
		case first.Path:
			dirtyStatus = entry.Status
		case second.Path:
			cleanStatus = entry.Status
		}
	}
	// An abort leaves the slot exactly as it was found, lease included.
	if dirtyStatus != "leased" {
		t.Fatalf("expected the aborted slot to keep its lease, got %q", dirtyStatus)
	}
	if got := gitCmd(t, first.Path, "status", "--porcelain"); got == "" {
		t.Fatal("expected the aborted slot to keep its uncommitted changes")
	}
	if cleanStatus != "available" {
		t.Fatalf("expected the slot after the abort to be returned, got %q", cleanStatus)
	}

	// --force is the documented way through, and must clear the whole board.
	_, forceErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--all", "--force")
	if code != 0 {
		t.Fatalf("return --all --force failed (code %d): %s", code, forceErr)
	}
	if got := gitCmd(t, first.Path, "status", "--porcelain"); got != "" {
		t.Fatalf("expected --force to clean the dirty slot, git status:\n%s", got)
	}
}

// TestReturnAllRejectsIncompatibleArguments pins the two combinations --all
// cannot honor: a named target, and a lease condition that identifies exactly
// one acquisition.
func TestReturnAllRejectsIncompatibleArguments(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	lease := acquireLeaseJSON(t, repoDir, homeDir, "agent")

	_, argErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--all", "1")
	if code == 0 || !strings.Contains(argErr, "--all takes no path or name") {
		t.Fatalf("expected --all with a target to be refused, code=%d stderr=%q", code, argErr)
	}

	_, condErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--all", "--if-lease-id", lease.LeaseID)
	if code == 0 || !strings.Contains(condErr, "--all cannot be combined with") {
		t.Fatalf("expected --all with a lease condition to be refused, code=%d stderr=%q", code, condErr)
	}

	// Both refusals must leave the pool exactly as it was.
	if got := statusEntry(t, repoDir, homeDir, "1"); got.Status != "leased" {
		t.Fatalf("a refused --all changed the pool: %+v", got)
	}
}

func TestCouldBeWorktreeName(t *testing.T) {
	names := []string{"1", "12", "slot-a", "a.b"}
	for _, name := range names {
		if !couldBeWorktreeName(name) {
			t.Errorf("%q must be readable as a worktree name", name)
		}
	}

	paths := []string{"", ".", "..", "./1", "../1", "a/1", "/abs/1", `a\1`}
	for _, path := range paths {
		if couldBeWorktreeName(path) {
			t.Errorf("%q is a path and must never be read as a worktree name", path)
		}
	}
}

// TestReturnAllSkipsASlotReacquiredMidRun pins the identity --all carries from
// its listing into every release. The run lists the pool once and then works
// through it, so a slot returned and handed to another acquisition while an
// earlier confirmation is still open is no longer the worktree the run set out
// to return: it must be left alone, with its new lease and its new tenant's
// files intact, and that skip is not a failure.
func TestReturnAllSkipsASlotReacquiredMidRun(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	dirty := acquireLeaseJSON(t, repoDir, homeDir, "dirty-agent")
	taken := acquireLeaseJSON(t, repoDir, homeDir, "first-holder")
	if dirty.Path == taken.Path {
		t.Fatalf("expected two distinct slots, both are %s", dirty.Path)
	}
	// The first slot prompts, which is what holds the run open long enough for
	// the second slot to change hands underneath it.
	if err := os.WriteFile(filepath.Join(dirty.Path, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	all, stdin, stderr := startReturnAllAtDirtyPrompt(t, repoDir, homeDir)

	if _, stderrOut, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--force", taken.Path); code != 0 {
		t.Fatalf("returning the second slot mid-run failed (code %d): %s", code, stderrOut)
	}
	reacquired := acquireLeaseJSON(t, repoDir, homeDir, "new-holder")
	if reacquired.Path != taken.Path {
		t.Fatalf("expected the freed slot %s to be re-acquired, got %s", taken.Path, reacquired.Path)
	}
	if reacquired.LeaseID == taken.LeaseID {
		t.Fatalf("re-acquisition reused the lease identity %q", taken.LeaseID)
	}
	sentinel := filepath.Join(reacquired.Path, "new-holder-work.txt")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output, waitErr := finishReturnAllAtPrompt(t, all, stdin, stderr, "y\n")
	if waitErr != nil {
		t.Fatalf("a skipped slot is not a failure, expected exit 0, got %v: %s", waitErr, output)
	}
	if !strings.Contains(output, "skipped: it is no longer the acquisition this run listed") {
		t.Fatalf("expected the re-acquired slot to be reported skipped, got: %s", output)
	}
	if !strings.Contains(output, "Returned 1 of 2 held worktree(s); 1 skipped") {
		t.Fatalf("expected the summary to count one return and one skip, got: %s", output)
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the re-acquired slot was reset under its new tenant: %v", err)
	}
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		switch entry.Path {
		case dirty.Path:
			if entry.Status != "available" {
				t.Fatalf("expected the confirmed slot returned, got %+v", entry)
			}
		case taken.Path:
			if entry.Status != "leased" || entry.LeaseID != reacquired.LeaseID {
				t.Fatalf("expected the re-acquired slot to keep its new lease, got %+v", entry)
			}
		}
	}
}

// TestReturnAllSkipsQuarantinedSlotsWithoutFailing pins the other skip. A
// rotated state key (a state version bump does the same) quarantines every
// entry in a pool as recovered: nothing proves one idle, so only a return
// naming it may clear it. Reporting that as a failure made --all exit 1 on
// every retry forever, so it is reported as a skip that names that return.
func TestReturnAllSkipsQuarantinedSlotsWithoutFailing(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	first := acquireLeaseJSON(t, repoDir, homeDir, "agent-a")
	second := acquireLeaseJSON(t, repoDir, homeDir, "agent-b")
	poolDir := filepath.Dir(filepath.Dir(first.Path))

	// Uncommitted work in one of them. --all never releases a quarantined
	// slot, so the run must skip it outright rather than first offer to
	// discard these changes.
	dirtyFile := filepath.Join(first.Path, "README.md")
	if err := os.WriteFile(dirtyFile, []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second.Path, "README.md"), []byte("also dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(poolDir, "treehouse-state.key"), []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, allErr, code := runTreehouse(t, repoDir, homeDir, nil, "return", "--all")
	if code != 0 {
		t.Fatalf("a quarantined pool must not report a failure, got code %d: %s", code, allErr)
	}
	if !strings.Contains(allErr, "Returned 0 of 2 held worktree(s); 2 skipped") {
		t.Fatalf("expected both slots counted as skipped, got: %s", allErr)
	}
	for _, path := range []string{first.Path, second.Path} {
		if want := "treehouse return " + quoteReturnPath(path); !strings.Contains(allErr, want) {
			t.Fatalf("expected the skip to name %q, got: %s", want, allErr)
		}
	}
	if strings.Contains(allErr, "Clean and return?") {
		t.Fatalf("a slot that cannot be released must not be offered for cleaning, got: %s", allErr)
	}
	if got := gitCmd(t, first.Path, "status", "--porcelain"); got == "" {
		t.Fatal("expected the quarantined slot to keep its uncommitted changes")
	}

	// A quarantine is a refusal, so both homes are exactly as they were.
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Status != "leased" {
			t.Fatalf("expected a quarantined slot to stay leased, got %+v", entry)
		}
	}
	for _, path := range []string{first.Path, second.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("quarantined worktree %s was disturbed: %v", path, err)
		}
	}
}

// TestReturnAllSkipsSlotsRecoveredAfterListing covers a pool whose state key
// is invalidated while --all waits on a confirmation: every entry, including
// the one being confirmed, is recovered before its release runs, and a bulk
// return must never release a recovered entry, only name the return that does.
func TestReturnAllSkipsSlotsRecoveredAfterListing(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	dirty := acquireLeaseJSON(t, repoDir, homeDir, "dirty-agent")
	later := acquireLeaseJSON(t, repoDir, homeDir, "later-agent")
	if dirty.Path == later.Path {
		t.Fatalf("expected two distinct slots, both are %s", dirty.Path)
	}
	dirtyFile := filepath.Join(dirty.Path, "README.md")
	if err := os.WriteFile(dirtyFile, []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(later.Path, "local-only.txt"), []byte("unpushable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, later.Path, "add", "local-only.txt")
	gitCmd(t, later.Path, "commit", "-m", "local-only")
	poolDir := filepath.Dir(filepath.Dir(dirty.Path))

	all, stdin, stderr := startReturnAllAtDirtyPrompt(t, repoDir, homeDir)

	if err := os.WriteFile(filepath.Join(poolDir, "treehouse-state.key"), []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, waitErr := finishReturnAllAtPrompt(t, all, stdin, stderr, "y\n")
	if waitErr != nil {
		t.Fatalf("a skipped slot is not a failure, expected exit 0, got %v: %s", waitErr, output)
	}
	if !strings.Contains(output, "Returned 0 of 2 held worktree(s); 2 skipped") {
		t.Fatalf("expected both slots skipped, got: %s", output)
	}
	for _, path := range []string{dirty.Path, later.Path} {
		if want := "treehouse return " + quoteReturnPath(path); !strings.Contains(output, want) {
			t.Fatalf("expected the skip to name %q, got: %s", want, output)
		}
	}
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Status != "leased" || !strings.HasPrefix(entry.LeaseHolder, "recovered:") {
			t.Fatalf("expected a recovered slot to stay leased, got %+v", entry)
		}
	}
	if data, err := os.ReadFile(dirtyFile); err != nil || string(data) != "dirty\n" {
		t.Fatalf("recovered slot was reset: data=%q err=%v", data, err)
	}
}

// startReturnAllAtDirtyPrompt launches `treehouse return --all` and blocks until
// it is waiting on the dirty confirmation of its first target. The run is then
// held open, which is the window every mid-run change in these tests lands in.
func startReturnAllAtDirtyPrompt(t *testing.T, repoDir, homeDir string) (*exec.Cmd, io.WriteCloser, io.ReadCloser) {
	t.Helper()

	all := exec.Command(treehouseBin, "return", "--all")
	all.Dir = repoDir
	all.Env = buildEnv(homeDir)
	stdin, err := all.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := all.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := all.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if all.ProcessState == nil {
			_ = all.Process.Kill()
			_ = all.Wait()
		}
	})

	promptRead := make(chan error, 1)
	go func() {
		promptRead <- readUntilSuffix(stderr, "[Y/n] ")
	}()
	select {
	case err := <-promptRead:
		if err != nil {
			t.Fatalf("failed to read the bulk return prompt: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("return --all did not prompt for the dirty slot")
	}
	return all, stdin, stderr
}

// finishReturnAllAtPrompt answers the open confirmation and reports everything
// the run printed after it, with the run's own exit error.
func finishReturnAllAtPrompt(t *testing.T, all *exec.Cmd, stdin io.WriteCloser, stderr io.ReadCloser, answer string) (string, error) {
	t.Helper()

	if _, err := io.WriteString(stdin, answer); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(stderr)
	if err != nil {
		t.Fatal(err)
	}
	return string(rest), all.Wait()
}

// TestReturnAllSkipReportsNoCauseForAParkedSlot covers the other half of the
// same sentinel. A slot whose lease was simply RETURNED mid-run is refused for
// exactly the reason a taken-over one is - the lease is no longer the one the
// listing saw - but nobody took it, so the report must not name re-acquisition.
func TestReturnAllSkipReportsNoCauseForAParkedSlot(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	dirty := acquireLeaseJSON(t, repoDir, homeDir, "dirty-agent")
	finishing := acquireLeaseJSON(t, repoDir, homeDir, "finishing-agent")
	if dirty.Path == finishing.Path {
		t.Fatalf("expected two distinct slots, both are %s", dirty.Path)
	}
	if err := os.WriteFile(filepath.Join(dirty.Path, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	all, stdin, stderr := startReturnAllAtDirtyPrompt(t, repoDir, homeDir)

	// The second agent parks its own slot while the run waits. Nobody takes it.
	if _, stderrOut, code := runTreehouse(t, repoDir, homeDir, nil, "return", finishing.Path); code != 0 {
		t.Fatalf("the second agent's own return failed (code %d): %s", code, stderrOut)
	}

	output, waitErr := finishReturnAllAtPrompt(t, all, stdin, stderr, "y\n")
	if waitErr != nil {
		t.Fatalf("a skipped slot is not a failure, expected exit 0, got %v: %s", waitErr, output)
	}
	if !strings.Contains(output, "skipped: it is no longer the acquisition this run listed") {
		t.Fatalf("expected the parked slot to be reported skipped, got: %s", output)
	}
	if strings.Contains(output, "re-acquired") {
		t.Fatalf("nobody took this slot over, so the report must not say so: %s", output)
	}

	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Status != "available" {
			t.Fatalf("expected every slot available after the run, got %+v", entry)
		}
	}
}

// TestReturnAllHonorsEveryPipedAnswer pins the scripted form of the bulk
// confirmation, which is what `--all` invites. Both answers arrive in a single
// write, exactly as a pipe delivers them, so a prompt that reads more bytes off
// the descriptor than its own line must keep the remainder for the next prompt.
// A terminal never shows this: canonical mode hands over one line per read.
func TestReturnAllHonorsEveryPipedAnswer(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	first := acquireLeaseJSON(t, repoDir, homeDir, "agent-a")
	second := acquireLeaseJSON(t, repoDir, homeDir, "agent-b")
	if first.Path == second.Path {
		t.Fatalf("expected two distinct slots, both are %s", first.Path)
	}
	// Both dirty, so both reach the confirmation.
	for _, path := range []string{first.Path, second.Path} {
		if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	all := exec.Command(treehouseBin, "return", "--all")
	all.Dir = repoDir
	all.Env = buildEnv(homeDir)
	all.Stdin = strings.NewReader("y\ny\n")
	var errBuf bytes.Buffer
	all.Stderr = &errBuf

	runErr := all.Run()
	output := errBuf.String()
	if runErr != nil {
		t.Fatalf("every slot was confirmed, so the run must exit 0, got %v: %s", runErr, output)
	}
	if !strings.Contains(output, "Returned 2 of 2 held worktree(s)") {
		t.Fatalf("expected both confirmed slots returned, got: %s", output)
	}

	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Status != "available" {
			t.Fatalf("expected every confirmed slot released, got %+v", entry)
		}
	}
	for _, path := range []string{first.Path, second.Path} {
		if got := gitCmd(t, path, "status", "--porcelain"); got != "" {
			t.Fatalf("expected %s cleaned by its confirmation, git status:\n%s", path, got)
		}
	}
}

// TestReturnAllHonorsAnUnterminatedFinalAnswer pins the boundary the buffered
// reader alone does not cover: bufio reports io.EOF TOGETHER with whatever it
// had already read, so a final answer typed or piped without a trailing newline
// arrives as a non-empty line and an error at once. Treating the error as
// decisive discards an answer the operator actually gave - the slot reads as
// unanswered, stays held, and the run reports an abort over an explicit
// confirmation. The input here deliberately ends without "\n"; adding one makes
// the test pass against the broken code and prove nothing.
func TestReturnAllHonorsAnUnterminatedFinalAnswer(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	first := acquireLeaseJSON(t, repoDir, homeDir, "agent-a")
	second := acquireLeaseJSON(t, repoDir, homeDir, "agent-b")
	if first.Path == second.Path {
		t.Fatalf("expected two distinct slots, both are %s", first.Path)
	}
	for _, path := range []string{first.Path, second.Path} {
		if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	all := exec.Command(treehouseBin, "return", "--all")
	all.Dir = repoDir
	all.Env = buildEnv(homeDir)
	all.Stdin = strings.NewReader("y\ny")
	var errBuf bytes.Buffer
	all.Stderr = &errBuf

	runErr := all.Run()
	output := errBuf.String()
	if runErr != nil {
		t.Fatalf("both slots were confirmed, so the run must exit 0, got %v: %s", runErr, output)
	}
	if !strings.Contains(output, "Returned 2 of 2 held worktree(s)") {
		t.Fatalf("expected the unterminated final answer honored, got: %s", output)
	}
	for _, entry := range statusEntries(t, repoDir, homeDir) {
		if entry.Status != "available" {
			t.Fatalf("expected every confirmed slot released, got %+v", entry)
		}
	}
}

// TestConfirmEOFWithNothingReadStaysUnanswered guards the other side of that
// boundary: an error carrying no ANSWER really is an unanswered prompt, and
// must keep aborting rather than being read as the default. The prompt trims
// before it interprets, so the trimmed value is what decides whether the read
// was answered at all - whitespace that arrives with the error holds no answer
// either. Without this, widening the EOF handling could silently turn a closed
// stdin into a "yes" that discards uncommitted work.
func TestConfirmEOFWithNothingReadStaysUnanswered(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin string
	}{
		{name: "closed stdin", stdin: ""},
		{name: "a space at EOF", stdin: " "},
		{name: "a tab at EOF", stdin: "\t"},
		{name: "blanks at EOF", stdin: "  \t "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoDir, homeDir := setupTestRepo(t)

			lease := acquireLeaseJSON(t, repoDir, homeDir, "agent-a")
			if err := os.WriteFile(filepath.Join(lease.Path, "README.md"), []byte("dirty\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			all := exec.Command(treehouseBin, "return", "--all")
			all.Dir = repoDir
			all.Env = buildEnv(homeDir)
			all.Stdin = strings.NewReader(tc.stdin)
			var errBuf bytes.Buffer
			all.Stderr = &errBuf

			runErr := all.Run()
			output := errBuf.String()
			if runErr == nil {
				t.Fatalf("an unanswerable dirty confirmation must not report success: %s", output)
			}
			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != ExitNotReturned {
				t.Fatalf("expected exit %d for an unanswered dirty prompt, got %v: %s", ExitNotReturned, runErr, output)
			}
			if got := gitCmd(t, lease.Path, "status", "--porcelain"); got == "" {
				t.Fatal("expected the unanswered slot to keep its uncommitted changes")
			}
		})
	}
}

// TestReturnAllKeepsChangesWhenTheFinalPromptGetsOnlyBlanks is the multi-prompt
// shape of the same boundary, which is the one `--all` invites: the first
// answer is real and the second prompt reads whitespace together with the EOF.
// Judging the raw read rather than the trimmed one would let that blank select
// the default - "yes" for the dirty confirmation - and discard uncommitted work
// on a prompt nobody answered.
func TestReturnAllKeepsChangesWhenTheFinalPromptGetsOnlyBlanks(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	first := acquireLeaseJSON(t, repoDir, homeDir, "agent-a")
	second := acquireLeaseJSON(t, repoDir, homeDir, "agent-b")
	if first.Path == second.Path {
		t.Fatalf("expected two distinct slots, both are %s", first.Path)
	}
	for _, path := range []string{first.Path, second.Path} {
		if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	all := exec.Command(treehouseBin, "return", "--all")
	all.Dir = repoDir
	all.Env = buildEnv(homeDir)
	all.Stdin = strings.NewReader("y\n ")
	var errBuf bytes.Buffer
	all.Stderr = &errBuf

	runErr := all.Run()
	output := errBuf.String()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != ExitNotReturned {
		t.Fatalf("expected exit %d for the unanswered second prompt, got %v: %s", ExitNotReturned, runErr, output)
	}

	kept := 0
	for _, path := range []string{first.Path, second.Path} {
		if gitCmd(t, path, "status", "--porcelain") != "" {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("expected the answered slot cleaned and the unanswered one kept, %d of 2 still dirty: %s", kept, output)
	}
}

// TestReturnAllLeavesAloneTheParkedSlotYouStandIn pins the one slot `--all`
// treats as unheld despite `status` not calling it available. `treehouse enter`
// leaves pool state untouched, so a parked, clean, quiet slot reports
// "you're here" purely because the caller's shell is inside it, and nobody
// holds it. Resetting it and counting it among the held worktrees returned
// would contradict what enter promises.
func TestReturnAllLeavesAloneTheParkedSlotYouStandIn(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	parked := acquireLeaseJSON(t, repoDir, homeDir, "agent-a")
	if _, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", parked.Path); code != 0 {
		t.Fatalf("parking the slot failed (code %d): %s", code, stderr)
	}
	headBefore := gitCmd(t, parked.Path, "rev-parse", "HEAD")

	_, allErr, code := runTreehouseFromDir(t, repoDir, parked.Path, homeDir, nil, "return", "--all")
	if code != 0 {
		t.Fatalf("a pool holding nothing must exit 0, got %d: %s", code, allErr)
	}
	if !strings.Contains(allErr, "No held worktrees to return") {
		t.Fatalf("expected the slot the caller stands in to be unheld, got: %s", allErr)
	}
	if strings.Contains(allErr, "Returning") {
		t.Fatalf("the parked slot must not be returned, got: %s", allErr)
	}
	if got := statusEntry(t, repoDir, homeDir, "1"); got.Status != "available" {
		t.Fatalf("expected the parked slot untouched, got %+v", got)
	}
	if got := gitCmd(t, parked.Path, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("expected HEAD unchanged at %s, got %s", headBefore, got)
	}
}

// TestReturnAllReturnsTheDirtySlotYouStandIn is the converse, and it is what
// keeps the skip above from widening: standing in a slot only excuses it while
// nobody holds it. Uncommitted work is a holder, so the slot is returned
// exactly as it would be from anywhere else.
func TestReturnAllReturnsTheDirtySlotYouStandIn(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	held := acquireLeaseJSON(t, repoDir, homeDir, "agent-a")
	if _, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", held.Path); code != 0 {
		t.Fatalf("parking the slot failed (code %d): %s", code, stderr)
	}
	if err := os.WriteFile(filepath.Join(held.Path, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, allErr, code := runTreehouseFromDir(t, repoDir, held.Path, homeDir, nil, "return", "--all", "--force")
	if code != 0 {
		t.Fatalf("return --all --force from inside a dirty slot failed (code %d): %s", code, allErr)
	}
	if !strings.Contains(allErr, "Returned 1 of 1 held worktree(s)") {
		t.Fatalf("expected the dirty slot returned, got: %s", allErr)
	}
	if got := gitCmd(t, held.Path, "status", "--porcelain"); got != "" {
		t.Fatalf("expected the dirty slot cleaned, git status:\n%s", got)
	}
	if got := statusEntry(t, repoDir, homeDir, "1"); got.Status != "available" {
		t.Fatalf("expected the dirty slot released, got %+v", got)
	}
}

// TestReturnAllLeavesAloneTheDamagedSlotYouStandIn pins the damaged exclusion
// as the unconditional rule every doc states. A missing marker means the slot's
// own contents cannot be judged, which is what `destroy` answers; standing in
// it does not make it readable, so the cwd must not relabel it into the bulk
// target set. The pool is in-project here because that is the layout where a
// markerless slot would otherwise be read through the repository ENCLOSING it.
func TestReturnAllLeavesAloneTheDamagedSlotYouStandIn(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	inProject := []string{"TREEHOUSE_ROOT=."}

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, inProject, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	if wtPath == "" {
		t.Fatal("could not capture the leased worktree path")
	}
	// Parked first, so the missing marker is the only thing classifying it.
	if _, stderr, code := runTreehouse(t, repoDir, homeDir, inProject, "return", wtPath); code != 0 {
		t.Fatalf("parking the slot failed (code %d): %s", code, stderr)
	}
	if err := os.Remove(filepath.Join(wtPath, ".git")); err != nil {
		t.Fatalf("removing the slot marker: %v", err)
	}

	standingIn := statusEntriesFromDir(t, repoDir, wtPath, homeDir, inProject)
	if len(standingIn) != 1 || standingIn[0].Status != "damaged" {
		t.Fatalf("standing in a damaged slot must not relabel it, got %+v", standingIn)
	}

	_, allErr, code := runTreehouseFromDir(t, repoDir, wtPath, homeDir, inProject, "return", "--all")
	if code != 0 {
		t.Fatalf("a pool holding nothing must exit 0, got %d: %s", code, allErr)
	}
	if strings.Contains(allErr, "Returning") {
		t.Fatalf("a damaged slot must never be a bulk target, got: %s", allErr)
	}
	if !strings.Contains(allErr, "No held worktrees to return") {
		t.Fatalf("expected the damaged slot reported unheld, got: %s", allErr)
	}

	// Naming it is a deliberate act and still works, exactly as before.
	if _, namedErr, code := runTreehouse(t, repoDir, homeDir, inProject, "return", wtPath); code != 0 {
		t.Fatalf("naming a damaged slot must still return it (code %d): %s", code, namedErr)
	}
}

// TestReturnAllSkipsASlotLeasedMidRun pins the other lease predicate. A slot
// observed UNLEASED carries "expect no lease" into its release, so a
// `treehouse lease` landing while an earlier confirmation holds the run open
// makes it no longer the acquisition the run listed.
func TestReturnAllSkipsASlotLeasedMidRun(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	dirty := acquireLeaseJSON(t, repoDir, homeDir, "dirty-agent")
	unleased := acquireLeaseJSON(t, repoDir, homeDir, "finishing-agent")
	if _, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", unleased.Path); code != 0 {
		t.Fatalf("parking the second slot failed (code %d): %s", code, stderr)
	}
	// Both dirty: the first to hold the run open at its prompt, the second so
	// it is a held target while carrying no lease.
	for _, path := range []string{dirty.Path, unleased.Path} {
		if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	all, stdin, stderr := startReturnAllAtDirtyPrompt(t, repoDir, homeDir)

	if _, leaseErr, code := runTreehouse(t, repoDir, homeDir, nil, "lease", "2"); code != 0 {
		t.Fatalf("leasing the second slot mid-run failed (code %d): %s", code, leaseErr)
	}

	output, waitErr := finishReturnAllAtPrompt(t, all, stdin, stderr, "y\n")
	if waitErr != nil {
		t.Fatalf("a skipped slot is not a failure, expected exit 0, got %v: %s", waitErr, output)
	}
	if !strings.Contains(output, "skipped: it is no longer the acquisition this run listed") {
		t.Fatalf("expected the newly leased slot to be reported skipped, got: %s", output)
	}

	for _, entry := range statusEntries(t, repoDir, homeDir) {
		switch entry.Path {
		case dirty.Path:
			if entry.Status != "available" {
				t.Fatalf("expected the confirmed slot returned, got %+v", entry)
			}
		case unleased.Path:
			if entry.Status != "leased" {
				t.Fatalf("expected the newly leased slot left alone, got %+v", entry)
			}
		}
	}
	if got := gitCmd(t, unleased.Path, "status", "--porcelain"); got == "" {
		t.Fatal("expected the skipped slot to keep its uncommitted changes")
	}
}

// TestReturnAllNeverPromptsOnAMarkerlessSlot pins the third instance of one
// rule: no markerless path on the return path may reach a backend through the
// fallback. A slot whose marker is gone is never reset, so it has no
// uncommitted changes of its own; asking for them in an in-project pool
// answered with the ENCLOSING repository's, which scripted a permanent abort
// over changes that were never in the slot.
func TestReturnAllNeverPromptsOnAMarkerlessSlot(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	inProject := []string{"TREEHOUSE_ROOT=."}

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, inProject, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	if wtPath == "" {
		t.Fatal("could not capture the leased worktree path")
	}
	// Leased, so the slot is a bulk target even once its marker is gone.
	if err := os.Remove(filepath.Join(wtPath, ".git")); err != nil {
		t.Fatalf("removing the slot marker: %v", err)
	}
	// The ENCLOSING repository is the one with uncommitted changes.
	enclosing := filepath.Join(repoDir, "enclosing-change.txt")
	if err := os.WriteFile(enclosing, []byte("not the slot's\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, allErr, code := runTreehouse(t, repoDir, homeDir, inProject, "return", "--all")
	if strings.Contains(allErr, "Clean and return?") {
		t.Fatalf("a markerless slot has no changes of its own to offer, got: %s", allErr)
	}
	if code != 0 {
		t.Fatalf("expected no abort over a foreign repository's changes, got code %d: %s", code, allErr)
	}
	if !strings.Contains(allErr, "Returned 1 of 1 held worktree(s)") {
		t.Fatalf("expected the leased slot released, got: %s", allErr)
	}

	if _, err := os.Stat(enclosing); err != nil {
		t.Fatalf("the enclosing repository's change was disturbed: %v", err)
	}
	entries := statusEntriesFromDir(t, repoDir, repoDir, homeDir, inProject)
	if len(entries) != 1 || entries[0].Status != "damaged" {
		t.Fatalf("expected the lease cleared and the slot left for destroy, got %+v", entries)
	}
}
