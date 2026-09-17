package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/ui"
	"github.com/kunchenguid/treehouse/internal/vcs"
)

var (
	returnForce         bool
	returnAll           bool
	returnIfLeaseID     string
	returnIfLeaseHolder string
)

var (
	errReturnWorktreeUnmanaged = errors.New("return worktree unmanaged")
	errReturnAborted           = errors.New("return aborted")
	errReturnAbortedNonTTY     = errors.New("return aborted: non-tty dirty")
)

// returnTarget is one worktree a return acts on, paired with the pool that owns
// it. The two travel together because a path can find its own pool while a name
// can only be resolved through the repository, and every later step needs both.
type returnTarget struct {
	path    string
	poolDir string
}

var returnCmd = &cobra.Command{
	Use:   "return [path|name]",
	Short: "Terminate lingering processes and return a worktree",
	Long: `Release any lease and return a worktree to the pool, after terminating
lingering processes and verifying no foreign process remains.

The worktree can be named three ways:

  treehouse return                  The worktree you are standing in (or
                                    $TREEHOUSE_DIR).
  treehouse return <path>           That worktree, by path. Works from outside
                                    the repository, because the pool is found
                                    from the path itself.
  treehouse return <name>           That worktree, by the name 'treehouse
                                    status' prints in its first column - the
                                    same identity 'treehouse lease' takes. A
                                    name is resolved against the pool of the
                                    repository you are standing in.

An argument is read as a path first and only then as a name, so an argument that
already resolves keeps resolving to exactly the same worktree.

  treehouse return --all            Return every held worktree in this
                                    repository's pool.

--all acts on every slot 'treehouse status' does not report 'available' or
'damaged': an available slot has nothing to return, and a damaged slot's marker
cannot be read, so 'treehouse destroy' - not return - is what removes it. This
is deliberately wider than 'prune' and 'destroy --all', which never touch a
leased slot: --all exists to reclaim a whole pool, so it clears leased and
in-use slots too. Each worktree is returned exactly as naming it would be,
including the confirmation before uncommitted changes are discarded; declining
one skips it and the rest still run. A slot re-acquired while the run works
through the pool is skipped rather than reset, because it is no longer the
worktree the run set out to return. --all takes no path or name, and cannot be
combined with the --if-lease-* conditions, which target a single lease
identity.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("if-lease-id") && returnIfLeaseID == "" {
			return fmt.Errorf("--if-lease-id cannot be empty")
		}

		conditional := cmd.Flags().Changed("if-lease-id") || cmd.Flags().Changed("if-lease-holder")

		if returnAll {
			if len(args) > 0 {
				return fmt.Errorf("--all takes no path or name; it returns every held worktree in this repository's pool")
			}
			if conditional {
				return fmt.Errorf("--all cannot be combined with --if-lease-id or --if-lease-holder; a lease condition identifies one acquisition, so name that worktree instead")
			}
			return returnHeldWorktrees()
		}

		target, err := resolveReturnTarget(args)
		if err != nil {
			return err
		}

		preconditions := pool.ReleasePreconditions{}
		if cmd.Flags().Changed("if-lease-id") {
			preconditions.ExpectedLeaseID = &returnIfLeaseID
		}
		if cmd.Flags().Changed("if-lease-holder") {
			preconditions.ExpectedLeaseHolder = &returnIfLeaseHolder
		}
		err = releaseWorktree(target, preconditions)
		// An abort is not a success: the worktree, and any lease on it, are
		// exactly as they were found. Reporting exit 0 here let a caller
		// conclude the slot was released, so a leaked lease starved the pool
		// with nothing in the exit status to detect it.
		if errors.Is(err, errReturnAbortedNonTTY) {
			return withExitCode(ExitNotReturned, fmt.Errorf(
				"🌳 worktree not returned: it has uncommitted changes and the confirmation could not be answered (stdin reached EOF); prune will not reclaim this slot. Use treehouse return --force %s to clean and return it",
				quoteReturnPath(target.path)))
		}
		if errors.Is(err, errReturnAborted) {
			return withExitCode(ExitNotReturned, fmt.Errorf(
				"🌳 worktree not returned: cleaning declined, so its uncommitted changes remain and prune will not reclaim this slot. Use treehouse return --force %s to clean and return it",
				quoteReturnPath(target.path)))
		}
		if err != nil {
			return fmt.Errorf("failed to return worktree: %w", err)
		}

		fmt.Fprintln(os.Stderr, "🌳 Worktree returned to pool.")
		return nil
	},
}

func init() {
	returnCmd.Flags().BoolVar(&returnForce, "force", false, "Clean, reset, and return without prompting")
	returnCmd.Flags().BoolVar(&returnAll, "all", false, "Return every held worktree in this repository's pool (skips available and damaged slots)")
	returnCmd.Flags().StringVar(&returnIfLeaseID, "if-lease-id", "", "Return only if the current lease has this identity")
	returnCmd.Flags().StringVar(&returnIfLeaseHolder, "if-lease-holder", "", "Return only if the current lease has this holder")
	rootCmd.AddCommand(returnCmd)
}

// releaseWorktree returns one worktree: the preconditions, then the dirty
// confirmation, then the release itself. It returns errReturnAborted and
// errReturnAbortedNonTTY unwrapped, because an abort left the worktree exactly
// as it was found and every caller has to tell that apart from a failure.
//
// The preconditions are checked before the confirmation so a slot this release
// will refuse is never announced as a dirty worktree about to be cleaned; the
// release re-checks them under its own state lock, which is what actually
// fences the reset.
func releaseWorktree(target returnTarget, preconditions pool.ReleasePreconditions) error {
	if err := pool.ValidateReleasePreconditions(target.poolDir, target.path, preconditions, nil); err != nil {
		return err
	}
	if err := confirmWorktreeReturn(target.path); err != nil {
		return err
	}
	return pool.ReleaseConditional(target.poolDir, target.path, returnBaseBranch(target.path), preconditions, func() error {
		return finalizeWorktreeReturn(target.path)
	})
}

// bulkReturnPreconditions carries what the `--all` listing observed about a
// slot into its release, so a slot re-acquired in between is refused instead of
// reset. The two instants are separated by every earlier confirmation in the
// run, a far wider window than a named return has.
//
// A leased slot is identified by its lease ID: every acquisition mints a new
// one, so a takeover can never match. An unleased observation carries the empty
// identity, which the pool reads as "expected no lease" and so refuses a slot
// leased since. A leased slot with no ID - a state file predating lease IDs, or
// a quarantined entry - offers nothing to compare, so it keeps the
// unconditional release it has today.
func bulkReturnPreconditions(wt pool.WorktreeStatus) pool.ReleasePreconditions {
	if wt.Status == pool.StatusLeased {
		if wt.LeaseID == "" {
			return pool.ReleasePreconditions{}
		}
		expected := wt.LeaseID
		return pool.ReleasePreconditions{ExpectedLeaseID: &expected}
	}
	unleased := ""
	return pool.ReleasePreconditions{ExpectedLeaseID: &unleased}
}

// returnableStatus reports whether `return --all` acts on a slot in this state.
//
// Available is excluded because there is nothing to return: the slot is already
// parked and a later `get` will hand it out. Damaged is excluded because its
// marker is missing or unreadable, so neither the detach nor the reset a return
// performs can be judged safe, and `destroy` - which `status` already spells out
// for such a slot - is the verb that removes it. Everything else (leased,
// in-use, you're here, dirty, unverified) is a slot somebody is holding, which
// is exactly what a bulk return exists to reclaim.
//
// That is deliberately WIDER than the other bulk verbs: prune skips a leased
// slot and destroy removes one only when its exact path is named with
// --include-leased, while `return --all` clears leased and in-use slots. A
// return leaves the slot in the pool, and reclaiming a pool whose agents are
// gone is the whole point of the verb.
//
// Naming a damaged slot explicitly still returns it, unchanged: the narrow
// target is a deliberate act, while the bulk one must not surprise.
func returnableStatus(status string) bool {
	switch status {
	case pool.StatusAvailable, pool.StatusDamaged:
		return false
	default:
		return true
	}
}

// returnHeldWorktrees implements `return --all`. Each worktree is released
// exactly as naming it would be, one at a time, and a per-worktree abort,
// skip, or failure never stops the ones after it: a bulk return that gave up
// on the first dirty slot would leave the rest held with no indication which.
//
// A slot is skipped, and the run neither fails nor reports an abort for it,
// when it is no longer the worktree the listing described (re-acquired) or when
// no release can clear it (quarantined without a trusted seed inventory).
// Nothing went wrong in either case, and calling them failures made a
// quarantined pool exit 1 on every retry forever.
func returnHeldWorktrees() error {
	poolDir, err := repositoryPoolDir()
	if err != nil {
		return err
	}

	worktrees, err := pool.List(poolDir)
	if err != nil {
		return err
	}

	var targets []pool.WorktreeStatus
	var notHeld int
	for _, wt := range worktrees {
		if returnableStatus(wt.Status) {
			targets = append(targets, wt)
			continue
		}
		notHeld++
	}

	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "🌳 No held worktrees to return (%d in the pool).\n", len(worktrees))
		return nil
	}

	var returned int
	var aborted, failed, skipped []string
	for _, wt := range targets {
		fmt.Fprintf(os.Stderr, "🌳 Returning %s (%s) at %s\n", wt.Name, wt.Status, ui.PrettyPath(wt.Path))
		err := releaseWorktree(returnTarget{path: wt.Path, poolDir: poolDir}, bulkReturnPreconditions(wt))
		switch {
		case err == nil:
			returned++
		// Neither skip is a failure: nothing went wrong and nothing was left
		// half-done, so retrying the run would report the same thing forever.
		case errors.Is(err, pool.ErrLeasePreconditionFailed):
			skipped = append(skipped, wt.Name)
			fmt.Fprintf(os.Stderr, "   %s skipped: it was re-acquired after this run listed it, so it is no longer the worktree this run set out to return.\n", wt.Name)
		case errors.Is(err, pool.ErrSeedInventoryUntrusted):
			skipped = append(skipped, wt.Name)
			fmt.Fprintf(os.Stderr, "   %s skipped: %v\n", wt.Name, err)
		case errors.Is(err, errReturnAborted), errors.Is(err, errReturnAbortedNonTTY):
			aborted = append(aborted, wt.Name)
			fmt.Fprintf(os.Stderr, "   %s left as found: its uncommitted changes were kept.\n", wt.Name)
		default:
			failed = append(failed, wt.Name)
			fmt.Fprintf(os.Stderr, "   %s failed: %v\n", wt.Name, err)
		}
	}

	fmt.Fprintf(os.Stderr, "🌳 Returned %d of %d held worktree(s); %d skipped; %d already available or damaged.\n",
		returned, len(targets), len(skipped), notHeld)

	// A failure outranks an abort: retrying is the right response to a failure
	// and the wrong one to a worktree deliberately left dirty, so the more
	// urgent of the two decides the exit status.
	if len(failed) > 0 {
		return fmt.Errorf("failed to return worktree(s) %s", strings.Join(failed, ", "))
	}
	if len(aborted) > 0 {
		return withExitCode(ExitNotReturned, fmt.Errorf(
			"🌳 worktree(s) %s not returned: they have uncommitted changes and cleaning was declined or could not be confirmed; prune will not reclaim those slots. Use treehouse return --all --force to clean and return them",
			strings.Join(aborted, ", ")))
	}
	return nil
}

// quoteReturnPath makes a worktree path safe to paste after
// `treehouse return --force`. Unquoted or double-quoted paths can still
// expand $(), backticks, or command separators in POSIX shells.
func quoteReturnPath(p string) string {
	if p == "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return quoteWindowsReturnPath(p)
	}
	return quotePOSIXReturnPath(p)
}

func quotePOSIXReturnPath(p string) string {
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

func quoteWindowsReturnPath(p string) string {
	// cmd.exe is Treehouse's Windows shell. Double quotes group the path and
	// neutralize $(), backticks, and command separators. Doubled quotes are
	// the cmd escape for embedded ".
	//
	// Interactive cmd expands %NAME% even inside double quotes, before the
	// process starts. Batch-style %% doubling is not paste-safe: a prompt
	// keeps the extra percents, so lookup misses the managed worktree.
	// Split %NAME% across a quote boundary (%"NAME"%) so cmd concatenates
	// the original path and does not expand NAME.
	var b strings.Builder
	b.Grow(len(p) + 2)
	b.WriteByte('"')
	for i := 0; i < len(p); {
		switch p[i] {
		case '"':
			b.WriteString(`""`)
			i++
		case '%':
			j := i + 1
			for j < len(p) && isCmdEnvNameChar(p[j]) {
				j++
			}
			if j > i+1 && j < len(p) && p[j] == '%' {
				b.WriteString(`%"`)
				b.WriteString(p[i+1 : j])
				b.WriteString(`"%`)
				i = j + 1
			} else {
				b.WriteByte('%')
				i++
			}
		default:
			b.WriteByte(p[i])
			i++
		}
	}
	b.WriteByte('"')
	return b.String()
}

func isCmdEnvNameChar(c byte) bool {
	return c == '_' ||
		(c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9')
}

func confirmWorktreeReturn(wtPath string) error {
	if !returnForce {
		dirty, _ := vcs.IsDirty(wtPath)
		if dirty {
			ok, err := ui.Confirm("Worktree has uncommitted changes. Clean and return?", true)
			if err != nil {
				return errReturnAbortedNonTTY
			}
			if !ok {
				return errReturnAborted
			}
		}
	}
	return nil
}

func finalizeWorktreeReturn(wtPath string) error {
	// A markerless slot must never be detached: dispatch on such a path falls
	// back to the configured backend, which in an in-project pool would detach
	// the HEAD of the repository ENCLOSING the pool.
	if !returnForce && vcs.WorktreeBackendName(wtPath) != "" {
		if err := vcs.DetachWorktree(wtPath); err != nil {
			return fmt.Errorf("failed to detach worktree HEAD: %w", err)
		}
	}

	return killLingeringProcesses(wtPath)
}

// resolveReturnTarget resolves the single worktree a return acts on.
//
// An argument is read as a PATH first and only then as a slot NAME, so every
// argument that resolves today keeps resolving to exactly the same worktree:
// the name reading is reached only where the path reading already failed to
// find a managed worktree. That ordering matters because the two vocabularies
// can collide - standing in a pool directory, "1" is both a slot name and a
// real subdirectory - and a path that already works must never be redirected.
func resolveReturnTarget(args []string) (returnTarget, error) {
	wtPath, err := resolveWorktreePath(args)
	if err != nil {
		return returnTarget{}, err
	}

	poolDir, pathErr := resolveReturnPoolDir(wtPath, len(args) > 0)
	if pathErr == nil {
		return returnTarget{path: wtPath, poolDir: poolDir}, nil
	}
	if len(args) == 0 || !errors.Is(pathErr, errReturnWorktreeUnmanaged) || !couldBeWorktreeName(args[0]) {
		if errors.Is(pathErr, errReturnWorktreeUnmanaged) {
			return returnTarget{}, fmt.Errorf("worktree %s is not managed by treehouse", wtPath)
		}
		return returnTarget{}, pathErr
	}
	return resolveReturnTargetByName(args[0])
}

// couldBeWorktreeName reports whether an argument can be read as a slot name.
// A pool names its slots itself and never puts a path separator in a name, so
// anything holding one is a path and only a path: its failure has to keep
// reporting the path diagnosis rather than a misleading "no worktree named".
// Backslash is rejected on every platform - it separates paths on Windows, and
// no generated slot name contains one anywhere.
func couldBeWorktreeName(arg string) bool {
	if arg == "" || arg == "." || arg == ".." {
		return false
	}
	if filepath.IsAbs(arg) {
		return false
	}
	return !strings.ContainsAny(arg, `/\`)
}

// resolveReturnTargetByName resolves a slot name against the pool of the
// repository the caller is standing in. Unlike a path, a name carries no
// information about where its pool lives, so the repository is required - the
// same contract `treehouse lease <name>` has.
func resolveReturnTargetByName(name string) (returnTarget, error) {
	poolDir, err := repositoryPoolDir()
	if err != nil {
		return returnTarget{}, fmt.Errorf("%q is not a treehouse-managed worktree path, and a worktree name can only be resolved from inside its repository: %w", name, err)
	}

	entry, err := pool.FindByName(poolDir, name)
	if err != nil {
		return returnTarget{}, err
	}
	if entry == nil {
		return returnTarget{}, unknownWorktreeNameError(poolDir, name)
	}
	return returnTarget{path: entry.Path, poolDir: poolDir}, nil
}

// unknownWorktreeNameError reports an argument that resolved as neither
// vocabulary. It names both readings, because the user picked one of them and
// only they know which, and it lists the names the pool does have - the same
// help `treehouse enter` gives for the same mistake. A pool whose names cannot
// be listed still produces the refusal: the listing is help, not the verdict.
func unknownWorktreeNameError(poolDir, name string) error {
	state, err := pool.ReadState(poolDir)
	if err != nil {
		return fmt.Errorf("no worktree named %q in pool, and it is not a treehouse-managed worktree path either. Run 'treehouse status' for details", name)
	}
	names := make([]string, 0, len(state.Worktrees))
	for _, wt := range state.Worktrees {
		names = append(names, wt.Name)
	}
	if len(names) == 0 {
		return fmt.Errorf("no worktree named %q: the pool is empty, and it is not a treehouse-managed worktree path either. Run 'treehouse get' to create one", name)
	}
	return fmt.Errorf("no worktree named %q in pool (available: %s), and it is not a treehouse-managed worktree path either. Run 'treehouse status' for details", name, strings.Join(names, ", "))
}

func resolveWorktreePath(args []string) (string, error) {
	if len(args) > 0 {
		return filepath.Abs(args[0])
	}
	if env := os.Getenv("TREEHOUSE_DIR"); env != "" {
		return filepath.Abs(env)
	}
	return os.Getwd()
}

// returnBaseBranch resolves the configured base branch for the repository that
// owns wtPath, so a worktree returned by 'treehouse return' is parked exactly
// where 'treehouse get' leaves one. Anything it cannot resolve yields "", the
// repository default, because a return must never fail over configuration.
func returnBaseBranch(wtPath string) string {
	if vcs.WorktreeBackendName(wtPath) == "" {
		// Damaged slot: it is never reset, so the branch is unused, and
		// resolving through the fallback would answer for the repository
		// enclosing an in-project pool.
		return ""
	}
	repoRoot, err := vcs.FindMainRepoRootFrom(wtPath)
	if err != nil {
		return ""
	}
	cfg, err := config.Load(repoRoot)
	if err != nil {
		return ""
	}
	return releaseBaseBranch(repoRoot, cfg)
}

// repositoryPoolDir resolves the pool serving the repository the caller is
// standing in. Both of return's repository-scoped forms - a slot name and
// --all - go through it, because neither carries a path to find a pool from.
func repositoryPoolDir() (string, error) {
	repoRoot, err := vcs.FindMainRepoRoot()
	if err != nil {
		return "", fmt.Errorf("not in a git or jj repository: %w", err)
	}
	return poolDirForRepoRoot(repoRoot)
}

func poolDirForRepoRoot(repoRoot string) (string, error) {
	cfg, err := config.Load(repoRoot)
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}
	return config.ResolvePoolDir(repoRoot, config.ResolveRoot(rootFlag, cfg))
}

func resolveReturnPoolDir(wtPath string, explicitPath bool) (string, error) {
	// The built-in layout puts a worktree two levels under its pool, which lets a
	// return succeed even when the repository is gone. The candidate is confirmed
	// to be a pool first, exactly as destroy does: a worktree_path worktree lives
	// somewhere else entirely, and reading state from whatever directory happens
	// to sit two levels up reconstructs entries from the worktrees it finds there.
	if pathPoolDir := filepath.Dir(filepath.Dir(wtPath)); pool.IsPoolDir(pathPoolDir) {
		entry, err := pool.FindByPath(pathPoolDir, wtPath)
		if err != nil {
			return "", err
		}
		if entry != nil {
			return pathPoolDir, nil
		}
	}

	var err error
	var repoRoot string
	if explicitPath {
		repoRoot, err = vcs.FindMainRepoRootFrom(wtPath)
	} else {
		repoRoot, err = vcs.FindMainRepoRoot()
	}
	if err != nil {
		if explicitPath {
			return "", errReturnWorktreeUnmanaged
		}
		return "", fmt.Errorf("not in a git or jj repository: %w", err)
	}

	fallbackPoolDir, err := poolDirForRepoRoot(repoRoot)
	if err != nil {
		return "", err
	}

	entry, err := pool.FindByPath(fallbackPoolDir, wtPath)
	if err != nil {
		return "", err
	}
	if entry == nil {
		return "", errReturnWorktreeUnmanaged
	}
	return fallbackPoolDir, nil
}
