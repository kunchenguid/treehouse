//go:build darwin

package fileclone

import (
	"fmt"
	"maps"
	"strings"

	"golang.org/x/sys/unix"
)

// Bound memory and skip metadata we cannot safely reproduce. No source-only
// xattr (including quarantine/provenance) may leak onto the destination.
const maximumXattrBytes = 1024 * 1024

func readXattrs(fd int) (map[string]string, error) {
	size, err := unix.Flistxattr(fd, nil)
	if err != nil {
		return nil, err
	}
	if size > maximumXattrBytes {
		return nil, fmt.Errorf("extended attribute names exceed safety limit")
	}
	buf := make([]byte, size)
	n, err := unix.Flistxattr(fd, buf)
	if err != nil || n > len(buf) {
		return nil, fmt.Errorf("extended attribute list changed: %v", err)
	}
	result := make(map[string]string)
	total := 0
	for _, name := range strings.Split(string(buf[:n]), "\x00") {
		if name == "" {
			continue
		}
		size, err = unix.Fgetxattr(fd, name, nil)
		if err != nil {
			return nil, err
		}
		total += size
		if total > maximumXattrBytes {
			return nil, fmt.Errorf("extended attributes exceed safety limit")
		}
		value := make([]byte, size)
		n, err = unix.Fgetxattr(fd, name, value)
		if err != nil || n > len(value) {
			return nil, fmt.Errorf("extended attribute changed: %v", err)
		}
		result[name] = string(value[:n])
	}
	return result, nil
}

func restoreMetadata(fd int, wanted metadata) error {
	current, err := readXattrs(fd)
	if err != nil {
		return err
	}
	for name := range current {
		if _, keep := wanted.xattr[name]; !keep {
			if err := unix.Fremovexattr(fd, name); err != nil {
				return err
			}
		}
	}
	for name, value := range wanted.xattr {
		if existing, ok := current[name]; ok && existing == value {
			continue
		}
		if err := unix.Fsetxattr(fd, name, []byte(value), 0); err != nil {
			return err
		}
	}
	if err := unix.Fchmod(fd, uint32(wanted.stat.Mode)); err != nil {
		return err
	}
	return restoreTimes(fd, wanted.stat)
}

func verifyMetadata(fd int, wanted metadata) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	before := wanted.stat
	if st.Mode != before.Mode || st.Uid != before.Uid || st.Gid != before.Gid ||
		st.Size != before.Size || st.Mtim != before.Mtim || st.Atim != before.Atim ||
		st.Flags != before.Flags || st.Nlink != 1 {
		return fmt.Errorf("cloning did not preserve destination metadata")
	}
	attrs, err := fileAttributes(fd)
	if err != nil || attrs.acl != wanted.attrs.acl {
		return fmt.Errorf("cloning changed ACL: %v", err)
	}
	xattrs, err := readXattrs(fd)
	if err != nil {
		return err
	}
	if !maps.Equal(xattrs, wanted.xattr) {
		return fmt.Errorf("cloning changed extended attributes")
	}
	return nil
}
