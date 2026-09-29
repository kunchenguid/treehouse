package gitvcs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldRecoveryHeadContained is the pre-fix implementation: one merge-base
// process per remote ref. It is kept here as the differential reference so
// the single rev-list walk must return the same answer on every fixture.
func oldRecoveryHeadContained(t *testing.T, dir, base string) bool {
	t.Helper()
	refs, err := runGitRaw(dir, "for-each-ref", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return false
	}
	candidates := strings.Fields(string(refs))
	if base != "" {
		for _, ref := range []string{"refs/heads/" + base, "refs/remotes/origin/" + base} {
			if _, e := runGitRaw(dir, "show-ref", "--verify", "--quiet", ref); e == nil {
				candidates = append(candidates, ref)
			}
		}
	}
	for _, ref := range candidates {
		if _, e := runGitRaw(dir, "merge-base", "--is-ancestor", "HEAD", ref); e == nil {
			return true
		}
	}
	return false
}

// setupContainmentRepo builds a repo with a pushed main plus an unpushed
// local commit, and returns the repo dir.
func setupContainmentRepo(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}

	bareDir := filepath.Join(base, "remote.git")
	repoDir := filepath.Join(base, "repo")

	mustGit(t, "", "init", "--bare", "--initial-branch=main", bareDir)
	mustGit(t, "", "init", "--initial-branch=main", repoDir)
	mustGit(t, repoDir, "config", "user.email", "test@test.com")
	mustGit(t, repoDir, "config", "user.name", "Test")
	mustGit(t, repoDir, "remote", "add", "origin", bareDir)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "add", ".")
	mustGit(t, repoDir, "commit", "-m", "initial")
	mustGit(t, repoDir, "push", "-u", "origin", "main")
	// Unpushed local commit: reachable from no remote ref.
	mustGit(t, repoDir, "commit", "--allow-empty", "-m", "local only")

	return repoDir
}

func assertContainment(t *testing.T, dir, base string, want bool) {
	t.Helper()
	if got := RecoveryHeadContained(dir, base); got != want {
		t.Errorf("RecoveryHeadContained(%q, %q) = %v, want %v", dir, base, got, want)
	}
	if got := oldRecoveryHeadContained(t, dir, base); got != want {
		t.Errorf("reference impl RecoveryHeadContained(%q, %q) = %v, want %v (fixture does not pin the old answer)", dir, base, got, want)
	}
}

// HEAD has an unpushed commit, so no remote ref contains it. The local main
// branch tip is HEAD itself, so base "main" still counts as contained.
func TestRecoveryHeadContainedNowhere(t *testing.T) {
	repoDir := setupContainmentRepo(t)

	assertContainment(t, repoDir, "", false)
	assertContainment(t, repoDir, "main", true)
	assertContainment(t, repoDir, "no-such-branch", false)
}

// HEAD exactly at the pushed tip is contained by the remote ref.
func TestRecoveryHeadContainedByRemoteRef(t *testing.T) {
	repoDir := setupContainmentRepo(t)
	mustGit(t, repoDir, "reset", "--hard", "HEAD~1")

	assertContainment(t, repoDir, "", true)
	assertContainment(t, repoDir, "main", true)
}

// HEAD behind the remote tip is still an ancestor of it.
func TestRecoveryHeadContainedAsAncestorOfRemoteTip(t *testing.T) {
	repoDir := setupContainmentRepo(t)
	mustGit(t, repoDir, "reset", "--hard", "HEAD~1")
	mustGit(t, repoDir, "commit", "--allow-empty", "-m", "remote advance")
	mustGit(t, repoDir, "push", "origin", "main")
	mustGit(t, repoDir, "reset", "--hard", "HEAD~1")

	assertContainment(t, repoDir, "", true)
}

// A local-only base branch containing HEAD counts even with no remote
// containment.
func TestRecoveryHeadContainedOnlyByBaseBranch(t *testing.T) {
	repoDir := setupContainmentRepo(t)
	mustGit(t, repoDir, "branch", "local-base")

	assertContainment(t, repoDir, "local-base", true)
	assertContainment(t, repoDir, "", false)
}

// A repo with commits but no remotes contains HEAD nowhere unless the base
// branch itself is counted.
func TestRecoveryHeadWithNoRemoteRefs(t *testing.T) {
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, "", "init", "--initial-branch=main", dir)
	mustGit(t, dir, "config", "user.email", "test@test.com")
	mustGit(t, dir, "config", "user.name", "Test")
	mustGit(t, dir, "commit", "--allow-empty", "-m", "initial")

	assertContainment(t, dir, "", false)
	assertContainment(t, dir, "main", true)
	assertContainment(t, dir, "no-such-branch", false)
}

// Missing refs and Git failures fail closed to false.
func TestRecoveryHeadContainedFailures(t *testing.T) {
	// Not a repository at all.
	assertContainment(t, t.TempDir(), "", false)
	assertContainment(t, t.TempDir(), "main", false)
	// Does not exist.
	assertContainment(t, filepath.Join(t.TempDir(), "missing"), "", false)

	// Unborn HEAD: for-each-ref succeeds with no output while rev-list and
	// merge-base both fail on HEAD.
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, "", "init", "--initial-branch=main", dir)
	assertContainment(t, dir, "", false)
	assertContainment(t, dir, "main", false)
}
