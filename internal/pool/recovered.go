package pool

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/treehouse/internal/vcs"
)

// recoverQuarantinedEntries runs under the state lock before every pool
// operation, so a recovered entry proven safe is freed whichever command runs
// next. An unreadable state is left for the operation itself to report.
func recoverQuarantinedEntries(poolDir string) error {
	state, err := ReadState(poolDir)
	if err != nil {
		return nil
	}
	changed := false
	for i := range state.Worktrees {
		wt := &state.Worktrees[i]
		if !wt.Leased || wt.LeaseHolder != RecoveredLeaseHolder {
			continue
		}
		if _, err := os.Stat(wt.Path); err != nil {
			continue
		}
		reason := recoverSafeEntry(poolDir, wt)
		if reason == "" {
			releaseEntry(wt)
			changed = true
		} else if reason != wt.RecoveryReason {
			wt.RecoveryReason = reason
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return WriteState(poolDir, state)
}

// recoverSafeEntry proves a 3.0.0-style recovered slot safe to free: nothing,
// including the caller and its ancestors, uses it, it has no tracked edits,
// including inside submodules or behind skip-worktree or assume-unchanged,
// and its HEAD is safely reachable. Untracked files are moved into a kept
// backup first where the platform supports it. It returns "" when the slot may
// be freed, and otherwise why it stays quarantined.
func recoverSafeEntry(poolDir string, wt *WorktreeEntry) string {
	if wt.RecoveryError != "" {
		return "its VCS marker could not be read, so it cannot be verified automatically"
	}
	if vcs.WorktreeBackendName(wt.Path) != "git" {
		return "automatic safety verification is only available for Git worktrees"
	}
	procs, err := findProcessesInWorktree(wt.Path)
	if err != nil {
		return "cannot verify whether a process is using this worktree"
	}
	if len(procs) != 0 || ownerAlive(*wt) {
		return "a process is using this worktree (a shell standing in it counts); stop it or leave the worktree"
	}
	untracked, reason := vcs.RecoveryWorktree(wt.Path)
	if reason != "" {
		return reason
	}
	if !headContained(wt) {
		return "HEAD is not contained in a remote-tracking ref or the slot's base branch; push or preserve it"
	}
	if len(untracked) > 0 {
		if untrackedBackupUnsupported != "" {
			return untrackedBackupUnsupported
		}
		backup := recoveryBackupDir(poolDir, wt.Name)
		if err := backupUntracked(backup, wt.Path, untracked); err != nil {
			return fmt.Sprintf("untracked files could not all be moved into backup %s (%v)", backup, err)
		}
		fmt.Fprintf(os.Stderr, "treehouse: recovered untracked files from %s into retained backup %s\n", wt.Path, backup)
	}
	return ""
}

func headContained(wt *WorktreeEntry) bool {
	base := wt.BaseBranch
	if base == "" {
		base, _ = vcs.DefaultBranchForWorktree(wt.Path)
	}
	return vcs.RecoveryHeadContained(wt.Path, base)
}

// recoveryBackupDir is the one backup folder a slot's recovery ever uses. It is
// derived rather than recorded, so every retry reuses it. Once state writes
// succeed, status can find it even if an earlier write after a move failed.
func recoveryBackupDir(poolDir, name string) string {
	return filepath.Join(filepath.Dir(poolDir), "treehouse-recovered-backup-"+filepath.Base(poolDir)+"-"+name)
}

// recoveryBackup reports the slot's recovery backup folder when it is a real
// directory that holds anything, and "" otherwise.
func recoveryBackup(poolDir, name string) string {
	backup := recoveryBackupDir(poolDir, name)
	if info, err := os.Lstat(backup); err != nil || !info.IsDir() {
		return ""
	}
	entries, err := os.ReadDir(backup)
	if err != nil || len(entries) == 0 {
		return ""
	}
	return backup
}

// backupUntracked moves, never copies-and-deletes, every reported untracked
// path into backup. The backup must be owner-only, and it and every folder
// inside it that a move lands in must be a real directory, never a symlink
// that would carry files elsewhere.
// A path already taken there by an earlier attempt gets a numeric suffix
// instead of being overwritten. A failed move leaves the slot quarantined;
// already moved files stay in the backup.
func backupUntracked(backup, worktreePath string, paths []string) error {
	if err := ensureRealDir(backup); err != nil {
		return err
	}
	if err := ensureOwnerOnlyDir(backup); err != nil {
		return err
	}
	for _, name := range paths {
		src := filepath.Join(worktreePath, filepath.FromSlash(name))
		dir := backup
		for _, part := range strings.Split(path.Dir(name), "/") {
			if part == "." {
				continue
			}
			dir = filepath.Join(dir, part)
			if err := ensureRealDir(dir); err != nil {
				return err
			}
		}
		dst := filepath.Join(dir, path.Base(name))
		for n := 0; ; n++ {
			free := dst
			if n != 0 {
				free = fmt.Sprintf("%s.%d", dst, n)
			}
			if err := moveNoReplace(src, free); os.IsExist(err) {
				continue
			} else if err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func ensureRealDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", dir)
	}
	return nil
}
