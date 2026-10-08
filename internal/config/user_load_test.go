package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUserMissingFileIsNoConfig(t *testing.T) {
	setUserHome(t, t.TempDir())
	_, present, err := loadUser()
	if err != nil || present {
		t.Fatalf("missing user config: present=%v, err=%v", present, err)
	}
}

func TestLoadUserPropagatesReadErrors(t *testing.T) {
	for _, kind := range []string{"directory", "symlink loop", "inaccessible parent"} {
		t.Run(kind, func(t *testing.T) {
			setUserHome(t, t.TempDir())
			path := userConfigPath()
			parent := filepath.Dir(path)
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink loop":
				if err := os.Symlink(filepath.Base(path), path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "inaccessible parent":
				if err := os.WriteFile(path, []byte("max_trees=7\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(parent, 0); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(parent, 0o755)
				if _, err := os.ReadFile(path); err == nil {
					t.Skip("current user can access mode-000 directories")
				}
			}
			_, present, err := loadUser()
			if err == nil || present {
				t.Fatalf("read failure treated as absent: present=%v, err=%v", present, err)
			}
			if _, err := LoadGlobal(); err == nil {
				t.Fatal("LoadGlobal swallowed user config read error")
			}
		})
	}
}

func TestLoadUserRequiresResolvableHome(t *testing.T) {
	setUserHome(t, "")
	if _, present, err := loadUser(); err == nil || present {
		t.Fatalf("unresolvable home treated as absent: present=%v, err=%v", present, err)
	}
}
