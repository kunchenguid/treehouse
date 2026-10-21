package gitvcs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Cached observation must notice edits made after a clean result, including
// files normal git status hides, without mistaking ignored files for changes.
func TestIsDirtyObservesChangesAfterCachedCleanResult(t *testing.T) {
	repo := t.TempDir()
	mustGit(t, "", "init", "--initial-branch=main", repo)
	mustGit(t, repo, "config", "user.email", "test@test.com")
	mustGit(t, repo, "config", "user.name", "Test")
	write := func(name, contents string) {
		t.Helper()
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tracked.txt", "initial\n")
	write(".gitignore", "*.ignored\n")
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "commit", "-m", "initial")
	mustGit(t, repo, "config", "status.showUntrackedFiles", "no")
	mustGit(t, repo, "config", "core.untrackedCache", "true")
	assertDirty := func(want bool) {
		t.Helper()
		dirty, err := IsDirty(repo)
		if err != nil || dirty != want {
			t.Fatalf("dirty=%v want=%v err=%v", dirty, want, err)
		}
	}
	write("cache.ignored", "ignored\n")
	assertDirty(false)
	assertDirty(false)
	write(filepath.Join("scratch", "nested", "notes.txt"), "untracked\n")
	assertDirty(true)
	if err := os.Remove(filepath.Join(repo, "scratch", "nested", "notes.txt")); err != nil {
		t.Fatal(err)
	}
	assertDirty(false)
	write("tracked.txt", "changed\n")
	assertDirty(true)
	mustGit(t, repo, "add", "tracked.txt")
	assertDirty(true)
}

// Listing checks cleanliness outside the pool lock while get or return may be
// resetting the same slot. Git refreshes a stale index only while holding
// index.lock, so an observation that rewrites the index can make that
// concurrent reset fail with "index.lock: File exists".
func TestIsDirtyLeavesStaleIndexUnwritten(t *testing.T) {
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	repo := t.TempDir()
	mustGit(t, "", "init", "--initial-branch=main", repo)
	mustGit(t, repo, "config", "user.email", "test@test.com")
	mustGit(t, repo, "config", "user.name", "Test")
	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "commit", "-m", "initial")
	mustGit(t, repo, "config", "core.untrackedCache", "true")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(tracked, past, past); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(repo, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	dirty, err := IsDirty(repo)
	if err != nil || dirty {
		t.Fatalf("dirty=%v err=%v, want a clean result", dirty, err)
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("dirty check rewrote the index, so it held index.lock")
	}
}
