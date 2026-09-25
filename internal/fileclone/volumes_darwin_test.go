//go:build darwin

package fileclone

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Explicitly enabled because disk-image mounting is not available in every
// macOS sandbox/CI runner. It uses only private images and mountpoints, never
// the developer's existing volumes. Run TREEHOUSE_TEST_VOLUMES=1 go test ... .
func TestSharingUnsupportedAndCrossVolume(t *testing.T) {
	if os.Getenv("TREEHOUSE_TEST_VOLUMES") != "1" {
		t.Skip("set TREEHOUSE_TEST_VOLUMES=1 for disposable disk-image integration")
	}
	for _, fs := range []string{"HFS+", "APFS"} {
		t.Run(fs, func(t *testing.T) {
			source, _, data := cloneFixture(t)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			image, mount := filepath.Join(root, "volume.dmg"), filepath.Join(root, "mounted")
			if out, err := exec.Command("hdiutil", "create", "-quiet", "-size", "96m", "-fs", fs, "-volname", "TreehouseTest", image).CombinedOutput(); err != nil {
				t.Fatalf("create %s: %v %s", fs, err, out)
			}
			if err := os.Mkdir(mount, 0o755); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("hdiutil", "attach", "-nobrowse", "-quiet", "-mountpoint", mount, image).CombinedOutput(); err != nil {
				t.Fatalf("attach: %v %s", err, out)
			}
			t.Cleanup(func() {
				if out, err := exec.Command("hdiutil", "detach", "-quiet", mount).CombinedOutput(); err != nil {
					t.Errorf("detach private test volume: %v %s", err, out)
				}
			})
			writeTestFile(t, filepath.Join(mount, "asset"), data)
			r, err := Share(context.Background(), source, mount, []string{"asset"})
			want := "requires APFS"
			if fs == "APFS" {
				want = "different volumes"
			}
			if err != nil || r.Cloned != 0 || !strings.Contains(r.Reason, want) {
				t.Fatalf("filesystem skip: %+v %v", r, err)
			}
			if reason := FilesystemReason(source, mount); !strings.Contains(reason, want) {
				t.Fatalf("preflight: %s", reason)
			}
			got, _ := os.ReadFile(filepath.Join(mount, "asset"))
			if !bytes.Equal(got, data) {
				t.Fatal("unsupported volume file changed")
			}
			assertNoStaging(t, mount)
		})
	}
}
