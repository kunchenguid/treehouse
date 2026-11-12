package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// hookFingerprint binds the parsed table (including the exact declared list)
// to the contents at that root. With no files it retains the existing table
// hash: absent approval_files and an explicit empty list already hash
// differently, and neither can match an approval that included file contents.
func hookFingerprint(tableHash string, paths []string, rootPath string) (string, error) {
	if len(paths) == 0 {
		return tableHash, nil
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer root.Close()
	type fileApproval struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	files := make([]fileApproval, 0, len(paths))
	for _, path := range paths {
		digest, err := approvalFileHash(root, path)
		if err != nil {
			return "", fmt.Errorf("approval_files %q: %w", path, err)
		}
		files = append(files, fileApproval{Path: path, SHA256: digest})
	}
	// Domain separation also invalidates approvals from versions which treated
	// approval_files as an unknown hook key and hashed only command strings.
	payload, err := json.Marshal(struct {
		Version string         `json:"version"`
		Hooks   string         `json:"hooks"`
		Files   []fileApproval `json:"files"`
	}{"treehouse-hook-files-v1", tableHash, files})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), nil
}

func approvalFileHash(root *os.Root, path string) (string, error) {
	path = filepath.FromSlash(path)
	if !filepath.IsLocal(path) {
		return "", fmt.Errorf("path must be relative and stay inside the repository")
	}
	// Check before opening to reject FIFOs/devices early. Rooted operations
	// reject symlink traversal outside the repository. In-repo relative
	// symlinks to regular files are supported; directories, absolute symlinks,
	// and special files are not.
	info, err := root.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("only regular files are supported")
	}
	return readApprovalFile(root, path, info)
}

// readApprovalFile hashes path under root, rejecting it unless the opened file
// is still the regular file the caller saw. The open must not block, because a
// replacement between the caller's check and this open would otherwise wait for
// a writer while post_return holds the pool lock.
func readApprovalFile(root *os.Root, path string, checked os.FileInfo) (string, error) {
	file, err := openApprovalFile(root, path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(checked, opened) {
		return "", fmt.Errorf("file changed while opening")
	}
	digest := sha256.New()
	n, err := io.Copy(digest, file)
	if err != nil {
		return "", err
	}
	after, err := file.Stat()
	if err != nil {
		return "", err
	}
	if n != opened.Size() || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return "", fmt.Errorf("file changed while reading")
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil)), nil
}
