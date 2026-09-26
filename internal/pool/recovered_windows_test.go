//go:build windows

package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveredUntrackedStaysLeasedOnWindows(t *testing.T) {
	_, poolDir, path := recoveredFixture(t)
	file := filepath.Join(path, "scratch", "notes.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := statusOf(t, poolDir, path)
	if st.Status != StatusLeased || !strings.Contains(st.RecoveryReason, "not supported on Windows") || st.RecoveryBackup != "" {
		t.Fatalf("status = %+v", st)
	}
	assertFileContents(t, file, "keep\n")
	if dirs, _ := filepath.Glob(filepath.Join(filepath.Dir(poolDir), "treehouse-recovered-backup-*")); len(dirs) != 0 {
		t.Fatalf("backup folders created on Windows: %v", dirs)
	}
}
