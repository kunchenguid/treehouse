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

func TestShareSameSizeDifferentBytes(t *testing.T) {
	src, dst, data := cloneFixture(t)
	writeTestFile(t, filepath.Join(dst, "asset"), bytes.Repeat([]byte("x"), len(data)))
	r, err := Share(context.Background(), src, dst, []string{"asset"})
	if err != nil || r.Skipped["different_content"] != 1 {
		t.Fatalf("different bytes: %+v %v", r, err)
	}
}

func TestShareSymlinkParentsAndUnsafePaths(t *testing.T) {
	src, dst, data := cloneFixture(t)
	if err := os.Symlink(dst, filepath.Join(dst, "link")); err != nil {
		t.Fatal(err)
	}
	r, err := Share(context.Background(), src, dst, []string{"link/asset", "../target/asset"})
	if err != nil || r.Skipped["destination_missing_or_symlink"] != 1 || r.Skipped["unsafe_path"] != 1 {
		t.Fatalf("unsafe paths: %+v %v", r, err)
	}
	alias := filepath.Join(filepath.Dir(dst), "alias")
	if err := os.Symlink(dst, alias); err != nil {
		t.Fatal(err)
	}
	r, err = Share(context.Background(), src, alias, []string{"asset"})
	if err != nil || !strings.Contains(r.Reason, "symlink") {
		t.Fatalf("symlink root: %+v %v", r, err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "asset"))
	if !bytes.Equal(got, data) {
		t.Fatal("symlink target changed")
	}
}

func TestShareMinimumSizeBoundary(t *testing.T) {
	for _, size := range []int{MinimumSize - 1, MinimumSize} {
		src, dst, _ := cloneFixture(t)
		for _, root := range []string{src, dst} {
			writeTestFile(t, filepath.Join(root, "asset"), bytes.Repeat([]byte("a"), size))
		}
		r, err := Share(context.Background(), src, dst, []string{"asset"})
		want := 0
		if size == MinimumSize {
			want = 1
		}
		if err != nil || r.Cloned != want {
			t.Fatalf("size %d: %+v %v", size, r, err)
		}
	}
}

func TestShareSourceMutationAndMetadataFailure(t *testing.T) {
	for _, kind := range []string{"source", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			src, dst, data := cloneFixture(t)
			ops := operations{clone: cloneFile, metadata: restoreMetadata}
			if kind == "source" {
				ops.clone = func(_ context.Context, fd, dir int, name string, flags int) error {
					writeTestFile(t, filepath.Join(src, "asset"), bytes.Repeat([]byte("x"), len(data)))
					return unix.Fclonefileat(fd, dir, name, flags)
				}
			} else {
				ops.metadata = func(int, metadata) error { return unix.EPERM }
			}
			r, err := share(context.Background(), src, dst, []string{"asset"}, ops)
			if err != nil || r.Cloned != 0 {
				t.Fatalf("mutation: %+v %v", r, err)
			}
			if kind == "source" && r.Skipped["source_changed"] != 1 || kind == "metadata" && r.Skipped["kept_after_error"] != 1 {
				t.Fatalf("missing reason: %+v", r)
			}
			got, _ := os.ReadFile(filepath.Join(dst, "asset"))
			if !bytes.Equal(got, data) {
				t.Fatal("unsafe content published")
			}
			assertNoStaging(t, dst)
		})
	}
}

func TestShareRejectsDestinationParentReplacement(t *testing.T) {
	src, dst, data := cloneFixture(t)
	for _, root := range []string{src, dst} {
		if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, "asset"), filepath.Join(root, "sub", "asset")); err != nil {
			t.Fatal(err)
		}
	}
	_, err := share(context.Background(), src, dst, []string{"sub/asset"}, operations{metadata: restoreMetadata, clone: func(_ context.Context, fd, dir int, name string, flags int) error {
		if err := unix.Fclonefileat(fd, dir, name, flags); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(dst, "sub"), filepath.Join(dst, "saved")); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(dst, "sub"), 0o755); err != nil {
			return err
		}
		writeTestFile(t, filepath.Join(dst, "sub", "asset"), []byte("replacement"))
		return nil
	}})
	if !errors.Is(err, errDestinationChanged) {
		t.Fatalf("parent replacement: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "sub", "asset"))
	if string(got) != "replacement" {
		t.Fatal("new parent overwritten")
	}
	got, _ = os.ReadFile(filepath.Join(dst, "saved", "asset"))
	if !bytes.Equal(got, data) {
		t.Fatal("old parent overwritten")
	}
	assertNoStaging(t, dst)
}

func TestShareSignalChild(t *testing.T) {
	if os.Getenv("TREEHOUSE_SHARING_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	_, err := shareWithSignals(context.Background(), os.Getenv("TREEHOUSE_SHARING_SOURCE"), os.Getenv("TREEHOUSE_SHARING_DEST"), []string{"asset"}, operations{metadata: restoreMetadata, clone: func(ctx context.Context, fd, dir int, name string, flags int) error {
		if err := unix.Fclonefileat(fd, dir, name, flags); err != nil {
			return err
		}
		if err := os.WriteFile(os.Getenv("TREEHOUSE_SHARING_READY"), []byte("ready"), 0o600); err != nil {
			return err
		}
		<-ctx.Done() // The real signal context must cancel while staging exists.
		return nil
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("signal did not cancel pass: %v", err)
	}
}

func TestShareSIGTERMCleansAndSIGKILLKeepsOriginal(t *testing.T) {
	for _, signal := range []os.Signal{unix.SIGTERM, unix.SIGKILL} {
		t.Run(signal.String(), func(t *testing.T) {
			src, dst, data := cloneFixture(t)
			marker := filepath.Join(filepath.Dir(src), "ready")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShareSignalChild$")
			child.Env = append(os.Environ(), "TREEHOUSE_SHARING_CHILD=1", "TREEHOUSE_SHARING_SOURCE="+src, "TREEHOUSE_SHARING_DEST="+dst, "TREEHOUSE_SHARING_READY="+marker)
			var output bytes.Buffer
			child.Stdout = &output
			child.Stderr = &output
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if child.ProcessState == nil {
					_ = child.Process.Kill()
					_ = child.Wait()
				}
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child never staged a clone")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := child.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			err := child.Wait()
			if signal == unix.SIGTERM && err != nil {
				t.Fatalf("SIGTERM cleanup: %v %s", err, output.String())
			}
			if signal == unix.SIGKILL && err == nil {
				t.Fatal("SIGKILL unexpectedly returned success")
			}
			got, _ := os.ReadFile(filepath.Join(dst, "asset"))
			if !bytes.Equal(got, data) {
				t.Fatal("interruption changed original")
			}
			if signal == unix.SIGTERM {
				assertNoStaging(t, dst)
			} else {
				entries, err := os.ReadDir(dst)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, entry := range entries {
					found = found || strings.HasPrefix(entry.Name(), ".treehouse-sharing-")
				}
				if !found {
					t.Fatal("SIGKILL fixture did not leave the staged clone for quarantine")
				}
			}
		})
	}
}
