package cmd

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestWindowsReturnPathArgRewritesGitBashDrivePaths(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"/c/Users/me/repo":    "C:/Users/me/repo",
		"/C/Users/me/repo":    "C:/Users/me/repo",
		"/d/work/slot/repo":   "D:/work/slot/repo",
		"/c\\Users\\me\\repo": "C:\\Users\\me\\repo",
	}
	for arg, want := range cases {
		if got := windowsReturnPathArg(arg, `C:\Users\me`); got != want {
			t.Errorf("windowsReturnPathArg(%q) = %q, want %q", arg, got, want)
		}
	}
}

func TestWindowsReturnPathArgLeavesOtherPathsAlone(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{
		`C:\Users\me\repo`,
		"C:/Users/me/repo",
		"/c",
		"/usr/share/x",
		"/cat/x",
		"3",
		"slot-a",
		"~",
		"~foo\\x",
	} {
		if got := windowsReturnPathArg(arg, `C:\Users\me`); got != arg {
			t.Errorf("windowsReturnPathArg(%q) = %q, want unchanged", arg, got)
		}
	}
}

func TestWindowsReturnPathArgExpandsTildeAgainstHome(t *testing.T) {
	t.Parallel()

	home := `C:\Users\me`
	cases := map[string]string{
		`~\AppData\Local\pool\1\repo`: filepath.Join(home, `AppData\Local\pool\1\repo`),
		"~/AppData/Local/pool/1/repo": filepath.Join(home, "AppData/Local/pool/1/repo"),
	}
	for arg, want := range cases {
		if got := windowsReturnPathArg(arg, home); got != want {
			t.Errorf("windowsReturnPathArg(%q) = %q, want %q", arg, got, want)
		}
	}
}

func TestWindowsReturnPathArgKeepsTildeWithoutHome(t *testing.T) {
	t.Parallel()

	arg := `~\AppData\Local\pool\1\repo`
	if got := windowsReturnPathArg(arg, ""); got != arg {
		t.Errorf("windowsReturnPathArg(%q, no home) = %q, want unchanged", arg, got)
	}
}

// resolveWorktreePath must hand the Git Bash spelling to filepath.Abs as a
// real drive path; before the fix Abs produced C:\c\Users\... instead.
func TestResolveWorktreePathAcceptsGitBashDrivePath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive-letter resolution is Windows-only")
	}

	got, err := resolveWorktreePath([]string{"/c/Users/treehouse-probe/repo"})
	if err != nil {
		t.Fatalf("resolveWorktreePath: %v", err)
	}
	if want := `C:\Users\treehouse-probe\repo`; got != want {
		t.Errorf("resolveWorktreePath(/c/...) = %q, want %q", got, want)
	}
}
