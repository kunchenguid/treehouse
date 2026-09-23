//go:build darwin

package pool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCOWCacheLeaseReuseAndCleanup(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	for name, content := range map[string]string{".gitignore": ".cache/\n.env\n", ".worktreeinclude": ".cache/**\n.env\n", ".cache/go-build/build.bin": "first", ".env": "secret"} {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", ".gitignore", ".worktreeinclude")
	runGit(t, repo, "commit", "-m", "select cache")
	opts := AcquireOptions{COWCache: true, SkipFetch: true}
	lease, err := AcquireLeaseInfoWithOptions(repo, poolDir, 1, nil, "test", opts)
	if err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, filepath.Join(lease.Path, ".cache/go-build/build.bin"), "first")
	if _, err := os.Stat(filepath.Join(lease.Path, ".env")); !os.IsNotExist(err) {
		t.Fatalf("private file seeded: %v", err)
	}
	if _, err := AcquireWithOptions(repo, poolDir, 1, nil, opts); err == nil {
		t.Fatal("leased slot handed out")
	}
	if err := Release(poolDir, lease.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(lease.Path, ".cache/go-build/build.bin")); !os.IsNotExist(err) {
		t.Fatalf("return left seeded cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".cache/go-build/build.bin"), []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	reused, err := AcquireWithOptions(repo, poolDir, 1, nil, opts)
	if err != nil || reused != lease.Path {
		t.Fatalf("reuse = %q, %v", reused, err)
	}
	assertFileContents(t, filepath.Join(reused, ".cache", "go-build", "build.bin"), "second")
	if err := Release(poolDir, reused); err != nil {
		t.Fatal(err)
	}
	// A dirty checkout is never reset just to refresh the cache.
	if err := os.WriteFile(filepath.Join(reused, "README.md"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireWithOptions(repo, poolDir, 1, nil, opts); err == nil {
		t.Fatal("dirty slot was recycled")
	}
	assertFileContents(t, filepath.Join(reused, "README.md"), "uncommitted")
}
