package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var (
	currentPath bool
	currentJSON bool
)

var currentCmd = &cobra.Command{
	Use:   "current",
	Short: "Report whether the current directory is inside a managed worktree",
	Long: `Report whether the current directory is inside a treehouse-managed worktree.

Detection is authoritative: it resolves the owning repository from git metadata
(so it works from inside a linked worktree) and cross-checks the current
directory against treehouse's own pool state, rather than trusting the
$TREEHOUSE_DIR environment variable that only the 'get' subshell exports.

Exit code is 0 when inside a managed worktree and non-zero otherwise, so it
composes in scripts:

  if treehouse current --path >/dev/null 2>&1; then ...; fi`,
	RunE: currentRunE,
}

func init() {
	currentCmd.Flags().BoolVar(&currentPath, "path", false, "Print only the worktree's absolute path to stdout")
	currentCmd.Flags().BoolVar(&currentJSON, "json", false, "Print worktree metadata as JSON")
	rootCmd.AddCommand(currentCmd)
}

// currentInfo is the JSON contract for `treehouse current --json`.
type currentInfo struct {
	InWorktree  bool   `json:"in_worktree"`
	Name        string `json:"name,omitempty"`
	Path        string `json:"path,omitempty"`
	Status      string `json:"status,omitempty"`
	LeaseHolder string `json:"lease_holder,omitempty"`
}

func currentRunE(cmd *cobra.Command, args []string) error {
	if currentPath && currentJSON {
		return fmt.Errorf("--path and --json are mutually exclusive")
	}

	wt, err := resolveCurrentWorktree()
	if err != nil {
		return err
	}

	if wt == nil {
		return reportNotInWorktree()
	}

	switch {
	case currentPath:
		fmt.Fprintln(os.Stdout, wt.Path)
	case currentJSON:
		if err := printJSON(currentInfo{
			InWorktree:  true,
			Name:        wt.Name,
			Path:        wt.Path,
			Status:      wt.Status,
			LeaseHolder: wt.LeaseHolder,
		}); err != nil {
			return err
		}
	default:
		fmt.Fprintln(os.Stdout, formatCurrentHuman(wt))
	}
	return nil
}

// resolveCurrentWorktree returns the managed worktree containing the current
// directory, or nil when the current directory is not inside one (including
// when it is not a git repository at all).
func resolveCurrentWorktree() (*pool.WorktreeStatus, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	// Resolve the OWNING repository, not the worktree's own toplevel: inside a
	// linked worktree `git rev-parse --show-toplevel` returns the worktree
	// path, which for remote-less repos hashes to the wrong pool. Deriving the
	// main repo root keeps pool resolution correct from inside a worktree.
	repoRoot, err := git.FindMainRepoRootFrom(cwd)
	if err != nil {
		// Not a git repository: definitively not in a managed worktree.
		return nil, nil
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	poolDir, err := config.ResolvePoolDir(repoRoot, cfg.Root)
	if err != nil {
		return nil, err
	}

	return pool.Current(poolDir, cwd)
}

// reportNotInWorktree emits the mode-appropriate "not in a worktree" output and
// returns errSilent so the process exits non-zero without extra decoration.
func reportNotInWorktree() error {
	switch {
	case currentJSON:
		if err := printJSON(currentInfo{InWorktree: false}); err != nil {
			return err
		}
	case currentPath:
		// Stay silent so `p=$(treehouse current --path)` yields an empty
		// string and callers rely on the exit code.
	default:
		fmt.Fprintln(os.Stderr, "🌳 Not in a treehouse worktree.")
	}
	return errSilent
}

func formatCurrentHuman(wt *pool.WorktreeStatus) string {
	var status string
	switch wt.Status {
	case pool.StatusAvailable:
		status = color.New(color.FgGreen).Sprint(wt.Status)
	case pool.StatusInUse:
		status = color.New(color.FgRed).Sprint(wt.Status)
	case pool.StatusDirty:
		status = color.New(color.FgYellow).Sprint(wt.Status)
	case pool.StatusLeased:
		status = color.New(color.FgMagenta).Sprint(wt.Status)
	case pool.StatusHere:
		status = color.New(color.FgCyan, color.Bold).Sprint(wt.Status)
	default:
		status = wt.Status
	}

	line := fmt.Sprintf("%s  %s  %s", wt.Name, status, ui.PrettyPath(wt.Path))
	if wt.Status == pool.StatusLeased && wt.LeaseHolder != "" {
		line += fmt.Sprintf("  (held by %s)", wt.LeaseHolder)
	}
	return line
}

func printJSON(info currentInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, strings.TrimSpace(string(data)))
	return nil
}
