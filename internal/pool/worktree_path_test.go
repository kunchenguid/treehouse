package pool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/internal/vcs/gitvcs"
)

// quotedPath renders a path the way the errors below name it. The messages quote
// paths with %q, which escapes the Windows separator, so a raw path is not a
// substring of the message that names it.
func quotedPath(path string) string {
	return fmt.Sprintf("%q", path)
}

// synthPaths returns a repository and pool directory that do not overlap.
// Neither has to exist, but the t.TempDir() base does: the resolver judges a
// placement by canonicalizing through the deepest ancestor that is on disk, and
// fails when nothing above the path exists at all.
func synthPaths(t *testing.T) (repoRoot, poolDir string) {
	t.Helper()
	base := t.TempDir()
	return filepath.Join(base, "src", "myrepo"), filepath.Join(base, "pool", "myrepo-abc123")
}

// TestResolveWorktreePath_EmptyTemplateKeepsBuiltInLayout is the byte-for-byte
// guarantee that an unset template changes nothing; cmd's
// TestGetDefaultLayoutPlacesWorktreeInThePool covers the same property through
// the CLI, so no third acquire-based copy is needed here.
func TestResolveWorktreePath_EmptyTemplateKeepsBuiltInLayout(t *testing.T) {
	repoRoot, poolDir := synthPaths(t)

	got, err := resolveWorktreePath(repoRoot, poolDir, "1", "")
	if err != nil {
		t.Fatalf("resolveWorktreePath failed: %v", err)
	}
	if want := filepath.Join(poolDir, "1", "myrepo"); got != want {
		t.Errorf("resolved %q, want the built-in layout %q", got, want)
	}
}

func TestResolveWorktreePath_Templates(t *testing.T) {
	repoRoot, poolDir := synthPaths(t)
	repoParent := filepath.Dir(repoRoot)
	t.Setenv("TREEHOUSE_TEST_WORKTREE_DIR", filepath.Join(repoParent, "elsewhere"))
	t.Setenv("TREEHOUSE_TEST_TRAILING_DIR", filepath.Join(repoParent, "trailing")+string(filepath.Separator))
	t.Setenv("TREEHOUSE_TEST_UPWARD_DIR", filepath.ToSlash(filepath.Join(repoParent, "work"))+"/../trees")

	cases := []struct {
		name     string
		template string
		want     string
	}{
		{
			name:     "sibling of the repository",
			template: "{repo_parent}/{repo}-{slot}",
			want:     filepath.Join(repoParent, "myrepo-2"),
		},
		{
			name:     "unique leaf inside the pool",
			template: "{pool}/{slot}/{repo}-{slot}",
			want:     filepath.Join(poolDir, "2", "myrepo-2"),
		},
		{
			name:     "environment variables expand",
			template: "$TREEHOUSE_TEST_WORKTREE_DIR/{repo}-{slot}",
			want:     filepath.Join(repoParent, "elsewhere", "myrepo-2"),
		},
		{
			name:     "braced environment variables expand",
			template: "${TREEHOUSE_TEST_WORKTREE_DIR}/{repo}-{slot}",
			want:     filepath.Join(repoParent, "elsewhere", "myrepo-2"),
		},
		{
			// A directory-valued variable normally ends in a separator, and path
			// cleaning collapses the doubled one, so the worktree still lands where
			// the template names.
			name:     "environment variable value ends in a separator",
			template: "$TREEHOUSE_TEST_TRAILING_DIR/{repo}-{slot}",
			want:     filepath.Join(repoParent, "trailing", "myrepo-2"),
		},
		{
			// A ".." inside a variable's value is ordinary path spelling. It cancels
			// a directory of its own value, not the slot, so slots stay apart.
			name:     "environment variable value walks back up",
			template: "$TREEHOUSE_TEST_UPWARD_DIR/{repo}-{slot}",
			want:     filepath.Join(repoParent, "trees", "myrepo-2"),
		},
		{
			name:     "literal absolute prefix",
			template: filepath.ToSlash(filepath.Join(repoParent, "trees")) + "/{repo}-wt-{slot}",
			want:     filepath.Join(repoParent, "trees", "myrepo-wt-2"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWorktreePath(repoRoot, poolDir, "2", tc.template)
			if err != nil {
				t.Fatalf("resolveWorktreePath(%q) failed: %v", tc.template, err)
			}
			if got != tc.want {
				t.Errorf("resolved %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveWorktreePath_RejectedTemplates(t *testing.T) {
	repoRoot, poolDir := synthPaths(t)
	t.Setenv("TREEHOUSE_TEST_WORKTREE_DIR", filepath.Join(filepath.Dir(repoRoot), "elsewhere"))
	t.Setenv("TREEHOUSE_TEST_EMPTY_DIR", "")
	t.Setenv("TREEHOUSE_TEST_PARENT_DIR", "..")

	cases := []struct {
		name     string
		template string
		wants    string
	}{
		{
			name:     "no slot placeholder collides every slot",
			template: "{repo_parent}/{repo}-worktree",
			wants:    "{slot}",
		},
		{
			name:     "unknown placeholder",
			template: "{repo_parent}/{repository}-{slot}",
			wants:    "{repository}",
		},
		{
			name:     "relative template",
			template: "trees/{repo}-{slot}",
			wants:    "relative path",
		},
		{
			// filepath.Clean cancels ".." against the segment {slot} produced, so
			// each of these resolves every slot to one directory even though {slot}
			// is present.
			name:     "parent segment cancels the slot out",
			template: "{repo_parent}/{slot}/../shared",
			wants:    "resolves to the same directory for every slot",
		},
		{
			name:     "parent segment swallows the pool directory",
			template: "{pool}/{slot}/..",
			wants:    "resolves to the same directory for every slot",
		},
		{
			name:     "parent segment swallows the repository",
			template: "{repo_parent}/{repo}/{slot}/..",
			wants:    "resolves to the same directory for every slot",
		},
		{
			// The collapse can come from a variable's value, so the check has to
			// run after expansion rather than on the template's own text.
			name:     "environment variable cancels the slot out",
			template: "{repo_parent}/{slot}/$TREEHOUSE_TEST_PARENT_DIR",
			wants:    "resolves to the same directory for every slot",
		},
		{
			name:     "inside the repository working tree",
			template: "{repo_parent}/{repo}/trees/{slot}",
			wants:    "inside the repository working tree",
		},
		{
			name:     "in-pool template without the slot directory",
			template: "{pool}/{repo}-{slot}",
			wants:    "so pool state can be recovered from disk",
		},
		{
			name:     "in-pool template nested too deep",
			template: "{pool}/{slot}/nested/{repo}",
			wants:    "so pool state can be recovered from disk",
		},
		{
			// $slot is an environment variable, not the slot placeholder, and it
			// expands to one fixed value for every slot in the pool.
			name:     "braced environment variable named like the slot placeholder",
			template: "{repo_parent}/{repo}-${slot}",
			wants:    "{slot}",
		},
		{
			name:     "no repository-scoping placeholder collides across repositories",
			template: "$TREEHOUSE_TEST_WORKTREE_DIR/{slot}",
			wants:    "{repo}",
		},
		{
			// {repo_parent} expands identically for two repositories that sit
			// side by side, so it scopes nothing on its own.
			name:     "repository parent alone collides between sibling repositories",
			template: "{repo_parent}/{slot}",
			wants:    "must contain one of {pool} {repo}",
		},
		{
			// An unset variable expands to nothing, filepath.Clean removes the
			// empty segment, and the worktree lands one directory higher than the
			// template names - beside the repository instead of under a subdir.
			name:     "unset environment variable drops a directory",
			template: "{repo_parent}/${TREEHOUSE_TEST_UNSET_DIR}/{repo}-{slot}",
			wants:    "leaves empty",
		},
		{
			name:     "environment variable set to nothing drops a directory",
			template: "{repo_parent}/${TREEHOUSE_TEST_EMPTY_DIR}/{repo}-{slot}",
			wants:    "leaves empty",
		},
		{
			// os.Expand eats "${}" without calling the mapping function, so the
			// empty-expansion check never sees it.
			name:     "empty variable reference drops a directory",
			template: "{repo_parent}/${}/{repo}-{slot}",
			wants:    "names no environment variable",
		},
		{
			// The same reference at the front resolves the whole template to the
			// filesystem root, where no path check has anything to object to.
			name:     "leading empty variable reference resolves to the root",
			template: "${}/{repo}-{slot}",
			wants:    "names no environment variable",
		},
		{
			name:     "unterminated variable reference",
			template: "{repo_parent}/${TREEHOUSE_TEST_WORKTREE_DIR/{repo}-{slot}",
			wants:    "no closing brace",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWorktreePath(repoRoot, poolDir, "1", tc.template)
			if err == nil {
				t.Fatalf("expected %q to be rejected, resolved to %q", tc.template, got)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q does not explain the problem (want it to mention %q)", err, tc.wants)
			}
		})
	}

	// The pool root is git-ignored, so an in-project pool stays legal even
	// though it sits inside the repository.
	inProjectPool := filepath.Join(repoRoot, ".treehouse", "myrepo-abc123")
	if _, err := resolveWorktreePath(repoRoot, inProjectPool, "1", "{pool}/{slot}/{repo}-{slot}"); err != nil {
		t.Errorf("expected an in-project pool template to be accepted: %v", err)
	}
}

// TestResolveWorktreePath_TwoSlotsNeverShareADirectory is the property the
// {slot} requirement exists for. Requiring the placeholder to appear is not
// enough on its own, because filepath.Clean can cancel it out again.
func TestResolveWorktreePath_TwoSlotsNeverShareADirectory(t *testing.T) {
	repoRoot, poolDir := synthPaths(t)
	template := "{repo_parent}/{slot}/../shared"

	first, firstErr := resolveWorktreePath(repoRoot, poolDir, "1", template)
	second, secondErr := resolveWorktreePath(repoRoot, poolDir, "2", template)
	if firstErr == nil && secondErr == nil && first == second {
		t.Fatalf("slots 1 and 2 both resolved to %q; a colliding template must be rejected", first)
	}
	if firstErr == nil {
		t.Errorf("expected %q to be rejected, resolved slot 1 to %q", template, first)
	}
}

// TestResolveWorktreePath_SiblingRepositoriesNeverShareASlotDirectory is the
// property the repository-scoping requirement exists for. Two repositories side
// by side share a parent directory, so a template scoped only by {repo_parent}
// sends both their slot 1s to one path: the second repository's `get` then dies
// on the existing directory forever, because its own pool state stays empty and
// keeps allocating slot 1. Pairing {repo_parent} with {repo} separates them.
func TestResolveWorktreePath_SiblingRepositoriesNeverShareASlotDirectory(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "src", "alpha")
	second := filepath.Join(base, "src", "beta")
	poolDir := filepath.Join(base, "pool", "p")

	unscoped := "{repo_parent}/{slot}"
	firstPath, firstErr := resolveWorktreePath(first, poolDir, "1", unscoped)
	secondPath, secondErr := resolveWorktreePath(second, poolDir, "1", unscoped)
	if firstErr == nil && secondErr == nil && firstPath == secondPath {
		t.Fatalf("both repositories resolved slot 1 to %q; a colliding template must be rejected", firstPath)
	}
	if firstErr == nil {
		t.Errorf("expected %q to be rejected, resolved to %q", unscoped, firstPath)
	}

	scoped := "{repo_parent}/{repo}-{slot}"
	firstPath, err := resolveWorktreePath(first, poolDir, "1", scoped)
	if err != nil {
		t.Fatalf("expected %q to stay accepted: %v", scoped, err)
	}
	secondPath, err = resolveWorktreePath(second, poolDir, "1", scoped)
	if err != nil {
		t.Fatalf("expected %q to stay accepted: %v", scoped, err)
	}
	if firstPath == secondPath {
		t.Fatalf("both repositories resolved slot 1 to %q", firstPath)
	}
}

// TestResolveWorktreePath_RejectsSwallowingTheRepositoryOrPool reaches the
// containment checks with plain templates, by placing the pool and the
// repository where a slot's own path lands on them.
func TestResolveWorktreePath_RejectsSwallowingTheRepositoryOrPool(t *testing.T) {
	base := t.TempDir()

	cases := []struct {
		name     string
		repoRoot string
		poolDir  string
		template string
		wants    string
	}{
		{
			name:     "resolves to the pool directory",
			repoRoot: filepath.Join(base, "src", "myrepo"),
			poolDir:  filepath.Join(base, "src", "myrepo", "1"),
			template: "{repo_parent}/{repo}/{slot}",
			wants:    "contains the pool directory",
		},
		{
			name:     "resolves to the repository",
			repoRoot: filepath.Join(base, "src", "1"),
			poolDir:  filepath.Join(base, "src", "pool"),
			template: "{pool}/../{slot}",
			wants:    "contains the repository",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWorktreePath(tc.repoRoot, tc.poolDir, "1", tc.template)
			if err == nil {
				t.Fatalf("expected %q to be rejected, resolved to %q", tc.template, got)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q does not explain the problem (want it to mention %q)", err, tc.wants)
			}
		})
	}
}

// TestResolveWorktreePath_SymlinksDoNotBypassPlacement covers the lexical hole in
// the containment checks: filepath.Rel compares spellings, but the worktree is
// created wherever the existing directories in its path really point.
func TestResolveWorktreePath_SymlinksDoNotBypassPlacement(t *testing.T) {
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Join(base, "src", "myrepo")
	poolDir := filepath.Join(base, "pool", "myrepo-abc123")
	for _, dir := range []string{repoRoot, filepath.Join(poolDir, "1")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name   string
		link   string
		target string
		wants  string
	}{
		{
			name:   "link into the repository working tree",
			link:   filepath.Join(base, "trees-in-repo"),
			target: repoRoot,
			wants:  "inside the repository working tree",
		},
		{
			name:   "link into the pool directory",
			link:   filepath.Join(base, "trees-in-pool"),
			target: poolDir,
			wants:  "so pool state can be recovered from disk",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Symlink(tc.target, tc.link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			template := filepath.ToSlash(tc.link) + "/{repo}-{slot}"
			got, err := resolveWorktreePath(repoRoot, poolDir, "1", template)
			if err == nil {
				t.Fatalf("expected %q to be rejected, resolved to %q", template, got)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q does not explain the problem (want it to mention %q)", err, tc.wants)
			}
		})
	}

	// A symlinked pool root must still resolve to the same pool, or macOS
	// /tmp -> /private/tmp alone would read every in-pool template as escaping.
	linkedPool := filepath.Join(base, "pool-link")
	if err := os.Symlink(filepath.Dir(poolDir), linkedPool); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	spelled := filepath.Join(linkedPool, filepath.Base(poolDir))
	if _, err := resolveWorktreePath(repoRoot, spelled, "1", "{pool}/{slot}/{repo}-{slot}"); err != nil {
		t.Errorf("expected a symlinked pool spelling to be accepted: %v", err)
	}
}

// TestResolveWorktreePath_InPoolPathMustBeSpelledThroughThePool covers the gap
// between the two spellings a placement has: the checks canonicalize, but state
// records the requested path. A symlink that reaches the pool from outside it
// satisfies every canonical rule, so without a lexical requirement the worktree
// is recorded under the link while recovery finds the same directory under the
// pool and registers it a second time.
func TestResolveWorktreePath_InPoolPathMustBeSpelledThroughThePool(t *testing.T) {
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Join(base, "src", "myrepo")
	poolDir := filepath.Join(base, "pool", "myrepo-abc123")
	for _, dir := range []string{repoRoot, filepath.Join(poolDir, "1")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	poolLink := filepath.Join(base, "pool-by-another-name")
	if err := os.Symlink(poolDir, poolLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The recoverable {slot}/<name> shape, spelled through the link.
	template := filepath.ToSlash(poolLink) + "/{slot}/{repo}"
	got, err := resolveWorktreePath(repoRoot, poolDir, "1", template)
	if err == nil {
		t.Fatalf("expected %q to be rejected, resolved to %q", template, got)
	}
	if !strings.Contains(err.Error(), "by another name") {
		t.Errorf("error %q does not explain the problem", err)
	}
}

// TestResolveWorktreePath_RejectsAnotherPoolsDirectory covers the pool the
// placement rules say nothing about: another repository's. That pool's state
// recovery scans its own directory two levels deep, so a worktree nested there
// is registered as a phantom leased slot of a pool that never created it, which
// consumes one of its slots for good and leaves the owning repository unable to
// return its own worktree.
func TestResolveWorktreePath_RejectsAnotherPoolsDirectory(t *testing.T) {
	// The foreign pool is discovered on disk, so the error names its canonical
	// path. t.TempDir() is not canonical everywhere (a Windows 8.3 short name,
	// macOS /tmp -> /private/tmp), so canonicalize before deriving from it.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Join(base, "src", "beta")
	poolDir := filepath.Join(base, "pool", "beta-abc123")
	foreignPool := filepath.Join(base, "pool", "alpha-def456")
	if err := os.MkdirAll(foreignPool, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFilePath(foreignPool), []byte(`{"worktrees":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	template := filepath.ToSlash(foreignPool) + "/{repo}-{slot}/wt"
	got, err := resolveWorktreePath(repoRoot, poolDir, "1", template)
	if err == nil {
		t.Fatalf("expected %q to be rejected, resolved to %q", template, got)
	}
	if !strings.Contains(err.Error(), quotedPath(foreignPool)) || !strings.Contains(err.Error(), "another repository") {
		t.Errorf("error %q does not name the foreign pool as the problem", err)
	}
}

// TestResolveWorktreePath_AllowsASiblingOfThePools bounds the check above: the
// treehouse root holds every pool but is not one itself, so a worktree placed
// beside the pools is registered by nobody and stays legal.
func TestResolveWorktreePath_AllowsASiblingOfThePools(t *testing.T) {
	base := t.TempDir()
	repoRoot := filepath.Join(base, "src", "beta")
	root := filepath.Join(base, "pool")
	poolDir := filepath.Join(root, "beta-abc123")
	otherPool := filepath.Join(root, "alpha-def456")
	if err := os.MkdirAll(otherPool, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFilePath(otherPool), []byte(`{"worktrees":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	template := filepath.ToSlash(root) + "/{repo}-{slot}"
	got, err := resolveWorktreePath(repoRoot, poolDir, "1", template)
	if err != nil {
		t.Fatalf("resolveWorktreePath rejected a sibling of the pools: %v", err)
	}
	if want := filepath.Join(root, "beta-1"); got != want {
		t.Errorf("resolved %q, want %q", got, want)
	}
}

func TestAcquire_WorktreePathRefusesAnExistingDirectory(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	occupied := filepath.Join(filepath.Dir(repoDir), "myrepo-1")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{
		WorktreePath: "{repo_parent}/{repo}-{slot}",
	})
	if err == nil {
		t.Fatal("expected an acquisition onto an existing directory to fail")
	}
	if !strings.Contains(err.Error(), quotedPath(occupied)) {
		t.Errorf("error %q does not name the occupied path", err)
	}

	state, readErr := ReadState(poolDir)
	if readErr == nil && len(state.Worktrees) != 0 {
		t.Errorf("expected no slot to be registered, got %#v", state.Worktrees)
	}
}

// TestAcquire_WorktreePathOccupiedByOurOwnWorktreeExplainsWhy covers the state a
// lost state file leaves an out-of-pool worktree in: recovery scans the pool
// directory only, so the worktree comes back unknown while it is still on disk
// and still registered, and every later acquisition resolves to the same name and
// refuses the same path. The refusal has to say what the occupant is, and must
// not prescribe a command whose preconditions it cannot see from here.
func TestAcquire_WorktreePathOccupiedByOurOwnWorktreeExplainsWhy(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	options := AcquireOptions{WorktreePath: "{repo_parent}/{repo}-{slot}"}

	occupied, err := AcquireWithOptions(repoDir, poolDir, 2, nil, options)
	if err != nil {
		t.Fatalf("AcquireWithOptions failed: %v", err)
	}
	if err := os.WriteFile(stateFilePath(poolDir), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = AcquireWithOptions(repoDir, poolDir, 2, nil, options)
	if err == nil {
		t.Fatal("expected the acquisition to refuse the occupied path")
	}
	for _, want := range []string{occupied, "already exists", "registered as a worktree of this repository", "git worktree list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	for _, unwanted := range []string{"git worktree remove", "jj workspace forget", "treehouse destroy"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("error %q prescribes %q, whose preconditions treehouse cannot check here", err, unwanted)
		}
	}
}

// TestAcquire_WorktreePathOccupiedBySomeoneElseKeepsQuiet is the other half: a
// directory treehouse does not own must not be described as its to inspect.
func TestAcquire_WorktreePathOccupiedBySomeoneElseKeepsQuiet(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	occupied := filepath.Join(filepath.Dir(repoDir), "myrepo-1")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{
		WorktreePath: "{repo_parent}/{repo}-{slot}",
	})
	if err == nil {
		t.Fatal("expected the acquisition to refuse the occupied path")
	}
	if !strings.Contains(err.Error(), "move it aside") {
		t.Errorf("error %q does not tell the user to move the directory aside", err)
	}
	for _, unwanted := range []string{"registered as a worktree of this repository", "git worktree list", "git worktree remove", "treehouse destroy"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("error %q says %q for a directory treehouse does not own", err, unwanted)
		}
	}
}

func TestAcquire_WorktreePathPlacesNewSlotsOutsideThePool(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	repoParent := filepath.Dir(repoDir)
	options := AcquireOptions{WorktreePath: "{repo_parent}/{repo}-{slot}"}

	first, err := AcquireWithOptions(repoDir, poolDir, 2, nil, options)
	if err != nil {
		t.Fatalf("AcquireWithOptions failed: %v", err)
	}
	if want := filepath.Join(repoParent, "myrepo-1"); first != want {
		t.Fatalf("acquired %s, want %s", first, want)
	}
	toplevel := filepath.Clean(filepath.FromSlash(gitOut(t, first, "rev-parse", "--show-toplevel")))
	if toplevel != filepath.Clean(first) {
		t.Errorf("git toplevel = %s, want %s", toplevel, filepath.Clean(first))
	}

	second, err := AcquireWithOptions(repoDir, poolDir, 2, nil, options)
	if err != nil {
		t.Fatalf("second AcquireWithOptions failed: %v", err)
	}
	if want := filepath.Join(repoParent, "myrepo-2"); second != want {
		t.Fatalf("second acquire returned %s, want %s", second, want)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if len(state.Worktrees) != 2 {
		t.Fatalf("expected both slots registered in pool state, got %#v", state.Worktrees)
	}
	for _, wt := range state.Worktrees {
		if filepath.Dir(wt.Path) != repoParent {
			t.Errorf("state records %s, want a path under %s", wt.Path, repoParent)
		}
	}

	// Recycling keys off the recorded path, so a later plain get reuses the
	// out-of-pool slot instead of growing the pool.
	if err := Release(poolDir, first); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	recycled, err := Acquire(repoDir, poolDir, 2, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if recycled != first {
		t.Fatalf("expected the out-of-pool slot to be recycled at %s, got %s", first, recycled)
	}
}

func TestAcquire_WorktreePathNeverMovesARecycledSlot(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	original, err := Acquire(repoDir, poolDir, 2, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, original); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	recycled, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{
		WorktreePath: "{repo_parent}/{repo}-{slot}",
	})
	if err != nil {
		t.Fatalf("AcquireWithOptions failed: %v", err)
	}
	if recycled != original {
		t.Fatalf("recycled slot moved from %s to %s", original, recycled)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repoDir), "myrepo-1")); !os.IsNotExist(err) {
		t.Errorf("expected no templated directory for a recycled slot, stat err: %v", err)
	}
}

func TestAcquire_InvalidWorktreePathFailsClosedWithoutTouchingThePool(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	// A reusable slot exists, so this acquisition would never expand the
	// template. The typo must still be reported instead of silently ignored.
	existing, err := Acquire(repoDir, poolDir, 2, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, existing); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	_, err = AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{
		WorktreePath: "{repo_parent}/{repo}-{slotname}",
	})
	if err == nil {
		t.Fatal("expected an unknown placeholder to fail the acquisition")
	}
	if !strings.Contains(err.Error(), "{slotname}") {
		t.Errorf("error %q does not name the unknown placeholder", err)
	}

	state, readErr := ReadState(poolDir)
	if readErr != nil {
		t.Fatalf("ReadState failed: %v", readErr)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0].Path != existing {
		t.Fatalf("expected the existing slot to be left alone, got %#v", state.Worktrees)
	}
	if state.Worktrees[0].Leased || state.Worktrees[0].OwnerPID != 0 {
		t.Errorf("expected the existing slot to stay available, got %#v", state.Worktrees[0])
	}
}

// TestAcquire_WorktreePathEscapingTheRepositoryFailsOnTheRecyclePath is the
// placement half of the same guarantee: an acquisition that recycles a slot never
// expands the template, so without a check against the name the pool would
// allocate next, whether an escaping template is reported depends on how full the
// pool is.
func TestAcquire_WorktreePathEscapingTheRepositoryFailsOnTheRecyclePath(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	existing, err := Acquire(repoDir, poolDir, 2, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if err := Release(poolDir, existing); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	_, err = AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{
		WorktreePath: "{repo_parent}/{repo}/trees/{slot}",
	})
	if err == nil {
		t.Fatal("expected a template inside the repository working tree to fail the acquisition")
	}
	if !strings.Contains(err.Error(), "inside the repository working tree") {
		t.Errorf("error %q does not name the reason", err)
	}

	state, readErr := ReadState(poolDir)
	if readErr != nil {
		t.Fatalf("ReadState failed: %v", readErr)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0].Path != existing {
		t.Fatalf("expected the existing slot to be left alone, got %#v", state.Worktrees)
	}
	if state.Worktrees[0].Leased || state.Worktrees[0].OwnerPID != 0 {
		t.Errorf("expected the existing slot to stay available, got %#v", state.Worktrees[0])
	}
	if _, err := os.Stat(filepath.Join(repoDir, "trees")); !os.IsNotExist(err) {
		t.Errorf("expected no directory inside the repository, stat err: %v", err)
	}

	// Still handed back to the next acquisition that asks for it.
	reacquired, err := Acquire(repoDir, poolDir, 2, nil)
	if err != nil {
		t.Fatalf("Acquire after the refusal failed: %v", err)
	}
	if reacquired != existing {
		t.Errorf("acquired %s, want the untouched slot %s", reacquired, existing)
	}
}

func TestRemovableWorktreeContainer_RemovesTheParentOnlyInsideThePool(t *testing.T) {
	base := t.TempDir()
	poolDir := filepath.Join(base, "pool", "myrepo-abc123")

	inPool := filepath.Join(poolDir, "1", "myrepo")
	got, err := removableWorktreeContainer(poolDir, inPool)
	if err != nil {
		t.Fatalf("removableWorktreeContainer failed: %v", err)
	}
	if want := filepath.Join(poolDir, "1"); got != want {
		t.Errorf("in-pool worktree: removing %q, want the slot directory %q", got, want)
	}

	// The parent here holds the repository and everything beside it, so only the
	// worktree may be removed.
	outOfPool := filepath.Join(base, "src", "myrepo-1")
	got, err = removableWorktreeContainer(poolDir, outOfPool)
	if err != nil {
		t.Fatalf("removableWorktreeContainer failed: %v", err)
	}
	if got != outOfPool {
		t.Errorf("out-of-pool worktree: removing %q, want only the worktree %q", got, outOfPool)
	}

	// A worktree directly inside the pool directory must never take the pool's
	// state file down with it.
	shallow := filepath.Join(poolDir, "myrepo-1")
	got, err = removableWorktreeContainer(poolDir, shallow)
	if err != nil {
		t.Fatalf("removableWorktreeContainer failed: %v", err)
	}
	if got != shallow {
		t.Errorf("worktree in the pool root: removing %q, want only the worktree %q", got, shallow)
	}
}

// TestRemovableWorktreeContainer_SymlinkOutOfThePoolIsNotPoolOwned pins the
// cleanup half of the placement rule: a path that only looks in-pool because it
// crosses a symlink out of the pool must not have its parent removed, or prune
// and destroy would delete whatever else lives in the real directory.
func TestRemovableWorktreeContainer_SymlinkOutOfThePoolIsNotPoolOwned(t *testing.T) {
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	poolDir := filepath.Join(base, "pool", "myrepo-abc123")
	external := filepath.Join(base, "external")
	if err := os.MkdirAll(filepath.Join(external, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(poolDir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// A sibling of the worktree in the real directory: prune must not reach it.
	bystander := filepath.Join(external, "1", "bystander.txt")
	if err := os.WriteFile(bystander, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(poolDir, "link", "1", "myrepo")
	got, err := removableWorktreeContainer(poolDir, wtPath)
	if err != nil {
		t.Fatalf("removableWorktreeContainer failed: %v", err)
	}
	if got != wtPath {
		t.Errorf("removing %q, want only the worktree %q: its parent is outside the pool", got, wtPath)
	}
}

// requireCaseInsensitiveFS skips a test on a filesystem where "a" and "A" name
// two directories, because there a differently-cased path is a different path.
func requireCaseInsensitiveFS(t *testing.T, dir string) {
	t.Helper()
	probe := filepath.Join(dir, "casecheck")
	if err := os.Mkdir(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "CASECHECK"))
	if removeErr := os.Remove(probe); removeErr != nil {
		t.Fatal(removeErr)
	}
	if err != nil {
		t.Skip("filesystem is case-sensitive")
	}
}

// TestResolveWorktreePath_CaseDifferingPrefixDoesNotBypassPlacement covers the
// second lexical hole in the containment checks: on a case-insensitive
// filesystem one directory has many spellings, and comparing bytes reads them as
// different directories.
func TestResolveWorktreePath_CaseDifferingPrefixDoesNotBypassPlacement(t *testing.T) {
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	requireCaseInsensitiveFS(t, base)

	repoRoot := filepath.Join(base, "src", "myrepo")
	poolDir := filepath.Join(base, "pool", "myrepo-abc123")
	for _, dir := range []string{filepath.Join(repoRoot, "trees"), poolDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	template := filepath.ToSlash(filepath.Join(base, "src", "MyRepo", "trees")) + "/{repo}-{slot}"
	got, err := resolveWorktreePath(repoRoot, poolDir, "1", template)
	if err == nil {
		t.Fatalf("expected %q to be rejected, resolved to %q", template, got)
	}
	if !strings.Contains(err.Error(), "inside the repository working tree") {
		t.Errorf("error %q does not explain the problem", err)
	}
}

// TestRemovableWorktreeContainer_CaseDifferingPoolSpelling is the other half of
// deciding containment by identity: the pool directory reached by a second
// spelling still holds the pool's state and must never be removed with a
// worktree, while a slot directory reached the same way is still the pool's own.
func TestRemovableWorktreeContainer_CaseDifferingPoolSpelling(t *testing.T) {
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	requireCaseInsensitiveFS(t, base)

	poolDir := filepath.Join(base, "pool", "myrepo-abc123")
	if err := os.MkdirAll(filepath.Join(poolDir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	misCasedPool := filepath.Join(base, "pool", "MyRepo-ABC123")

	inPoolRoot := filepath.Join(misCasedPool, "myrepo-1")
	got, err := removableWorktreeContainer(poolDir, inPoolRoot)
	if err != nil {
		t.Fatalf("removableWorktreeContainer failed: %v", err)
	}
	if got != inPoolRoot {
		t.Errorf("removing %q, want only the worktree %q: its parent is the pool directory", got, inPoolRoot)
	}

	inSlot := filepath.Join(misCasedPool, "1", "myrepo")
	got, err = removableWorktreeContainer(poolDir, inSlot)
	if err != nil {
		t.Fatalf("removableWorktreeContainer failed: %v", err)
	}
	if want := filepath.Join(misCasedPool, "1"); got != want {
		t.Errorf("removing %q, want the slot directory %q", got, want)
	}
}

func TestPrune_RemovesIdleWorktreePlacedOutsideThePool(t *testing.T) {
	repoDir, poolDir := setupRepo(t)
	repoParent := filepath.Dir(repoDir)

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{
		WorktreePath: "{repo_parent}/{repo}-{slot}",
	})
	if err != nil {
		t.Fatalf("AcquireWithOptions failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// A sibling of the worktree, which prune must not touch: the templated
	// worktree's parent is a directory treehouse does not own.
	bystander := filepath.Join(repoParent, "bystander.txt")
	if err := os.WriteFile(bystander, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Prune(repoDir, poolDir, false, nil)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if len(result.Pruned) != 1 || result.Pruned[0].Path != wtPath {
		t.Fatalf("expected %s to be pruned, got %#v (skipped: %#v)", wtPath, result.Pruned, result.Skipped)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed, stat err: %v", wtPath, err)
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Errorf("prune removed a bystander beside the worktree: %v", err)
	}
	if _, err := os.Stat(repoDir); err != nil {
		t.Errorf("prune removed the repository beside the worktree: %v", err)
	}
}

func TestDestroyPool_RemovesWorktreePlacedOutsideThePool(t *testing.T) {
	repoDir, poolDir := setupRepo(t)

	wtPath, err := AcquireWithOptions(repoDir, poolDir, 2, nil, AcquireOptions{
		WorktreePath: "{repo_parent}/{repo}-{slot}",
	})
	if err != nil {
		t.Fatalf("AcquireWithOptions failed: %v", err)
	}
	if err := Release(poolDir, wtPath); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	result, err := DestroyPool(poolDir, DestroyOptions{})
	if err != nil {
		t.Fatalf("DestroyPool failed: %v", err)
	}
	if len(result.Destroyed) != 1 || result.Destroyed[0].Path != wtPath {
		t.Fatalf("expected %s to be destroyed, got %#v (skipped: %#v)", wtPath, result.Destroyed, result.Skipped)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed, stat err: %v", wtPath, err)
	}
	if _, err := os.Stat(repoDir); err != nil {
		t.Errorf("destroy removed the repository beside the worktree: %v", err)
	}
}

// TestRemoveManagedWorktree_DropsJJSeedAuthOnPlainRoute covers the routes that
// never call vcs.RemoveWorktree - orphaned and markerless slots - and so never
// reach the removal that normally drops a jj slot's seed authentication. Under
// the built-in layout the slot container took the file along; a worktree
// worktree_path placed outside the pool has only itself removed, and a leftover
// there poisons the path: the next acquisition cannot seed, and the slot it
// leaves behind cannot be destroyed.
func TestRemoveManagedWorktree_DropsJJSeedAuthOnPlainRoute(t *testing.T) {
	authPath, entry, poolDir, worktree := seededJJSlotOutsideThePool(t)

	// Markerless: the directory is still there but its .jj marker is gone, so
	// removal takes the plain-directory route.
	if err := os.RemoveAll(filepath.Join(worktree, ".jj")); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedWorktree(poolDir, "", entry); err != nil {
		t.Fatalf("removeManagedWorktree failed: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("expected the worktree to be removed, stat err: %v", err)
	}
	if _, err := os.Lstat(authPath); !os.IsNotExist(err) {
		t.Errorf("expected the jj seed authentication to be dropped, stat err: %v", err)
	}
}

// TestRemoveManagedWorktree_LeavesUnownedJJSeedAuthOnPlainRoute is the other half:
// dropping the authentication is best effort, and it must still refuse a file it
// cannot prove belongs to that entry. The removal itself has to succeed anyway,
// because the worktree is already gone.
func TestRemoveManagedWorktree_LeavesUnownedJJSeedAuthOnPlainRoute(t *testing.T) {
	authPath, entry, poolDir, worktree := seededJJSlotOutsideThePool(t)

	// Swap in a file with a different inode, the way the suite's other
	// fail-closed test does, so the recorded identity cannot match.
	forged := authPath + ".forged"
	if err := os.WriteFile(forged, []byte("someone else's data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(forged, authPath); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(worktree, ".jj")); err != nil {
		t.Fatal(err)
	}

	if err := removeManagedWorktree(poolDir, "", entry); err != nil {
		t.Fatalf("removeManagedWorktree failed: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("expected the worktree to be removed, stat err: %v", err)
	}
	if _, err := os.Lstat(authPath); err != nil {
		t.Errorf("expected an unowned authentication file to be left alone: %v", err)
	}
}

// seededJJSlotOutsideThePool builds a jj slot with a signed seed inventory at a
// path outside the pool, the shape worktree_path allows, and returns its
// authentication file, its state entry as persisted, the pool dir and the
// worktree. It needs no jj binary: the marker file and PrepareJJSeededCleanup are
// what the authentication is derived from.
func seededJJSlotOutsideThePool(t *testing.T) (authPath string, entry WorktreeEntry, poolDir, worktree string) {
	t.Helper()
	base := t.TempDir()
	poolDir = filepath.Join(base, "pool", "myrepo-abc123")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree = filepath.Join(base, "src", "myrepo-1")
	marker := filepath.Join(worktree, ".jj", "repo")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("store"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gitvcs.PrepareJJSeededCleanup(worktree); err != nil {
		t.Fatal(err)
	}
	authDir := filepath.Join(filepath.Dir(worktree), ".treehouse-jj-seed-auth")
	authEntries, err := os.ReadDir(authDir)
	if err != nil || len(authEntries) != 1 {
		t.Fatalf("authentication entries = %v, %v", authEntries, err)
	}
	authPath = filepath.Join(authDir, authEntries[0].Name())

	seeded := WorktreeEntry{Name: "1", Path: worktree}
	setSeedInventory(&seeded, []string{"selected.env"}, true)
	if err := WriteState(poolDir, State{Worktrees: []WorktreeEntry{seeded}}); err != nil {
		t.Fatal(err)
	}
	// Read it back: the digest, backend and authentication identity are stamped
	// on write, and the drop only acts on an entry whose inventory validates.
	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Worktrees) != 1 || state.Worktrees[0].SeedAuthIdentity == "" {
		t.Fatalf("expected a signed jj seed inventory, got %#v", state.Worktrees)
	}
	return authPath, state.Worktrees[0], poolDir, worktree
}
