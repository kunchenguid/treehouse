package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/ui"
	"github.com/kunchenguid/treehouse/internal/vcs"
)

var statusJSON bool

type statusJSONProcess struct {
	PID  int32  `json:"pid"`
	Name string `json:"name"`
}

type statusJSONWorktree struct {
	Name        string              `json:"name"`
	Path        string              `json:"path"`
	Status      string              `json:"status"`
	Branch      string              `json:"branch"`
	Detached    bool                `json:"detached,omitempty"`
	BranchErr   string              `json:"branch_error,omitempty"`
	Flavor      string              `json:"flavor,omitempty"`
	LeaseID     string              `json:"lease_id"`
	LeaseHolder string              `json:"lease_holder"`
	LeasedAt    *time.Time          `json:"leased_at"`
	Processes   []statusJSONProcess `json:"processes"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the status of all worktrees in the pool",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := vcs.FindMainRepoRoot()
		if err != nil {
			return fmt.Errorf("not in a git or jj repository: %w", err)
		}

		cfg, err := config.Load(repoRoot)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		poolDir, err := config.ResolvePoolDir(repoRoot, config.ResolveRoot(rootFlag, cfg))
		if err != nil {
			return err
		}

		worktrees, err := pool.List(poolDir)
		if err != nil {
			return err
		}
		repoFlavor := vcs.BackendNameFor(repoRoot)

		if statusJSON {
			return writeStatusJSON(worktrees)
		}

		green := color.New(color.FgGreen).SprintFunc()
		red := color.New(color.FgRed).SprintFunc()
		yellow := color.New(color.FgYellow).SprintFunc()
		cyan := color.New(color.FgCyan, color.Bold).SprintFunc()
		magenta := color.New(color.FgMagenta).SprintFunc()

		fmt.Fprintln(os.Stdout, baseBranchLine(repoRoot, cfg, yellow))

		if len(worktrees) == 0 {
			fmt.Fprintln(os.Stderr, "🌳 No worktrees in pool.")
			return nil
		}

		// statusWidth must be >= longest status string ("you're here" = 11;
		// "unverified" = 10)
		const statusWidth = 11

		for _, wt := range worktrees {
			var status string
			switch wt.Status {
			case pool.StatusAvailable:
				status = green(wt.Status)
			case pool.StatusInUse:
				status = red(wt.Status)
			case pool.StatusDirty:
				status = yellow(wt.Status)
			case pool.StatusLeased:
				status = magenta(wt.Status)
			case pool.StatusHere:
				status = cyan(wt.Status)
			case pool.StatusDamaged:
				status = red(wt.Status)
			case pool.StatusUnverified:
				status = red(wt.Status)
			}

			// "%-4s  %-11s  " = 4 + 2 + 11 + 2 = 19 chars before path
			statusPad := strings.Repeat(" ", statusWidth-len(wt.Status))
			line := fmt.Sprintf("%-4s  %s%s  %s", wt.Name, status, statusPad, ui.PrettyPath(wt.Path))
			if wt.Branch != "" {
				line += fmt.Sprintf("  [%s]", wt.Branch)
			}
			if wt.BranchErr != "" {
				line += yellow(fmt.Sprintf("  (branch unreadable: %s)", wt.BranchErr))
			}
			if wt.Status == pool.StatusLeased && wt.LeaseHolder != "" {
				line += fmt.Sprintf("  (held by %s)", wt.LeaseHolder)
			}
			if wt.Flavor != "" && wt.Flavor != repoFlavor {
				line += yellow(fmt.Sprintf("  (%s-flavored; repo selects %s — destroy to migrate)", wt.Flavor, repoFlavor))
			}
			if wt.Status == pool.StatusDamaged {
				line += yellow(fmt.Sprintf("  (no .git or .jj marker — 'treehouse destroy %s --include-unlanded' to remove)", ui.PrettyPath(wt.Path)))
			}
			fmt.Fprintln(os.Stdout, line)

			if len(wt.Processes) > 0 {
				var procStrs []string
				for _, p := range wt.Processes {
					procStrs = append(procStrs, p.String())
				}
				fmt.Fprintf(os.Stdout, "%s%s\n", strings.Repeat(" ", 4+2+statusWidth+2), strings.Join(procStrs, ", "))
			}
		}
		return nil
	},
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Print pool status as JSON")
	rootCmd.AddCommand(statusCmd)
}

// baseBranchLine reports the branch worktrees are cut from. It never fails the
// command: an unresolvable base is a finding to report, not a status error.
// Human output only; status --json is a top-level array and stays one.
func baseBranchLine(repoRoot string, cfg config.Config, warn func(a ...interface{}) string) string {
	const prefix = "base  "
	if cfg.BaseBranch == "" {
		branch, err := vcs.GetDefaultBranch(repoRoot)
		if err != nil {
			return prefix + warn(fmt.Sprintf("cannot be determined (%v)", err))
		}
		return prefix + branch + "  (repository default)"
	}
	if err := vcs.VerifyBaseBranch(repoRoot, cfg.BaseBranch); err != nil {
		// Short: this sits above a table, and get prints the full diagnosis.
		return prefix + cfg.BaseBranch + warn("  (configured, but cannot be resolved — 'treehouse get' will fail)")
	}
	return prefix + cfg.BaseBranch + "  (configured)"
}

func writeStatusJSON(worktrees []pool.WorktreeStatus) error {
	output := make([]statusJSONWorktree, 0, len(worktrees))
	for _, wt := range worktrees {
		item := statusJSONWorktree{
			Name:        wt.Name,
			Path:        wt.Path,
			Status:      wt.Status,
			Branch:      wt.Branch,
			Detached:    wt.Detached,
			BranchErr:   wt.BranchErr,
			Flavor:      wt.Flavor,
			LeaseID:     wt.LeaseID,
			LeaseHolder: wt.LeaseHolder,
			Processes:   make([]statusJSONProcess, 0, len(wt.Processes)),
		}
		if !wt.LeasedAt.IsZero() {
			leasedAt := wt.LeasedAt
			item.LeasedAt = &leasedAt
		}
		for _, process := range wt.Processes {
			item.Processes = append(item.Processes, statusJSONProcess{
				PID:  process.PID,
				Name: process.Name,
			})
		}
		output = append(output, item)
	}
	return json.NewEncoder(os.Stdout).Encode(output)
}
