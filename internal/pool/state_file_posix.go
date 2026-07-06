//go:build !windows

package pool

import (
	"os"
	"path/filepath"
)

func commitStateFile(tmpPath, path string, _ bool) error {
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
