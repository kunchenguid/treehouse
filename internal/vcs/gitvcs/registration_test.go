package gitvcs

import (
	"os"
	"path/filepath"
	"testing"
)

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
