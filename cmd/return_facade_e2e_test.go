package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/pool"
)

func symlinkReturnTestDirectory(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		var errno syscall.Errno
		// Windows reports missing symlink privileges as 1314 rather than
		// mapping them to os.ErrPermission on all Go versions.
		if runtime.GOOS == "windows" && (os.IsPermission(err) || errors.As(err, &errno) && errno == 1314) {
			t.Skipf("creating directory symlinks requires Windows symlink permission: %v", err)
		}
		t.Fatal(err)
	}
}

func TestReturnOutsideRepositoryRejectsMissingUnmanagedPath(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	missing := filepath.Join(t.TempDir(), "missing")
	_, stderr, code := runTreehouseFromDir(t, repoDir, t.TempDir(), homeDir, nil, "return", missing)
	if code == 0 || !strings.Contains(stderr, "is not managed by treehouse") {
		t.Fatalf("missing unmanaged path was not refused as a path (code %d): %s", code, stderr)
	}
}

func TestReturnOutsideRepositoryKeepsMissingManagedPath(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--json", "--no-fetch")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	var lease pool.LeaseInfo
	if err := json.Unmarshal([]byte(stdout), &lease); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repoDir, "worktree", "remove", "--force", lease.Path)
	if _, err := os.Lstat(lease.Path); !os.IsNotExist(err) {
		t.Fatalf("test worktree path still exists: %v", err)
	}
	_, stderr, code = runTreehouseFromDir(t, repoDir, t.TempDir(), homeDir, nil, "return", "--if-lease-id", lease.LeaseID, lease.Path)
	if code != 0 {
		t.Fatalf("return missing registered path failed (code %d): %s", code, stderr)
	}
}

func TestReturnOutsideRepositoryResolvesRecordedSymlinkedPaths(t *testing.T) {
	for _, tc := range []struct {
		name           string
		templated      bool
		returnVia      string
		withoutRootEnv bool
	}{
		{name: "pool root via recorded path", returnVia: "recorded"},
		{name: "pool root via physical path", returnVia: "physical"},
		{name: "pool root via physical path without root override", returnVia: "physical", withoutRootEnv: true},
		{name: "pool root via facade", returnVia: "facade"},
		{name: "templated parent via facade", templated: true, returnVia: "facade"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoDir, homeDir := setupTestRepo(t)
			physicalParent := t.TempDir()
			physicalParent, err := filepath.EvalSymlinks(physicalParent)
			if err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(t.TempDir(), "alias")
			symlinkReturnTestDirectory(t, physicalParent, alias)

			extraEnv := []string{"TREEHOUSE_ROOT=" + alias}
			if tc.templated {
				extraEnv = []string{"TREEHOUSE_WORKTREE_PATH=" + filepath.Join(alias, "{repo}-{slot}")}
			}
			stdout, stderr, code := runTreehouse(t, repoDir, homeDir, extraEnv, "get", "--lease", "--json", "--no-fetch")
			if code != 0 {
				t.Fatalf("get --lease failed (code %d): %s", code, stderr)
			}
			var lease pool.LeaseInfo
			if err := json.Unmarshal([]byte(stdout), &lease); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(lease.Path, alias+string(filepath.Separator)) {
				t.Fatalf("acquired %s, want a path through %s", lease.Path, alias)
			}
			physicalPath, err := filepath.EvalSymlinks(lease.Path)
			if err != nil {
				t.Fatal(err)
			}
			if physicalPath == lease.Path {
				t.Fatalf("test did not create distinct requested and physical paths: %s", lease.Path)
			}

			argument := lease.Path
			switch tc.returnVia {
			case "physical":
				argument = physicalPath
			case "facade":
				argument = filepath.Join(t.TempDir(), "facade")
				symlinkReturnTestDirectory(t, lease.Path, argument)
			}
			outside := t.TempDir()
			returnEnv := extraEnv
			if tc.withoutRootEnv {
				returnEnv = nil
			}
			_, stderr, code = runTreehouseFromDir(t, repoDir, outside, homeDir, returnEnv, "return", "--if-lease-id", lease.LeaseID, argument)
			if code != 0 {
				t.Fatalf("return %s from outside repository failed (code %d): %s", argument, code, stderr)
			}
			root := homeDir
			if !tc.templated {
				root = alias
			}
			poolDir, err := config.ResolvePoolDir(repoDir, root)
			if err != nil {
				t.Fatal(err)
			}
			state, err := pool.ReadState(poolDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Worktrees) != 1 || state.Worktrees[0].Path != lease.Path || state.Worktrees[0].Leased {
				t.Fatalf("return must clear the lease at its recorded path: %+v", state.Worktrees)
			}
		})
	}
}
