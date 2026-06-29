// Package herdr integrates treehouse with the herdr terminal multiplexer
// (https://herdr.dev). When treehouse runs inside a herdr-managed pane it can
// open each acquired worktree in its own herdr pane and route the agent that
// lands there to the herdr skill, so treehouse worktrees become first-class
// herdr citizens instead of subshells nested blindly inside the caller's pane.
//
// Everything here degrades gracefully: detection is a pure environment check,
// and spawning shells out to the `herdr` binary, so the caller can fall back to
// treehouse's classic subshell when herdr is absent or the call fails. The
// package shells out only (no platform syscalls), so it builds on every
// supported OS even though herdr itself is unix-only.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// RuntimeEnvVar is set to "1" by herdr inside every pane it manages.
const RuntimeEnvVar = "HERDR_ENV"

// Marker is exported into the worktree subshell so the agent (and the treehouse
// skill) can tell it is in a treehouse-managed worktree running under herdr,
// distinct from a bare herdr pane.
const Marker = "TREEHOUSE_HERDR=1"

// DisableEnvVar lets a user force treehouse's classic subshell even inside
// herdr, mirroring the --no-herdr flag.
const DisableEnvVar = "TREEHOUSE_NO_HERDR"

// HoldSubcommand is the hidden treehouse subcommand run inside the spawned herdr
// pane. It holds the worktree for the lifetime of that pane's shell and returns
// it to the pool when the shell exits, preserving treehouse's exit-to-return UX.
const HoldSubcommand = "__hold"

// LeaseHolder is the lease holder label recorded for herdr-opened worktrees. It
// surfaces in `treehouse status` as "(held by herdr)".
const LeaseHolder = "herdr"

// binary is the herdr CLI name; a var so tests can point it at a known command.
var binary = "herdr"

// spawnTimeout bounds how long SpawnHold waits for `herdr agent start`. If the
// herdr server is unresponsive or its socket is unavailable the call is
// abandoned so getHerdrRunE can release the lease and fall back to a classic
// subshell instead of blocking `treehouse get` forever. A var so tests can
// shrink it.
var spawnTimeout = 10 * time.Second

// IsRuntime reports whether treehouse is running inside a herdr-managed pane.
func IsRuntime() bool {
	return os.Getenv(RuntimeEnvVar) == "1"
}

// Disabled reports whether the user opted out of herdr-native behavior via the
// TREEHOUSE_NO_HERDR environment variable.
func Disabled() bool {
	return os.Getenv(DisableEnvVar) == "1"
}

// Available reports whether the herdr CLI is on PATH.
func Available() bool {
	_, err := exec.LookPath(binary)
	return err == nil
}

// RoutingMessage is the one-line guidance that points an agent landing in a
// treehouse worktree at the herdr skill.
func RoutingMessage() string {
	return "🌳 herdr runtime detected - use the /herdr skill to open panes, spawn sibling agents, run servers and logs in adjacent panes, and coordinate via `herdr wait agent-status`."
}

// SpawnOptions configures opening a worktree as a dedicated herdr pane.
type SpawnOptions struct {
	// Exe is the absolute path to the treehouse binary run as the holder.
	Exe string
	// WorktreePath is the acquired worktree to open and hold.
	WorktreePath string
	// PoolDir is the pool the worktree belongs to (passed to the holder so it
	// can return the worktree when its shell exits).
	PoolDir string
	// Label is the pane/agent label shown in herdr's sidebar.
	Label string
	// Split is "right" or "down"; anything else defaults to "right".
	Split string
	// Focus moves focus to the new pane when true.
	Focus bool
}

// Pane identifies a spawned herdr pane.
type Pane struct {
	ID    string
	Label string
}

// buildStartArgs constructs the `herdr agent start` argument vector that opens a
// worktree pane running `<exe> __hold <worktree> <pool>`. It is kept pure so the
// exact command treehouse will run is unit-testable without a herdr server.
func buildStartArgs(o SpawnOptions) []string {
	split := "right"
	if o.Split == "down" {
		split = "down"
	}
	args := []string{"agent", "start", o.Label, "--cwd", o.WorktreePath, "--split", split}
	if o.Focus {
		args = append(args, "--focus")
	} else {
		args = append(args, "--no-focus")
	}
	// Everything after "--" is the argv herdr runs inside the new pane.
	args = append(args, "--", o.Exe, HoldSubcommand, o.WorktreePath, o.PoolDir)
	return args
}

// SpawnHold opens the worktree in a new herdr pane that runs the treehouse
// holder. It returns the new pane on success. A non-nil error means no pane was
// created and the caller should fall back to a classic subshell. The call is
// bounded by spawnTimeout so an unresponsive herdr server triggers the fallback
// rather than hanging `treehouse get` indefinitely.
func SpawnHold(o SpawnOptions) (Pane, error) {
	ctx, cancel := context.WithTimeout(context.Background(), spawnTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, buildStartArgs(o)...)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return Pane{}, fmt.Errorf("herdr agent start timed out after %s", spawnTimeout)
		}
		return Pane{}, fmt.Errorf("herdr agent start failed: %w", withStderr(err))
	}
	return Pane{ID: parsePaneID(out), Label: o.Label}, nil
}

// parsePaneID best-effort extracts a pane or terminal id from `herdr agent
// start` JSON. herdr nests its payload under "result" and the spawned context
// may be reported as a terminal, pane, or agent object; this tolerates all of
// those shapes and returns "" when none is found, since the id is only used for
// display.
func parsePaneID(out []byte) string {
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		return ""
	}
	scope := root
	if r, ok := root["result"].(map[string]any); ok {
		scope = r
	}
	for _, key := range []string{"terminal", "pane", "agent"} {
		if obj, ok := scope[key].(map[string]any); ok {
			if id := idField(obj); id != "" {
				return id
			}
		}
	}
	return idField(scope)
}

func idField(obj map[string]any) string {
	for _, key := range []string{"pane_id", "terminal_id", "id"} {
		if v, ok := obj[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// withStderr unwraps an *exec.ExitError so its captured stderr (populated by
// cmd.Output when Stderr is unset) surfaces in the returned error.
func withStderr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return errors.New(string(bytes.TrimSpace(ee.Stderr)))
	}
	return err
}
