package gitvcs

import (
	"path/filepath"
	"testing"
)

func TestRecoveryHeadContained(t *testing.T) {
	tests := []struct {
		name string
		// setup runs in a repo whose main holds "base" and whose checked-out
		// feature branch holds one more commit, "feature", on top of it.
		setup func(t *testing.T, repo string)
		base  string
		want  bool
	}{
		{
			name: "ancestor of a non-base remote-tracking ref",
			setup: func(t *testing.T, repo string) {
				mustGit(t, repo, "commit", "--allow-empty", "-m", "pushed later")
				mustGit(t, repo, "update-ref", "refs/remotes/fork/topic", "HEAD")
				mustGit(t, repo, "reset", "--hard", "HEAD~1")
			},
			base: "main",
			want: true,
		},
		{
			name: "tip of the remote base branch",
			setup: func(t *testing.T, repo string) {
				mustGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
			},
			base: "main",
			want: true,
		},
		{
			name: "only on the local base branch",
			setup: func(t *testing.T, repo string) {
				mustGit(t, repo, "branch", "-f", "main", "HEAD")
			},
			base: "main",
			want: true,
		},
		{
			name: "unpushed commit above every ref",
			setup: func(t *testing.T, repo string) {
				mustGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
				mustGit(t, repo, "update-ref", "refs/remotes/origin/feature", "main")
			},
			base: "main",
			want: false,
		},
		{
			name: "commit only on another local branch",
			setup: func(t *testing.T, repo string) {
				mustGit(t, repo, "branch", "other", "HEAD")
			},
			base: "main",
			want: false,
		},
		{
			name:  "missing base ref still honors remote refs",
			setup: func(t *testing.T, repo string) { mustGit(t, repo, "update-ref", "refs/remotes/origin/feature", "HEAD") },
			base:  "missing",
			want:  true,
		},
		{
			name:  "missing base ref with nothing containing head",
			setup: func(t *testing.T, repo string) {},
			base:  "missing",
			want:  false,
		},
		{
			name:  "no remotes and no base",
			setup: func(t *testing.T, repo string) {},
			base:  "",
			want:  false,
		},
		{
			name: "dangling remote HEAD symref does not hide a containing ref",
			setup: func(t *testing.T, repo string) {
				mustGit(t, repo, "update-ref", "refs/remotes/origin/feature", "HEAD")
				mustGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/gone")
			},
			base: "main",
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := recoveryRepo(t)
			tt.setup(t, repo)
			if got := RecoveryHeadContained(repo, tt.base); got != tt.want {
				t.Fatalf("RecoveryHeadContained(%q) = %t, want %t", tt.base, got, tt.want)
			}
		})
	}
}

// A linked worktree must be judged by its own HEAD, not the main checkout's.
func TestRecoveryHeadContainedUsesLinkedWorktreeHead(t *testing.T) {
	repo := recoveryRepo(t)
	mustGit(t, repo, "update-ref", "refs/remotes/origin/feature", "HEAD")
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, repo, "worktree", "add", "-b", "slot", wt, "feature")
	mustGit(t, wt, "commit", "--allow-empty", "-m", "unpushed in slot")

	if !RecoveryHeadContained(repo, "main") {
		t.Fatal("main checkout HEAD should be contained by origin/feature")
	}
	if RecoveryHeadContained(wt, "main") {
		t.Fatal("slot HEAD with an unpushed commit reported as contained")
	}
}

// Git errors print nothing on stdout, which must never be read as "nothing
// unpushed".
func TestRecoveryHeadContainedFailsClosedWhenHeadCannotBeRead(t *testing.T) {
	t.Run("not a repository", func(t *testing.T) {
		dir := t.TempDir()
		// Keep git from discovering an enclosing repository above the temp dir.
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
		if RecoveryHeadContained(dir, "main") {
			t.Fatal("non-repository reported as contained")
		}
	})
	t.Run("unborn head", func(t *testing.T) {
		repo := t.TempDir()
		mustGit(t, "", "init", "--initial-branch=main", repo)
		if RecoveryHeadContained(repo, "main") {
			t.Fatal("unborn HEAD reported as contained")
		}
	})
}

func recoveryRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	mustGit(t, "", "init", "--initial-branch=main", repo)
	mustGit(t, repo, "config", "user.email", "test@test.com")
	mustGit(t, repo, "config", "user.name", "Test")
	mustGit(t, repo, "commit", "--allow-empty", "-m", "base")
	mustGit(t, repo, "checkout", "-b", "feature")
	mustGit(t, repo, "commit", "--allow-empty", "-m", "feature")
	return repo
}
