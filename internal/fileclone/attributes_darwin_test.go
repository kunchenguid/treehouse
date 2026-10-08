//go:build darwin

package fileclone

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAllocationAndACLAttributes(t *testing.T) {
	root := t.TempDir()
	if reason := FilesystemReason(root, root); reason != "" {
		t.Skip(reason)
	}
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, bytes.Repeat([]byte("a"), 128*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	before, err := fileAttributes(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if before.acl || before.privateBytes < 128*1024 {
		t.Fatalf("ordinary file attributes: %+v", before)
	}
	if err := unix.Clonefile(source, target, 0); err != nil {
		t.Skipf("APFS clone unavailable: %v", err)
	}
	g, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	a, err := fileAttributes(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := fileAttributes(int(g.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if a.cloneID == 0 || a.cloneID != b.cloneID || b.privateBytes != 0 {
		t.Fatalf("cloned data not shared: %+v %+v", a, b)
	}
	if out, err := exec.Command("chmod", "+a", "everyone allow read", target).CombinedOutput(); err != nil {
		t.Fatalf("add ACL: %v: %s", err, out)
	}
	b, err = fileAttributes(int(g.Fd()))
	if err != nil || !b.acl {
		t.Fatalf("ACL undetected: %+v %v", b, err)
	}
}
