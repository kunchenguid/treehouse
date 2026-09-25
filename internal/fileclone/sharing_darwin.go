//go:build darwin

package fileclone

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const Supported = true

// FilesystemReason is a cheap preflight so unsupported volumes do not pay for
// Git enumeration/status. Share repeats these checks using no-follow handles.
func FilesystemReason(source, destination string) string {
	var src, dst unix.Statfs_t
	if unix.Statfs(source, &src) != nil || unix.Statfs(destination, &dst) != nil {
		return "filesystem cannot be verified"
	}
	if unix.ByteSliceToString(src.Fstypename[:]) != "apfs" || unix.ByteSliceToString(dst.Fstypename[:]) != "apfs" {
		return "requires APFS on both paths"
	}
	if src.Fsid != dst.Fsid {
		return "source and destination are on different volumes"
	}
	return ""
}

var (
	errDestinationChanged = errors.New("destination changed during APFS sharing")
	errCleanup            = errors.New("APFS sharing staging cleanup incomplete")
)

type metadata struct {
	stat  unix.Stat_t
	attrs attributes
	xattr map[string]string
}

// Operations are instance-scoped so behavioral failure tests never change
// production globals or interfere with another sharing pass.
type operations struct {
	clone    func(context.Context, int, int, string, int) error
	metadata func(int, metadata) error
}

func Share(ctx context.Context, source, destination string, paths []string) (Report, error) {
	return shareWithSignals(ctx, source, destination, paths, operations{cloneFile, restoreMetadata})
}

func cloneFile(_ context.Context, source, destination int, name string, flags int) error {
	return unix.Fclonefileat(source, destination, name, flags)
}

func shareWithSignals(ctx context.Context, source, destination string, paths []string, ops operations) (Report, error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, unix.SIGTERM)
	defer stop()
	return share(ctx, source, destination, paths, ops)
}

func share(ctx context.Context, source, destination string, paths []string, ops operations) (Report, error) {
	var report Report
	src, err := openDirectory(source)
	if err != nil {
		report.Reason = "source path unavailable or symlinked: " + err.Error()
		return report, nil
	}
	defer src.Close()
	dst, err := openDirectory(destination)
	if err != nil {
		report.Reason = "destination path unavailable or symlinked: " + err.Error()
		return report, nil
	}
	defer dst.Close()
	var srcFS, dstFS unix.Statfs_t
	if err := unix.Fstatfs(int(src.Fd()), &srcFS); err != nil {
		return Report{Reason: "source filesystem cannot be verified"}, nil
	}
	if err := unix.Fstatfs(int(dst.Fd()), &dstFS); err != nil {
		return Report{Reason: "destination filesystem cannot be verified"}, nil
	}
	if unix.ByteSliceToString(srcFS.Fstypename[:]) != "apfs" || unix.ByteSliceToString(dstFS.Fstypename[:]) != "apfs" {
		return Report{Reason: "requires APFS on both paths"}, nil
	}
	var srcStat, dstStat unix.Stat_t
	if err := unix.Fstat(int(src.Fd()), &srcStat); err != nil {
		return Report{Reason: "source identity cannot be verified"}, nil
	}
	if err := unix.Fstat(int(dst.Fd()), &dstStat); err != nil {
		return Report{Reason: "destination identity cannot be verified"}, nil
	}
	if srcStat.Dev != dstStat.Dev {
		return Report{Reason: "source and destination are on different volumes"}, nil
	}
	if srcStat.Ino == dstStat.Ino {
		return Report{Reason: "source and destination are the same directory"}, nil
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if !filepath.IsLocal(path) || filepath.Clean(path) != path {
			report.skip("unsafe_path")
			continue
		}
		reason, logical, saved, err := shareFile(ctx, src, dst, destination, path, dstStat, ops)
		if errors.Is(err, errDestinationChanged) || errors.Is(err, errCleanup) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return report, err
		}
		if err != nil {
			report.skip("kept_after_error")
			if len(report.Errors) < 3 {
				report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", path, err))
			}
			continue
		}
		if reason != "" {
			report.skip(reason)
			continue
		}
		report.Cloned++
		report.LogicalBytes += logical
		report.PrivateBytesReduced += saved
	}
	return report, nil
}

// openDirectory rejects symlinks in every component, including either root.
// Once opened, traversal and publication use pinned directory descriptors.
func openDirectory(path string) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := os.Open(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return openRelativeDirectory(root, strings.TrimPrefix(abs, string(filepath.Separator)))
}

func openRelativeDirectory(root *os.File, path string) (*os.File, error) {
	fd, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if path != "." && path != "" {
		for _, part := range strings.Split(path, string(filepath.Separator)) {
			next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			_ = unix.Close(fd)
			if openErr != nil {
				return nil, openErr
			}
			fd = next
		}
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openRegular(parent *os.File, name string) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, st, err
	}
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, st, fmt.Errorf("not a readable regular file: %v", err)
	}
	return os.NewFile(uintptr(fd), name), st, nil
}

func shareFile(ctx context.Context, source, destination *os.File, destinationPath, path string, rootStat unix.Stat_t, ops operations) (reason string, logical, saved uint64, err error) {
	parentPath, leaf := filepath.Dir(path), filepath.Base(path)
	dp, err := openRelativeDirectory(destination, parentPath)
	if err != nil {
		return "destination_missing_or_symlink", 0, 0, nil
	}
	defer dp.Close()
	dst, old, err := openRegular(dp, leaf)
	if err != nil {
		return "destination_not_regular_or_symlink", 0, 0, nil
	}
	defer dst.Close()
	if old.Size < MinimumSize {
		return "below_threshold", 0, 0, nil
	}
	sp, err := openRelativeDirectory(source, parentPath)
	if err != nil {
		return "source_missing_or_symlink", 0, 0, nil
	}
	defer sp.Close()
	src, donor, err := openRegular(sp, leaf)
	if err != nil {
		return "source_not_regular_or_symlink", 0, 0, nil
	}
	defer src.Close()
	if old.Dev != donor.Dev {
		return "different_volume", 0, 0, nil
	}
	if old.Ino == donor.Ino {
		return "same_file", 0, 0, nil
	}
	if old.Nlink != 1 || donor.Nlink != 1 {
		return "hardlink", 0, 0, nil
	}
	if old.Uid != donor.Uid || old.Gid != donor.Gid {
		return "different_owner", 0, 0, nil
	}
	if old.Flags != 0 || donor.Flags != 0 || old.Mode&0o7000 != 0 || donor.Mode&0o7000 != 0 {
		return "unsupported_flags_or_mode", 0, 0, nil
	}
	if old.Size != donor.Size {
		return "different_content", 0, 0, nil
	}
	// Conservatively exclude sparse/compressed representations. Their hashes
	// may match while metadata changes their storage interpretation.
	if old.Blocks*512 < old.Size || donor.Blocks*512 < donor.Size {
		return "sparse_or_compressed", 0, 0, nil
	}
	// Settle this newly checked-out file's allocation before measuring private
	// bytes; logical payload is reported separately and is never a substitute.
	if err := dst.Sync(); err != nil {
		return "", 0, 0, fmt.Errorf("measuring allocation: %w", err)
	}
	destAttrs, err := fileAttributes(int(dst.Fd()))
	if err != nil {
		return "", 0, 0, fmt.Errorf("destination attributes: %w", err)
	}
	sourceAttrs, err := fileAttributes(int(src.Fd()))
	if err != nil {
		return "", 0, 0, fmt.Errorf("source attributes: %w", err)
	}
	parentAttrs, err := fileAttributes(int(dp.Fd()))
	if err != nil {
		return "", 0, 0, fmt.Errorf("parent attributes: %w", err)
	}
	if destAttrs.acl || sourceAttrs.acl || parentAttrs.acl {
		return "acl", 0, 0, nil
	}
	xattrs, err := readXattrs(int(dst.Fd()))
	if err != nil {
		return "", 0, 0, fmt.Errorf("destination metadata: %w", err)
	}
	donorXattrs, err := readXattrs(int(src.Fd()))
	if err != nil {
		return "", 0, 0, fmt.Errorf("source metadata: %w", err)
	}
	for _, attrs := range []map[string]string{xattrs, donorXattrs} {
		if _, ok := attrs["com.apple.decmpfs"]; ok {
			return "representation_xattr", 0, 0, nil
		}
		if _, ok := attrs["com.apple.ResourceFork"]; ok {
			return "representation_xattr", 0, 0, nil
		}
	}
	expected, err := hash(ctx, dst)
	if err != nil {
		return "", 0, 0, err
	}
	actual, err := hash(ctx, src)
	if err != nil {
		return "", 0, 0, err
	}
	if expected != actual {
		return "different_content", 0, 0, nil
	}
	if sourceAttrs.cloneID != 0 && sourceAttrs.cloneID == destAttrs.cloneID {
		return "already_shared", 0, 0, nil
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", 0, 0, err
	}
	name := fmt.Sprintf(".treehouse-sharing-%x", random)
	if err := unix.Mkdirat(int(dp.Fd()), name, 0o700); err != nil {
		return "", 0, 0, err
	}
	temp, err := openRelativeDirectory(dp, name)
	if err != nil {
		if cleanupErr := unix.Unlinkat(int(dp.Fd()), name, unix.AT_REMOVEDIR); cleanupErr != nil {
			return "", 0, 0, errors.Join(err, errCleanup, cleanupErr)
		}
		return "", 0, 0, err
	}
	defer temp.Close()
	defer func() {
		if cleanupErr := unix.Unlinkat(int(temp.Fd()), "clone", 0); cleanupErr != nil && cleanupErr != unix.ENOENT {
			err = errors.Join(err, fmt.Errorf("%w: %v", errCleanup, cleanupErr))
		}
		var opened, named unix.Stat_t
		if unix.Fstat(int(temp.Fd()), &opened) != nil || unix.Fstatat(int(dp.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || opened.Dev != named.Dev || opened.Ino != named.Ino {
			err = errors.Join(err, errCleanup)
			return
		}
		if cleanupErr := unix.Unlinkat(int(dp.Fd()), name, unix.AT_REMOVEDIR); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("%w: %v", errCleanup, cleanupErr))
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", 0, 0, err
	}
	if err := ops.clone(ctx, int(src.Fd()), int(temp.Fd()), "clone", 0); err != nil {
		return "", 0, 0, fmt.Errorf("clonefile: %w", err)
	}
	staged, _, err := openRegular(temp, "clone")
	if err != nil {
		return "", 0, 0, err
	}
	defer staged.Close()
	actual, err = hash(ctx, staged)
	if err != nil {
		return "", 0, 0, err
	}
	if actual != expected {
		return "source_changed", 0, 0, nil
	}
	wanted := metadata{stat: old, attrs: destAttrs, xattr: xattrs}
	if err := ops.metadata(int(staged.Fd()), wanted); err != nil {
		return "", 0, 0, fmt.Errorf("preserving metadata: %w", err)
	}
	actual, err = hash(ctx, staged)
	if err != nil {
		return "", 0, 0, err
	}
	if actual != expected {
		return "metadata_changed_content", 0, 0, nil
	}
	// Hashing can update atime; restore both timestamps after the final read.
	if err := restoreTimes(int(staged.Fd()), old); err != nil {
		return "", 0, 0, err
	}
	if err := verifyMetadata(int(staged.Fd()), wanted); err != nil {
		return "", 0, 0, err
	}
	finalAttrs, err := fileAttributes(int(staged.Fd()))
	if err != nil {
		return "", 0, 0, err
	}
	if err := staged.Sync(); err != nil {
		return "", 0, 0, err
	}
	if err := ctx.Err(); err != nil {
		return "", 0, 0, err
	}
	// Check both the open file and its name, as well as the root and parent
	// reachable through the original path. Still requires exclusive ownership:
	// no filesystem check can close the interval between this and renameat.
	var now, named unix.Stat_t
	if unix.Fstat(int(dst.Fd()), &now) != nil || !sameIdentity(old, now) ||
		unix.Fstatat(int(dp.Fd()), leaf, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameIdentity(old, named) ||
		!directoryUnchanged(destinationPath, rootStat) || !parentUnchanged(destination, parentPath, dp) {
		return "", 0, 0, fmt.Errorf("%w: %s", errDestinationChanged, path)
	}
	if err := unix.Renameat(int(temp.Fd()), "clone", int(dp.Fd()), leaf); err != nil {
		return "", 0, 0, err
	}
	if destAttrs.privateBytes > finalAttrs.privateBytes {
		saved = destAttrs.privateBytes - finalAttrs.privateBytes
	}
	return "", uint64(old.Size), saved, nil
}

func sameIdentity(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim && a.Nlink == b.Nlink
}

func directoryUnchanged(path string, before unix.Stat_t) bool {
	f, err := openDirectory(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var st unix.Stat_t
	return unix.Fstat(int(f.Fd()), &st) == nil && st.Dev == before.Dev && st.Ino == before.Ino
}

func parentUnchanged(root *os.File, path string, parent *os.File) bool {
	f, err := openRelativeDirectory(root, path)
	if err != nil {
		return false
	}
	defer f.Close()
	var a, b unix.Stat_t
	return unix.Fstat(int(f.Fd()), &a) == nil && unix.Fstat(int(parent.Fd()), &b) == nil && a.Dev == b.Dev && a.Ino == b.Ino
}

func hash(ctx context.Context, f *os.File) ([32]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	buf := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, err
		}
		n, err := f.Read(buf)
		_, _ = h.Write(buf[:n])
		if err == io.EOF {
			var sum [32]byte
			copy(sum[:], h.Sum(nil))
			return sum, nil
		}
		if err != nil {
			return [32]byte{}, err
		}
	}
}
