package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyWorktreeIncludes_NoFile(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := CopyWorktreeIncludes(src, dst); err != nil {
		t.Fatalf("expected no error when .worktreeinclude absent, got: %v", err)
	}
}

func TestCopyWorktreeIncludes_CopiesListedFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".worktreeinclude"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyWorktreeIncludes(src, dst); err != nil {
		t.Fatalf("CopyWorktreeIncludes failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatalf("expected .env copied to worktree: %v", err)
	}
	if string(data) != "SECRET=1\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}

func TestCopyWorktreeIncludes_SkipsCommentsAndBlanks(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.WriteFile(filepath.Join(src, ".worktreeinclude"), []byte("# a comment\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyWorktreeIncludes(src, dst); err != nil {
		t.Fatalf("CopyWorktreeIncludes failed: %v", err)
	}

	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Fatalf("expected no files copied for comment-only include, got %v", entries)
	}
}

func TestCopyWorktreeIncludes_SkipsMissingSourceFile(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.WriteFile(filepath.Join(src, ".worktreeinclude"), []byte("does-not-exist.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyWorktreeIncludes(src, dst); err != nil {
		t.Fatalf("expected missing source file to be silently skipped, got: %v", err)
	}
}

func TestCopyWorktreeIncludes_GlobPattern(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	secretsDir := filepath.Join(src, "secrets")
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "a.json"), []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "b.json"), []byte(`{"b":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".worktreeinclude"), []byte("secrets/*.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyWorktreeIncludes(src, dst); err != nil {
		t.Fatalf("CopyWorktreeIncludes failed: %v", err)
	}

	for _, name := range []string{"a.json", "b.json"} {
		if _, err := os.Stat(filepath.Join(dst, "secrets", name)); err != nil {
			t.Fatalf("expected secrets/%s copied: %v", name, err)
		}
	}
}

func TestCopyWorktreeIncludes_CreatesParentDirs(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	nested := filepath.Join(src, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".worktreeinclude"), []byte("a/b/config.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyWorktreeIncludes(src, dst); err != nil {
		t.Fatalf("CopyWorktreeIncludes failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "a", "b", "config.json")); err != nil {
		t.Fatalf("expected nested file copied with parent dirs: %v", err)
	}
}
