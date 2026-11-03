package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReclaimEmptySlotDirectory(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "nonempty", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "slot")
			switch kind {
			case "empty", "nonempty":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if kind == "nonempty" {
					if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("keep"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			case "file":
				if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := t.TempDir()
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			err := reclaimEmptySlotDirectory(path)
			if kind == "missing" || kind == "empty" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("slot path remains: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("expected refusal naming %s, got %v", path, err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("occupant removed: %v", err)
			}
			if kind == "nonempty" || kind == "file" {
				file := path
				if kind == "nonempty" {
					file = filepath.Join(path, "work.txt")
				}
				if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
					t.Fatalf("work changed: %q, %v", data, err)
				}
			}
		})
	}
}

func TestAcquireLeftoverSlotDirectory(t *testing.T) {
	for _, nonempty := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "nonempty"}[nonempty], func(t *testing.T) {
			repo, pool := setupRepo(t)
			path := filepath.Join(pool, "1", filepath.Base(repo))
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(path, "work.txt")
			if nonempty {
				if err := os.WriteFile(file, []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Acquire(repo, pool, 2, nil)
			if !nonempty {
				if err != nil || got != path {
					t.Fatalf("acquire = %q, %v; want %s", got, err, path)
				}
				if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "inspect") {
				t.Fatalf("expected actionable refusal, got %q, %v", got, err)
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
				t.Fatalf("work changed: %q, %v", data, err)
			}
		})
	}
}
