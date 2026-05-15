package hooks

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// quotePath returns a path suitably quoted for use as a shell argument. On
// Windows we use double quotes for cmd.exe; on Unix we use single quotes for
// /bin/sh.
func quotePath(p string) string {
	if runtime.GOOS == "windows" {
		return `"` + p + `"`
	}
	return `'` + p + `'`
}

func TestRun_Success(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "ran.txt")

	var script string
	if runtime.GOOS == "windows" {
		script = "echo hi > " + quotePath(sentinel)
	} else {
		script = "echo hi > " + quotePath(sentinel)
	}

	var stdout, stderr bytes.Buffer
	Run([]string{script}, dir, &stdout, &stderr)

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected sentinel %s to exist: %v\nstderr: %s", sentinel, err, stderr.String())
	}
}

func TestRun_FailingCommandDoesNotStopRemaining(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "after.txt")

	// First command: a non-existent program that should fail.
	// Second command: writes the sentinel - must run despite the first failure.
	var fail, ok string
	if runtime.GOOS == "windows" {
		fail = "this-command-definitely-does-not-exist-xyzzy"
		ok = "echo hi > " + quotePath(sentinel)
	} else {
		fail = "this-command-definitely-does-not-exist-xyzzy"
		ok = "echo hi > " + quotePath(sentinel)
	}

	var stdout, stderr bytes.Buffer
	Run([]string{fail, ok}, dir, &stdout, &stderr)

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("expected second command to still run despite first failing: %v\nstderr: %s", err, stderr.String())
	}

	// The failure should be logged to stderr.
	if !strings.Contains(stderr.String(), "hook command failed") {
		t.Errorf("expected stderr to log hook failure, got: %s", stderr.String())
	}
}

func TestRun_EmptyListIsNoop(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	Run(nil, dir, &stdout, &stderr)
	Run([]string{}, dir, &stdout, &stderr)
	if stderr.Len() != 0 || stdout.Len() != 0 {
		t.Errorf("expected no output for empty hooks, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRun_RunsInGivenDir(t *testing.T) {
	dir := t.TempDir()
	// Write a relative-path sentinel so we can confirm cwd is dir.
	var script string
	if runtime.GOOS == "windows" {
		script = "echo hi > cwd-sentinel.txt"
	} else {
		script = "echo hi > cwd-sentinel.txt"
	}

	var stdout, stderr bytes.Buffer
	Run([]string{script}, dir, &stdout, &stderr)

	if _, err := os.Stat(filepath.Join(dir, "cwd-sentinel.txt")); err != nil {
		t.Fatalf("expected hook to run in %s: %v\nstderr: %s", dir, err, stderr.String())
	}
}
