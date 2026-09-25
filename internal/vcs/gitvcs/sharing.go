package gitvcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/treehouse/internal/fileclone"
)

// ShareWorktreeFiles is only called during creation of a fresh, reserved Git
// slot, before Treehouse hooks or publication. Existing worktrees must never be
// passed here by the lifecycle. Exclusive destination ownership is required.
func ShareWorktreeFiles(repoRoot, worktreePath string) (fileclone.Report, error) {
	if reason := fileclone.FilesystemReason(repoRoot, worktreePath); reason != "" {
		return fileclone.Report{Reason: reason}, nil
	}
	// Git's checkout/reference hooks run before our insertion point and may
	// leave asynchronous writers. Do not infer that a finished hook or a quiet
	// process scan means the destination is exclusively owned.
	for _, root := range []string{repoRoot, worktreePath} {
		monitor, err := gitOutput(root, nil, "config", "--get", "core.fsmonitor")
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				return fileclone.Report{Reason: "Git fsmonitor configuration cannot be verified"}, nil
			}
		} else {
			switch strings.ToLower(strings.TrimSpace(string(monitor))) {
			case "", "false", "0", "no", "off", "true", "1", "yes", "on":
				// Boolean values select Git's built-in monitor, not a script.
			default:
				return fileclone.Report{Reason: "Git fsmonitor hook may have started a writer"}, nil
			}
		}
		configuredHooksPath, configErr := gitOutput(root, nil, "config", "--get", "core.hooksPath")
		if configErr != nil {
			var exit *exec.ExitError
			if !errors.As(configErr, &exit) || exit.ExitCode() != 1 {
				return fileclone.Report{Reason: "Git hook configuration cannot be verified"}, nil
			}
		}
		for _, hook := range []string{"post-checkout", "reference-transaction"} {
			var path string
			if configErr == nil && strings.TrimSpace(string(configuredHooksPath)) != "" {
				path = filepath.Join(filepath.FromSlash(strings.TrimSpace(string(configuredHooksPath))), hook)
				if !filepath.IsAbs(path) {
					path = filepath.Join(root, path)
				}
			} else {
				var err error
				path, err = gitPath(root, filepath.Join("hooks", hook))
				if err != nil {
					return fileclone.Report{Reason: "Git hook configuration cannot be verified"}, nil
				}
			}
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 != 0 {
				return fileclone.Report{Reason: "Git " + hook + " hook may have started a writer"}, nil
			}
		}
	}
	listed, err := runGitRaw(worktreePath, "ls-files", "-z")
	if err != nil {
		return fileclone.Report{Reason: "tracked paths cannot be enumerated"}, nil
	}
	if len(listed) == 0 {
		return fileclone.Report{}, nil
	}
	// Smudge/process filters, like hooks, may start background writers. Check
	// actual path attributes without executing filters. This deliberately also
	// skips LFS-configured checkouts; do not trade ownership safety for coverage.
	attrs, err := gitOutput(worktreePath, listed, "check-attr", "-z", "--stdin", "filter")
	if err != nil {
		return fileclone.Report{Reason: "checkout filters cannot be verified"}, nil
	}
	fields := bytes.Split(bytes.TrimSuffix(attrs, []byte{0}), []byte{0})
	if len(fields)%3 != 0 {
		return fileclone.Report{Reason: "checkout filter attributes are malformed"}, nil
	}
	for i := 2; i < len(fields); i += 3 {
		if string(fields[i]) != "unspecified" && string(fields[i]) != "unset" {
			return fileclone.Report{Reason: "checkout filter may have started a writer"}, nil
		}
	}
	before, err := sharingStatus(worktreePath)
	if err != nil {
		return fileclone.Report{Reason: "initial Git status cannot be verified"}, nil
	}
	head, err := worktreeHead(worktreePath)
	if err != nil {
		return fileclone.Report{Reason: "initial Git HEAD cannot be verified"}, nil
	}
	index, err := runGitRaw(worktreePath, "ls-files", "--stage", "-z")
	if err != nil {
		return fileclone.Report{Reason: "initial Git index cannot be verified"}, nil
	}
	var paths []string
	for _, name := range bytes.Split(bytes.TrimSuffix(listed, []byte{0}), []byte{0}) {
		paths = append(paths, filepath.FromSlash(string(name)))
	}
	report, err := fileclone.Share(context.Background(), repoRoot, worktreePath, paths)
	if err != nil {
		return report, err
	}
	after, statusErr := sharingStatus(worktreePath)
	afterHead, headErr := worktreeHead(worktreePath)
	afterIndex, indexErr := runGitRaw(worktreePath, "ls-files", "--stage", "-z")
	if statusErr != nil || headErr != nil || indexErr != nil || !bytes.Equal(before, after) || head != afterHead || !bytes.Equal(index, afterIndex) {
		return report, fmt.Errorf("worktree status, HEAD or index changed during APFS sharing; inspect before use")
	}
	return report, nil
}

func sharingStatus(path string) ([]byte, error) {
	// A user-defined fsmonitor command must not start another writer as part
	// of our verification. This override is scoped to these read-only commands.
	return runGitRaw(path, "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "-z", "--untracked-files=all")
}
