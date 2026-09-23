//go:build darwin

package gitvcs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSeedWorktreeCOW(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, ".cache/go-build/**\n.env\nnode_modules/.cache/**\n")
	writeTestFile(t, repo, ".cache/go-build/object", "build artifact")
	writeTestFile(t, repo, ".cache/go-build/.env", "credential")
	writeTestFile(t, repo, ".cache/go-build/credentials.json", "credential")
	writeTestFile(t, repo, ".cache/go-build/state/private", "private")
	writeTestFile(t, repo, "node_modules/.cache/nested/.jj/marker", "nested")
	writeTestFile(t, repo, ".env", "credential")
	writeTestFile(t, repo, "node_modules/.cache/package/index", "package")
	writeTestFile(t, repo, "node_modules/.cache/nested/index", "nested")
	if err := os.Symlink(filepath.Join(repo, ".env"), filepath.Join(repo, ".cache/go-build/link")); err != nil {
		t.Fatal(err)
	}
	orig := cloneFileAt
	calls := 0
	cloneFileAt = func(src, dst int, name string, flags int) error {
		calls++
		return orig(src, dst, name, flags)
	}
	t.Cleanup(func() { cloneFileAt = orig })
	seeded, err := SeedWorktreeCOW(repo, worktree, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 2 || calls != 2 {
		t.Fatalf("seeded = %v, clone calls = %d", seeded, calls)
	}
	for _, name := range []string{".cache/go-build/object", "node_modules/.cache/package/index"} {
		assertTestFile(t, worktree, name, map[string]string{".cache/go-build/object": "build artifact", "node_modules/.cache/package/index": "package"}[name])
	}
	for _, name := range []string{".cache/go-build/.env", ".cache/go-build/credentials.json", ".cache/go-build/state/private", "node_modules/.cache/nested/.jj/marker", ".cache/go-build/link", ".env", "node_modules/.cache/nested/index"} {
		if _, err := os.Lstat(filepath.Join(worktree, name)); !os.IsNotExist(err) {
			t.Fatalf("unsafe path %s was seeded: %v", name, err)
		}
	}
	writeTestFile(t, worktree, ".cache/go-build/object", "changed clone")
	assertTestFile(t, repo, ".cache/go-build/object", "build artifact")
	if err := ResetWorktreeWithSeededPaths(worktree, "main", seeded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".cache/go-build/object")); !os.IsNotExist(err) {
		t.Fatalf("inventory cleanup failed: %v", err)
	}
}

func TestSafeCachePath(t *testing.T) {
	for _, name := range []string{".cache/go-build/.env.local", ".cache/uv/config/key", "node_modules/.cache/secrets.json", "target/debug/incremental/data/private", ".next/cache/credentials", ".gradle/caches/firstmate/state"} {
		if safeCachePath(name) {
			t.Errorf("unsafe path allowed: %s", name)
		}
	}
	for _, name := range []string{".cache/go-build/hash", ".cache/uv/wheels/hash", "target/release/incremental/pkg/hash", ".gradle/caches/v1/hash", ".next/cache/webpack/hash"} {
		if !safeCachePath(name) {
			t.Errorf("cache path refused: %s", name)
		}
	}
	if safeCachePath(strings.ToUpper(".cache/go-build/.env")) {
		t.Fatal("case-variant private path allowed")
	}
}

func TestSeedWorktreeCOWRejectsExistingDestination(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, ".cache/go-build/**\n")
	writeTestFile(t, repo, ".cache/go-build/a", "source")
	writeTestFile(t, worktree, ".cache/go-build/a", "owner")
	if _, err := SeedWorktreeCOW(repo, worktree, nil); err == nil {
		t.Fatal("expected existing destination refusal")
	}
	assertTestFile(t, worktree, ".cache/go-build/a", "owner")
}

func TestSeedWorktreeCOWUnsupportedAndFailure(t *testing.T) {
	repo, worktree := setupSeedWorktree(t, ".cache/go-build/**\n")
	writeTestFile(t, repo, ".cache/go-build/a", "a")
	orig := cloneFileAt
	t.Cleanup(func() { cloneFileAt = orig })
	cloneFileAt = func(int, int, string, int) error { return unix.EXDEV }
	seeded, err := SeedWorktreeCOW(repo, worktree, nil)
	if err != nil || len(seeded) != 0 {
		t.Fatalf("cross-device = %v, %v", seeded, err)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".cache/go-build/a")); !os.IsNotExist(err) {
		t.Fatalf("cross-device cache was copied: %v", err)
	}
	cloneFileAt = func(int, int, string, int) error { return unix.ENOSPC }
	if _, err := SeedWorktreeCOW(repo, worktree, nil); !errors.Is(err, unix.ENOSPC) {
		t.Fatalf("storage failure = %v", err)
	}
	cloneFileAt = func(_, dst int, name string, _ int) error {
		fd, err := unix.Openat(dst, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		unix.Close(fd)
		return unix.ENOSPC
	}
	seeded, err = SeedWorktreeCOW(repo, worktree, nil)
	if !errors.Is(err, unix.ENOSPC) || len(seeded) != 1 {
		t.Fatalf("partial clone = %v, %v", seeded, err)
	}
	if err := RemoveSeededPaths(worktree, seeded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".cache/go-build/a")); !os.IsNotExist(err) {
		t.Fatalf("partial clone cleanup: %v", err)
	}
}
