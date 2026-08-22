//go:build !windows

package pool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireDoesNotExecuteSeedFilter(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	marker := filepath.Join(t.TempDir(), "filter-started")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("seed.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".worktreeinclude"), []byte("seed.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("seed.env filter=seed-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".gitignore", ".worktreeinclude", ".gitattributes")
	runGit(t, repo, "commit", "-m", "configure seed filter")
	runGit(t, repo, "config", "filter.seed-test.clean", "sh -c 'touch \"$1\"; cat' - "+marker)
	if err := os.WriteFile(filepath.Join(repo, "seed.env"), []byte("seeded\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := AcquireLeaseInfo(repo, poolDir, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Git content filter executed during acquisition: %v", err)
	}
	seeded, err := os.ReadFile(filepath.Join(info.Path, "seed.env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(seeded) != "seeded\n" {
		t.Fatalf("seeded contents = %q, want %q", seeded, "seeded\n")
	}
	if err := Release(poolDir, info.Path); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireIgnoresBrokenRequiredSeedFilter(t *testing.T) {
	repo, poolDir := setupLocalRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("seed.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".worktreeinclude"), []byte("seed.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("seed.env filter=broken-seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".gitignore", ".worktreeinclude", ".gitattributes")
	runGit(t, repo, "commit", "-m", "configure broken seed")
	runGit(t, repo, "config", "filter.broken-seed.clean", "false")
	runGit(t, repo, "config", "filter.broken-seed.required", "true")
	if err := os.WriteFile(filepath.Join(repo, "seed.env"), []byte("seeded\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := AcquireLeaseInfo(repo, poolDir, 1, nil, "test")
	if err != nil {
		t.Fatalf("acquisition invoked required Git filter: %v", err)
	}
	seeded, err := os.ReadFile(filepath.Join(info.Path, "seed.env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(seeded) != "seeded\n" {
		t.Fatalf("seeded contents = %q, want %q", seeded, "seeded\n")
	}
}
