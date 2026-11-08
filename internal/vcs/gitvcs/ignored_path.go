package gitvcs

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// IsPathIgnored reports whether Git ignores path, preserving inspection failures.
func IsPathIgnored(repoRoot, path string) (bool, error) {
	cmd := exec.Command("git", "check-ignore", "--quiet", "--", path)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git check-ignore: %w: %s", err, strings.TrimSpace(string(output)))
}
