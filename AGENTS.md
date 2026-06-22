# Treehouse - Agent Guide

## What is this?

Treehouse is a Go CLI tool that manages a pool of git worktrees for parallel AI coding agent workflows. It maintains reusable, pre-warmed worktrees so agents get isolated environments instantly.

## Project Structure

- `main.go` - entry point, calls `cmd.Execute()`
- `cmd/` - CLI commands (cobra): `get` (incl. `get --lease`), `return`, `status`, `prune`, `destroy`
- `internal/config/` - config file loading (`treehouse.toml`)
- `internal/hooks/` - user-configured lifecycle hook command execution
- `internal/pool/` - pool manager (acquire, release, list, destroy, prune) + state file
- `internal/git/` - git operations (shells out to `git` binary)
- `internal/process/` - in-use detection and lingering process termination for worktrees
- `internal/shell/` - subshell spawning
- `internal/ui/` - Y/n confirmation prompts

## Building

```sh
go build -o treehouse .
# or
make build
```

## Testing

```sh
go test ./...
# or
make test
```

## Key Design Decisions

- No daemon - all operations are inline CLI commands
- Detached HEAD worktrees reset to whichever of local or origin default branch is further ahead (prefers origin on divergence)
- In-use detection uses process scanning plus short-lived persisted owner reservations for lifecycle operations
- Durable leases are a separate, process-independent reservation: `WorktreeEntry.Leased`/`LeaseHolder`/`LeasedAt` persist in the state file (all `omitempty`, so pre-lease state files keep today's behavior). A lease is NOT derived from live processes, so it survives with zero processes inside the worktree and `healState` never clears it (it only clears dead owner reservations). Leased worktrees are skipped by `Acquire` and `prune`, treated as in-use by `worktreeInUse` (so non-force `destroy` rejects them), surfaced by `status` as `StatusLeased`, and cleared by `Release` (`return`)
- `get --lease` (see `getLeaseRunE`) is the non-interactive acquire: it implies the durable lease, opens no subshell, routes post-create hook stdout and all banners to stderr, and prints ONLY the worktree path to stdout so `path=$(treehouse get --lease)` is clean. `--lease-holder`/`$TREEHOUSE_LEASE_HOLDER` set the recorded holder. `pool.AcquireLease` is the entry point; both it and `Acquire` delegate to the shared `acquire(..., acquireOptions)` core, and `markAcquired` stamps either a lease or an owner reservation. Concurrency safety comes from the existing `WithStateLock` (flock) around all pool mutation
- Dirty checks include untracked files even when repository config hides them from normal `git status` output
- Prune deletes only idle managed worktrees that are clean and whose HEAD is merged into the default branch; dry run is the default
- Prune reports unsafe idle worktrees in grouped, stable categories and keeps raw git diagnostics for verbose output instead of default output
- Prune treats backing-repository-missing linked worktrees as orphans; they are only deletable with explicit `--prune-orphans --yes`, and each candidate warns that content could not be verified
- Prune never treats an unreachable origin as a deletable orphan; those worktrees stay skipped because the repository may still be valid
- Global prune enumerates managed pool directories under the user-level treehouse root and derives each worktree's owning repository from git metadata instead of relying on the current directory
- Global prune loads user-level config and hooks only because it can run without a repository context
- State file tracks pool membership, temporary owner/destroy reservations, and explicit durable leases.
  It still does not infer long-term usage from processes.
- Git operations shell out to `git` (go-git has incomplete worktree support)
- Self-healing: stale state entries are auto-removed

## Windows Compatibility

This project targets Linux, macOS, and Windows. All new code **must** work on Windows. Follow these rules:

- **Paths**: Never hardcode `/` as a path separator. Use `filepath.Join()`, `filepath.Separator`, or `filepath.ToSlash()` as appropriate.
- **Shell**: Do not assume `/bin/sh` or `$SHELL` exist. On Windows, use `%COMSPEC%` (usually `cmd.exe`). See `internal/shell/shell.go` for the pattern.
- **Syscalls**: Unix-only syscalls (e.g., `syscall.Flock`) must be isolated behind build tags (`//go:build !windows` / `//go:build windows`). See `internal/pool/lock_unix.go` and `lock_windows.go` for the pattern.
- **Build tags**: Follow the existing `_unix.go` / `_windows.go` naming convention (see also `internal/updater/sysproc_*.go`).
- **CI**: The CI matrix runs tests on `ubuntu`, `macOS`, and `windows`. Cross-compile locally with `GOOS=windows go build ./...` to catch issues early.
- **Process detection**: `gopsutil` is cross-platform - no special handling needed, but avoid importing platform-specific process APIs directly.

## Config

Place repo-safe settings in repo root `treehouse.toml` or user-level `~/.config/treehouse/config.toml`:

```toml
max_trees = 16

# Optional worktree root.
# Relative roots need a repo context; use an absolute user-level root for global prune.
# root = "$HOME/worktrees"

# User-level config only:
[hooks]
post_create = ["./scripts/setup-venv.sh"]
pre_destroy = ["./scripts/teardown.sh"]
```

Hooks are ignored in repo-level config for safety.
