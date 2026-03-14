package ui

import (
	"os"
	"path/filepath"
	"strings"
)

// PrettyPath replaces the user's home directory prefix with "~" for display.
func PrettyPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}
