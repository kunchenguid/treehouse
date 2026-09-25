//go:build darwin

package fileclone

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func cloneFixture(t *testing.T) (string, string, []byte) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(root, &fs); err != nil || unix.ByteSliceToString(fs.Fstypename[:]) != "apfs" {
		t.Skip("requires an APFS temporary directory")
	}
	src, dst := filepath.Join(root, "source"), filepath.Join(root, "target")
	data := bytes.Repeat([]byte("independent file data\n"), 8192)
	for _, dir := range []string{src, dst} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "asset")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	return src, dst, data
}

func attributesOf(t *testing.T, path string) (attributes, unix.Stat_t) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a, err := fileAttributes(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	return a, st
}

func assertNoStaging(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".treehouse-sharing-") {
			t.Errorf("staging survived: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSharePreservesMetadataAndIndependentFiles(t *testing.T) {
	src, dst, original := cloneFixture(t)
	source, target := filepath.Join(src, "asset"), filepath.Join(dst, "asset")
	if err := os.Chmod(target, 0o751); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1234567890, 123456789)
	if err := os.Chtimes(target, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(source, "com.treehouse.source-only", []byte("donor"), 0); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(target, "com.treehouse.destination", []byte("original"), 0); err != nil {
		t.Fatal(err)
	}
	before, old := attributesOf(t, target)
	r, err := Share(context.Background(), src, dst, []string{"asset"})
	if err != nil || r.Cloned != 1 || r.PrivateBytesReduced != before.privateBytes || r.LogicalBytes != uint64(len(original)) {
		t.Fatalf("share: %+v, %v", r, err)
	}
	a, sourceStat := attributesOf(t, source)
	b, st := attributesOf(t, target)
	if a.cloneID == 0 || a.cloneID != b.cloneID || sourceStat.Ino == st.Ino || st.Ino == old.Ino || b.privateBytes != 0 {
		t.Fatalf("not independent clones: %+v %+v", a, b)
	}
	if st.Mode != old.Mode || st.Mtim != old.Mtim || st.Atim != old.Atim {
		t.Fatalf("metadata changed: %+v -> %+v", old, st)
	}
	f, _ := os.Open(target)
	attrs, err := readXattrs(int(f.Fd()))
	f.Close()
	if err != nil || attrs["com.treehouse.destination"] != "original" {
		t.Fatalf("destination xattrs: %v %v", attrs, err)
	}
	if _, leaked := attrs["com.treehouse.source-only"]; leaked {
		t.Fatal("source-only xattr leaked")
	}
	r, err = Share(context.Background(), src, dst, []string{"asset"})
	if err != nil || r.Skipped["already_shared"] != 1 {
		t.Fatalf("repeat: %+v %v", r, err)
	}
	f, err = os.OpenFile(target, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("target edit"), 0); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got, err := os.ReadFile(source); err != nil || !bytes.Equal(got, original) {
		t.Fatal("target edit changed source")
	}
	if err := os.WriteFile(source, []byte("source edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.HasPrefix(got, []byte("target edit")) || !bytes.Equal(got[11:], original[11:]) {
		t.Fatal("source edit/deletion affected clone")
	}
	assertNoStaging(t, dst)
}

func TestShareSafeSkips(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		change func(*testing.T, string, string)
	}{
		{"different", "different_content", func(t *testing.T, src, dst string) {
			writeTestFile(t, filepath.Join(dst, "asset"), bytes.Repeat([]byte("x"), 172032))
		}},
		{"small", "below_threshold", func(t *testing.T, src, dst string) { writeTestFile(t, filepath.Join(dst, "asset"), []byte("small")) }},
		{"hardlink", "hardlink", func(t *testing.T, src, dst string) {
			if err := os.Link(filepath.Join(dst, "asset"), filepath.Join(dst, "alias")); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", "destination_not_regular_or_symlink", func(t *testing.T, src, dst string) {
			if err := os.Remove(filepath.Join(dst, "asset")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(src, "asset"), filepath.Join(dst, "asset")); err != nil {
				t.Fatal(err)
			}
		}},
		{"acl", "acl", func(t *testing.T, src, dst string) {
			out, err := exec.Command("chmod", "+a", "everyone allow read", filepath.Join(dst, "asset")).CombinedOutput()
			if err != nil {
				t.Fatalf("chmod: %s %v", out, err)
			}
		}},
		{"parent-acl", "acl", func(t *testing.T, src, dst string) {
			out, err := exec.Command("chmod", "+a", "everyone allow read", dst).CombinedOutput()
			if err != nil {
				t.Fatalf("chmod: %s %v", out, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, dst, _ := cloneFixture(t)
			tc.change(t, src, dst)
			before, err := os.Lstat(filepath.Join(dst, "asset"))
			if err != nil {
				t.Fatal(err)
			}
			r, err := Share(context.Background(), src, dst, []string{"asset"})
			if err != nil || r.Cloned != 0 || r.Skipped[tc.reason] != 1 {
				t.Fatalf("skip: %+v %v", r, err)
			}
			after, err := os.Lstat(filepath.Join(dst, "asset"))
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("skip replaced destination")
			}
			assertNoStaging(t, dst)
		})
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestShareFailureKeepsOriginal(t *testing.T) {
	for _, code := range []error{unix.ENOSPC, unix.ENOTSUP} {
		t.Run(code.Error(), func(t *testing.T) {
			src, dst, original := cloneFixture(t)
			r, err := share(context.Background(), src, dst, []string{"asset"}, operations{
				clone: func(context.Context, int, int, string, int) error { return code }, metadata: restoreMetadata,
			})
			if err != nil || r.Skipped["kept_after_error"] != 1 {
				t.Fatalf("failure: %+v %v", r, err)
			}
			got, _ := os.ReadFile(filepath.Join(dst, "asset"))
			if !bytes.Equal(got, original) {
				t.Fatal("failure changed original")
			}
			assertNoStaging(t, dst)
		})
	}
}

func TestShareRejectsMetadataContentCorruption(t *testing.T) {
	src, dst, original := cloneFixture(t)
	r, err := share(context.Background(), src, dst, []string{"asset"}, operations{clone: cloneFile, metadata: func(fd int, m metadata) error {
		paths, err := filepath.Glob(filepath.Join(dst, ".treehouse-sharing-*", "clone"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("expected one staged clone: %v %v", paths, err)
		}
		if err := os.WriteFile(paths[0], bytes.Repeat([]byte("x"), len(original)), 0o644); err != nil {
			return err
		}
		return restoreMetadata(fd, m)
	}})
	if err != nil || r.Skipped["metadata_changed_content"] != 1 {
		t.Fatalf("corruption: %+v %v", r, err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "asset"))
	if !bytes.Equal(got, original) {
		t.Fatal("corruption published")
	}
	assertNoStaging(t, dst)
}

func TestShareDetectsDestinationWriterAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "writer", true: "cancel"}[cancel], func(t *testing.T) {
			src, dst, original := cloneFixture(t)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			_, err := share(ctx, src, dst, []string{"asset"}, operations{metadata: restoreMetadata, clone: func(_ context.Context, fd, dir int, name string, flags int) error {
				if err := unix.Fclonefileat(fd, dir, name, flags); err != nil {
					return err
				}
				if cancel {
					stop()
				} else {
					writeTestFile(t, filepath.Join(dst, "asset"), []byte("new edit"))
				}
				return nil
			}})
			want := errDestinationChanged
			if cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			got, _ := os.ReadFile(filepath.Join(dst, "asset"))
			if cancel && !bytes.Equal(got, original) || !cancel && string(got) != "new edit" {
				t.Fatal("new edit/original lost")
			}
			assertNoStaging(t, dst)
		})
	}
}
