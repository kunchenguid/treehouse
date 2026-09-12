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
	returnIfLeaseID     string
	returnIfLeaseHolder string
)

var (
	errReturnWorktreeUnmanaged = errors.New("return worktree unmanaged")
	errReturnAborted           = errors.New("return aborted")
	errReturnAbortedNonTTY     = errors.New("return aborted: non-tty dirty")
)

var returnCmd = &cobra.Command{
	Use:   "return [path]",
	Short: "Terminate lingering processes and return a worktree",
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("if-lease-id") && returnIfLeaseID == "" {
			return fmt.Errorf("--if-lease-id cannot be empty")
		}

		wtPath, err := resolveWorktreePath(args)
		if err != nil {
			return err
		}

		poolDir, err := resolveReturnPoolDir(wtPath, len(args) > 0)
		if err != nil {
			if errors.Is(err, errReturnWorktreeUnmanaged) {
				return fmt.Errorf("worktree %s is not managed by treehouse", wtPath)
			}
			return err
		}

		conditional := cmd.Flags().Changed("if-lease-id") || cmd.Flags().Changed("if-lease-holder")
		if conditional {
			preconditions := pool.ReleasePreconditions{}
			if cmd.Flags().Changed("if-lease-id") {
				preconditions.ExpectedLeaseID = &returnIfLeaseID
			}
			if cmd.Flags().Changed("if-lease-holder") {
				preconditions.ExpectedLeaseHolder = &returnIfLeaseHolder
			}
			err = pool.ValidateReleasePreconditions(poolDir, wtPath, preconditions, nil)
			if err == nil {
				err = confirmWorktreeReturn(wtPath)
			}
			if err == nil {
				err = pool.ReleaseConditional(poolDir, wtPath, returnBaseBranch(wtPath), preconditions, func() error {
					return finalizeWorktreeReturn(wtPath)
				})
			}
		} else {
			err = confirmWorktreeReturn(wtPath)
			if err == nil {
				err = pool.ReleaseConditional(poolDir, wtPath, returnBaseBranch(wtPath), pool.ReleasePreconditions{}, func() error {
					return finalizeWorktreeReturn(wtPath)
				})
			}
		}
		// An abort is not a success: the worktree, and any lease on it, are
		// exactly as they were found. Reporting exit 0 here let a caller
		// conclude the slot was released, so a leaked lease starved the pool
		// with nothing in the exit status to detect it.
		if errors.Is(err, errReturnAbortedNonTTY) {
			return withExitCode(ExitNotReturned, fmt.Errorf(
				"🌳 worktree not returned: it has uncommitted changes and the confirmation could not be answered (stdin reached EOF); prune will not reclaim this slot. Use treehouse return --force %s to clean and return it",
				quoteReturnPath(wtPath)))
		}
		if errors.Is(err, errReturnAborted) {
			return withExitCode(ExitNotReturned, fmt.Errorf(
				"🌳 worktree not returned: cleaning declined, so its uncommitted changes remain and prune will not reclaim this slot. Use treehouse return --force %s to clean and return it",
				quoteReturnPath(wtPath)))
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
	returnCmd.Flags().StringVar(&returnIfLeaseID, "if-lease-id", "", "Return only if the current lease has this identity")
	returnCmd.Flags().StringVar(&returnIfLeaseHolder, "if-lease-holder", "", "Return only if the current lease has this holder")
	rootCmd.AddCommand(returnCmd)
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

func resolveReturnPoolDir(wtPath string, explicitPath bool) (string, error) {
	pathPoolDir := filepath.Dir(filepath.Dir(wtPath))
	entry, err := pool.FindByPath(pathPoolDir, wtPath)
	if err != nil {
		return "", err
	}
	if entry != nil {
		return pathPoolDir, nil
	}

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

	cfg, err := config.Load(repoRoot)
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}

	fallbackPoolDir, err := config.ResolvePoolDir(repoRoot, config.ResolveRoot(rootFlag, cfg))
	if err != nil {
		return "", err
	}

	entry, err = pool.FindByPath(fallbackPoolDir, wtPath)
	if err != nil {
		return "", err
	}
	if entry == nil {
		return "", errReturnWorktreeUnmanaged
	}
	return fallbackPoolDir, nil
}
