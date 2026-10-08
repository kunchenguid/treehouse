//go:build darwin

package fileclone

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSharingUnsupportedRepresentations(t *testing.T) {
	for _, kind := range []string{"flags", "resource-fork", "sparse"} {
		t.Run(kind, func(t *testing.T) {
			src, dst, _ := cloneFixture(t)
			target := filepath.Join(dst, "asset")
			want := ""
			switch kind {
			case "flags":
				if err := unix.Chflags(target, unix.UF_HIDDEN); err != nil {
					t.Fatal(err)
				}
				want = "unsupported_flags_or_mode"
			case "resource-fork":
				if err := unix.Setxattr(target, "com.apple.ResourceFork", []byte("representation metadata"), 0); err != nil {
					t.Fatal(err)
				}
				want = "representation_xattr"
			case "sparse":
				for _, root := range []string{src, dst} {
					if err := os.Truncate(filepath.Join(root, "asset"), 8*1024*1024); err != nil {
						t.Fatal(err)
					}
				}
				want = "sparse_or_compressed"
			}
			before, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			r, err := Share(context.Background(), src, dst, []string{"asset"})
			if err != nil || r.Cloned != 0 || r.Skipped[want] != 1 {
				t.Fatalf("unsafe metadata: %+v %v", r, err)
			}
			after, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("unsupported representation changed")
			}
		})
	}
}

func TestSharingIncompleteCleanupFailsWithoutDeletingUnknownData(t *testing.T) {
	src, dst, data := cloneFixture(t)
	var unknown string
	_, err := share(context.Background(), src, dst, []string{"asset"}, operations{clone: cloneFile, metadata: func(fd int, m metadata) error {
		paths, err := filepath.Glob(filepath.Join(dst, ".treehouse-sharing-*"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("staging: %v %v", paths, err)
		}
		unknown = filepath.Join(paths[0], "unknown")
		writeTestFile(t, unknown, []byte("preserve this"))
		return restoreMetadata(fd, m)
	}})
	if !errors.Is(err, errCleanup) {
		t.Fatalf("cleanup failure was not fatal: %v", err)
	}
	got, err := os.ReadFile(unknown)
	if err != nil || string(got) != "preserve this" {
		t.Fatal("cleanup recursively deleted unknown data")
	}
	got, err = os.ReadFile(filepath.Join(dst, "asset"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("cleanup error changed bytes")
	}
}
