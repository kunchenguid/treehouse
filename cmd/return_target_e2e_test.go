package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// statusEntries reads the machine-readable pool status, which is what these
// tests judge a return by: the name column `return <name>` accepts and the
// status `return --all` selects on are the same two fields.
func statusEntries(t *testing.T, repoDir, homeDir string) []statusJSONResult {
	t.Helper()
	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "status", "--json")
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
