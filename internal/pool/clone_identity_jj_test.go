package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/vcs"
)

func setupJJCloneIdentity(t *testing.T) (origin, poolDir string) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	config := filepath.Join(t.TempDir(), "jjconfig.toml")
	if err := os.WriteFile(config, []byte("[user]\nname = \"Treehouse Tests\"\nemail = \"treehouse-tests@example.com\"\n[git]\ncolocate = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JJ_CONFIG", config)
	t.Setenv("TREEHOUSE_VCS", "jj")
	repo, poolDir := setupRepo(t)
	return filepath.Join(filepath.Dir(repo), "remote.git"), poolDir
}

func runJJCloneIdentity(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("jj", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("jj %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func acquireJJCloneIdentity(t *testing.T, repo, poolDir string, capacity int, leased bool) string {
	t.Helper()
	var path string
	var err error
	if leased {
		path, err = AcquireLease(repo, poolDir, capacity, nil, "clone identity test")
	} else {
		path, err = Acquire(repo, poolDir, capacity, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAcquireColocatedJJCloneIdentity(t *testing.T) {
	for _, mode := range []struct {
		name   string
		leased bool
	}{{"ordinary", false}, {"lease", true}} {
		t.Run(mode.name, func(t *testing.T) {
			origin, poolDir := setupJJCloneIdentity(t)
			base := filepath.Dir(poolDir)
			repoA := filepath.Join(base, "clone-a")
			repoB := filepath.Join(base, "clone-b")
			runJJCloneIdentity(t, "git", "clone", "--colocate", origin, repoA)
			runJJCloneIdentity(t, "git", "clone", "--colocate", origin, repoB)

			slotA := acquireJJCloneIdentity(t, repoA, poolDir, 2, mode.leased)
			if err := Release(poolDir, slotA); err != nil {
				t.Fatal(err)
			}
			slotB := acquireJJCloneIdentity(t, repoB, poolDir, 2, mode.leased)
			if slotB == slotA {
				t.Fatalf("clone B received clone A's returned workspace %s", slotA)
			}
			for _, pair := range [][2]string{{repoA, slotA}, {repoB, slotB}} {
				want, err := vcs.CommonGitDir(pair[0])
				if err != nil {
					t.Fatal(err)
				}
				got, err := vcs.CommonGitDir(pair[1])
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("workspace %s belongs to %s, want %s", pair[1], got, want)
				}
			}
			if err := Release(poolDir, slotB); err != nil {
				t.Fatal(err)
			}
			if got := acquireJJCloneIdentity(t, repoA, poolDir, 2, mode.leased); got != slotA {
				t.Fatalf("clone A reused %s, want its original workspace %s", got, slotA)
			}
			if got := acquireJJCloneIdentity(t, repoB, poolDir, 2, mode.leased); got != slotB {
				t.Fatalf("clone B reused %s, want its original workspace %s", got, slotB)
			}
		})
	}
}

func TestAcquireNonColocatedJJRefusesReuse(t *testing.T) {
	for _, mode := range []struct {
		name   string
		leased bool
	}{{"ordinary", false}, {"lease", true}} {
		t.Run(mode.name, func(t *testing.T) {
			origin, poolDir := setupJJCloneIdentity(t)
			repo := filepath.Join(filepath.Dir(poolDir), "jj-only")
			runJJCloneIdentity(t, "git", "clone", origin, repo)
			if _, err := os.Lstat(filepath.Join(repo, ".git")); !os.IsNotExist(err) {
				t.Fatalf("fixture must be non-colocated, .git inspection returned %v", err)
			}
			if identity, err := acquisitionCommonGitDir(repo); err == nil {
				t.Fatalf("non-colocated jj identity = %v without error; unprovable identity must not match", identity)
			}
			slot := acquireJJCloneIdentity(t, repo, poolDir, 1, mode.leased)
			if err := Release(poolDir, slot); err != nil {
				t.Fatal(err)
			}
			before, err := ReadState(poolDir)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			if mode.leased {
				got, err = AcquireLease(repo, poolDir, 1, nil, "clone identity test")
			} else {
				got, err = Acquire(repo, poolDir, 1, nil)
			}
			if err == nil || got != "" || !strings.Contains(err.Error(), "1 whose clone identity cannot be verified; max_trees = 1") || !strings.Contains(err.Error(), "this repository's clone identity cannot be verified") {
				t.Fatalf("non-colocated jj got path=%q err=%v, want explicit unverifiable-identity exhaustion", got, err)
			}
			after, err := ReadState(poolDir)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused reuse changed state: %#v -> %#v (%v)", before, after, err)
			}
			if fresh := acquireJJCloneIdentity(t, repo, poolDir, 2, mode.leased); fresh == slot {
				t.Fatalf("non-colocated jj reused unverifiable workspace %s", slot)
			}
		})
	}
}

func TestAcquireSkipsUnresolvableColocatedJJIdentity(t *testing.T) {
	origin, poolDir := setupJJCloneIdentity(t)
	repo := filepath.Join(filepath.Dir(poolDir), "colocated")
	runJJCloneIdentity(t, "git", "clone", "--colocate", origin, repo)
	slot := acquireJJCloneIdentity(t, repo, poolDir, 2, false)
	if err := Release(poolDir, slot); err != nil {
		t.Fatal(err)
	}
	before, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join(slot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	pointerPath := filepath.Join(slot, ".jj", "repo")
	brokenPointer := filepath.Join(filepath.Dir(poolDir), "missing", ".jj", "repo")
	if err := os.WriteFile(pointerPath, []byte(brokenPointer), 0o644); err != nil {
		t.Fatal(err)
	}
	if identity, err := acquisitionCommonGitDir(slot); err == nil {
		t.Fatalf("damaged jj workspace identity = %v without error; must not be treated as unsupported", identity)
	}
	if got := acquireJJCloneIdentity(t, repo, poolDir, 2, false); got == slot {
		t.Fatalf("acquired workspace with unresolvable identity: %s", got)
	}
	after, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Worktrees) != 2 || !reflect.DeepEqual(after.Worktrees[0], before.Worktrees[0]) {
		t.Fatalf("damaged workspace state changed: before %+v, after %+v", before.Worktrees, after.Worktrees)
	}
	if got, err := os.ReadFile(pointerPath); err != nil || string(got) != brokenPointer {
		t.Fatalf("damaged pointer changed: got %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(slot, "README.md")); err != nil || string(got) != string(readme) {
		t.Fatalf("damaged workspace files changed: README = %q, want %q, %v", got, readme, err)
	}
}

func TestAcquireNonColocatedJJRefusesIdentifiableWorkspace(t *testing.T) {
	origin, poolDir := setupJJCloneIdentity(t)
	base := filepath.Dir(poolDir)
	colocated := filepath.Join(base, "colocated")
	jjOnly := filepath.Join(base, "jj-only")
	runJJCloneIdentity(t, "git", "clone", "--colocate", origin, colocated)
	runJJCloneIdentity(t, "git", "clone", origin, jjOnly)

	slot := acquireJJCloneIdentity(t, colocated, poolDir, 1, false)
	if err := Release(poolDir, slot); err != nil {
		t.Fatal(err)
	}
	for _, leased := range []bool{false, true} {
		var got string
		var err error
		if leased {
			got, err = AcquireLease(jjOnly, poolDir, 1, nil, "clone identity test")
		} else {
			got, err = Acquire(jjOnly, poolDir, 1, nil)
		}
		if err == nil || got != "" || !strings.Contains(err.Error(), "1 whose clone identity cannot be verified") {
			t.Fatalf("leased=%t: non-colocated requester got path=%q err=%v, want explicit unverifiable-identity exhaustion", leased, got, err)
		}
	}

	own := acquireJJCloneIdentity(t, jjOnly, poolDir, 2, false)
	if own == slot {
		t.Fatalf("non-colocated requester received colocated clone's workspace %s", slot)
	}
	if err := Release(poolDir, own); err != nil {
		t.Fatal(err)
	}
	if got := acquireJJCloneIdentity(t, colocated, poolDir, 2, true); got != slot {
		t.Fatalf("colocated clone reused %s, want its own workspace %s", got, slot)
	}
	if got, err := AcquireLease(jjOnly, poolDir, 2, nil, "clone identity test"); err == nil || got != "" || !strings.Contains(err.Error(), "1 whose clone identity cannot be verified; max_trees = 2") {
		t.Fatalf("non-colocated clone got path=%q err=%v, want refusal: its identity cannot be proven, so it reuses nothing", got, err)
	}
}

func TestAcquireSecondNonColocatedJJCloneRefusedForeignWorkspace(t *testing.T) {
	origin, poolDir := setupJJCloneIdentity(t)
	base := filepath.Dir(poolDir)
	first := filepath.Join(base, "jj-first")
	second := filepath.Join(base, "jj-second")
	runJJCloneIdentity(t, "git", "clone", origin, first)
	runJJCloneIdentity(t, "git", "clone", origin, second)

	slot := acquireJJCloneIdentity(t, first, poolDir, 1, false)
	if err := Release(poolDir, slot); err != nil {
		t.Fatal(err)
	}
	before, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join(slot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leased := range []bool{false, true} {
		var got string
		if leased {
			got, err = AcquireLease(second, poolDir, 1, nil, "clone identity test")
		} else {
			got, err = Acquire(second, poolDir, 1, nil)
		}
		if err == nil || got != "" || !strings.Contains(err.Error(), "1 whose clone identity cannot be verified; max_trees = 1") {
			t.Fatalf("leased=%t: second clone got path=%q err=%v, want explicit refusal of the first clone's workspace", leased, got, err)
		}
	}
	after, err := ReadState(poolDir)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("refusal changed state: %#v -> %#v (%v)", before, after, err)
	}
	if got, err := os.ReadFile(filepath.Join(slot, "README.md")); err != nil || string(got) != string(readme) {
		t.Fatalf("first clone's workspace changed: README = %q, want %q, %v", got, readme, err)
	}
	mainRoot, err := vcs.FindMainRepoRootFrom(slot)
	if err != nil {
		t.Fatal(err)
	}
	if want, err := filepath.EvalSymlinks(first); err != nil || mainRoot != want {
		t.Fatalf("workspace %s now belongs to %s, want %s (%v)", slot, mainRoot, want, err)
	}
}
