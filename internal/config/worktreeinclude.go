package config

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CopyWorktreeIncludes copies gitignored files listed in .worktreeinclude from
// repoRoot into the new worktree at wtPath. Lines starting with # are comments;
// each other non-empty line is a path pattern relative to repoRoot (globs ok).
// Missing .worktreeinclude or missing source files are silently skipped.
func CopyWorktreeIncludes(repoRoot, wtPath string) error {
	includePath := filepath.Join(repoRoot, ".worktreeinclude")
	f, err := os.Open(includePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	patterns, err := parseWorktreeInclude(f)
	if err != nil {
		return err
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(repoRoot, pattern))
		if err != nil {
			continue // malformed pattern — skip
		}
		for _, src := range matches {
			rel, err := filepath.Rel(repoRoot, src)
			if err != nil {
				continue
			}
			dst := filepath.Join(wtPath, rel)
			if err := copyFile(src, dst); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseWorktreeInclude(r io.Reader) ([]string, error) {
	var patterns []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns, scanner.Err()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
