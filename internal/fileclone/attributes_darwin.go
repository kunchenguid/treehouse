//go:build darwin

package fileclone

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Extended attributes from Darwin's sys/attr.h, requested with
// FSOPT_ATTR_CMN_EXTENDED. getattrlist packs attributes on four-byte boundaries,
// not the alignment of a Go struct. Always check returned masks and lengths.
const (
	privateSizeAttr  = 0x8
	cloneIDAttr      = 0x100
	cloneRefsAttr    = 0x1000
	extendedAttrs    = 0x20
	packInvalidAttrs = 0x8
)

type attributes struct {
	privateBytes uint64
	cloneID      uint64
	acl          bool
}

func fileAttributes(fd int) (attributes, error) {
	request := unix.Attrlist{Bitmapcount: 5,
		Commonattr: unix.ATTR_CMN_RETURNED_ATTRS | unix.ATTR_CMN_EXTENDED_SECURITY,
		Forkattr:   privateSizeAttr | cloneIDAttr | cloneRefsAttr}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return attributes{}, err
	}
	fixedSize := uint32(52)
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		// Parents need an ACL check but do not have clone data streams.
		request.Forkattr = 0
		fixedSize = 32
	}
	buf := make([]byte, 8192) // Includes the maximum Darwin ACL, not just its reference.
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, uintptr(fd), uintptr(unsafe.Pointer(&request)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), extendedAttrs|packInvalidAttrs, 0)
	runtime.KeepAlive(request)
	if errno != 0 {
		return attributes{}, errno
	}
	length := binary.LittleEndian.Uint32(buf[:4])
	if length < fixedSize || length > uint32(len(buf)) ||
		binary.LittleEndian.Uint32(buf[4:8])&unix.ATTR_CMN_RETURNED_ATTRS == 0 ||
		binary.LittleEndian.Uint32(buf[20:24])&request.Forkattr != request.Forkattr {
		return attributes{}, fmt.Errorf("APFS allocation/security attributes unavailable: length=%d common=%#x extended=%#x", length, binary.LittleEndian.Uint32(buf[4:8]), binary.LittleEndian.Uint32(buf[20:24]))
	}
	// The security attribute is an attrreference_t at offset 24. Its offset
	// is relative to the reference itself, not to the start of the buffer.
	offset := int64(24) + int64(int32(binary.LittleEndian.Uint32(buf[24:28])))
	size := int64(binary.LittleEndian.Uint32(buf[28:32]))
	acl := false
	// APFS clears EXTENDED_SECURITY and returns a zero-length reference
	// when no ACL exists. Its offset is meaningless when the length is zero.
	if binary.LittleEndian.Uint32(buf[4:8])&unix.ATTR_CMN_EXTENDED_SECURITY == 0 && size != 0 {
		return attributes{}, fmt.Errorf("security attribute was not returned")
	}
	if size != 0 {
		if offset < int64(fixedSize) || size < 44 || offset+size > int64(length) {
			return attributes{}, fmt.Errorf("invalid Darwin security attribute")
		}
		sec := buf[offset : offset+size]
		if binary.LittleEndian.Uint32(sec[:4]) != 0x012cc16d { // KAUTH_FILESEC_MAGIC
			return attributes{}, fmt.Errorf("unknown Darwin security attribute")
		}
		entries := binary.LittleEndian.Uint32(sec[36:40])
		// An empty explicit ACL is also skipped: inheritance can distinguish
		// it from KAUTH_FILESEC_NOACL, and cloning must not change that policy.
		acl = entries != ^uint32(0)
	}
	result := attributes{acl: acl}
	if request.Forkattr != 0 {
		result.privateBytes = binary.LittleEndian.Uint64(buf[32:40])
		result.cloneID = binary.LittleEndian.Uint64(buf[40:48])
	}
	return result, nil
}

func restoreTimes(fd int, st unix.Stat_t) error {
	request := unix.Attrlist{Bitmapcount: 5, Commonattr: unix.ATTR_CMN_MODTIME | unix.ATTR_CMN_ACCTIME}
	// fsetattrlist preserves nanoseconds; futimes would truncate them to us.
	times := [2]unix.Timespec{st.Mtim, st.Atim}
	_, _, errno := unix.Syscall6(unix.SYS_FSETATTRLIST, uintptr(fd), uintptr(unsafe.Pointer(&request)),
		uintptr(unsafe.Pointer(&times[0])), unsafe.Sizeof(times), 0, 0)
	runtime.KeepAlive(request)
	runtime.KeepAlive(times)
	if errno != 0 {
		return errno
	}
	return nil
}
