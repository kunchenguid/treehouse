package gitvcs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelativeWorktreeRegistrationCanBeCleanedFromOtherDirectory(t *testing.T) {
	wtPath, resetRef, head := setupSafeResetWorktree(t, refFormatInitFlag(t, "files"))
	gitdir, err := runGit(wtPath, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	gitdir = strings.TrimSpace(gitdir)
	marker := "gitdir: " + gitdir + "\n"
	if err := os.WriteFile(filepath.Join(wtPath, ".git"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	// Resolve symlinks first (macOS temp dirs live under /var -> /private/var):
	// a backlink computed across two spellings climbs to / and resolves from
	// any working directory, which would hide a cwd-relative lookup.
	realGitdir, err := filepath.EvalSymlinks(gitdir)
	if err != nil {
		t.Fatal(err)
	}
	realWorktree, err := filepath.EvalSymlinks(wtPath)
	if err != nil {
		t.Fatal(err)
	}
	backlink, err := filepath.Rel(realGitdir, filepath.Join(realWorktree, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "gitdir"), []byte(backlink+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(otherDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if _, err := os.Stat(backlink); err == nil {
		t.Fatalf("test setup: backlink %q resolves from the working directory, so it cannot catch a cwd-relative lookup", backlink)
	}
	info, err := os.Stat(wtPath)
	if err != nil {
		t.Fatal(err)
	}
	root, handle, err := openCleanupRoot(wtPath, info, authenticateLinkedWorktree)
	if err != nil {
		t.Fatalf("open relative registration: %v", err)
	}
	root.Close()
	handle.Close()
	if err := ResetWorktreeToRefVerified(wtPath, resetRef, head, nil, func() error { return nil }); err != nil {
		t.Fatalf("verified reset: %v", err)
	}
	got, err := runGit(wtPath, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(got) != resetRef {
		t.Fatalf("HEAD = %q, err = %v; want %s", strings.TrimSpace(got), err, resetRef)
	}
}

func TestValidateWorktreeRegistration(t *testing.T) {
	for _, kind := range []string{"absolute", "relative", "wrong-slot", "missing-backlink", "missing-registration", "empty-backlink", "malformed-marker", "wrong-filename"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			worktree := filepath.Join(base, "slot")
			registration := filepath.Join(base, "registration")
			for _, dir := range []string{worktree, registration} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			gitdir := registration
			backlink := filepath.Join(worktree, ".git")
			switch kind {
			case "relative":
				var err error
				gitdir, err = filepath.Rel(worktree, registration)
				if err != nil {
					t.Fatal(err)
				}
				backlink, err = filepath.Rel(registration, backlink)
				if err != nil {
					t.Fatal(err)
				}
			case "wrong-slot":
				other := t.TempDir()
				backlink = filepath.Join(other, ".git")
			case "empty-backlink":
				backlink = ""
			case "wrong-filename":
				backlink = filepath.Join(worktree, "other")
			}
			marker := "gitdir: " + gitdir + "\n"
			if kind == "malformed-marker" {
				marker = "not a gitdir\n"
			}
			if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte(marker), 0o644); err != nil {
				t.Fatal(err)
			}
			if kind != "missing-backlink" && kind != "missing-registration" {
				if err := os.WriteFile(filepath.Join(registration, "gitdir"), []byte(backlink+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "missing-registration" {
				if err := os.Remove(registration); err != nil {
					t.Fatal(err)
				}
			}
			err := ValidateWorktreeRegistration(worktree)
			wantValid := kind == "absolute" || kind == "relative"
			if (err == nil) != wantValid {
				t.Fatalf("validation = %v, want valid=%t", err, wantValid)
			}
			if wantValid {
				alias := filepath.Join(base, "alias")
				if err := os.Symlink(worktree, alias); err != nil {
					t.Logf("symlink alias unavailable: %v", err)
					return
				}
				if err := ValidateWorktreeRegistration(alias); err != nil {
					t.Fatalf("same directory through an alias: %v", err)
				}
			}
		})
	}
}
