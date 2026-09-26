package pool

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kunchenguid/treehouse/internal/vcs"
)

// recoverSafeEntry only frees 3.0.0-style recovered leases after proving the
// slot is unused, has no tracked edits, and its HEAD is safely reachable. Any
// uncertainty leaves the quarantine intact and returns a status explanation.
func recoverSafeEntry(poolDir string, wt *WorktreeEntry) (string, error) {
	if !wt.Leased || wt.LeaseHolder != RecoveredLeaseHolder {
		return "", nil
	}
	if wt.RecoveryError != "" || vcs.WorktreeBackendName(wt.Path) != "git" {
		return "automatic safety verification is unavailable for this VCS flavor; inspect it and run treehouse return <path>", nil
	}
	procs, err := findProcessesInWorktree(wt.Path)
	if err != nil {
		return "cannot verify whether a process is using this worktree; inspect it and run treehouse return <path>", nil
	}
	if procs, err = dropProtectedProcesses(procs); err != nil {
		return "cannot verify whether a process is using this worktree; inspect it and run treehouse return <path>", nil
	}
	if len(procs) != 0 || ownerAlive(*wt) {
		return "a process is using this worktree; stop it, inspect the worktree, then run treehouse return <path>", nil
	}
	tracked, err := gitRaw(wt.Path, "diff", "--name-only", "HEAD", "--")
	if err != nil {
		return "cannot verify tracked changes; inspect the worktree and run treehouse return <path>", nil
	}
	if len(bytes.TrimSpace(tracked)) != 0 {
		return "tracked changes are present; inspect and commit or preserve them, then run treehouse return <path>", nil
	}
	untrackedBytes, err := gitRaw(wt.Path, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "cannot verify untracked files; inspect the worktree and run treehouse return <path>", nil
	}
	untracked := splitNUL(untrackedBytes)
	if !headContained(wt) {
		return "HEAD is not contained in a remote-tracking ref or the slot's base branch; inspect/push it, then run treehouse return <path>", nil
	}
	if len(untracked) > 0 {
		backup, err := backupUntracked(poolDir, wt, untracked)
		if err != nil {
			return fmt.Sprintf("untracked files could not be backed up (%v); inspect and run treehouse return <path>", err), nil
		}
		fmt.Fprintf(os.Stderr, "treehouse: recovered untracked files from %s into retained backup %s\n", wt.Path, backup)
	}
	wt.Leased = false
	wt.LeaseID = ""
	wt.LeaseHolder = ""
	wt.LeasedAt = time.Time{}
	return "", nil
}

func gitRaw(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

func splitNUL(data []byte) []string {
	var paths []string
	for _, p := range bytes.Split(data, []byte{0}) {
		if len(p) != 0 {
			paths = append(paths, string(p))
		}
	}
	return paths
}

func headContained(wt *WorktreeEntry) bool {
	refs, err := gitRaw(wt.Path, "for-each-ref", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return false
	}
	candidates := splitLines(refs)
	base := wt.BaseBranch
	if base == "" {
		base, err = vcs.DefaultBranchForWorktree(wt.Path)
		if err == nil && base != "" {
			for _, ref := range []string{"refs/heads/" + base, "refs/remotes/origin/" + base} {
				if _, e := gitRaw(wt.Path, "show-ref", "--verify", "--quiet", ref); e == nil {
					candidates = append(candidates, ref)
				}
			}
		}
	} else {
		for _, ref := range []string{"refs/heads/" + base, "refs/remotes/origin/" + base} {
			if _, e := gitRaw(wt.Path, "show-ref", "--verify", "--quiet", ref); e == nil {
				candidates = append(candidates, ref)
			}
		}
	}
	for _, ref := range candidates {
		if ref == "" {
			continue
		}
		// Recovery requires commit containment, not the broader squash-merge
		// content equivalence used by prune/destroy.
		if _, e := gitRaw(wt.Path, "merge-base", "--is-ancestor", "HEAD", ref); e == nil {
			return true
		}
	}
	return false
}

func splitLines(data []byte) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// backupUntracked moves, never copies-and-deletes, every reported untracked
// path into a unique kept directory beside the pool. A failed move leaves the
// slot quarantined; already moved files remain safely recoverable in the backup.
func backupUntracked(poolDir string, wt *WorktreeEntry, paths []string) (string, error) {
	backup, err := os.MkdirTemp(filepath.Dir(poolDir), "treehouse-recovered-backup-"+filepath.Base(poolDir)+"-"+wt.Name+"-")
	if err != nil {
		return "", err
	}
	for _, name := range paths {
		src := filepath.Join(wt.Path, filepath.FromSlash(name))
		dst := filepath.Join(backup, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return backup, err
		}
		if err := os.Rename(src, dst); err != nil {
			return backup, err
		}
	}
	return backup, nil
}
