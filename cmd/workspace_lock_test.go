package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspaceLockSurvivesCleanupAndCallbackError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ws-test")
	key, err := workspaceLockPath(path)
	if err != nil {
		t.Fatal(err)
	}
	var original os.FileInfo
	if err := withWorkspaceLock(path, func() error {
		if err := os.Mkdir(path, 0o755); err != nil {
			return err
		}
		existingKey, err := workspaceLockPath(path)
		if err != nil || existingKey != key {
			t.Fatalf("creation changed lock identity: %s, %v", existingKey, err)
		}
		original, err = os.Stat(key)
		if err != nil {
			return err
		}
		called := false
		err = withWorkspaceLock(path, func() error { called = true; return nil })
		if !errors.Is(err, errWorkspaceBusy) || called {
			t.Fatalf("same workspace entered twice: %v, callback=%t", err, called)
		}
		if err := withWorkspaceLock(filepath.Join(filepath.Dir(path), "ws-independent"), func() error { return nil }); err != nil {
			t.Fatalf("independent workspace was blocked: %v", err)
		}
		return os.Remove(path)
	}); err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("callback failure")
	if err := withWorkspaceLock(path, func() error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatal(err)
	}
	if err := withWorkspaceLock(path, func() error { return nil }); err != nil {
		t.Fatalf("callback failure kept the native lock held: %v", err)
	}
	current, err := os.Stat(key)
	if err != nil || !os.SameFile(original, current) {
		t.Fatalf("cleanup or failure replaced the persistent lock: %v", err)
	}
}

func TestWorkspaceLockAliasesShareIdentity(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "ws-test")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(path, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privileges required: %v", err)
		}
		t.Fatal(err)
	}
	if err := withWorkspaceLock(path, func() error {
		err := withWorkspaceLock(alias, func() error { t.Fatal("alias bypassed the lock"); return nil })
		if !errors.Is(err, errWorkspaceBusy) {
			t.Fatalf("alias did not contend: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	parentAlias := filepath.Join(t.TempDir(), "parent-alias")
	if err := os.Symlink(parent, parentAlias); err != nil {
		t.Fatal(err)
	}
	before, err := workspaceLockPath(filepath.Join(parentAlias, "ws-test"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	after, err := workspaceLockPath(filepath.Join(parentAlias, "ws-test"))
	if err != nil || after != before {
		t.Fatalf("parent alias changed identity after removal: %s -> %s (%v)", before, after, err)
	}
}

func TestWorkspaceLockRefusesNonregularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ws-test")
	key, err := workspaceLockPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(key, 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := withWorkspaceLock(path, func() error { called = true; return nil }); err == nil || called {
		t.Fatalf("nonregular lock accepted: %v, callback=%t", err, called)
	}
}
