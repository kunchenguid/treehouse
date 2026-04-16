# Treehouse — Agent Guide

## What is this?

Treehouse is a Go CLI tool that manages a pool of git worktrees for parallel AI coding agent workflows. It maintains reusable, pre-warmed worktrees so agents get isolated environments instantly.

## Project Structure

- `main.go` — entry point, calls `cmd.Execute()`
- `cmd/` — CLI commands (cobra): `get`, `return`, `status`, `destroy`
- `internal/config/` — config file loading (`treehouse.toml`)
- `internal/pool/` — pool manager (acquire, release, list, destroy) + state file
- `internal/git/` — git operations (shells out to `git` binary)
- `internal/process/` — in-use detection and lingering process termination for worktrees
- `internal/shell/` — subshell spawning
- `internal/ui/` — Y/n confirmation prompts

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

- No daemon — all operations are inline CLI commands
- Detached HEAD worktrees reset to whichever of local or origin default branch is further ahead (prefers origin on divergence)
- In-use detection is runtime-only (process scanning), never persisted
- State file only tracks pool membership, not usage status
- Git operations shell out to `git` (go-git has incomplete worktree support)
- Self-healing: stale state entries are auto-removed

## Windows Compatibility

This project targets Linux, macOS, and Windows. All new code **must** work on Windows. Follow these rules:

- **Paths**: Never hardcode `/` as a path separator. Use `filepath.Join()`, `filepath.Separator`, or `filepath.ToSlash()` as appropriate.
- **Shell**: Do not assume `/bin/sh` or `$SHELL` exist. On Windows, use `%COMSPEC%` (usually `cmd.exe`). See `internal/shell/shell.go` for the pattern.
- **Syscalls**: Unix-only syscalls (e.g., `syscall.Flock`) must be isolated behind build tags (`//go:build !windows` / `//go:build windows`). See `internal/pool/lock_unix.go` and `lock_windows.go` for the pattern.
- **Build tags**: Follow the existing `_unix.go` / `_windows.go` naming convention (see also `internal/updater/sysproc_*.go`).
- **CI**: The CI matrix runs tests on `ubuntu`, `macOS`, and `windows`. Cross-compile locally with `GOOS=windows go build ./...` to catch issues early.
- **Process detection**: `gopsutil` is cross-platform — no special handling needed, but avoid importing platform-specific process APIs directly.

## Config

Place `treehouse.toml` in repo root or `~/.config/treehouse/config.toml`:

```toml
max_trees = 16
```
